import { del, get, patch, post } from '@/utils/request'

export type QaAnswerStatus = 'pending' | 'streaming' | 'completed' | 'failed'
export type QaConversation = { id: number; title: string; message_count: number; last_message_at?: string | null; created_at?: string; updated_at?: string }
export type QaMessageRecord = { id: number; question: string; answer: string; answer_status: QaAnswerStatus; error_message?: string | null; citations?: any[]; scope_snapshot?: Record<string, any>; retrieval_meta?: Record<string, any>; request_id?: string | null; created_at?: string; completed_at?: string | null }
export type QaConversationDetail = QaConversation & { messages: QaMessageRecord[] }
export const createQaConversation = (title?: string) => post<QaConversation>('/api/qa/conversations', title ? { title } : {})
export const listQaConversations = (page = 1, pageSize = 20) => get<{ items: QaConversation[]; page: number; page_size: number; total: number }>(`/api/qa/conversations?page=${page}&page_size=${pageSize}`)
export const getQaConversation = (id: number | string) => get<QaConversationDetail>(`/api/qa/conversations/${id}`)
export const renameQaConversation = (id: number | string, title: string) => patch<QaConversation>(`/api/qa/conversations/${id}`, { title })
export const deleteQaConversation = (id: number | string) => del(`/api/qa/conversations/${id}`)
