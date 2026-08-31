import { get, getDown, put } from '@/utils/request'
import type { ConnectorPlatform } from '@/api/info-profile'

export interface KnowledgeBaseSummary {
  platform: ConnectorPlatform
  display_name: string
  bound: boolean
  enabled: boolean
  selected_conversation_count: number
  last_sync_at: string | null
}

export interface KnowledgeConversationSummary {
  id: number
  external_id: string
  name: string
  conversation_type: 'group' | 'direct' | string
  last_seen_at: string | null
  is_active: boolean
  selected: boolean
  members: number
  message_count: number
  attachment_count: number
}

export interface KnowledgeConversationListResponse {
  conversations: KnowledgeConversationSummary[]
  page: number
  page_size: number
  total: number
}

export interface KnowledgeConversationAttachment {
  id: number
  file_name: string
  extension: string
  mime_type: string
  file_category: string
  file_size: number | null
  parse_status: string
  preview_capability: string
  is_deleted: boolean
  document_id: number | null
  document_status: string | null
  content: string
}

export interface KnowledgeConversationMessage {
  id: string
  source_message_id: string
  sender_id: number | null
  sender_name: string
  sender_avatar_url: string
  occurred_at: string | null
  message_type: string
  source_message_type: string
  text: string
  is_deleted: boolean
  is_updated: boolean
  metadata: Record<string, unknown>
  vector_status: string
  attachments: KnowledgeConversationAttachment[]
}

export interface KnowledgeConversationMessagesResponse {
  messages: KnowledgeConversationMessage[]
  limit: number
  offset: number
}

export interface IngestionConnectorConfig {
  listen_mode: 'whitelist' | 'all'
  selected_conversations: string[]
  history_start_at: string | null
  config_updated_at: string | null
  enabled?: boolean
}

export interface UpdateIngestionConnectorConfigInput {
  selected_conversations: string[]
  history_start_at: string | null
  enabled?: boolean
}

export interface KnowledgeBaseResponse {
  knowledge_bases: KnowledgeBaseSummary[]
}

export function getKnowledgeBases() {
  return get<KnowledgeBaseResponse>('/api/knowledge-bases')
}

export function listKnowledgeConversations(platform: ConnectorPlatform, options: { page?: number; page_size?: number; search?: string; type?: string } = {}) {
  const params = new URLSearchParams()
  if (options.page && options.page > 0) params.set('page', String(options.page))
  if (options.page_size && options.page_size > 0) params.set('page_size', String(options.page_size))
  if (options.search) params.set('search', options.search)
  if (options.type) params.set('type', options.type)
  const query = params.toString()
  return get<KnowledgeConversationListResponse>(`/api/knowledge-bases/${platform}/conversations${query ? `?${query}` : ''}`)
}

export function getKnowledgeConversation(platform: ConnectorPlatform, id: number | string) {
  return get<KnowledgeConversationSummary>(`/api/knowledge-bases/${platform}/conversations/${id}`)
}

export function getKnowledgeAttachmentContent(id: number | string, download = false) {
  return getDown(`/api/attachments/${id}/content${download ? '?download=1' : ''}`)
}

export function listKnowledgeConversationMessages(platform: ConnectorPlatform, id: number | string, options: { limit?: number; offset?: number } = {}) {
  const params = new URLSearchParams()
  if (options.limit && options.limit > 0) params.set('limit', String(options.limit))
  if (options.offset && options.offset >= 0) params.set('offset', String(options.offset))
  const query = params.toString()
  return get<KnowledgeConversationMessagesResponse>(`/api/knowledge-bases/${platform}/conversations/${id}/messages${query ? `?${query}` : ''}`)
}

export function getIngestionConnectorConfig(platform: ConnectorPlatform) {
  return get<IngestionConnectorConfig>(`/api/ingestion/${platform}/config`)
}

export function updateIngestionConnectorConfig(platform: ConnectorPlatform, body: UpdateIngestionConnectorConfigInput) {
  return put<IngestionConnectorConfig>(`/api/ingestion/${platform}/config`, body)
}
