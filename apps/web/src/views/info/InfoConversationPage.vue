<template>
  <InfoConversation v-if="chat" :chat="chat" @back="router.push(`/knowledge/${chat.source}`)" @toggle="toggleChat" @edit="saveEdit" @toast="toast" />
  <div v-else class="conversation-missing"><t-icon name="error-circle" size="28px" /><h3>找不到这个会话</h3><t-button theme="primary" @click="router.push('/knowledge')">返回知识库</t-button></div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import InfoConversation from '@/components/InfoConversation.vue'
import { useInfoMockStore } from '@/stores/infoMock'
const route = useRoute(); const router = useRouter(); const store = useInfoMockStore(); const chat = computed(() => store.findChat(String(route.params.conversationId)))
function toggleChat(current: any) { const next = current.collectionStatus === 'collecting' ? 'paused' : 'collecting'; store.setCollection(current, next, current.historyStart || new Date().toISOString().slice(0, 10)); toast(next === 'collecting' ? '已恢复采集' : '已暂停采集') }
function saveEdit(payload: { kind: string; chatId: string; recordId: string; content: string }) { if (payload.kind === '消息') store.updateMessage(payload.chatId, payload.recordId, payload.content); else store.updateFile(payload.chatId, payload.recordId, payload.content); toast('内容已更新到本地 Mock 数据') }
function toast(text: string) { MessagePlugin.success(text) }
</script>
<style scoped lang="less">.conversation-missing { display: grid; place-items: center; min-height: 360px; gap: 10px; color: var(--td-text-color-secondary); text-align: center; }.conversation-missing h3 { margin: 0; color: var(--td-text-color-primary); }</style>
