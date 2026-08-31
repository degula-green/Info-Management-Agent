<template>
  <InfoConversation v-if="chat" :chat="chat" @back="router.push(`/knowledge/${chat.source}`)" @toggle="toggleChat" @edit="saveEdit" @toast="toast" />
  <div v-else-if="loading" class="conversation-missing"><t-icon name="loading" size="28px" /><h3>正在加载会话</h3><p>正在从 Core 同步消息和附件。</p></div>
  <div v-else class="conversation-missing"><t-icon name="error-circle" size="28px" /><h3>找不到这个会话</h3><t-button theme="primary" @click="router.push('/knowledge')">返回知识库</t-button></div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import InfoConversation from '@/components/InfoConversation.vue'
import { normalizeSourceKey, useInfoKnowledgeStore } from '@/stores/infoKnowledge'
const route = useRoute(); const router = useRouter(); const store = useInfoKnowledgeStore()
const sourceKey = computed(() => normalizeSourceKey(String(route.params.platform)) || 'wechat')
const conversationId = computed(() => String(route.params.conversationId))
const chat = computed(() => store.findConversation(sourceKey.value, conversationId.value))
const loading = ref(true)
const pollTimer = ref<number | null>(null)
async function toggleChat(current: any) {
  if (current.collectionStatus === 'collecting') {
    await store.pauseConversation(sourceKey.value, current.externalId || current.id)
    toast('已暂停采集')
    return
  }
  await store.accessSession(sourceKey.value, current.externalId || current.id, current.historyStart || new Date().toISOString().slice(0, 10))
  await store.loadConversation(sourceKey.value, conversationId.value, true)
  toast('已恢复采集')
}
function saveEdit(payload: { kind: string; chatId: string; recordId: string; content: string }) {
  if (payload.kind === '消息') store.updateMessage(payload.chatId, payload.recordId, payload.content)
  else store.updateFile(payload.chatId, payload.recordId, payload.content)
  toast('内容已更新')
}
function toast(text: string) { MessagePlugin.success(text) }
async function loadCurrentConversation(platform: string, id: string, force = false) {
  loading.value = true
  try {
    // The shell refreshes conversation summaries globally. Do not force a
    // second summary refresh here, because it would temporarily clear detail
    // messages while this page is loading them.
    await store.ensureSources(false)
    await store.loadConversation(platform as 'feishu' | 'wecom' | 'wechat', id, force)
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  await loadCurrentConversation(sourceKey.value, conversationId.value)
  pollTimer.value = window.setInterval(() => {
    void loadCurrentConversation(sourceKey.value, conversationId.value, true)
  }, 30000)
})
watch([sourceKey, conversationId], async ([platform, id]) => {
  await loadCurrentConversation(platform, id, true)
})
onBeforeUnmount(() => {
  if (pollTimer.value != null) window.clearInterval(pollTimer.value)
})
</script>
<style scoped lang="less">.conversation-missing { display: grid; place-items: center; min-height: 360px; gap: 10px; color: var(--td-text-color-secondary); text-align: center; }.conversation-missing h3 { margin: 0; color: var(--td-text-color-primary); }.conversation-missing p { margin: 0; font-size: 12px; }</style>
