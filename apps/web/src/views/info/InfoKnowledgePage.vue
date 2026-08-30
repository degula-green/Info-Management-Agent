<template>
  <InfoKnowledgeBases :sources="store.sources" :initial-source-key="sourceKey" @profile="router.push('/profile')" @home="router.push('/knowledge')" @chat="openChat" @open-source="openSource" @access="accessSession" @toast="toast" />
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import InfoKnowledgeBases from '@/components/InfoKnowledgeBases.vue'
import { useInfoMockStore } from '@/stores/infoMock'
const route = useRoute(); const router = useRouter(); const store = useInfoMockStore(); const sourceKey = computed(() => String(route.params.platform) as 'feishu' | 'wecom' | 'wechat')
function openChat(chat: { source: string; id: string }) { router.push(`/knowledge/${chat.source}/conversations/${chat.id}`) }
function openSource(key: string) { if (key !== sourceKey.value) router.push(`/knowledge/${key}`) }
function accessSession(source: 'feishu' | 'wecom' | 'wechat', sessionId: string) { const chat = store.accessSession(source, sessionId); if (chat) toast(`已接入「${chat.name}」`) }
function toast(text: string) { MessagePlugin.success(text) }
</script>
