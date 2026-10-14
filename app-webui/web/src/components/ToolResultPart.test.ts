import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import ToolResultPart from './ToolResultPart.vue'
import { i18n } from '../i18n'

let wrapper: VueWrapper

afterEach(() => wrapper?.unmount())

describe('tool result text', () => {
  it('preserves whitespace and renders markup and escape sequences literally', async () => {
    const text = '\n  第一行  with spaces\n\n\tsecond line\r\n<b>literal</b> \\n\n'
    wrapper = mount(ToolResultPart, { props: { parts: [{ kind: 'text', text }] } })
    // wrapper.text() trims whitespace; textContent checks the original string.
    expect(wrapper.get('.tool-result-text').element.textContent).toBe(text)
    expect(wrapper.find('b').exists()).toBe(false)
    const updated = '  next result\n\n'
    await wrapper.setProps({ parts: [{ kind: 'text', text: updated }] })
    expect(wrapper.get('.tool-result-text').element.textContent).toBe(updated)
  })

  it('keeps text and media parts in their original order', () => {
    wrapper = mount(ToolResultPart, {
      props: { parts: [
        { kind: 'text', text: 'before\n' },
        { kind: 'file', name: 'report.txt', mimeType: 'text/plain', source: { kind: 'uri', uri: 'https://example.com/report.txt' } },
        { kind: 'text', text: '\n  after' },
      ] },
      global: { plugins: [i18n] },
    })
    const parts = wrapper.findAll('.tool-result-text, .media-part')
    expect(parts).toHaveLength(3)
    expect(parts[0].element.textContent).toBe('before\n')
    expect(parts[1].classes()).toContain('media-part')
    expect(parts[1].text()).toContain('report.txt')
    expect(parts[2].element.textContent).toBe('\n  after')
  })
})
