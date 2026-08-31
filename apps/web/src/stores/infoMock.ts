import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { cloneMockState, type CollectionStatus, type InfoChat, type InfoFile, type InfoMessage, type InfoProfile, type InfoSource, type SourceKey } from '@/mock'

export const useInfoMockStore = defineStore('infoMock', () => {
  const initial = cloneMockState()
  const isAuthenticated = ref(typeof localStorage !== 'undefined' && !!localStorage.getItem('weknora_token'))
  const profile = ref<InfoProfile>(initial.profile)
  const sources = ref<InfoSource[]>(initial.sources)
  const qaSessions = ref(initial.qaSessions)
  const recentSearches = ref(initial.recentSearches)

  const allChats = computed(() => sources.value.flatMap((source) => source.chats))
  const boundSources = computed(() => sources.value.filter((source) => source.bound))
  const collectingChats = computed(() => allChats.value.filter((item) => item.collectionStatus === 'collecting'))
  const totalMessages = computed(() => allChats.value.reduce((sum, item) => sum + item.messages.length, 0))
  const totalFiles = computed(() => allChats.value.reduce((sum, item) => sum + item.files.length, 0))

  function login(email: string, nickname?: string) {
    isAuthenticated.value = true
    if (typeof sessionStorage !== 'undefined') sessionStorage.setItem('info_agent_authenticated', 'true')
    profile.value.email = email
    if (nickname?.trim()) profile.value.nickname = nickname.trim()
    profile.value.avatar = profile.value.nickname.slice(0, 1) || '林'
  }

  function logout() {
    isAuthenticated.value = false
    if (typeof sessionStorage !== 'undefined') sessionStorage.removeItem('info_agent_authenticated')
    if (typeof localStorage !== 'undefined') {
      localStorage.removeItem('weknora_token')
      localStorage.removeItem('weknora_refresh_token')
    }
  }

  function findSource(key?: string | null) { return sources.value.find((item) => item.key === key) }
  function findChat(id?: string | null) { return allChats.value.find((item) => item.id === id) }

  function bindSource(key: SourceKey) {
    const source = findSource(key); if (!source) return
    source.bound = true; source.account = `${profile.value.nickname} · ${source.name}`
  }

  function unbindSource(key: SourceKey) {
    const source = findSource(key); if (!source) return
    source.bound = false; source.account = ''
  }

  function updateProfile(next: Partial<InfoProfile>) {
    profile.value = { ...profile.value, ...next }
    profile.value.avatar = profile.value.avatar.trim() || profile.value.nickname.slice(0, 1) || '林'
  }

  function setCollection(chat: InfoChat, status: CollectionStatus, historyStart?: string) {
    chat.collectionStatus = status; chat.collecting = status === 'collecting'
    if (historyStart !== undefined) chat.historyStart = historyStart
    chat.lastSync = status === 'collecting' ? '刚刚开始采集' : status === 'paused' ? '已暂停' : '尚未同步'
    if (status === 'collecting') chat.recentMessageTime = chat.messages.length ? chat.messages[chat.messages.length - 1].time : '等待首条消息'
  }

  function accessSession(sourceKey: SourceKey, sessionId: string) {
    const source = findSource(sourceKey)
    const available = source?.availableSessions.find((item) => item.id === sessionId)
    if (!source || !available) return

    const existing = source.chats.find((item) => item.id === sessionId)
    if (existing) return existing

    const chat: InfoChat = {
      id: available.id,
      name: available.name,
      source: sourceKey,
      members: available.members,
      isDirect: available.isDirect,
      collecting: false,
      collectionStatus: 'not_started',
      historyStart: '',
      interval: '每 30 分钟',
      lastSync: '尚未同步',
      recentMessageTime: '尚未同步',
      messages: [],
      files: [],
    }
    source.chats.unshift(chat)
    source.availableSessions = source.availableSessions.filter((item) => item.id !== sessionId)
    return chat
  }

  function updateMessage(chatId: string, messageId: string, content: string) {
    const message = findChat(chatId)?.messages.find((item) => item.id === messageId); if (message) message.content = content
  }

  function updateFile(chatId: string, fileId: string, content: string) {
    const file = findChat(chatId)?.files.find((item) => item.id === fileId); if (file) file.content = content
  }

  function addRecentSearch(query: string) {
    const value = query.trim(); if (!value) return
    recentSearches.value = [value, ...recentSearches.value.filter((item) => item !== value)].slice(0, 8)
  }

  function setQaSessions(next: typeof initial.qaSessions) {
    qaSessions.value = next
  }

  function getMessage(chatId?: string, messageId?: string): InfoMessage | undefined { return findChat(chatId)?.messages.find((item) => item.id === messageId) }
  function getFile(chatId?: string, fileId?: string): InfoFile | undefined { return findChat(chatId)?.files.find((item) => item.id === fileId) }

  return { isAuthenticated, profile, sources, qaSessions, recentSearches, allChats, boundSources, collectingChats, totalMessages, totalFiles, login, logout, findSource, findChat, bindSource, unbindSource, updateProfile, setCollection, accessSession, updateMessage, updateFile, addRecentSearch, setQaSessions, getMessage, getFile }
})
