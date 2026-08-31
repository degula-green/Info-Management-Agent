<template>
  <div class="attachment-preview">
    <div v-if="loading" class="preview-state" role="status">
      <t-loading size="medium" />
      <span>正在加载源文件</span>
    </div>

    <div v-else-if="error" class="preview-state preview-state--error" role="alert">
      <t-icon name="error-circle" />
      <strong>源文件预览失败</strong>
      <span>{{ error }}</span>
      <t-button size="small" theme="primary" variant="outline" @click="loadPreview">重试</t-button>
    </div>

    <div v-else-if="previewType === 'docx'" ref="docxContainer" class="preview-shell preview-docx" />

    <div v-else-if="previewType === 'pptx' && pptxData" class="preview-shell preview-pptx">
      <VueOfficePptx :src="pptxData" @rendered="() => {}" @error="onPptxError" />
    </div>

    <iframe v-else-if="previewType === 'pdf' && blobUrl" class="pdf-preview" :src="blobUrl" :title="file.name" />

    <div v-else-if="previewType === 'image' && blobUrl" class="image-preview">
      <img :src="blobUrl" :alt="file.name" />
    </div>

    <div v-else-if="previewType === 'excel' && excelHtml" class="preview-shell preview-excel">
      <div class="excel-container" v-html="excelHtml" />
    </div>

    <div v-else-if="previewType === 'markdown' && markdownHtml" class="preview-shell preview-markdown">
      <div class="markdown-body" v-html="markdownHtml" />
    </div>

    <pre v-else-if="previewType === 'text'" class="text-preview">{{ textContent }}</pre>

    <div v-else-if="previewType === 'audio' && blobUrl" class="preview-shell preview-audio">
      <div class="audio-wrapper">
        <t-icon name="sound" size="44px" />
        <p class="audio-name">{{ file.name }}</p>
        <audio controls :src="blobUrl" class="audio-element">当前浏览器不支持音频播放</audio>
      </div>
    </div>

    <div v-else class="preview-state">
      <t-icon name="file-unknown" />
      <strong>暂不支持在线预览</strong>
      <span>可以下载源文件后使用本地应用打开</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, nextTick, onUnmounted, ref, shallowRef, watch } from 'vue'
import { getKnowledgeAttachmentContent } from '@/api/info-knowledge'
import type { InfoFile } from '@/mock'
import { sanitizeHTML, safeMarkdownToHTML } from '@/utils/security'

const VueOfficePptx = defineAsyncComponent(() => import('@vue-office/pptx'))

type PreviewType = 'docx' | 'pptx' | 'pdf' | 'image' | 'excel' | 'text' | 'markdown' | 'audio' | 'unsupported'

const props = defineProps<{
  file: InfoFile
  active: boolean
}>()

const loading = ref(false)
const error = ref('')
const previewType = ref<PreviewType>('unsupported')
const blobUrl = ref('')
const textContent = ref('')
const markdownHtml = ref('')
const excelHtml = ref('')
const pptxData = shallowRef<ArrayBuffer | null>(null)
const docxContainer = ref<HTMLElement | null>(null)
let loadedKey = ''
let requestVersion = 0

function extensionOf(file: InfoFile) {
  const declared = String(file.type || '').trim().toLowerCase().replace(/^\./, '')
  if (declared && declared !== 'document' && declared !== 'file') return declared
  const match = file.name.toLowerCase().match(/\.([a-z0-9]+)$/)
  return match?.[1] || ''
}

function resolvePreviewType(extension: string): PreviewType {
  const ext = extension.toLowerCase()
  if (ext === 'doc' || ext === 'docx') return 'docx'
  if (ext === 'ppt' || ext === 'pptx') return 'pptx'
  if (ext === 'pdf') return 'pdf'
  if (['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp', 'tiff', 'svg'].includes(ext)) return 'image'
  if (['xlsx', 'xls', 'csv'].includes(ext)) return 'excel'
  if (['md', 'markdown'].includes(ext)) return 'markdown'
  if (['mp3', 'wav', 'm4a', 'flac', 'ogg'].includes(ext)) return 'audio'
  if (['txt', 'json', 'xml', 'html', 'css', 'js', 'ts', 'py', 'java', 'go', 'cpp', 'c', 'h', 'sh', 'yaml', 'yml', 'ini', 'conf', 'log', 'sql', 'rs', 'rb', 'php', 'swift', 'kt', 'scala', 'r', 'lua', 'pl', 'toml'].includes(ext)) return 'text'
  return 'unsupported'
}

function getMimeType(ft: string): string {
  const map: Record<string, string> = {
    pdf: 'application/pdf',
    docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    doc: 'application/msword',
    pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    ppt: 'application/vnd.ms-powerpoint',
    xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    xls: 'application/vnd.ms-excel',
    csv: 'text/csv',
    jpg: 'image/jpeg', jpeg: 'image/jpeg',
    png: 'image/png', gif: 'image/gif', bmp: 'image/bmp',
    webp: 'image/webp', tiff: 'image/tiff', svg: 'image/svg+xml',
    txt: 'text/plain', md: 'text/markdown', markdown: 'text/markdown',
    json: 'application/json', xml: 'application/xml',
    html: 'text/html', css: 'text/css',
    js: 'text/javascript', ts: 'text/typescript',
    py: 'text/x-python', java: 'text/x-java', go: 'text/x-go',
    mp3: 'audio/mpeg', wav: 'audio/wav', m4a: 'audio/mp4',
    flac: 'audio/flac', ogg: 'audio/ogg',
  }
  return map[ft?.toLowerCase()] || 'application/octet-stream'
}

function ensureBlobType(blob: Blob, ft: string): Blob {
  const expected = getMimeType(ft)
  if (blob.type === expected) return blob
  return new Blob([blob], { type: expected })
}

function releasePreview() {
  if (blobUrl.value) URL.revokeObjectURL(blobUrl.value)
  blobUrl.value = ''
  textContent.value = ''
  markdownHtml.value = ''
  excelHtml.value = ''
  pptxData.value = null
  if (docxContainer.value) docxContainer.value.innerHTML = ''
}

async function renderDocx(blob: Blob) {
  const { renderAsync } = await import('docx-preview')
  await nextTick()
  if (!docxContainer.value) return
  docxContainer.value.innerHTML = ''
  await renderAsync(blob, docxContainer.value, undefined, {
    className: 'info-docx',
    inWrapper: true,
    ignoreWidth: false,
    ignoreHeight: false,
    ignoreFonts: false,
    breakPages: true,
    ignoreLastRenderedPageBreak: true,
    experimental: false,
    trimXmlDeclaration: true,
    useBase64URL: true,
  })
}

async function renderExcel(blob: Blob, fileType: string) {
  const XLSX = await import('xlsx')
  const arrayBuffer = await blob.arrayBuffer()
  let workbook
  if (fileType === 'csv') {
    workbook = XLSX.read(await blob.text(), { type: 'string' })
  } else {
    workbook = XLSX.read(arrayBuffer, { type: 'array' })
  }
  let html = ''
  workbook.SheetNames.forEach((name, sheetIdx) => {
    const sheet = workbook.Sheets[name]
    const sheetHtml = XLSX.utils.sheet_to_html(sheet, { id: `sheet-${sheetIdx}` })
    html += '<div class="excel-sheet">'
    if (workbook.SheetNames.length > 1) {
      html += `<div class="excel-sheet-name">${name}</div>`
    }
    html += sheetHtml
    html += '</div>'
  })
  excelHtml.value = sanitizeHTML(html)
}

async function renderMarkdown(blob: Blob) {
  const { marked } = await import('marked')
  const { default: markedKatex } = await import('marked-katex-extension')
  marked.use({ breaks: true, gfm: true })
  marked.use(markedKatex({ throwOnError: false, nonStandard: true }))
  const rawText = await blob.text()
  markdownHtml.value = sanitizeHTML(marked.parse(safeMarkdownToHTML(rawText)) as string)
}

async function loadPreview() {
  const key = String(props.file.id)
  const extension = extensionOf(props.file)
  const nextType = resolvePreviewType(extension)
  const version = ++requestVersion

  releasePreview()
  error.value = ''
  previewType.value = nextType
  if (nextType === 'unsupported') {
    loadedKey = key
    loading.value = false
    return
  }

  loading.value = true
  try {
    const rawBlob = await getKnowledgeAttachmentContent(props.file.id)
    if (version !== requestVersion || !props.active) return
    loadedKey = key
    const blob = ensureBlobType(rawBlob, extension)

    switch (nextType) {
      case 'docx':
        await renderDocx(blob)
        break
      case 'pptx':
        pptxData.value = await blob.arrayBuffer()
        break
      case 'pdf':
      case 'image':
      case 'audio':
        blobUrl.value = URL.createObjectURL(blob)
        break
      case 'excel':
        await renderExcel(blob, extension)
        break
      case 'markdown':
        await renderMarkdown(blob)
        break
      case 'text':
        textContent.value = await blob.text()
        break
    }
  } catch (cause: any) {
    if (version !== requestVersion) return
    loadedKey = ''
    error.value = cause?.message || '源文件尚未下载完成或文件存储暂不可用'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

function onPptxError(event: any) {
  error.value = event?.message || 'PPT 预览失败'
}

watch(
  () => [props.active, props.file.id, props.file.type] as const,
  ([active]) => {
    if (!active) {
      requestVersion += 1
      loadedKey = ''
      releasePreview()
      return
    }
    if (loadedKey !== String(props.file.id)) loadPreview()
  },
  { immediate: true },
)

onUnmounted(() => {
  requestVersion += 1
  releasePreview()
})
</script>

<style scoped lang="less">
.attachment-preview {
  display: flex;
  width: 100%;
  min-height: 100%;
  flex-direction: column;
  align-items: center;
}

.preview-state {
  display: flex;
  width: min(100%, 760px);
  min-height: 300px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 24px;
  color: var(--td-text-color-placeholder);
  text-align: center;
}

.preview-state > svg {
  width: 42px;
  height: 42px;
  color: var(--td-text-color-disabled);
}

.preview-state strong {
  color: var(--td-text-color-primary);
  font-size: 15px;
  font-weight: 600;
}

.preview-state span {
  max-width: 480px;
  font-size: 12px;
  line-height: 1.7;
}

.preview-state--error > svg {
  color: var(--td-error-color);
}

.preview-shell {
  width: min(100%, 1280px);
  margin: 0 auto 14px;
  border: 0;
  border-radius: 0;
  background: transparent;
  box-shadow: none;
}

.preview-docx,
.preview-excel,
.preview-markdown,
.preview-pptx {
  max-height: min(76vh, 860px);
  overflow: auto;
}

.preview-docx {
  width: min(100%, 960px);
}

.preview-pptx {
  width: min(100%, 1260px);
}

.preview-excel {
  width: min(100%, 1120px);
}

.preview-markdown {
  width: min(100%, 920px);
  padding: 18px 20px;
}

.pdf-preview {
  display: block;
  width: min(100%, 1240px);
  height: min(76vh, 860px);
  margin: 0 auto 12px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: #fff;
  box-shadow: 0 1px 8px rgb(0 0 0 / 8%);
}

.image-preview {
  display: flex;
  width: min(100%, 1240px);
  min-height: min(76vh, 860px);
  align-items: flex-start;
  justify-content: center;
  margin: 0 auto 12px;
  padding: 20px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: #fff;
  box-shadow: 0 1px 8px rgb(0 0 0 / 8%);
}

.image-preview img {
  max-width: 100%;
  height: auto;
  background: #fff;
}

.text-preview {
  width: min(100%, 760px);
  margin: 0 auto 12px;
  padding: 14px 18px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: #fff;
  box-shadow: 0 1px 8px rgb(0 0 0 / 8%);
  color: var(--td-text-color-primary);
  font: 14px/1.75 "Microsoft YaHei", system-ui, sans-serif;
  max-height: min(60vh, 640px);
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.audio-wrapper {
  display: flex;
  min-height: 360px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
  padding: 28px;
}

.audio-name {
  margin: 0;
  color: var(--td-text-color-primary);
  font-size: 14px;
}

.audio-element {
  width: min(100%, 520px);
}

:deep(.info-docx-wrapper) {
  min-height: 100%;
  padding: 12px 10px;
  background: transparent;
}

:deep(.info-docx-wrapper > section.info-docx) {
  margin: 0 auto 16px;
  box-shadow: 0 2px 8px rgb(0 0 0 / 8%);
}

:deep(.vue-office-pptx) {
  width: 100%;
  min-height: 100%;
}

:deep(.vue-office-pptx-main) {
  width: 100%;
  min-height: 100%;
}

:deep(.excel-sheet) {
  padding: 0;
}

:deep(.excel-sheet-name) {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: 8px 16px;
  border-bottom: 1px solid var(--td-component-stroke);
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-primary);
  font-size: 13px;
  font-weight: 600;
}

:deep(.excel-sheet table) {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

:deep(.excel-sheet th),
:deep(.excel-sheet td) {
  border: 1px solid var(--td-component-stroke);
  padding: 6px 12px;
  text-align: left;
}

:deep(.excel-sheet th) {
  background: var(--td-bg-color-secondarycontainer);
  font-weight: 600;
}

:deep(.markdown-body) {
  color: var(--td-text-color-primary);
  font-size: 14px;
  line-height: 1.7;
  word-break: break-word;
}

:deep(.markdown-body h1),
:deep(.markdown-body h2),
:deep(.markdown-body h3),
:deep(.markdown-body h4),
:deep(.markdown-body h5),
:deep(.markdown-body h6) {
  margin: 20px 0 10px;
  line-height: 1.4;
}

:deep(.markdown-body p),
:deep(.markdown-body ul),
:deep(.markdown-body ol) {
  margin: 8px 0;
}

:deep(.markdown-body pre) {
  margin: 12px 0;
  padding: 14px;
  overflow: auto;
  border-radius: 8px;
  background: var(--td-bg-color-secondarycontainer);
}

:deep(.markdown-body code) {
  padding: 2px 6px;
  border-radius: 4px;
  background: var(--td-bg-color-secondarycontainer);
}

:deep(.markdown-body table) {
  width: 100%;
  border-collapse: collapse;
}

:deep(.markdown-body th),
:deep(.markdown-body td) {
  border: 1px solid var(--td-component-stroke);
  padding: 6px 12px;
}

@media (max-width: 760px) {
  .preview-state {
    min-height: 280px;
    padding: 20px 16px;
  }

  .preview-shell,
  .pdf-preview,
  .image-preview,
  .text-preview {
    width: calc(100% - 16px);
  }

  .preview-markdown {
    padding: 16px 18px;
  }

  .image-preview {
    min-height: 280px;
    padding: 16px;
  }

  .text-preview {
    padding: 16px 18px;
    font-size: 13px;
  }
}
</style>
