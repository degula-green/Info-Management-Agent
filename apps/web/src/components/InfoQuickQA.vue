<template>
  <!-- WeKnora creatChat.vue layout adapted to local mock data. -->
  <section class="dialogue-wrap">
    <div class="dialogue-answers">
      <div class="dialogue-title">
        <span class="dialogue-title__logo"><img src="../assets/img/weknora.png" alt="" /></span>
        <span>AI 问答</span>
      </div>
      <p class="dialogue-subtitle">快速模式：只基于已采集的消息和文件回答。</p>

      <div class="scope-panel">
        <div class="scope-panel__header">
          <span>回答范围</span>
          <small>选择群聊</small>
        </div>
        <t-checkbox-group v-model="selected" class="scope-panel__list">
          <t-checkbox v-for="chat in chats" :key="chat.id" :value="chat.id" class="scope-item">
            <span class="scope-item__body">
              <strong>{{ chat.name }}</strong>
              <small>{{ sourceName(chat.source) }} · {{ chat.messages.length }} 条消息</small>
            </span>
          </t-checkbox>
        </t-checkbox-group>
        <span v-if="!selected.length" class="scope-panel__hint">未选择时使用全部已绑定群聊</span>
      </div>

      <div class="suggested-questions-container">
        <div class="suggested-questions-inner">
          <div class="suggested-questions-title-row">
            <p class="suggested-questions-caption">
              <span class="suggested-questions-title">推荐问题</span>
              <button type="button" class="suggested-questions-refresh" title="刷新推荐问题" aria-label="刷新推荐问题" @click="refreshQuestions">
                <t-icon name="refresh" />
              </button>
            </p>
          </div>
          <div class="suggested-questions-grid">
            <button v-for="questionItem in suggestedQuestions" :key="questionItem" type="button" class="suggested-question-card" @click="askSuggested(questionItem)">
              <span class="suggested-question-text">{{ questionItem }}</span>
            </button>
          </div>
        </div>
      </div>

      <div class="answers-input">
        <div v-if="messages.length > 1" class="answer-history">
          <div v-for="(message, index) in messages" :key="index" :class="['answer-message', message.role]">
            <span v-if="message.role === 'assistant'" class="answer-message__avatar"><t-icon name="robot" /></span>
            <p>{{ message.text }}</p>
          </div>
        </div>
        <div class="answers-input__composer">
          <t-textarea v-model="question" placeholder="请输入问题，按 Ctrl + Enter 发送" :autosize="{ minRows: 2, maxRows: 5 }" @keydown.ctrl.enter.prevent="ask" />
          <div class="answers-input__footer">
            <span class="answers-input__mode"><t-icon name="file-search" />文档快速模式</span>
            <t-button theme="primary" :disabled="!question.trim()" @click="ask">
              <template #icon><t-icon name="send" /></template>发送
            </t-button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import type { InfoChat } from '../mock'
import { sourceName } from '../mock'

const props = defineProps<{ chats: InfoChat[] }>()
const question = ref('')
const selected = ref<string[]>(['feishu-product'])
const suggestedQuestions = ref([
  '本周有哪些需要跟进的事项？',
  '产品讨论组最近的结论是什么？',
  '找出最近提到的会议和时间。',
  '哪些消息包含待办或风险？',
])
const messages = ref<{ role: 'assistant' | 'user'; text: string }[]>([
  { role: 'assistant', text: '选择需要参考的群聊后，输入问题。我会只基于已采集的消息和文件回答。' },
])

function askSuggested(text: string) {
  question.value = text
  ask()
}

function refreshQuestions() {
  suggestedQuestions.value = [...suggestedQuestions.value.slice(1), suggestedQuestions.value[0]]
}

function ask() {
  const text = question.value.trim()
  if (!text) return
  messages.value.push({ role: 'user', text })
  const scope = selected.value.map((id) => props.chats.find((chat) => chat.id === id)?.name).filter(Boolean).join('、') || '已绑定的全部群聊'
  messages.value.push({ role: 'assistant', text: `基于「${scope}」的本地示例资料，已找到与“${text}”相关的内容。正式接入后，这里会展示带来源的回答。` })
  question.value = ''
}
</script>

<style lang="less" scoped>
@import './css/suggested-questions.less';

.dialogue-wrap { display: flex; justify-content: center; min-height: 100%; padding: 32px 34px 56px; box-sizing: border-box; }
.dialogue-answers { display: flex; flex-flow: column; align-items: center; width: 100%; max-width: 960px; gap: 14px; }
.dialogue-title { display: flex; align-items: center; gap: 11px; margin-top: 8px; color: var(--td-text-color-primary); font-family: var(--app-font-family); font-size: 28px; font-weight: 600; }
.dialogue-title__logo { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 7px; background: var(--td-bg-color-container); box-shadow: var(--td-shadow-1); }.dialogue-title__logo img { width: 24px; height: 24px; object-fit: contain; }
.dialogue-subtitle { margin: -2px 0 2px; color: var(--td-text-color-secondary); font-size: 13px; }
.scope-panel { width: min(860px, 100%); padding: 13px 16px 10px; border: 1px solid var(--td-component-stroke); border-radius: 8px; background: var(--td-bg-color-container); box-sizing: border-box; }
.scope-panel__header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 7px; color: var(--td-text-color-primary); font-size: 12px; font-weight: 600; }.scope-panel__header small { color: var(--td-text-color-placeholder); font-size: 11px; font-weight: 400; }
.scope-panel__list { display: flex; flex-wrap: wrap; gap: 6px 18px; }.scope-item { margin: 0; }.scope-item__body { display: inline-flex; flex-direction: column; line-height: 1.35; }.scope-item__body strong { font-size: 12px; font-weight: 500; }.scope-item__body small { color: var(--td-text-color-secondary); font-size: 10px; }.scope-panel__hint { display: block; margin-top: 6px; color: var(--td-text-color-placeholder); font-size: 11px; }
.suggested-questions-container { padding: 8px 16px 4px; }.suggested-questions-title-row { margin-bottom: 10px; }.suggested-questions-grid { gap: 9px; }.suggested-question-card { appearance: none; font: inherit; }.suggested-question-text { white-space: normal; }
.answers-input { width: min(860px, 100%); padding: 0; position: static; transform: none; }.answers-input__composer { border: 1px solid var(--td-component-stroke); border-radius: 10px; background: var(--td-bg-color-container); box-shadow: var(--td-shadow-1); overflow: hidden; }.answers-input__composer :deep(.t-textarea__inner) { min-height: 76px; padding: 13px 14px; border: 0; resize: none; box-shadow: none; }.answers-input__footer { display: flex; justify-content: space-between; align-items: center; padding: 8px 10px 9px 14px; border-top: 1px solid var(--td-component-stroke); }.answers-input__mode { display: inline-flex; align-items: center; gap: 5px; color: var(--td-text-color-placeholder); font-size: 11px; }.answers-input__mode svg { width: 14px; }.answers-input__footer .t-button { min-height: 32px; }
.answer-history { width: 100%; max-height: 300px; overflow-y: auto; margin-bottom: 12px; padding: 0 8px; box-sizing: border-box; }.answer-message { display: flex; align-items: flex-start; gap: 8px; max-width: 82%; margin: 0 0 11px; }.answer-message.user { justify-content: flex-end; margin-left: auto; }.answer-message p { margin: 0; padding: 9px 12px; border-radius: 7px; color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); font-size: 12px; line-height: 1.65; }.answer-message.user p { color: #fff; background: var(--td-brand-color); }.answer-message__avatar { display: grid; place-items: center; flex: 0 0 25px; width: 25px; height: 25px; border-radius: 50%; color: var(--td-brand-color); background: var(--td-brand-color-1); }.answer-message__avatar svg { width: 14px; }
@media (max-width: 760px) { .dialogue-wrap { padding: 22px 16px 45px; }.dialogue-title { font-size: 23px; }.scope-panel__list { display: grid; grid-template-columns: 1fr; gap: 6px; }.suggested-questions-container { padding-left: 0; padding-right: 0; }.suggested-questions-grid { justify-content: stretch; }.suggested-question-card { width: 100%; }.answers-input { width: 100%; } }
</style>
