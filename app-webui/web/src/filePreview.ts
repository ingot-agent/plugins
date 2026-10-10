import { nextTick, shallowRef, type InjectionKey, type Ref } from 'vue'
import type { FileLink } from './fileLinks'

export interface FileContext { sessionId?: string; basePath?: string }
export type FilePreviewRequest = FileLink & FileContext
export interface FilePreview { path: string; name: string; text: string; size: number }

export function createFilePreview() {
  const current = shallowRef<FilePreviewRequest>()
  let trigger: HTMLElement | undefined
  function open(file: FilePreviewRequest, element?: HTMLElement) {
    if (element && !element.closest('.file-preview')) trigger = element
    current.value = { ...file }
  }
  function close() {
    const element = trigger
    current.value = undefined
    trigger = undefined
    void nextTick(() => { if (!current.value && element?.isConnected) element.focus({ preventScroll: true }) })
  }
  return { current, open, close }
}

export const filePreviewKey: InjectionKey<ReturnType<typeof createFilePreview>> = Symbol('filePreview')
export const fileContextKey: InjectionKey<Readonly<Ref<FileContext>>> = Symbol('fileContext')
