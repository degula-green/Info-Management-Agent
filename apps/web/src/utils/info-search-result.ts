import type { SearchHit, SearchPlatform } from '@/api/info-search'
import type { InfoMessage, SearchResult, SourceKey } from '@/mock'

export const infoPlatformOptions: Array<{ label: string; value: SearchPlatform }> = [
  { label: '全部数据', value: 'all' },
  { label: '飞书', value: 'feishu' },
  { label: '企业微信', value: 'wecom' },
  { label: '个人微信', value: 'wechat' },
]

export function platformLabel(value?: string | null) {
  const key = normalizePlatform(value)
  return infoPlatformOptions.find((item) => item.value === key)?.label || String(value || '未知平台')
}

export function normalizePlatform(value?: string | null): SourceKey | 'all' {
  const text = String(value || '').trim().toLowerCase().replace('-', '_')
  if (text === 'feishu' || text === 'wechat' || text === 'wecom') return text
  if (text === 'personal_wechat' || text === 'personalwechat') return 'wechat'
  if (!text || text === 'all') return 'all'
  return 'all'
}

function formatTime(value?: string | null) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString('zh-CN')
}

function contextMessages(value: SearchHit['context']): InfoMessage[] | undefined {
  if (!Array.isArray(value) || !value.length) return undefined
  return value.map((item, index) => ({
    id: String(item.id || index),
    sender: String(item.sender || item.sender_name || '未知成员'),
    content: String(item.content || item.text || ''),
    time: formatTime(String(item.time || item.occurred_at || '')),
    timestamp: String(item.time || item.occurred_at || ''),
  }))
}

function resultKind(item: SearchHit): SearchResult['kind'] {
  if (item.kind === 'chat' || item.kind === 'qa') return item.kind
  if (item.kind === 'file' || item.resource_type === 'attachment' || item.file_id) return 'file'
  return 'message'
}

export function mapInfoSearchResult(item: SearchHit): SearchResult {
  const kind = resultKind(item)
  const platform = kind === 'qa' && !item.platform ? 'qa' : normalizePlatform(item.platform || item.source)
  const source = platform === 'qa' ? '历史问答' : platformLabel(platform)
  const conversation = item.conversation_name || (item.conversation_id ? `会话 ${item.conversation_id}` : '未知会话')
  const time = formatTime(item.occurred_at || item.last_seen_at)

  if (kind === 'chat') {
    const chatType = item.conversation_type === 'direct' ? '私聊' : '群聊'
    return {
      id: String(item.id || `conversation:${item.conversation_id}`),
      kind,
      title: item.title || item.conversation_name || '未命名会话',
      subtitle: `${source} · ${chatType}${time ? ` · 最近消息 ${time}` : ''}`,
      source,
      platform,
      chatId: item.conversation_id ? String(item.conversation_id) : undefined,
      recordId: item.conversation_id ? String(item.conversation_id) : undefined,
      score: item.rerank_score ?? item.rrf_score ?? item.score,
    }
  }

  if (kind === 'qa') {
    const answer = item.answer || item.content || ''
    return {
      id: String(item.session_id || item.id || item.chunk_id),
      kind,
      title: item.question || item.title || '历史问答',
      subtitle: `${source}${time ? ` · ${time}` : ''}`,
      source,
      platform: platform === 'qa' ? 'qa' : platform,
      recordId: item.session_id ? String(item.session_id) : String(item.id || ''),
      question: item.question || item.title,
      answer,
      content: answer,
      time,
      citations: item.citations,
      score: item.rerank_score ?? item.rrf_score ?? item.score,
    }
  }

  if (kind === 'file') {
    return {
      id: String(item.chunk_id || item.id || item.file_id),
      kind,
      title: item.file_name || item.title || '附件',
      subtitle: `${source} · ${conversation} · ${item.uploader || item.sender_name || '未知成员'}${time ? ` · ${time}` : ''}`,
      source,
      platform,
      chatId: item.conversation_id ? String(item.conversation_id) : undefined,
      recordId: item.file_id || (item.attachment_id != null ? String(item.attachment_id) : undefined),
      content: item.content || item.message_text || '',
      uploader: item.uploader || item.sender_name,
      time,
      score: item.rerank_score ?? item.rrf_score ?? item.score,
    }
  }

  return {
    id: String(item.chunk_id || item.id || item.message_id),
    kind,
    title: item.message_text || item.content || item.title || '消息',
    subtitle: `${source} · ${conversation} · ${item.sender_name || '未知成员'}${time ? ` · ${time}` : ''}`,
    source,
    platform,
    chatId: item.conversation_id ? String(item.conversation_id) : undefined,
    recordId: item.message_id,
    content: item.message_text || item.content || '',
    context: contextMessages(item.context),
    sender: item.sender_name,
    time,
    score: item.rerank_score ?? item.rrf_score ?? item.score,
  }
}
