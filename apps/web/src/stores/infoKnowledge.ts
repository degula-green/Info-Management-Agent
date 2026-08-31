import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  getKnowledgeBases,
  getKnowledgeConversation,
  getIngestionConnectorConfig,
  listKnowledgeConversationMessages,
  listKnowledgeConversations,
  updateIngestionConnectorConfig,
  type IngestionConnectorConfig,
  type KnowledgeBaseSummary,
  type KnowledgeConversationMessage,
  type KnowledgeConversationSummary,
} from '@/api/info-knowledge'
import { getConnectors, type Connector } from '@/api/info-profile'
import { type CollectionStatus, type InfoChat, type InfoFile, type InfoMessage, type InfoSource, type SourceKey } from '@/mock'

const PLATFORM_ORDER: SourceKey[] = ['feishu', 'wecom', 'wechat']

const PLATFORM_META: Record<SourceKey, { name: string; kbName: string; description: string }> = {
  feishu: { name: '飞书', kbName: '飞书知识库', description: '飞书群聊、私聊消息与文件' },
  wecom: { name: '企业微信', kbName: '企业微信知识库', description: '企业微信知识库（当前不可采集消息）' },
  wechat: { name: '个人微信', kbName: '个人微信知识库', description: '个人微信群聊、私聊消息与文件' },
}

export function normalizeSourceKey(value?: string | null): SourceKey | null {
  const text = String(value || '').trim().toLowerCase().replace('-', '_')
  if (text === 'personal_wechat' || text === 'personalwechat') return 'wechat'
  if (text === 'feishu' || text === 'wecom' || text === 'wechat') return text
  return null
}

function displayTime(value?: string | null) {
  if (!value) return '尚未同步'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toLocaleString('zh-CN', { hour12: false })
}

function displayDate(value?: string | null) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toISOString().slice(0, 10)
}

function formatBytes(bytes?: number | null) {
  if (bytes == null || Number.isNaN(bytes)) return '-'
  const units = ['B', 'KB', 'MB', 'GB']
  let size = bytes
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`
}

function collectionStatus(connectorStatus?: string, selected = false): CollectionStatus {
  if (!selected) return 'not_started'
  if (connectorStatus === 'active') return 'collecting'
  return 'paused'
}

function conversationSelected(conversation: KnowledgeConversationSummary, config?: IngestionConnectorConfig | null) {
  // A disabled connector represents a paused whitelist. Do not resurrect
  // conversations that were selected by the legacy pause implementation.
  if (config && config.enabled === false) return false
  if (conversation.selected) return true
  return !!config?.selected_conversations?.includes(conversation.external_id)
}

function mapMessage(message: KnowledgeConversationMessage): InfoMessage {
  const occurredAt = message.occurred_at ? new Date(message.occurred_at).toISOString() : ''
  return {
    id: String(message.id),
    sender: message.sender_name || '未知成员',
    content: message.text || '',
    time: displayTime(message.occurred_at),
    timestamp: occurredAt || displayTime(message.occurred_at),
    attachments: (message.attachments || []).map((attachment) => ({
      id: String(attachment.id),
      name: attachment.file_name || `附件 ${attachment.id}`,
      type: attachment.file_category === 'image' ? 'image' : 'file',
    })),
    vectorStatus: message.vector_status,
    sourceMessageId: message.source_message_id,
    senderAvatarUrl: message.sender_avatar_url,
    isDeleted: message.is_deleted,
    isUpdated: message.is_updated,
    messageType: message.message_type,
    sourceMessageType: message.source_message_type,
    metadata: message.metadata,
  }
}

function mapFile(message: KnowledgeConversationMessage, attachment: NonNullable<KnowledgeConversationMessage['attachments']>[number]): InfoFile {
  return {
    id: String(attachment.id),
    name: attachment.file_name || `附件 ${attachment.id}`,
    type: attachment.extension || attachment.file_category || 'file',
    size: formatBytes(attachment.file_size),
    time: displayTime(message.occurred_at),
    uploadedAt: displayTime(message.occurred_at),
    uploader: message.sender_name || '未知成员',
    content: attachment.content || '',
    documentId: attachment.document_id ?? null,
    documentStatus: attachment.document_status ?? null,
    parseStatus: attachment.parse_status,
    previewCapability: attachment.preview_capability,
    isDeleted: attachment.is_deleted,
    fileSizeBytes: attachment.file_size ?? null,
  }
}

function mapChatSummary(
  platform: SourceKey,
  source: InfoSource,
  conversation: KnowledgeConversationSummary,
  config?: IngestionConnectorConfig | null,
  connectorStatus?: string,
): InfoChat {
  const selected = conversationSelected(conversation, config)
  const status = collectionStatus(connectorStatus, selected)
  return {
    id: String(conversation.id),
    externalId: conversation.external_id,
    name: conversation.name || conversation.external_id,
    source: platform,
    members: conversation.members || (conversation.conversation_type === 'direct' ? 2 : 0),
    isDirect: conversation.conversation_type === 'direct',
    collecting: status === 'collecting',
    collectionStatus: status,
    historyStart: displayDate(config?.history_start_at),
    interval: '系统轮询',
    lastSync: displayTime(source.lastSyncAt || conversation.last_seen_at),
    recentMessageTime: displayTime(conversation.last_seen_at),
    messages: [],
    files: [],
    lastSeenAt: conversation.last_seen_at,
    messageCount: conversation.message_count,
    attachmentCount: conversation.attachment_count,
    selected,
  }
}

function mapAvailableSession(
  conversation: KnowledgeConversationSummary,
): NonNullable<InfoSource['availableSessions']>[number] {
  return {
    id: conversation.external_id,
    externalId: conversation.external_id,
    name: conversation.name || conversation.external_id,
    members: conversation.members || (conversation.conversation_type === 'direct' ? 2 : 0),
    isDirect: conversation.conversation_type === 'direct',
    lastSeenAt: conversation.last_seen_at,
    messageCount: conversation.message_count,
    attachmentCount: conversation.attachment_count,
  }
}

function baseSource(platform: SourceKey): InfoSource {
  const meta = PLATFORM_META[platform]
  return {
    key: platform,
    name: meta.name,
    kbName: meta.kbName,
    description: meta.description,
    account: '',
    bound: false,
    chats: [],
    availableSessions: [],
    available: true,
    enabled: false,
    selectedConversationCount: 0,
    lastSyncAt: null,
    lastError: null,
    status: 'unbound',
  }
}

export const useInfoKnowledgeStore = defineStore('infoKnowledge', () => {
  // Avoid rendering stale mock conversations while the authoritative API
  // snapshot is loading (which caused paused chats to flash after refresh).
  const sources = ref<InfoSource[]>([])
  const loading = ref(false)
  const loadedAt = ref<number | null>(null)
  const loadError = ref<string | null>(null)
  const inflight = ref<Promise<InfoSource[]> | null>(null)
  let refreshVersion = 0
  const conversationLoadVersions = new Map<string, number>()

  const allChats = computed(() => sources.value.flatMap((source) => source.chats))

  function findSource(key?: string | null) {
    return sources.value.find((item) => item.key === key)
  }

  function findConversation(platform: string | null | undefined, id?: string | number | null) {
    if (!platform || id == null) return undefined
    const source = findSource(platform as SourceKey)
    if (!source) return undefined
    return source.chats.find((item) => String(item.id) === String(id) || String(item.externalId) === String(id))
  }

  async function refreshSources(force = false): Promise<InfoSource[]> {
    // A forced refresh may be requested by both the shell and the detail page
    // at the same time. Share the active request so a slower duplicate cannot
    // replace the store with an older summary snapshot.
    if (inflight.value) return inflight.value
    loading.value = true
    const requestVersion = ++refreshVersion
    const previousSources = new Map(sources.value.map((source) => [source.key, source]))
    const task = (async () => {
      try {
        const [knowledgeBasesResponse, connectors] = await Promise.all([getKnowledgeBases(), getConnectors()])
        const knowledgeBases = knowledgeBasesResponse.knowledge_bases || []
        const kbMap = new Map<SourceKey, KnowledgeBaseSummary>()
        for (const item of knowledgeBases) {
          const key = normalizeSourceKey(item.platform)
          if (key) kbMap.set(key, item)
        }
        const connectorMap = new Map<SourceKey, Connector>()
        for (const item of connectors) {
          const key = normalizeSourceKey(item.platform)
          if (key) connectorMap.set(key, item)
        }
        const loaded: InfoSource[] = []

        for (const platform of PLATFORM_ORDER) {
          const meta = PLATFORM_META[platform]
          const kb = kbMap.get(platform)
          const connector = connectorMap.get(platform)
          const source = baseSource(platform)
          source.account = connector?.account_name || source.account
          source.bound = Boolean(kb?.bound ?? connector?.bound)
          source.available = connector?.availability !== 'coming_soon'
          source.enabled = Boolean(kb?.enabled)
          source.selectedConversationCount = kb?.selected_conversation_count ?? connector?.selected_conversation_count ?? 0
          source.lastSyncAt = kb?.last_sync_at ?? connector?.last_sync_at ?? null
          source.lastError = connector?.last_error || null
          source.status = connector?.status || (source.bound ? 'paused' : 'unbound')
          source.description = source.available === false && platform === 'wecom'
            ? '企业微信知识库（当前不可采集消息）'
            : meta.description

          if (source.bound && source.available !== false) {
            const [configResult, convResult] = await Promise.allSettled([
              getIngestionConnectorConfig(platform),
              listKnowledgeConversations(platform, { page: 1, page_size: 500 }),
            ])
            const config = configResult.status === 'fulfilled' ? configResult.value : null
            if (convResult.status !== 'fulfilled') {
              // Keep the last usable directory during a transient API failure.
              // Replacing it with [] makes the selected chat disappear even
              // though its data is still present on the server.
              const previous = previousSources.get(platform)
              if (previous) {
                source.chats = previous.chats
                source.availableSessions = previous.availableSessions
                source.historyStartAt = previous.historyStartAt
                loaded.push(source)
                continue
              }
            }
            const conversations = convResult.status === 'fulfilled' ? convResult.value.conversations || [] : []
            const selectedConversations = conversations.filter((conversation) => conversationSelected(conversation, config))
            const availableConversations = conversations.filter((conversation) => !conversationSelected(conversation, config))
            source.historyStartAt = config?.history_start_at ?? null
            source.selectedConversationCount = config?.selected_conversations?.length ?? selectedConversations.length
            const previousChats = new Map<string, InfoChat>()
            for (const previous of previousSources.get(platform)?.chats || []) {
              previousChats.set(String(previous.id), previous)
              if (previous.externalId) previousChats.set(String(previous.externalId), previous)
            }
            source.chats = selectedConversations.map((conversation) => {
              const summary = mapChatSummary(platform, source, conversation, config, source.status)
              const previous = previousChats.get(String(conversation.id)) || previousChats.get(conversation.external_id)
              // Conversation list polling only refreshes metadata. Keep the
              // detail payload already on screen until its own request updates it.
              return previous ? { ...summary, messages: previous.messages, files: previous.files } : summary
            })
            source.availableSessions = availableConversations.map((conversation) => mapAvailableSession(conversation))
          }

          loaded.push(source)
        }

        if (requestVersion !== refreshVersion) return sources.value
        sources.value = loaded
        loadedAt.value = Date.now()
        loadError.value = null
        return loaded
      } catch (error: any) {
        loadError.value = error?.message || 'failed to load knowledge sources'
        return sources.value
      } finally {
        loading.value = false
        inflight.value = null
      }
    })()
    inflight.value = task
    return task
  }

  async function ensureSources(force = false) {
    if (!force && loadedAt.value) return sources.value
    return refreshSources(force)
  }

  async function ensureSource(platform: SourceKey, force = false) {
    await ensureSources(force)
    return findSource(platform)
  }

  async function accessSession(platform: SourceKey, sessionId: string, historyStart?: string | null) {
    const current = await getIngestionConnectorConfig(platform).catch(() => null)
    const next = new Set<string>(current?.selected_conversations || [])
    next.add(String(sessionId))
    await updateIngestionConnectorConfig(platform, {
      selected_conversations: Array.from(next),
      history_start_at: historyStart || current?.history_start_at || null,
      enabled: true,
    })
    await refreshSources(true)
    return findConversation(platform, sessionId)
  }

  async function pauseConversation(platform: SourceKey, sessionId: string) {
    const current = await getIngestionConnectorConfig(platform).catch(() => null)
    const paused = findConversation(platform, sessionId)
    const next = (current?.selected_conversations || []).filter((id) => String(id) !== String(sessionId))
    await updateIngestionConnectorConfig(platform, {
      selected_conversations: next,
      history_start_at: current?.history_start_at || null,
      // Keep the connector enabled when other sessions remain selected. An
      // empty whitelist still pauses message collection while allowing the
      // conversation directory to refresh.
      enabled: next.length > 0,
    })
    await refreshSources(true)
    return paused
  }

  async function loadConversation(platform: SourceKey, conversationId: string | number, force = false) {
    await ensureSource(platform)
    const existing = findConversation(platform, conversationId)
    if (existing && existing.messages.length > 0 && !force) return existing

    const requestKey = `${platform}:${conversationId}`
    const requestVersion = (conversationLoadVersions.get(requestKey) || 0) + 1
    conversationLoadVersions.set(requestKey, requestVersion)

    const [conversationResult, messageResult] = await Promise.allSettled([
      getKnowledgeConversation(platform, conversationId),
      listKnowledgeConversationMessages(platform, conversationId, { limit: 200, offset: 0 }),
    ])

    const source = findSource(platform)
    if (!source) return undefined

    // A transient request failure must not turn a populated conversation into
    // an empty one. Keep the last known detail and let the next poll retry.
    if (messageResult.status !== 'fulfilled') return existing
    if (conversationLoadVersions.get(requestKey) !== requestVersion) return findConversation(platform, conversationId)

    const selectedConversation = source.chats.find((item) => String(item.id) === String(conversationId))
    const messages = messageResult.value.messages || []
    if (messages.length === 0 && existing && existing.messages.length > 0 && (existing.messageCount || 0) > 0) {
      return existing
    }
    const detail = conversationResult.status === 'fulfilled' ? conversationResult.value : null
    const mappedMessages = messages.map(mapMessage)
    const mappedFiles = messages.flatMap((message) => (message.attachments || []).map((attachment) => mapFile(message, attachment)))
    const updated: InfoChat = {
      id: String(detail?.id ?? conversationId),
      source: platform,
      name: detail?.name || selectedConversation?.name || String(conversationId),
      members: detail?.members || selectedConversation?.members || (detail?.conversation_type === 'direct' ? 2 : 0),
      isDirect: detail ? detail.conversation_type === 'direct' : selectedConversation?.isDirect,
      collecting: detail ? collectionStatus(source.status, conversationSelected(detail, null)) === 'collecting' : (selectedConversation?.collecting || false),
      collectionStatus: detail ? collectionStatus(source.status, conversationSelected(detail, null)) : (selectedConversation?.collectionStatus || 'not_started'),
      historyStart: selectedConversation?.historyStart || displayDate(source.historyStartAt),
      interval: selectedConversation?.interval || '系统轮询',
      lastSync: selectedConversation?.lastSync || displayTime(source.lastSyncAt || detail?.last_seen_at),
      recentMessageTime: selectedConversation?.recentMessageTime || displayTime(detail?.last_seen_at || messages[0]?.occurred_at || null),
      messages: mappedMessages,
      files: mappedFiles,
      externalId: detail?.external_id || selectedConversation?.externalId || String(conversationId),
      lastSeenAt: detail?.last_seen_at || selectedConversation?.lastSeenAt || null,
      messageCount: mappedMessages.length,
      attachmentCount: mappedFiles.length,
      selected: detail ? conversationSelected(detail, null) : (selectedConversation?.selected ?? true),
    }

    const index = source.chats.findIndex((item) => String(item.id) === String(conversationId))
    if (index >= 0) source.chats[index] = updated
    else source.chats.unshift(updated)
    return updated
  }

  function updateMessage(chatId: string, messageId: string, content: string) {
    const chat = allChats.value.find((item) => String(item.id) === String(chatId) || String(item.externalId) === String(chatId))
    if (!chat) return
    const message = chat.messages.find((item) => String(item.id) === String(messageId))
    if (message) message.content = content
  }

  function updateFile(chatId: string, fileId: string, content: string) {
    const chat = allChats.value.find((item) => String(item.id) === String(chatId) || String(item.externalId) === String(chatId))
    if (!chat) return
    const file = chat.files.find((item) => String(item.id) === String(fileId))
    if (file) file.content = content
  }

  function totalMessages() {
    return allChats.value.reduce((sum, item) => sum + (item.messageCount ?? item.messages.length), 0)
  }

  function totalFiles() {
    return allChats.value.reduce((sum, item) => sum + (item.attachmentCount ?? item.files.length), 0)
  }

  return {
    sources,
    loading,
    loadedAt,
    loadError,
    allChats,
    findSource,
    findConversation,
    refreshSources,
    ensureSources,
    ensureSource,
    accessSession,
    pauseConversation,
    loadConversation,
    updateMessage,
    updateFile,
    totalMessages,
    totalFiles,
  }
})
