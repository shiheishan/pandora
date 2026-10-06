import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createAppRuntime } from '../shell/runtime'
import '../styles/index.css'
import { App } from './App'
import { takeInviteFromUrl } from './entry-links'

const runtime = createAppRuntime('portal')
const invite = takeInviteFromUrl()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App runtime={runtime} invite={invite} />
  </StrictMode>,
)
