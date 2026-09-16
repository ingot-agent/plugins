import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import CommandPalette from './CommandPalette.vue'
import { i18n } from '../i18n'

describe('command palette', () => {
  it('gives ungrouped operations a visible fallback label', () => {
    i18n.global.locale.value = 'en'
    const view = mount(CommandPalette, {
      props: {
        phase: 'group',
        groups: [{ name: '', operations: [{ id: 'config-1', name: 'config', group: '', description: 'Configure.', inputSchema: {}, outputSchema: {} }] }],
        selected: 0,
      },
      global: { plugins: [i18n] },
    })
    expect(view.get('[role="option"]').text()).toContain('Ungrouped')
    view.unmount()
  })
})
