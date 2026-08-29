<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'

const coreStatus = ref('未检查')
const ragStatus = ref('未检查')
const collectorStatus = ref('未知')
const wechatStatus = ref<any>({ status: 'stopped' })
const wechatDbDir = ref('')
const wechatWxid = ref('')
const wechatBusy = ref(false)
const tasks = ref<any[]>([])
const messages = ref<any[]>([])
const taskError = ref('')
let timer: number | undefined

async function check(path: string, target: { value: string }) {
  target.value = '检查中...'
  try {
    const response = await fetch(path)
    target.value = response.ok ? '正常' : `异常 (${response.status})`
  } catch {
    target.value = '未连接'
  }
}

const checkCore = () => check('/api/core/health', coreStatus)
const checkRag = () => check('/api/rag/health', ragStatus)

async function refreshPipeline() {
  try {
    const [workersResponse, tasksResponse, messagesResponse, wechatResponse] = await Promise.all([
      fetch('/api/core/api/workers'),
      fetch('/api/core/api/tasks?type=vectorization'),
      fetch('/api/core/api/messages'),
      fetch('/api/core/api/ingestion/wechat/status'),
    ])
    if (!workersResponse.ok || !tasksResponse.ok || !messagesResponse.ok) throw new Error('状态接口不可用')
    const workers = await workersResponse.json()
    const worker = workers.workers?.find((item: any) => item.name === 'feishu-collector')
    collectorStatus.value = worker?.status === 'running' ? '运行中' : (worker?.status || '未注册')
    tasks.value = (await tasksResponse.json()).tasks || []
    messages.value = (await messagesResponse.json()).messages || []
    if (wechatResponse.ok) {
      wechatStatus.value = await wechatResponse.json()
      if (!wechatDbDir.value && wechatStatus.value.db_dir) wechatDbDir.value = wechatStatus.value.db_dir
      if (!wechatWxid.value && wechatStatus.value.wxid) wechatWxid.value = wechatStatus.value.wxid
    }
    taskError.value = ''
  } catch (error) {
    taskError.value = error instanceof Error ? error.message : '无法读取流水线状态'
  }
}

const taskSummary = computed(() => tasks.value.reduce((summary, task) => {
  summary[task.status] = (summary[task.status] || 0) + 1
  return summary
}, {} as Record<string, number>))

function authorizeFeishu() {
  window.location.href = '/api/core/api/ingestion/feishu/authorize'
}

async function updateWechat(action: 'bind' | 'rebind' | 'stop') {
  wechatBusy.value = true
  taskError.value = ''
  try {
    const options: RequestInit = { method: 'POST', headers: { 'Content-Type': 'application/json' } }
    if (action !== 'stop') options.body = JSON.stringify({ db_dir: wechatDbDir.value.trim(), wxid: wechatWxid.value.trim() })
    const response = await fetch(`/api/core/api/ingestion/wechat/${action}`, options)
    const result = await response.json()
    if (!response.ok) {
      const detail = result.detail ?? result.error
      const message = typeof detail === 'string' ? detail : detail ? JSON.stringify(detail) : '微信绑定失败'
      throw new Error(message)
    }
    wechatStatus.value = result
    await refreshPipeline()
  } catch (error) {
    taskError.value = error instanceof Error ? error.message : '微信绑定失败'
  } finally { wechatBusy.value = false }
}

onMounted(() => { refreshPipeline(); timer = window.setInterval(refreshPipeline, 5000) })
onUnmounted(() => { if (timer) window.clearInterval(timer) })
</script>

<template>
  <main class="page">
    <section class="hero">
      <p class="eyebrow">INFO-AGENT</p>
      <h1>信息管理 Agent</h1>
      <p class="description">连接飞书后，消息会自动进入采集、向量化流水线。</p>
      <button class="primary" @click="authorizeFeishu">授权飞书账号</button>
    </section>

    <section class="status-grid">
      <article class="status-card">
        <span>Core Service</span>
        <strong>{{ coreStatus }}</strong>
        <button @click="checkCore">检查服务</button>
      </article>
      <article class="status-card">
        <span>RAG Service</span>
        <strong>{{ ragStatus }}</strong>
        <button @click="checkRag">检查服务</button>
      </article>
      <article class="status-card">
        <span>Feishu Collector</span>
        <strong>{{ collectorStatus }}</strong>
        <button @click="refreshPipeline">刷新状态</button>
      </article>
      <article class="status-card">
        <span>向量化任务</span>
        <strong>{{ taskSummary.completed || 0 }} / {{ tasks.length }}</strong>
        <small>处理中 {{ taskSummary.processing || 0 }} · 待处理 {{ taskSummary.pending || 0 }} · 失败 {{ taskSummary.failed || 0 }}</small>
      </article>
    </section>

    <section class="pipeline-panel binding-panel">
      <div class="panel-heading"><div><p class="eyebrow">WECHAT</p><h2>本机微信采集</h2></div><strong :class="['worker-state', `worker-${wechatStatus.status}`]">{{ wechatStatus.status === 'running' ? '运行中' : wechatStatus.status === 'error' ? '异常' : '已停止' }}</strong></div>
      <div class="binding-form">
        <label><span>微信数据库路径</span><input v-model="wechatDbDir" placeholder="C:\Users\...\xwechat_files 或 contact.db" /></label>
        <label><span>内部 wxid</span><input v-model="wechatWxid" placeholder="wxid_xxxxx" /></label>
      </div>
      <div class="binding-actions">
        <button class="primary" :disabled="wechatBusy || wechatStatus.status === 'running'" @click="updateWechat('bind')">绑定并启动</button>
        <button :disabled="wechatBusy || wechatStatus.status !== 'running'" @click="updateWechat('stop')">停止采集</button>
        <button class="secondary" :disabled="wechatBusy" @click="updateWechat('rebind')">重新绑定</button>
      </div>
      <div class="binding-meta">
        <small v-if="wechatStatus.bound_at">绑定时间 {{ new Date(wechatStatus.bound_at).toLocaleString() }}</small>
        <small v-if="wechatStatus.last_collected_at">最近采集 {{ new Date(wechatStatus.last_collected_at).toLocaleString() }}</small>
        <small v-if="wechatStatus.last_error" class="error">{{ wechatStatus.last_error }}</small>
      </div>
    </section>

    <section class="pipeline-panel">
      <div class="panel-heading"><div><p class="eyebrow">LIVE PIPELINE</p><h2>最近向量化任务</h2></div><span class="refresh-note">每 5 秒更新</span></div>
      <p v-if="taskError" class="error">{{ taskError }}</p>
      <div v-else-if="!tasks.length" class="empty">暂无向量化任务</div>
      <div v-else class="task-list">
        <div v-for="task in tasks.slice(0, 12)" :key="task.id" class="task-row">
          <span class="task-id">#{{ task.id }}</span>
          <span class="task-entity">{{ task.entity_id }}</span>
          <strong :class="['task-status', `status-${task.status}`]">{{ task.status }}</strong>
          <time>{{ new Date(task.updated_at).toLocaleTimeString() }}</time>
        </div>
      </div>
    </section>
    <section class="pipeline-panel">
      <div class="panel-heading"><div><p class="eyebrow">INGESTION</p><h2>最近采集的消息</h2></div></div>
      <div v-if="!messages.length" class="empty">暂无采集消息</div>
      <div v-else class="message-list">
        <article v-for="message in messages.slice(0, 12)" :key="message.id" class="message-row">
          <div class="message-main"><strong>{{ message.text || `[${message.message_type || 'unknown'}]` }}</strong><small>{{ message.conversation_id || '未知会话' }} · {{ message.source }}</small></div>
          <span :class="['task-status', `status-${message.vector_status}`]">{{ message.vector_status }}</span>
          <time>{{ new Date(message.occurred_at || message.created_at).toLocaleTimeString() }}</time>
        </article>
      </div>
    </section>
  </main>
</template>
