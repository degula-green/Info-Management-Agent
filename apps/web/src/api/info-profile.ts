import { del, get, patch, post, postUpload } from '@/utils/request'

export type ConnectorPlatform = 'feishu' | 'wecom' | 'wechat'

export interface Profile {
  id: number
  username: string
  nickname: string
  email: string
  avatar_url: string | null
  updated_at: string
}

export interface Connector {
  platform: ConnectorPlatform
  display_name: string
  availability: 'available' | 'coming_soon'
  bound: boolean
  cleanup_pending: boolean
  status: 'unbound' | 'active' | 'paused' | 'error' | 'offline'
  account_name: string | null
  account_avatar_url: string | null
  bound_at: string | null
  last_sync_at: string | null
  selected_conversation_count: number
  last_error: string | null
  actions: string[]
}

export const getProfile = () => get<Profile>('/api/profile')
export const updateProfile = (nickname: string) => patch<Profile>('/api/profile', { nickname })
export const uploadAvatar = (file: File) => {
  const data = new FormData()
  data.append('file', file)
  return postUpload('/api/profile/avatar', data) as Promise<Profile>
}
export const removeAvatar = () => del<Profile>('/api/profile/avatar')
export const getConnectors = async () => (await get<{ connectors: Connector[] }>('/api/connectors')).connectors
export const getFeishuAuthorizeURL = async (intent: 'bind' | 'rebind') => (await post<{ authorize_url: string }>('/api/connectors/feishu/authorize', { intent })).authorize_url
export const bindWechat = (body: { wxid: string; db_dir: string }, rebind = false) =>
  post<Connector>(`/api/connectors/wechat/${rebind ? 'rebind' : 'bind'}`, body)
export const unbindConnector = (platform: ConnectorPlatform) => del<Connector>(`/api/connectors/${platform}`)
