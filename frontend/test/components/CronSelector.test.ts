// @vitest-environment happy-dom
import { defineComponent, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import CronSelector from '@/components/common/CronSelector.vue'

const globalStubs = {
  ElSelect: {
    props: ['modelValue'],
    emits: ['update:modelValue', 'change'],
    template: `
      <select
        data-test="cron-preset"
        :value="modelValue"
        @change="$emit('update:modelValue', $event.target.value); $emit('change', $event.target.value)"
      >
        <slot />
      </select>
    `,
  },
  ElOption: {
    props: ['label', 'value'],
    template: '<option :value="value">{{ label }}</option>',
  },
  ElInput: {
    props: ['modelValue'],
    emits: ['update:modelValue', 'input'],
    template: `
      <input
        data-test="custom-cron"
        :value="modelValue"
        @input="$emit('update:modelValue', $event.target.value); $emit('input', $event.target.value)"
      />
    `,
  },
}

describe('CronSelector', () => {
  it('切换预设后再切回自定义时恢复父组件保存的自定义 Cron', async () => {
    const Harness = defineComponent({
      components: { CronSelector },
      setup() {
        const cron = ref('15 1 * * *')
        const customCron = ref('15 1 * * *')
        return { cron, customCron }
      },
      template: '<CronSelector v-model="cron" v-model:custom-value="customCron" />',
    })

    const wrapper = mount(Harness, {
      global: { stubs: globalStubs },
    })
    await nextTick()

    await wrapper.get('[data-test="custom-cron"]').setValue('10 5 * * *')
    expect((wrapper.vm as unknown as { cron: string; customCron: string }).customCron).toBe(
      '10 5 * * *',
    )

    await wrapper.get('[data-test="cron-preset"]').setValue('0 3 * * *')
    expect((wrapper.vm as unknown as { cron: string; customCron: string }).cron).toBe('0 3 * * *')

    await wrapper.get('[data-test="cron-preset"]').setValue('custom')

    expect((wrapper.vm as unknown as { cron: string; customCron: string }).cron).toBe('10 5 * * *')
  })

  it('自定义草稿等于预设值时切回自定义也同步实际 Cron', async () => {
    const Harness = defineComponent({
      components: { CronSelector },
      setup() {
        const cron = ref('0 3 * * *')
        const customCron = ref('0 3 * * *')
        return { cron, customCron }
      },
      template: '<CronSelector v-model="cron" v-model:custom-value="customCron" />',
    })

    const wrapper = mount(Harness, {
      global: { stubs: globalStubs },
    })
    await nextTick()

    await wrapper.get('[data-test="cron-preset"]').setValue('0 */4 * * *')
    expect((wrapper.vm as unknown as { cron: string; customCron: string }).cron).toBe('0 */4 * * *')

    await wrapper.get('[data-test="cron-preset"]').setValue('custom')

    expect((wrapper.vm as unknown as { cron: string; customCron: string }).cron).toBe('0 3 * * *')
  })
})
