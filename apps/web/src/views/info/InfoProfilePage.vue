<template>
  <section class="profile-page">
    <div class="profile-hero">
      <button class="profile-hero__avatar" type="button" aria-label="编辑头像" @click="openAvatarEditor">
        <span>{{ form.avatar }}</span>
        <span class="profile-hero__badge"><t-icon name="check-circle-filled" /></span>
      </button>
      <h1>{{ form.nickname || '未设置昵称' }}</h1>
      <p>{{ form.email }}</p>
    </div>

    <div class="profile-layout">
      <section class="profile-panel" aria-labelledby="profile-info-title">
        <div class="profile-panel__heading">
          <span class="profile-panel__icon"><t-icon name="user" /></span>
          <div>
            <h2 id="profile-info-title">个人资料</h2>
            <p>查看和编辑你的基本信息</p>
          </div>
        </div>
        <div class="profile-settings">
          <div class="profile-setting-row">
            <div class="profile-setting-row__label"><t-icon name="user" /><span>昵称</span></div>
            <t-input v-model="form.nickname" class="profile-setting-row__input" placeholder="请输入昵称" aria-label="昵称" />
          </div>
          <div class="profile-setting-row">
            <div class="profile-setting-row__label"><t-icon name="email" /><span>邮箱</span></div>
            <span class="profile-setting-row__value">{{ form.email }}</span>
          </div>
        </div>
      </section>

      <section class="profile-panel" aria-labelledby="connector-title">
        <div class="profile-panel__heading">
          <span class="profile-panel__icon"><t-icon name="link-1" /></span>
          <div>
            <h2 id="connector-title">连接器</h2>
            <p>绑定后开放对应知识库，数据仅本人可见</p>
          </div>
        </div>
        <div class="connector-list">
          <div v-for="source in store.sources" :key="source.key" class="connector-row">
            <span class="connector-mark" :style="{ background: sourceColor[source.key] }">{{ source.name.slice(0, 1) }}</span>
            <div class="connector-row__body">
              <strong>{{ source.name }}</strong>
              <small>{{ source.bound ? source.account : `未绑定，绑定后开放${source.kbName}` }}</small>
            </div>
            <t-tag :theme="source.bound ? 'success' : 'default'" variant="light">{{ source.bound ? '已绑定' : '未绑定' }}</t-tag>
            <t-button :theme="source.bound ? 'default' : 'primary'" :variant="source.bound ? 'outline' : 'base'" size="small" @click="toggleSource(source.key)">
              {{ source.bound ? '解除绑定' : `绑定${source.name}` }}
            </t-button>
          </div>
        </div>
      </section>
    </div>

    <div class="profile-actions">
      <t-button theme="primary" @click="saveProfile">
        <template #icon><t-icon name="check" /></template>
        保存修改
      </t-button>
    </div>

    <t-dialog v-model:visible="avatarDialogVisible" header="编辑头像" :confirm-btn="'确定'" :cancel-btn="'取消'" @confirm="confirmAvatarEdit">
      <div class="avatar-dialog">
        <span class="avatar-dialog__preview">{{ avatarDraft || '?' }}</span>
        <t-input v-model="avatarDraft" maxlength="2" placeholder="输入 1-2 个字" aria-label="头像文字" />
      </div>
    </t-dialog>

    <t-dialog v-model:visible="authDialogVisible" :header="`绑定${pendingSource?.name || ''}`" :confirm-btn="'确认授权'" :cancel-btn="'取消'" @confirm="confirmBind">
      <div class="auth-dialog">
        <div class="auth-dialog__icon" :style="{ background: pendingSource ? sourceColor[pendingSource.key] : '' }">{{ pendingSource?.name.slice(0, 1) }}</div>
        <h3>模拟授权{{ pendingSource?.name }}连接器</h3>
        <p>授权后，Info Agent 将读取你有权限访问的群聊、私聊消息和文件，并在本地 Mock 数据中开放对应知识库。</p>
        <div class="auth-dialog__scope">
          <span><t-icon name="check-circle-filled" />读取会话消息</span>
          <span><t-icon name="check-circle-filled" />读取文件和图片</span>
          <span><t-icon name="lock-on" />数据仅本人可见</span>
        </div>
      </div>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { sourceColor, type InfoSource, type SourceKey } from '@/mock'
import { useInfoMockStore } from '@/stores/infoMock'

const store = useInfoMockStore()
const form = reactive({ nickname: store.profile.nickname, email: store.profile.email, avatar: store.profile.avatar })
const avatarDraft = ref(form.avatar)
const avatarDialogVisible = ref(false)
const authDialogVisible = ref(false)
const pendingSource = ref<InfoSource | null>(null)

function saveProfile() {
  store.updateProfile(form)
  MessagePlugin.success('个人资料已保存')
}

function openAvatarEditor() {
  avatarDraft.value = form.avatar
  avatarDialogVisible.value = true
}

function confirmAvatarEdit() {
  form.avatar = avatarDraft.value.trim() || form.nickname.slice(0, 1) || '林'
  avatarDialogVisible.value = false
}

function toggleSource(key: SourceKey) {
  const source = store.findSource(key)
  if (!source) return
  if (source.bound) {
    store.unbindSource(key)
    MessagePlugin.success(`已解除${source.name}绑定`)
  } else {
    pendingSource.value = source
    authDialogVisible.value = true
  }
}

function confirmBind() {
  if (!pendingSource.value) return
  store.bindSource(pendingSource.value.key)
  MessagePlugin.success(`${pendingSource.value.name}已绑定，知识库已开放`)
  authDialogVisible.value = false
  pendingSource.value = null
}
</script>

<style lang="less" scoped>
.profile-page {
  width: min(920px, 100%);
  margin: 0 auto;
  padding: 36px 34px 56px;
}

.profile-hero {
  display: flex;
  align-items: center;
  flex-direction: column;
  margin-bottom: 34px;
  text-align: center;
}

.profile-hero__avatar {
  position: relative;
  display: grid;
  place-items: center;
  width: 112px;
  height: 112px;
  margin-bottom: 15px;
  padding: 0;
  border: 5px solid var(--td-bg-color-container);
  border-radius: 50%;
  color: var(--td-text-color-anti, #fff);
  background: var(--td-brand-color);
  box-shadow: 0 4px 16px rgba(31, 35, 41, .12);
  font-size: 38px;
  font-weight: 650;
  cursor: pointer;
}

.profile-hero__avatar:hover {
  box-shadow: 0 5px 19px rgba(31, 35, 41, .18);
}

.profile-hero__badge {
  position: absolute;
  right: -1px;
  bottom: 1px;
  display: grid;
  place-items: center;
  width: 27px;
  height: 27px;
  border: 3px solid var(--td-bg-color-container);
  border-radius: 50%;
  color: #fff;
  background: var(--td-brand-color);
  font-size: 16px;
}

.profile-hero h1 {
  margin: 0;
  font-size: 27px;
  font-weight: 650;
}

.profile-hero p {
  margin: 7px 0 0;
  color: var(--td-text-color-secondary);
  font-size: 13px;
}

.profile-layout {
  display: grid;
  gap: 20px;
}

.profile-panel {
  overflow: hidden;
  border: 1px solid var(--td-component-stroke);
  border-radius: 14px;
  background: var(--td-bg-color-container);
  box-shadow: 0 5px 18px rgba(31, 35, 41, .05);
}

.profile-panel__heading {
  display: flex;
  align-items: center;
  gap: 17px;
  padding: 25px 36px 24px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.profile-panel__icon {
  display: grid;
  place-items: center;
  flex: 0 0 60px;
  width: 60px;
  height: 60px;
  border-radius: 15px;
  color: var(--td-brand-color);
  background: var(--td-brand-color-light);
  font-size: 27px;
}

.profile-panel__heading h2 {
  margin: 0 0 5px;
  font-size: 19px;
  font-weight: 650;
}

.profile-panel__heading p {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: 13px;
}

.profile-settings,
.connector-list {
  display: grid;
}

.profile-setting-row,
.connector-row {
  display: flex;
  align-items: center;
  min-height: 72px;
  padding: 14px 36px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.profile-setting-row:last-child,
.connector-row:last-child {
  border-bottom: 0;
}

.profile-setting-row__label {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 170px;
  color: var(--td-text-color-secondary);
  font-size: 14px;
}

.profile-setting-row__label :deep(svg) {
  width: 18px;
  height: 18px;
}

.profile-setting-row__input {
  width: min(280px, 100%);
  margin-left: auto;
}

.profile-setting-row__input :deep(.t-input) {
  border-color: transparent;
  background: transparent;
  text-align: right;
}

.profile-setting-row__value {
  margin-left: auto;
  color: var(--td-text-color-primary);
  font-size: 14px;
}

.connector-row {
  gap: 14px;
  min-height: 84px;
}

.connector-mark {
  display: grid;
  place-items: center;
  flex: 0 0 38px;
  width: 38px;
  height: 38px;
  border-radius: 9px;
  color: #fff;
  font-size: 14px;
  font-weight: 650;
}

.connector-row__body {
  flex: 1;
  min-width: 0;
}

.connector-row__body strong,
.connector-row__body small {
  display: block;
}

.connector-row__body strong {
  font-size: 14px;
}

.connector-row__body small {
  margin-top: 4px;
  overflow: hidden;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.profile-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 20px;
}

.avatar-dialog {
  display: flex;
  align-items: center;
  gap: 16px;
}

.avatar-dialog__preview {
  display: grid;
  place-items: center;
  flex: 0 0 56px;
  width: 56px;
  height: 56px;
  border-radius: 50%;
  color: #fff;
  background: var(--td-brand-color);
  font-size: 20px;
  font-weight: 650;
}

.avatar-dialog .t-input {
  flex: 1;
}

.auth-dialog {
  text-align: center;
}

.auth-dialog__icon {
  display: grid;
  place-items: center;
  width: 52px;
  height: 52px;
  margin: 3px auto 14px;
  border-radius: 12px;
  color: #fff;
  font-size: 20px;
  font-weight: 600;
}

.auth-dialog h3 {
  margin: 0 0 8px;
  font-size: 16px;
}

.auth-dialog p {
  max-width: 48ch;
  margin: 0 auto;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.7;
}

.auth-dialog__scope {
  display: grid;
  gap: 7px;
  margin-top: 17px;
  padding: 12px;
  border-radius: 6px;
  background: var(--td-bg-color-secondarycontainer);
  text-align: left;
}

.auth-dialog__scope span {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--td-text-color-secondary);
  font-size: 11px;
}

.auth-dialog__scope span :deep(svg) {
  width: 14px;
  color: var(--td-brand-color);
}

@media (max-width: 700px) {
  .profile-page {
    padding: 24px 16px 44px;
  }

  .profile-hero {
    margin-bottom: 25px;
  }

  .profile-panel__heading,
  .profile-setting-row,
  .connector-row {
    padding-right: 18px;
    padding-left: 18px;
  }

  .profile-setting-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
  }

  .profile-setting-row__label {
    min-width: 0;
  }

  .profile-setting-row__input,
  .profile-setting-row__value {
    width: 100%;
    margin-left: 30px;
    text-align: left;
  }

  .profile-setting-row__input :deep(.t-input) {
    text-align: left;
  }

  .connector-row {
    flex-wrap: wrap;
  }

  .connector-row__body {
    min-width: calc(100% - 52px);
  }

  .connector-row > .t-button {
    margin-left: 52px;
  }
}
</style>
