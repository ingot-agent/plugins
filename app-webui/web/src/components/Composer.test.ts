import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import Composer from './Composer.vue'
import { useRuntime } from '../stores/runtime'
import { i18n } from '../i18n'
import type { Operation } from '../protocol'
import { command } from '../api'

vi.mock('../api', async importOriginal => ({ ...await importOriginal<typeof import('../api')>(), command: vi.fn() }))

let wrapper: VueWrapper
function composer(operations: Operation[] = []) {
  const pinia = createPinia()
  const runtime = useRuntime(pinia)
  runtime.connection = 'online'
  runtime.operations = operations
  wrapper = mount(Composer, { props: { sessionKey: 'new', running: [] }, global: { plugins: [pinia, i18n] } })
  return wrapper
}
afterEach(() => { wrapper?.unmount(); vi.resetAllMocks() })

describe('composer drafts', () => {
  it('selects a local file and sends it without creating a browser upload', async () => {
    const view = composer()
    const runtime = useRuntime()
    runtime.files = { available: true, maxBytes: 1000 }
    runtime.defaultWorkspace = '/workspace'
    const file = { kind: 'file', path: '/workspace/report.pdf', name: 'report.pdf', size: 8, mimeType: 'application/pdf' }
    vi.mocked(command).mockResolvedValueOnce({ files: [file] })
    await view.vm.$nextTick()
    await view.get('button[aria-label="' + i18n.global.t('attach') + '"]').trigger('click')
    await flushPromises()
    expect(command).toHaveBeenCalledWith('/files/select', 'POST', { initialPath: '/workspace' }, expect.any(AbortSignal))
    expect(view.find('input[type=file]').exists()).toBe(false)
    expect(view.get('.upload-chip').text()).toContain('report.pdf')
    await view.get('form').trigger('submit')
    expect(view.emitted('send')?.[0].slice(0, 2)).toEqual(['', [file]])
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.find('.upload-chip').exists()).toBe(false)
  })

  it('keeps a pending selection with the draft that opened it', async () => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    let complete!: (value: unknown) => void
    vi.mocked(command).mockReturnValueOnce(new Promise(resolve => { complete = resolve }))
    await view.vm.$nextTick()
    await view.get('button[aria-label="' + i18n.global.t('attach') + '"]').trigger('click')
    await view.setProps({ sessionKey: 'other' })
    complete({ files: [{ kind: 'file', path: '/workspace/report.pdf', name: 'report.pdf', size: 8 }] })
    await flushPromises()
    expect(view.find('.upload-chip').exists()).toBe(false)
    await view.setProps({ sessionKey: 'new' })
    expect(view.get('.upload-chip').text()).toContain('report.pdf')
  })

  it('preserves the text draft when native file selection is canceled', async () => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    await view.get('textarea').setValue('keep me')
    vi.mocked(command).mockResolvedValueOnce({ files: [] })
    await view.get('button[aria-label="' + i18n.global.t('attach') + '"]').trigger('click')
    await flushPromises()
    expect(view.get('textarea').element.value).toBe('keep me')
    expect(view.find('.upload-chip').exists()).toBe(false)
  })

  it('does not send Enter used to confirm IME composition or add a newline', async () => {
    const view = composer()
    const input = view.get('textarea')
    await input.setValue('你好')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    await input.trigger('keydown', { key: 'Enter', keyCode: 229 })
    await input.trigger('keydown', { key: 'Enter', shiftKey: true })
    expect(view.emitted('send')).toBeUndefined()
    await input.trigger('keydown', { key: 'Enter' })
    expect(view.emitted('send')?.[0][0]).toBe('你好')
  })

  it('retains a draft after lazy session creation until sending is accepted', async () => {
    const view = composer()
    await view.get('textarea').setValue('keep this draft')
    await view.get('form').trigger('submit')
    await view.setProps({ sending: true, sessionKey: 'created-session' })
    await view.setProps({ sending: false })
    expect(view.get('textarea').element.value).toBe('keep this draft')
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.get('textarea').element.value).toBe('')
  })

  it('does not clear a different session draft when an earlier send completes', async () => {
    const view = composer()
    await view.setProps({ sessionKey: 'first' })
    await view.get('textarea').setValue('first draft')
    await view.get('form').trigger('submit')
    await view.setProps({ sessionKey: 'second' })
    await view.get('textarea').setValue('second draft')
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.get('textarea').element.value).toBe('second draft')
    await view.setProps({ sessionKey: 'first' })
    expect(view.get('textarea').element.value).toBe('')
  })

  it('blocks message submission while still allowing global commands without a workspace', async () => {
    const view = composer()
    await view.get('textarea').setValue('wait for workspace')
    await view.setProps({ disabled: true })
    await view.get('form').trigger('submit')
    expect(view.get('textarea').attributes('disabled')).toBeUndefined()
    expect(view.emitted('send')).toBeUndefined()
  })

  it('executes a complete slash command without emitting a message', async () => {
    const operation: Operation = { id: 'shell-config', group: 'tool-shell', name: 'config', description: 'Configure shell', inputSchema: {}, outputSchema: {} }
    const view = composer([operation])
    await view.setProps({ disabled: true })
    await view.get('textarea').setValue('/tool-shell config')
    await view.get('textarea').trigger('keydown', { key: 'Enter' })
    expect(view.emitted('command')?.[0]).toEqual([operation])
    expect(view.emitted('send')).toBeUndefined()
  })

  it('completes group and operation candidates from the keyboard', async () => {
    const operation: Operation = { id: 'shell-config', group: 'tool-shell', name: 'config', description: 'Configure shell', inputSchema: {}, outputSchema: {} }
    const view = composer([operation])
    const input = view.get('textarea')
    await input.setValue('/tool')
    await input.trigger('keydown', { key: 'Enter' })
    expect(input.element.value).toBe('/tool-shell ')
    await input.trigger('keydown', { key: 'Tab' })
    expect(input.element.value).toBe('/tool-shell config')
  })
})
