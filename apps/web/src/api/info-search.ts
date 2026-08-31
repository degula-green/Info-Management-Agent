import { post } from '@/utils/request'

export type SearchPlatform = 'all' | 'feishu' | 'wecom' | 'wechat'

export interface SearchHit {
  id?: string
  chunk_id?: string
  kind?: 'chat' | 'message' | 'file' | 'qa'
  title?: string
  subtitle?: string
  resource_type?: 'message' | 'attachment' | string
  content?: string
  message_text?: string
  message_id?: string
  document_id?: number
  attachment_id?: number
  file_id?: string
  file_name?: string
  uploader?: string
  platform?: string
  source?: string
  conversation_id?: number
  conversation_name?: string
  conversation_type?: string
  sender_name?: string
  occurred_at?: string
  last_seen_at?: string
  context?: Array<Record<string, unknown>>
  citations?: Array<Record<string, unknown>>
  session_id?: number
  question?: string
  answer?: string
  score?: number
  rrf_score?: number
  rerank_score?: number
  highlight?: string | string[]
}

export interface SearchResponse {
  query: string
  items: SearchHit[]
  total: number
  page?: number
  page_size?: number
  [key: string]: unknown
}

export interface InfoSearchRequest {
  query: string
  platforms?: string[]
  resource_types?: string[]
  sender_name?: string
  occurred_after?: string
  occurred_before?: string
  conversation_ids?: number[]
  page?: number
  page_size?: number
}

export function searchInfo(data: InfoSearchRequest): Promise<SearchResponse>
export function searchInfo(query: string, platform?: SearchPlatform, options?: Record<string, unknown>): Promise<SearchResponse>
export function searchInfo(dataOrQuery: InfoSearchRequest | string, platform: SearchPlatform = 'all', options: Record<string, unknown> = {}) {
  const data: InfoSearchRequest = typeof dataOrQuery === 'string'
    ? { query: dataOrQuery, platforms: platform === 'all' ? [] : [platform], ...options }
    : dataOrQuery
  return post<SearchResponse>('/api/search', data)
}
