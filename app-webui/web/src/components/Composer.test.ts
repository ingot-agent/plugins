import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import Composer from './Composer.vue'
import { useRuntime } from '../stores/runtime'
import { i18n } from '../i18n'
import type { Operation } from '../protocol'
import { turnSnapshot } from '../test/turn'

import { command, request } from '../api'

vi.mock('../api', async importOriginal => ({ ...await importOriginal<typeof import('../api')>(), command: vi.fn(), request: vi.fn() }))

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

function pasteFiles(view: VueWrapper, files: File[], itemsOnly = false) {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', { value: {
    files: itemsOnly ? [] : files,
    items: files.map(file => ({ kind: 'file', getAsFile: () => file })),
  } })
  view.get('textarea').element.dispatchEvent(event)
  return event
}

describe('clipboard attachments', () => {
  it('leaves ordinary text paste to the browser', () => {
    const view = composer()
    expect(pasteFiles(view, []).defaultPrevented).toBe(false)
    expect(request).not.toHaveBeenCalled()
  })

  it('uploads pasted images and documents in clipboard order before allowing send', async () => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    const image = new File(['image'], 'photo.png', { type: 'image/png' })
    const document = new File(['document'], 'report.pdf', { type: 'application/pdf' })
    const attachments = [
      { kind: 'image', name: image.name, mimeType: image.type, assetId: 'clipboard-image', size: image.size },
      { kind: 'file', name: document.name, mimeType: document.type, path: '/uploads/file-1/report.pdf', size: document.size },
    ]
    let complete!: (value: unknown) => void
    vi.mocked(request).mockReturnValueOnce(new Promise(resolve => { complete = resolve })).mockResolvedValueOnce(attachments[1])
    await view.get('textarea').setValue('keep this text')
    expect(pasteFiles(view, [image, document]).defaultPrevented).toBe(true)
    await flushPromises()
    expect(view.findAll('.upload-chip').map(item => item.text())).toEqual([expect.stringContaining('photo.png'), expect.stringContaining('report.pdf')])
    expect(view.get('button[type=submit]').attributes('disabled')).toBeDefined()
    await view.get('form').trigger('submit')
    expect(view.emitted('send')).toBeUndefined()
    expect(request).toHaveBeenNthCalledWith(1, '/files/upload?name=photo.png', expect.objectContaining({ method: 'POST', body: image, signal: expect.any(AbortSignal) }))
    expect(request).toHaveBeenNthCalledWith(2, '/files/upload?name=report.pdf', expect.objectContaining({ body: document }))
    complete(attachments[0])
    await flushPromises()
    expect(view.get('.upload-chip img').attributes('src')).toBe('/api/assets/clipboard-image')
    await view.get('form').trigger('submit')
    expect(view.emitted('send')?.[0].slice(0, 2)).toEqual(['keep this text', attachments])
  })

  it('accepts clipboard items with only Asset support and retains the originating draft', async () => {
    const view = composer()
    useRuntime().assets = { available: true, maxBytes: 1000 }
    let complete!: (value: unknown) => void
    vi.mocked(request).mockReturnValueOnce(new Promise(resolve => { complete = resolve }))
    pasteFiles(view, [new File(['image'], 'image.png')], true)
    await view.setProps({ sessionKey: 'other' })
    complete({ kind: 'image', name: 'image.png', assetId: 'image-one', size: 5 })
    await flushPromises()
    expect(view.find('.upload-chip').exists()).toBe(false)
    await view.setProps({ sessionKey: 'new' })
    expect(view.get('.upload-chip').text()).toContain('image.png')
    expect(view.get('button[type=submit]').attributes('disabled')).toBeUndefined()
  })

  it('cancels a removed upload without losing other pending files', async () => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    const completions: ((value: unknown) => void)[] = []
    vi.mocked(request).mockImplementation(() => new Promise(resolve => { completions.push(resolve) }))
    pasteFiles(view, [new File(['a'], 'a.txt'), new File(['b'], 'b.txt')])
    await view.vm.$nextTick()
    const signal = vi.mocked(request).mock.calls[0]![1]!.signal!
    await view.findAll('.upload-chip button')[0]!.trigger('click')
    expect(signal.aborted).toBe(true)
    completions[0]!({ kind: 'file', name: 'a.txt', path: '/uploads/a.txt' })
    completions[1]!({ kind: 'file', name: 'b.txt', path: '/uploads/b.txt' })
    await flushPromises()
    await view.get('form').trigger('submit')
    expect(view.emitted('send')?.[0][1]).toEqual([{ kind: 'file', name: 'b.txt', path: '/uploads/b.txt' }])
  })

  it('reports oversize and failed uploads while preserving the text draft', async () => {
    const view = composer()
    const runtime = useRuntime()
    runtime.files = { available: true, maxBytes: 3 }
    vi.mocked(request).mockRejectedValueOnce(new Error('Upload failed'))
    await view.get('textarea').setValue('keep me')
    pasteFiles(view, [new File(['large'], 'large.txt'), new File(['ok'], 'small.txt')])
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(1)
    expect(runtime.notices.map(item => item.message)).toEqual([
      i18n.global.t('attachmentTooLarge', { name: 'large.txt', size: '1 KB' }), 'small.txt: Upload failed',
    ])
    expect(view.find('.upload-chip').exists()).toBe(false)
    expect(view.get('textarea').element.value).toBe('keep me')
    expect(view.get('button[type=submit]').attributes('disabled')).toBeUndefined()
  })

  it.each(['archived', 'disabled', 'sending'] as const)('blocks file paste when %s', async prop => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    await view.setProps({ [prop]: true })
    expect(pasteFiles(view, [new File(['file'], 'file.txt')]).defaultPrevented).toBe(true)
    expect(request).not.toHaveBeenCalled()
  })

  it('reports unavailable attachments without uploading', () => {
    const view = composer()
    pasteFiles(view, [new File(['file'], 'file.txt')])
    expect(request).not.toHaveBeenCalled()
    expect(useRuntime().notices[0]?.message).toBe(i18n.global.t('assetUnavailable'))
  })

  it('aborts uploads from inactive drafts when unmounted', async () => {
    const view = composer()
    useRuntime().files = { available: true, maxBytes: 1000 }
    vi.mocked(request).mockReturnValueOnce(new Promise(() => {}))
    pasteFiles(view, [new File(['file'], 'file.txt')])
    const signal = vi.mocked(request).mock.calls[0]![1]!.signal!
    await view.setProps({ sessionKey: 'other' })
    view.unmount()
    expect(signal.aborted).toBe(true)
    expect(useRuntime().notices).toEqual([])
  })
})

describe('composer drafts', () => {
  it('submits messages during a turn and exposes independent stop and send controls', async () => {
    const view = composer()
    await view.setProps({ sessionKey: 's', running: [turnSnapshot({ id: 'active' })] })
    await view.get('textarea').setValue('next request')
    expect(view.find('button[aria-label="' + i18n.global.t('stop') + '"]').exists()).toBe(true)
    expect(view.get('button[type=submit]').attributes('disabled')).toBeUndefined()
    await view.get('textarea').trigger('keydown', { key: 'Enter' })
    expect(view.emitted('send')?.[0][0]).toBe('next request')
  })

  it('keeps text entered while a submission is being acknowledged', async () => {
    const view = composer()
    await view.get('textarea').setValue('submitted')
    await view.get('form').trigger('submit')
    await view.setProps({ sending: true })
    expect(view.get('textarea').attributes('disabled')).toBeUndefined()
    await view.get('textarea').setValue('new draft')
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.get('textarea').element.value).toBe('new draft')
  })

  it('shows only queued inputs in the queue and supports removing them', async () => {
    const view = composer()
    const runtime = useRuntime()
    await view.setProps({ sessionKey: 's' })
    runtime.turns.queued = turnSnapshot({ id: 'q', sequence: 2, input: 'queued request', status: 'queued', started: false, persisted: false, sdkTurnId: undefined })
    runtime.turns.done = turnSnapshot({ id: 'done', input: 'done request', revision: 2, status: 'succeeded' })
    await view.vm.$nextTick()
    expect(view.get('.turn-queue-list').text()).toContain('queued request')
    expect(view.get('.turn-queue-list').text()).not.toContain('done request')
    vi.mocked(command).mockResolvedValueOnce(undefined)
    await view.get('.turn-queue-item .icon-button').trigger('click')
    await flushPromises()
    expect(command).toHaveBeenCalledWith('/turns/q?queued=true', 'DELETE')
  })

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
