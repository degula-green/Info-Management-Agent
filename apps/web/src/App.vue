<script setup lang="ts">
import { ref } from 'vue'

const coreStatus = ref('未检查')
const ragStatus = ref('未检查')

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
</script>

<template>
  <main class="page">
    <section class="hero">
      <p class="eyebrow">INFO-AGENT</p>
      <h1>信息管理 Agent</h1>
      <p class="description">Vue Web 前端骨架，统一连接核心业务与 RAG 服务。</p>
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
    </section>
  </main>
</template>
