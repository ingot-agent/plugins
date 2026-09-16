import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { createPinia } from 'pinia'
import OperationFields from './OperationFields.vue'
import InteractionForm from './InteractionForm.vue'
import { i18n } from '../i18n'
import { initialInteractionValues, interactionValues, InteractionValueError } from '../forms'
import type { InteractionField } from '../protocol'
import { useRuntime } from '../stores/runtime'

const field = (overrides: Partial<InteractionField>): InteractionField => ({ name: 'name', label: 'Name', kind: 'string', required: false, sensitive: false, hasDefault: false, ...overrides })
const fields = [field({ name: 'providers', label: 'Providers', kind: 'list', hasDefault: true,
  default: [{ name: 'Example', secret: undefined, models: ['model-a'], prompt: 'line one\nline two' }],
  element: field({ name: 'provider', kind: 'object', fields: [
    field({ required: true }), field({ name: 'secret', label: 'API key', sensitive: true, hasDefault: true }),
    field({ name: 'prompt', label: 'Prompt' }),
    field({ name: 'models', label: 'Models', kind: 'list', element: field({ name: 'model' }) }),
  ] }),
})]
const mountEditor = () => mount(defineComponent({ components: { OperationFields }, setup() { return { fields, values: ref(initialInteractionValues(fields)) } }, template: '<OperationFields id="test" v-model="values" :fields="fields" />' }), { global: { plugins: [i18n] } })

describe('operation field navigation', () => {
  it('opens one level at a time and retains edits and untouched multiline values', async () => {
    const view = mountEditor()
    expect(view.findAll('input')).toHaveLength(0)
    await view.get('#test-providers-open').trigger('click')
    expect(view.findAll('input')).toHaveLength(0)
    await view.get('#test-providers-0-open').trigger('click')
    expect(view.findAll('textarea')).toHaveLength(0)
    expect(view.get('#test-providers-0-secret').attributes('type')).toBe('password')
    await view.get('#test-providers-0-name').setValue('Updated')
    await view.get('#test-providers-0-models-open').trigger('click')
    expect(view.find('#test-providers-0-name').exists()).toBe(false)
    await view.get('#test-providers-0-models-0').setValue('model-b')
    await view.get('.field-breadcrumbs .icon-button').trigger('click')
    expect(view.get<HTMLInputElement>('#test-providers-0-name').element.value).toBe('Updated')
    const values = interactionValues(fields, view.vm.values)
    expect(values).toEqual({ providers: [{ name: 'Updated', models: ['model-b'], prompt: 'line one\nline two' }] })
    view.unmount()
  })

  it('preserves explicit empty lists, and isolates newly added item values', async () => {
    const view = mountEditor()
    await view.get('#test-providers-open').trigger('click')
    await view.get('.field-page-actions .btn').trigger('click')
    await view.get('#test-providers-1-name').setValue('Second')
    await view.get('.field-breadcrumbs .icon-button').trigger('click')
    await view.get('.field-remove').trigger('click')
    expect(view.vm.values.providers).toEqual([{ name: 'Second' }])
    await view.get('#test-providers-0-open').trigger('click')
    expect(view.get<HTMLInputElement>('#test-providers-0-name').element.value).toBe('Second')
    await view.get('.field-breadcrumbs .icon-button').trigger('click')
    await view.get('.field-remove').trigger('click')
    expect(interactionValues(fields, view.vm.values)).toEqual({ providers: [] })
    view.unmount()
  })

  it('reveals a missing required field inside a previously hidden item on submit', async () => {
    const pinia = createPinia()
    useRuntime(pinia).connection = 'online'
    const view = mount(InteractionForm, { props: { interaction: { id: 'op', name: 'config', scope: { operation: { invocationId: 'call' } }, fields: [{ ...fields[0], default: [{ models: [] }] }] } }, global: { plugins: [pinia, i18n] } })
    expect(view.findAll('input')).toHaveLength(0)
    await view.get('form').trigger('submit')
    expect(view.find('input').exists()).toBe(true)
    expect(view.find('.field-invalid').exists()).toBe(true)
    expect(view.get('[role="alert"]').text()).toBeTruthy()
    expect(() => interactionValues(fields, { providers: [{ models: [] }] })).toThrow(InteractionValueError)
    try { interactionValues(fields, { providers: [{ models: [] }] }) }
    catch (cause) { expect((cause as InteractionValueError).path).toEqual(['providers', 0, 'name']) }
    view.unmount()
  })
})
