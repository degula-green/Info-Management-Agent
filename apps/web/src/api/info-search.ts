import { post } from '@/utils/request'

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

export function searchInfo(data: InfoSearchRequest) {
  return post('/api/search', data)
}
