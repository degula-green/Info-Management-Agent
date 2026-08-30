<template>
  <section class="wk-page conversation-page">
    <div class="conversation-head">
      <div class="wk-breadcrumb">
        <button type="button" @click="emit('back')">知识库</button>
        <t-icon name="chevron-right" />
        <span>{{ sourceName(chat.source) }}</span>
        <t-icon name="chevron-right" />
        <span>{{ chat.name }}</span>
      </div>

      <div class="conversation-title-row">
        <div>
          <h1>{{ chat.name }}</h1>
          <p>{{ sourceName(chat.source) }} · {{ chat.isDirect ? '私聊' : `${chat.members} 位成员` }} · 最近同步 {{ chat.lastSync }}</p>
        </div>
        <span class="chat-status" :class="`chat-status--${chat.collectionStatus}`"><i />{{ statusLabel(chat.collectionStatus) }}</span>
        <t-button variant="outline" :theme="chat.collectionStatus === 'collecting' ? 'warning' : 'primary'" @click="emit('toggle', chat)">
          <template #icon><t-icon :name="chat.collectionStatus === 'collecting' ? 'pause-circle' : 'play-circle'" /></template>
          {{ chat.collectionStatus === 'collecting' ? '暂停采集' : '开启采集' }}
        </t-button>
      </div>
    </div>

    <div class="conversation-meta">
      <span><t-icon name="time" /> 历史起点：{{ chat.historyStart || '尚未设置' }}</span>
      <span><t-icon name="chat" /> {{ chat.messages.length }} 条消息</span>
      <span><t-icon name="file" /> {{ chat.files.length }} 个文件</span>
    </div>

    <div class="conversation-list wk-panel">
      <div class="conversation-list__tabs">
        <button type="button" class="conversation-tab conversation-tab--active">
          消息与文件 <span>（{{ items.length }}）</span>
        </button>
        <span class="conversation-list__hint">{{ collectionHint }}</span>
      </div>

      <div v-if="items.length" class="conversation-table" role="table" aria-label="群聊消息和文件列表">
        <div class="conversation-table__head" role="row">
          <span>名称</span>
          <span>状态</span>
          <span>大小</span>
          <span>类型</span>
          <span>来源</span>
          <span>更新时间</span>
        </div>
        <button
          v-for="item in items"
          :key="item.key"
          type="button"
          class="conversation-row"
          :aria-label="`打开${item.kind === 'file' ? '文件' : '消息'}：${item.name}`"
          @click="openItem(item)"
        >
          <span class="conversation-cell conversation-cell--name" data-label="名称">
            <t-icon :name="item.kind === 'file' ? 'file' : 'chat-bubble'" />
            <span>
              <strong>{{ item.name }}</strong>
              <small>{{ item.detail }}</small>
            </span>
          </span>
          <span class="conversation-cell" data-label="状态"><em class="conversation-status">{{ item.status }}</em></span>
          <span class="conversation-cell conversation-cell--muted" data-label="大小">{{ item.size }}</span>
          <span class="conversation-cell" data-label="类型"><em class="conversation-type">{{ item.type }}</em></span>
          <span class="conversation-cell" data-label="来源">{{ item.source }}</span>
          <span class="conversation-cell conversation-cell--muted" data-label="更新时间">{{ item.updatedAt }}</span>
        </button>
      </div>
      <div v-else class="wk-empty-inline">还没有采集到消息或文件</div>
    </div>

    <t-drawer v-model:visible="drawerVisible" :header="drawerTitle" size="520px" :footer="false" destroy-on-close>
      <div class="wk-preview">
        <div class="wk-preview__meta"><t-tag theme="primary" variant="light">{{ drawerKind }}</t-tag><span>{{ sourceName(chat.source) }} · {{ chat.name }}</span></div>
        <div v-if="drawerFile" class="wk-file-preview"><t-icon name="file" /><div><strong>{{ drawerFile.name }}</strong><small>{{ drawerFile.type }} · {{ drawerFile.size }} · 上传人 {{ drawerFile.uploader }}</small></div></div>
        <div v-if="drawerKind === '消息'" class="drawer-facts"><span><b>发送人</b>{{ drawerMessage?.sender }}</span><span><b>时间</b>{{ drawerMessage?.time }}</span><span><b>来源平台</b>{{ sourceName(chat.source) }}</span></div>
        <t-textarea v-model="draft" :readonly="!editing" :autosize="{ minRows: 10, maxRows: 24 }" placeholder="内容" />
        <div class="wk-preview__actions">
          <t-button v-if="!editing" variant="outline" @click="editing = true"><template #icon><t-icon name="edit" /></template>编辑</t-button>
          <t-button v-else theme="primary" @click="save"><template #icon><t-icon name="check" /></template>保存</t-button>
          <t-button variant="outline" @click="emit('toast', '已生成下载文件（原型演示）')"><template #icon><t-icon name="download" /></template>下载</t-button>
        </div>
      </div>
    </t-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { CollectionStatus, InfoChat, InfoFile, InfoMessage } from '@/mock'
import { sourceName } from '@/mock'

type ConversationItem = {
  key: string
  kind: 'message' | 'file'
  name: string
  detail: string
  status: string
  size: string
  type: string
  source: string
  updatedAt: string
  message?: InfoMessage
  file?: InfoFile
}

const props = defineProps<{ chat: InfoChat }>()
const emit = defineEmits<{
  (event: 'back'): void
  (event: 'toggle', chat: InfoChat): void
  (event: 'toast', text: string): void
  (event: 'edit', payload: { kind: string; chatId: string; recordId: string; content: string }): void
}>()

const chat = computed(() => props.chat)
const drawerVisible = ref(false)
const drawerKind = ref('消息')
const drawerTitle = ref('')
const drawerFile = ref<InfoFile | null>(null)
const drawerMessage = ref<InfoMessage | null>(null)
const draft = ref('')
const editing = ref(false)

const items = computed<ConversationItem[]>(() => [
  ...chat.value.messages.map((message) => ({
    key: `message-${message.id}`,
    kind: 'message' as const,
    name: message.content,
    detail: `${message.sender} · ${message.time}`,
    status: '采集完成',
    size: '-',
    type: '消息',
    source: sourceName(chat.value.source),
    updatedAt: message.time,
    message,
  })),
  ...chat.value.files.map((file) => ({
    key: `file-${file.id}`,
    kind: 'file' as const,
    name: file.name,
    detail: `${file.uploader} · ${file.type}`,
    status: '解析完成',
    size: file.size,
    type: file.type,
    source: sourceName(chat.value.source),
    updatedAt: file.uploadedAt || file.time,
    file,
  })),
])

const collectionHint = computed(() => chat.value.collectionStatus === 'collecting' ? '持续采集中' : chat.value.collectionStatus === 'paused' ? '采集已暂停' : '尚未开始采集')

function statusLabel(status: CollectionStatus) { return status === 'collecting' ? '采集中' : status === 'paused' ? '已暂停' : '未开始' }
function openItem(item: ConversationItem) { if (item.kind === 'message' && item.message) openMessage(item.message); if (item.kind === 'file' && item.file) openFile(item.file) }
function openMessage(message: InfoMessage) { drawerKind.value = '消息'; drawerTitle.value = '原始消息'; drawerFile.value = null; drawerMessage.value = message; draft.value = message.content; editing.value = false; drawerVisible.value = true }
function openFile(file: InfoFile) { drawerKind.value = '文件'; drawerTitle.value = file.name; drawerFile.value = file; drawerMessage.value = null; draft.value = file.content; editing.value = false; drawerVisible.value = true }
function save() {
  if (drawerKind.value === '消息' && drawerMessage.value) emit('edit', { kind: '消息', chatId: chat.value.id, recordId: drawerMessage.value.id, content: draft.value })
  if (drawerKind.value === '文件' && drawerFile.value) emit('edit', { kind: '文件', chatId: chat.value.id, recordId: drawerFile.value.id, content: draft.value })
  editing.value = false
}
</script>

<style lang="less" scoped>
.wk-page { width: min(1120px, 100%); margin: 0 auto; padding: 30px 34px 56px; }
.conversation-head { margin-bottom: 14px; }
.wk-breadcrumb { display: flex; align-items: center; gap: 4px; margin-bottom: 15px; color: var(--td-text-color-secondary); font-size: 12px; }
.wk-breadcrumb button { padding: 0; border: 0; color: var(--td-brand-color); background: transparent; cursor: pointer; }
.wk-breadcrumb svg { width: 13px; }
.conversation-title-row { display: flex; align-items: center; gap: 14px; }
.conversation-title-row > div { flex: 1; min-width: 0; }
.conversation-title-row h1 { margin: 0 0 6px; font-size: 24px; font-weight: 600; }
.conversation-title-row p { margin: 0; color: var(--td-text-color-secondary); font-size: 12px; }
.chat-status { display: inline-flex; align-items: center; gap: 5px; color: var(--td-text-color-placeholder); font-size: 11px; white-space: nowrap; }
.chat-status i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.chat-status--collecting { color: var(--td-success-color); }
.chat-status--paused { color: var(--td-warning-color); }
.conversation-meta { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 23px; color: var(--td-text-color-placeholder); font-size: 11px; }
.conversation-meta span { display: inline-flex; align-items: center; gap: 5px; }
.conversation-meta svg { width: 13px; }
.wk-panel { overflow: hidden; border: 1px solid var(--td-component-stroke); border-radius: 8px; background: var(--td-bg-color-container); }
.conversation-list__tabs { display: flex; align-items: center; justify-content: space-between; min-height: 64px; padding: 0 20px; border-bottom: 1px solid var(--td-component-stroke); }
.conversation-tab { align-self: stretch; padding: 0 4px; border: 0; border-bottom: 3px solid transparent; color: var(--td-text-color-secondary); background: transparent; font-size: 16px; cursor: pointer; }
.conversation-tab--active { border-bottom-color: var(--td-brand-color); color: var(--td-text-color-primary); font-weight: 600; }
.conversation-tab span { color: var(--td-text-color-secondary); font-weight: 400; }
.conversation-list__hint { color: var(--td-text-color-placeholder); font-size: 11px; }
.conversation-table__head, .conversation-row { display: grid; grid-template-columns: minmax(300px, 2.2fr) .9fr .7fr .9fr 1.1fr 1fr; align-items: center; gap: 18px; }
.conversation-table__head { min-height: 58px; padding: 0 20px; color: var(--td-text-color-secondary); font-size: 12px; }
.conversation-row { width: 100%; min-height: 76px; padding: 12px 20px; border: 0; border-top: 1px solid var(--td-component-stroke); color: var(--td-text-color-primary); background: var(--td-bg-color-container); text-align: left; cursor: pointer; }
.conversation-row:hover { background: var(--td-bg-color-container-hover); }
.conversation-cell { min-width: 0; overflow: hidden; color: var(--td-text-color-secondary); font-size: 12px; }
.conversation-cell--name { display: flex; align-items: center; gap: 11px; color: var(--td-text-color-primary); }
.conversation-cell--name > svg { flex: 0 0 21px; width: 21px; height: 21px; color: var(--td-brand-color); }
.conversation-cell--name > span { min-width: 0; }
.conversation-cell--name strong, .conversation-cell--name small { display: block; overflow: hidden; text-overflow: ellipsis; }
.conversation-cell--name strong { display: -webkit-box; max-height: 42px; white-space: normal; -webkit-box-orient: vertical; -webkit-line-clamp: 2; font-size: 13px; line-height: 1.6; }
.conversation-cell--name small { margin-top: 3px; color: var(--td-text-color-placeholder); font-size: 11px; white-space: nowrap; }
.conversation-cell--muted { color: var(--td-text-color-placeholder); }
.conversation-status, .conversation-type { display: inline-flex; align-items: center; min-height: 25px; padding: 3px 9px; border-radius: 6px; font-style: normal; white-space: nowrap; }
.conversation-status { color: var(--td-success-color); background: var(--td-success-color-1); }
.conversation-type { color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); }
.wk-empty-inline { padding: 70px 20px; color: var(--td-text-color-placeholder); text-align: center; font-size: 12px; }
.wk-preview { padding: 2px 0 8px; }
.wk-preview__meta { display: flex; align-items: center; gap: 9px; margin-bottom: 18px; color: var(--td-text-color-secondary); font-size: 11px; }
.wk-file-preview { display: flex; align-items: center; gap: 10px; margin-bottom: 15px; padding: 13px; border: 1px solid var(--td-component-stroke); border-radius: 6px; background: var(--td-bg-color-secondarycontainer); }
.wk-file-preview > svg { width: 24px; color: var(--td-warning-color); }
.wk-file-preview strong, .wk-file-preview small { display: block; }
.wk-file-preview strong { font-size: 13px; }
.wk-file-preview small { margin-top: 4px; color: var(--td-text-color-secondary); font-size: 11px; }
.drawer-facts { display: grid; grid-template-columns: 1fr 1fr; gap: 7px; margin-bottom: 14px; padding: 10px 12px; border: 1px solid var(--td-component-stroke); border-radius: 6px; background: var(--td-bg-color-secondarycontainer); font-size: 11px; }
.drawer-facts span { display: flex; gap: 5px; color: var(--td-text-color-secondary); }
.drawer-facts b { color: var(--td-text-color-placeholder); font-weight: 400; }
.wk-preview__actions { display: flex; gap: 8px; margin-top: 15px; }
@media (max-width: 850px) {
  .conversation-table__head { display: none; }
  .conversation-row { grid-template-columns: 1fr 1fr; gap: 10px 16px; padding: 15px 17px; }
  .conversation-cell--name { grid-column: 1 / -1; }
  .conversation-cell::before { display: block; margin-bottom: 3px; color: var(--td-text-color-placeholder); font-size: 10px; content: attr(data-label); }
  .conversation-cell--name::before { display: none; }
}
@media (max-width: 760px) {
  .wk-page { padding: 22px 16px 45px; }
  .conversation-title-row { align-items: flex-start; flex-wrap: wrap; }
  .conversation-title-row .chat-status { margin-left: auto; }
  .conversation-list__tabs { padding: 0 14px; }
  .conversation-tab { font-size: 14px; }
  .conversation-list__hint { display: none; }
}
</style>
