import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '../styles/index.css'
import { ToastProvider } from '../ui'
import { Showcase } from './Showcase'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ToastProvider>
      <Showcase />
    </ToastProvider>
  </StrictMode>,
)
