import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ToolCard from './ToolCard.vue'
import { request } from '../api'
import { i18n } from '../i18n'
import { useRuntime } from '../stores/runtime'

vi.mock('../api', async importOriginal => ({
  ...await importOriginal<typeof import('../api')>(),
  request: vi.fn(),
}))

let wrapper: VueWrapper
beforeEach(() => vi.resetAllMocks())
afterEach(() => wrapper?.unmount())

describe('child session usage', () => {
  it('queries each child on expansion and keeps its total separate from the root', async () => {
    const pinia = createPinia()
    const runtime = useRuntime(pinia)
    runtime.totalTokenBySession.root = 189
    vi.mocked(request)
      .mockResolvedValueOnce({ id: 'A', title: 'child', createdAt: '', updatedAt: '', totalToken: 20 })
      .mockResolvedValueOnce({ id: 'B', title: 'child', createdAt: '', updatedAt: '', totalToken: 35 })
    wrapper = mount(ToolCard, {
      props: { name: 'list_agents', content: [{ kind: 'text', text: JSON.stringify({ page: { children: [{ session_id: 'A' }, { session_id: 'B' }, { session_id: 'B' }] } }) }] },
      global: { plugins: [pinia, i18n] },
    })
    expect(request).not.toHaveBeenCalled()
    const details = wrapper.get('details')
    const element = details.element as HTMLDetailsElement
    element.open = true
    await details.trigger('toggle')
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('.child-session-usage strong').map(item => item.text())).toEqual(['20', '35'])
    expect(runtime.totalTokenBySession.root).toBe(189)
    expect(runtime.sessions).toEqual([])

    const name = 'model-runtime.session-usage/B'
    runtime.receive(1, { type: 'interaction.state.set', data: {
      id: name, name, scope: { agent: { sessionId: 'B' } },
      values: [{ name: 'sessionId', value: 'B' }, { name: 'totalToken', value: 45 }],
    } })
    await flushPromises()
    expect(wrapper.findAll('.child-session-usage strong').map(item => item.text())).toEqual(['20', '45'])
  })

  it('loads a child snapshot arriving after expansion and accepts plain tool output', async () => {
    const pinia = createPinia()
    vi.mocked(request).mockResolvedValue({ id: 'child', title: '', createdAt: '', updatedAt: '', totalToken: 13 })
    wrapper = mount(ToolCard, { props: { name: 'spawn_agent', content: [{ kind: 'text', text: 'Starting a child' }] }, global: { plugins: [pinia, i18n] } })
    const details = wrapper.get('details')
    const element = details.element as HTMLDetailsElement
    element.open = true
    await details.trigger('toggle')
    expect(request).not.toHaveBeenCalled()
    await wrapper.setProps({ content: [{ kind: 'text', text: '{"child":{"session_id":"child"}}' }] })
    await flushPromises()
    expect(request).toHaveBeenCalledWith('/sessions/child')
    expect(wrapper.get('.child-session-usage strong').text()).toBe('13')
  })
})
