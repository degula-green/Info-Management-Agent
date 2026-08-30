<template>
  <!-- WeKnora new-chat surface adapted to the Info Agent's fixed platform knowledge bases. -->
  <section ref="pageRef" class="qa-chat-page" @keydown.esc="closeMenus">
    <div ref="scrollRef" class="qa-chat-scroll">
      <div v-if="!messages.length" class="qa-welcome">
        <h1>开始新的对话</h1>
        <p>向你的知识库提问，获取回答与分析</p>
      </div>

      <div v-else class="qa-transcript">
        <h1 class="qa-transcript__title">{{ conversationTitle }}</h1>
        <div v-for="(message, index) in messages" :key="index" :class="['qa-message', `qa-message--${message.role}`]">
          <div v-if="message.role === 'user'" class="qa-user-bubble">{{ message.text }}</div>
          <div v-else class="qa-answer">
            <p>{{ message.text }}</p>
            <div class="qa-answer__meta"><t-icon name="file" />{{ scopeLabel }} · {{ modeLabel }} · 本地示例资料</div>
            <div class="qa-answer__actions" aria-label="回答操作">
              <button type="button" title="复制回答" aria-label="复制回答" @click="copyAnswer(message.text)"><t-icon name="file-copy" /></button>
              <button type="button" title="编辑问题" aria-label="编辑问题" @click="editQuestion(message.text)"><t-icon name="edit-1" /></button>
              <button type="button" title="反馈回答" aria-label="反馈回答" @click="reportAnswer"><t-icon name="error-circle" /></button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="qa-composer-area">
      <div class="qa-composer" :class="{ 'qa-composer--focused': focused }">
        <t-textarea ref="textareaRef" v-model="question" class="qa-textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="请输入您想要咨询的问题或需要帮助的内容..." @focus="focused = true" @blur="focused = false" @keydown.ctrl.enter.prevent="sendQuestion" />
        <div class="qa-controls">
          <div class="qa-controls__left">
            <div class="qa-control-wrap">
              <button type="button" class="qa-control" :aria-expanded="kbMenuOpen" @mousedown.prevent @click="toggleKbMenu">
                <t-icon name="book-open" /><span>{{ kbLabel }}</span><t-icon name="chevron-down" class="qa-control__arrow" />
              </button>
              <div v-if="kbMenuOpen" class="qa-dropdown qa-kb-dropdown" role="menu" aria-label="知识库范围">
                <div class="qa-dropdown__heading">知识库范围</div>
                <p class="qa-dropdown__hint">选择回答时使用的平台知识库</p>
                <button type="button" class="qa-kb-option" :class="{ selected: scope === 'all' }" role="menuitemradio" :aria-checked="scope === 'all'" @click="chooseScope('all')">
                  <span class="qa-check"><t-icon v-if="scope === 'all'" name="check" /></span><span class="qa-kb-option__name">全部知识库</span><span class="qa-kb-option__count">{{ totalCount }} 条</span>
                </button>
                <button v-for="source in store.sources" :key="source.key" type="button" class="qa-kb-option" :class="{ selected: scope === source.key, disabled: !source.bound }" role="menuitemradio" :aria-checked="scope === source.key" :disabled="!source.bound" @click="chooseScope(source.key)">
                  <span class="qa-check"><t-icon v-if="scope === source.key" name="check" /></span><span class="qa-kb-option__name">{{ source.name }}</span><span class="qa-kb-option__count">{{ sourceCount(source) }} 条<span v-if="!source.bound"> · 未绑定</span></span>
                </button>
              </div>
            </div>

            <div class="qa-control-wrap">
              <button type="button" class="qa-control" :aria-expanded="modeMenuOpen" @mousedown.prevent @click="toggleModeMenu">
                <t-icon name="lightning" /><span>{{ modeLabel }}</span><t-icon name="chevron-down" class="qa-control__arrow" />
              </button>
              <div v-if="modeMenuOpen" class="qa-dropdown qa-mode-dropdown" role="menu" aria-label="回答模式">
                <button type="button" class="qa-mode-option" :class="{ selected: mode === 'quick' }" role="menuitemradio" :aria-checked="mode === 'quick'" @click="chooseMode('quick')"><t-icon name="lightning" /><span><strong>快速模式</strong><small>直接检索知识库</small></span><t-icon v-if="mode === 'quick'" name="check" class="qa-mode-option__check" /></button>
                <button type="button" class="qa-mode-option" :class="{ selected: mode === 'deep' }" role="menuitemradio" :aria-checked="mode === 'deep'" @click="chooseMode('deep')"><t-icon name="component-breadcrumb" /><span><strong>深度模式</strong><small>理解上下文，拆分问题</small></span><t-icon v-if="mode === 'deep'" name="check" class="qa-mode-option__check" /></button>
              </div>
            </div>

            <button type="button" class="qa-control qa-image-control" @click="imageHint"><t-icon name="image" /><span>图片</span></button>
          </div>
          <button type="button" class="qa-send" :class="{ disabled: !question.trim() }" :disabled="!question.trim()" aria-label="发送问题" @click="sendQuestion"><t-icon name="arrow-up" /></button>
        </div>
      </div>
      <p class="qa-disclaimer">内容由 AI 生成，仅供参考</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { sourceName, type InfoSource, type SourceKey } from '@/mock'
import { useInfoMockStore } from '@/stores/infoMock'

type Scope = SourceKey | 'all'
type Mode = 'quick' | 'deep'
type QaMessage = { role: 'user' | 'assistant'; text: string }

const store = useInfoMockStore()
const question = ref('')
const scope = ref<Scope>('all')
const mode = ref<Mode>('quick')
const messages = ref<QaMessage[]>([])
const kbMenuOpen = ref(false)
const modeMenuOpen = ref(false)
const focused = ref(false)
const pageRef = ref<HTMLElement | null>(null)
const scrollRef = ref<HTMLElement | null>(null)
const textareaRef = ref<{ focus?: () => void } | null>(null)

const scopeLabel = computed(() => scope.value === 'all' ? '全部知识库' : sourceName(scope.value))
const kbLabel = computed(() => scopeLabel.value)
const modeLabel = computed(() => mode.value === 'quick' ? '快速' : '深度')
const conversationTitle = computed(() => messages.value.find((message) => message.role === 'user')?.text || '新的对话')
const totalCount = computed(() => store.allChats.reduce((sum, chat) => sum + chat.messages.length + chat.files.length, 0))

function sourceCount(source: InfoSource) {
  return source.chats.reduce((sum, chat) => sum + chat.messages.length + chat.files.length, 0)
}

function closeMenus() {
  kbMenuOpen.value = false
  modeMenuOpen.value = false
}

function toggleKbMenu() {
  modeMenuOpen.value = false
  kbMenuOpen.value = !kbMenuOpen.value
}

function toggleModeMenu() {
  kbMenuOpen.value = false
  modeMenuOpen.value = !modeMenuOpen.value
}

function chooseScope(nextScope: Scope) {
  if (nextScope !== 'all' && !store.findSource(nextScope)?.bound) {
    MessagePlugin.warning(`请先在个人中心绑定${sourceName(nextScope)}`)
    return
  }
  scope.value = nextScope
  kbMenuOpen.value = false
}

function chooseMode(nextMode: Mode) {
  mode.value = nextMode
  modeMenuOpen.value = false
}

function imageHint() {
  MessagePlugin.info('图片上传将在接口接入后开放')
}

async function copyAnswer(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    MessagePlugin.success('回答已复制')
  } catch {
    MessagePlugin.info('复制功能将在接口接入后开放')
  }
}

function editQuestion(text: string) {
  question.value = text
  nextTick(() => textareaRef.value?.focus?.())
}

function reportAnswer() {
  MessagePlugin.info('反馈功能将在接口接入后开放')
}

async function sendQuestion() {
  const text = question.value.trim()
  if (!text) return
  closeMenus()
  messages.value.push({ role: 'user', text })
  const sourceText = scope.value === 'all' ? '全部已绑定知识库' : `${scopeLabel.value}知识库`
  const answer = mode.value === 'quick'
    ? `基于「${sourceText}」的本地示例资料，已找到与“${text}”相关的内容。`
    : `已按深度模式拆分“${text}”，并在「${sourceText}」中完成本地示例检索。`
  messages.value.push({ role: 'assistant', text: `${answer} 正式接入后，这里会展示带来源引用的回答。` })
  store.qaSessions.unshift({ id: `qa-${Date.now()}`, question: text, answer, summary: `${sourceText} · ${modeLabel.value}`, source: scopeLabel.value as '全部数据' | '飞书' | '企业微信' | '个人微信', time: '刚刚' })
  question.value = ''
  await nextTick()
  scrollRef.value?.scrollTo({ top: scrollRef.value.scrollHeight, behavior: 'smooth' })
}

function onDocumentPointerdown(event: PointerEvent) {
  if (!pageRef.value?.contains(event.target as Node)) closeMenus()
}

onMounted(() => document.addEventListener('pointerdown', onDocumentPointerdown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocumentPointerdown))
</script>

<style lang="less" scoped>
.qa-chat-page { position: relative; display: flex; flex-direction: column; width: 100%; height: 100%; min-height: 620px; background: var(--td-bg-color-container); }
.qa-chat-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 32px 34px 200px; }
.qa-welcome { display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 100%; padding-bottom: 120px; text-align: center; }.qa-welcome h1 { margin: 0 0 12px; color: var(--td-text-color-primary); font-size: 36px; font-weight: 600; letter-spacing: -0.02em; }.qa-welcome p { margin: 0; color: var(--td-text-color-secondary); font-size: 16px; }
.qa-transcript { width: min(960px, 100%); margin: 0 auto; padding-bottom: 20px; }.qa-transcript__title { margin: 8px 0 42px; color: var(--td-text-color-primary); font-size: 24px; font-weight: 600; }.qa-message { display: flex; width: 100%; margin-bottom: 28px; }.qa-message--user { justify-content: flex-end; }.qa-user-bubble { max-width: min(620px, 75%); padding: 11px 15px; border-radius: 14px; color: var(--td-text-color-primary); background: var(--td-bg-color-secondarycontainer); font-size: 14px; line-height: 1.6; }.qa-answer { max-width: 760px; padding-left: 2px; }.qa-answer p { margin: 0; color: var(--td-text-color-primary); font-size: 15px; line-height: 1.8; }.qa-answer__meta { display: inline-flex; align-items: center; gap: 5px; margin-top: 13px; padding: 5px 8px; border-radius: 5px; color: var(--td-brand-color); background: var(--td-brand-color-1); font-size: 11px; }.qa-answer__meta svg { width: 13px; }.qa-answer__actions { display: flex; align-items: center; gap: 7px; margin-top: 12px; }.qa-answer__actions button { display: inline-grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 6px; color: var(--td-text-color-placeholder); background: transparent; cursor: pointer; }.qa-answer__actions button:hover { color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); }.qa-answer__actions svg { width: 16px; height: 16px; }
.qa-composer-area { position: absolute; z-index: 5; right: 0; bottom: 0; left: 0; display: flex; flex-direction: column; align-items: center; padding: 0 24px 22px; pointer-events: none; background: linear-gradient(to top, var(--td-bg-color-container) 62%, transparent); }.qa-composer, .qa-disclaimer { pointer-events: auto; }.qa-composer { position: relative; width: min(960px, 100%); border: 1px solid var(--td-component-stroke); border-radius: 14px; background: var(--td-bg-color-container); box-shadow: 0 2px 8px rgba(0, 0, 0, .04), 0 8px 16px -4px rgba(0, 0, 0, .06); transition: border-color .15s, box-shadow .15s; }.qa-composer--focused { border-color: var(--td-brand-color); box-shadow: 0 0 0 3px var(--td-brand-color-focus), 0 8px 18px -8px rgba(0, 0, 0, .18); }.qa-textarea :deep(.t-textarea__inner) { min-height: 112px; padding: 16px 18px 56px; border: 0; border-radius: 14px; resize: none; color: var(--td-text-color-primary); font-size: 16px; line-height: 1.5; box-shadow: none; }.qa-textarea :deep(.t-textarea__inner:focus) { box-shadow: none; }.qa-textarea :deep(.t-textarea__inner::placeholder) { color: var(--td-text-color-placeholder); font-size: 16px; }
.qa-controls { position: absolute; right: 14px; bottom: 12px; left: 14px; display: flex; align-items: center; justify-content: space-between; gap: 10px; }.qa-controls__left { display: flex; align-items: center; gap: 8px; min-width: 0; }.qa-control-wrap { position: relative; }.qa-control { display: inline-flex; align-items: center; gap: 7px; min-height: 34px; padding: 6px 8px; border: 0; border-radius: 8px; color: var(--td-text-color-primary); background: transparent; font-size: 15px; white-space: nowrap; cursor: pointer; }.qa-control:hover, .qa-control[aria-expanded='true'] { background: var(--td-bg-color-secondarycontainer); }.qa-control > svg:first-child { width: 18px; height: 18px; }.qa-control__arrow { width: 13px; color: var(--td-text-color-secondary); }.qa-image-control { padding-right: 10px; }
.qa-send { display: grid; place-items: center; width: 38px; height: 38px; padding: 0; border: 0; border-radius: 50%; color: #fff; background: var(--td-brand-color); cursor: pointer; }.qa-send:hover:not(.disabled) { background: var(--td-brand-color-hover); }.qa-send.disabled { color: #fff; background: var(--td-brand-color-disabled); cursor: not-allowed; }.qa-send svg { width: 21px; height: 21px; }
.qa-dropdown { position: absolute; z-index: 20; bottom: calc(100% + 9px); left: 0; width: 320px; padding: 14px 13px; border: 1px solid var(--td-component-stroke); border-radius: 12px; background: var(--td-bg-color-container); box-shadow: var(--td-shadow-2); }.qa-dropdown__heading { color: var(--td-text-color-primary); font-size: 16px; font-weight: 600; }.qa-dropdown__hint { margin: 4px 0 12px; color: var(--td-text-color-secondary); font-size: 12px; }.qa-kb-option { display: flex; align-items: center; gap: 9px; width: 100%; min-height: 39px; padding: 7px 8px; border: 0; border-radius: 7px; color: var(--td-text-color-primary); background: transparent; text-align: left; cursor: pointer; }.qa-kb-option:hover:not(:disabled), .qa-kb-option.selected { background: var(--td-bg-color-secondarycontainer); }.qa-kb-option:disabled { color: var(--td-text-color-disabled); cursor: not-allowed; }.qa-check { display: grid; place-items: center; width: 19px; height: 19px; border: 1px solid var(--td-component-border); border-radius: 5px; color: #fff; background: transparent; }.qa-kb-option.selected .qa-check { border-color: var(--td-brand-color); background: var(--td-brand-color); }.qa-check svg { width: 13px; }.qa-kb-option__name { flex: 1; font-size: 14px; }.qa-kb-option__count { color: var(--td-text-color-secondary); font-size: 11px; }
.qa-mode-dropdown { width: 245px; padding: 8px; }.qa-mode-option { display: flex; align-items: flex-start; gap: 11px; width: 100%; padding: 12px 10px; border: 0; border-radius: 9px; color: var(--td-text-color-primary); background: transparent; text-align: left; cursor: pointer; }.qa-mode-option:hover, .qa-mode-option.selected { background: var(--td-bg-color-secondarycontainer); }.qa-mode-option > svg:first-child { width: 20px; height: 20px; margin-top: 1px; }.qa-mode-option span { display: flex; flex: 1; flex-direction: column; gap: 3px; }.qa-mode-option strong { font-size: 15px; font-weight: 500; }.qa-mode-option small { color: var(--td-text-color-secondary); font-size: 12px; }.qa-mode-option__check { width: 16px !important; color: var(--td-brand-color); }
.qa-disclaimer { margin: 10px 0 0; color: var(--td-text-color-placeholder); font-size: 12px; }
@media (max-width: 760px) { .qa-chat-page { min-height: 560px; }.qa-chat-scroll { padding: 24px 16px 185px; }.qa-welcome { padding-bottom: 80px; }.qa-welcome h1 { font-size: 28px; }.qa-welcome p { font-size: 14px; }.qa-composer-area { padding: 0 12px 14px; }.qa-composer { border-radius: 12px; }.qa-textarea :deep(.t-textarea__inner) { min-height: 100px; padding: 13px 14px 58px; font-size: 14px; }.qa-textarea :deep(.t-textarea__inner::placeholder) { font-size: 14px; }.qa-controls { right: 9px; bottom: 9px; left: 9px; }.qa-control { min-height: 31px; padding: 5px 6px; font-size: 13px; }.qa-control__arrow { width: 11px; }.qa-image-control { display: none; }.qa-send { width: 35px; height: 35px; }.qa-dropdown { width: min(300px, calc(100vw - 28px)); }.qa-mode-dropdown { width: 225px; }.qa-user-bubble { max-width: 88%; font-size: 13px; }.qa-answer p { font-size: 14px; } }
</style>
