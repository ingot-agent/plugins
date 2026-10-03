import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ExecutionPanel from './ExecutionPanel.vue'
import { useRuntime } from '../stores/runtime'
import { i18n } from '../i18n'

let wrapper: VueWrapper
afterEach(() => wrapper?.unmount())

describe('session usage sidebar card', () => {
  it('shows only session and context totals even when Turn and Round events exist', async () => {
    const pinia = createPinia()
    const runtime = useRuntime(pinia)
    runtime.totalTokenBySession.s = 150
    runtime.contextBySession.s = { sessionId: 's', inputTokens: 0, accuracy: 'estimate', source: 'estimator', provider: 'test-provider', model: 'test-model', turnId: 'turn', roundIndex: 3 }
    runtime.traces.s = [{ cursor: 1, type: 'agent.model.finished', scope: { agent: { sessionId: 's', turnId: 'turn', roundIndex: 3 } }, data: { status: 'succeeded', response: { usage: { reported: true, inputTokens: 40, outputTokens: 10, totalTokens: 50 } } } }]
    wrapper = mount(ExecutionPanel, { props: { sessionId: 's' }, global: { plugins: [pinia, i18n] } })
    expect(wrapper.get('[data-testid="session-total"]').text()).toBe('150')
    expect(wrapper.get('[data-testid="context-total"]').text()).toBe('0')
    expect(wrapper.findAll('.usage-metrics > div')).toHaveLength(2)
    expect(wrapper.find('details, table, .execution-outcome, .trace-turn').exists()).toBe(false)
    expect(wrapper.text()).not.toMatch(/Turn|Round|Latest request input|test-provider|test-model|Estimate/)
    await wrapper.setProps({ sessionId: 'other' })
    expect(wrapper.get('[data-testid="session-total"]').text()).toBe('Unavailable')
    expect(wrapper.get('[data-testid="context-total"]').text()).toBe('Unavailable')
  })
})
