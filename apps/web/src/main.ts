import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import TDesign from 'tdesign-vue-next'
import './assets/fonts.css'
import 'tdesign-vue-next/dist/tdesign.css'
import '@/assets/theme/theme.css'
import '@/assets/dropdown-menu.less'
import '@/components/css/chat-hljs-dark.less'

const app = createApp(App)
app.config.errorHandler = (error, _instance, info) => console.error('[Info Agent] UI error:', error, info)
app.use(TDesign)
app.use(createPinia())
app.use(router)
router.isReady().then(() => app.mount('#app'))
