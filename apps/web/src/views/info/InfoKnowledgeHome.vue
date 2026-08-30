<template><InfoKnowledgeBases :sources="store.sources" @profile="router.push('/profile')" @open-source="openSource" /></template>
<script setup lang="ts">
import { useRouter } from 'vue-router'
import InfoKnowledgeBases from '@/components/InfoKnowledgeBases.vue'
import { useInfoMockStore } from '@/stores/infoMock'
const router = useRouter(); const store = useInfoMockStore(); function openSource(key: string) { router.push(`/knowledge/${key}`) }
</script>
