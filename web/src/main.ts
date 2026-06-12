import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { installMock, mockEnabled } from './api/mock'
import './tokens.css'
import './app.css'

// dev-only: patch fetch + EventSource with fake wire data when ?mock or VITE_MOCK=1
if (mockEnabled()) installMock()

const app = createApp(App)
app.use(createPinia())
app.mount('#app')
