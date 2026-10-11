<script setup lang="ts">
import { computed, inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownIt from 'markdown-it'
import hljs from 'highlight.js/lib/common'
import { errorMessage } from '../api'
import { fileFragment, parseFileLink } from '../fileLinks'
import { fileContextKey, filePreviewKey, type FileContext } from '../filePreview'
const props = defineProps<{ text: string; partIndex?: number }>()
const { t } = useI18n()
const copyError = ref('')
const preview = inject(filePreviewKey, undefined)
const fileContext = inject(fileContextKey, ref<FileContext>({}))
const markdown = new MarkdownIt({
  html: false, linkify: true, breaks: true,
  highlight(source, language) {
    if (language && hljs.getLanguage(language)) {
      return hljs.highlight(source, { language, ignoreIllegals: true }).value
    }
    return ''
  },
})
const originalValidateLink = markdown.validateLink
markdown.validateLink = href => originalValidateLink(href) || Boolean(parseFileLink(href))
const originalFence = markdown.renderer.rules.fence!
markdown.renderer.rules.fence = (tokens, index, options, env, self) => {
  const label = markdown.utils.escapeHtml(t('copy'))
  const language = markdown.utils.escapeHtml(tokens[index].info.trim().split(/\s+/)[0])
  return '<div class="code-block"><div class="code-toolbar"><span>' + language +
    '</span><button type="button" class="text-button" data-copy-code>' + label + '</button></div>' +
    originalFence(tokens, index, options, env, self) + '</div>'
}
const originalLink = markdown.renderer.rules.link_open
markdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  const token = tokens[index]
  const href = token.attrGet('href') || ''
  if (parseFileLink(href) || (fileContext.value.basePath && href.startsWith('#'))) {
    token.attrSet('data-file-link', href)
    token.attrSet('href', '#')
    token.attrSet('title', href)
    token.attrSet('class', 'local-file-link')
  } else {
    token.attrSet('target', '_blank')
    token.attrSet('rel', 'noopener noreferrer')
  }
  return originalLink ? originalLink(tokens, index, options, env, self) : self.renderToken(tokens, index, options)
}
// Remote images are links until explicitly opened, avoiding background fetches.
markdown.renderer.rules.image = (tokens, index) => {
  const token = tokens[index]
  const src = token.attrGet('src') || ''
  const text = markdown.utils.escapeHtml(token.content || src)
  return /^https?:\/\//i.test(src)
    ? '<a target="_blank" rel="noopener noreferrer" href="' + markdown.utils.escapeHtml(src) + '">' + text + '</a>'
    : text
}
const html = computed(() => markdown.render(props.text))
function click(event: MouseEvent) {
  const link = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-file-link]') : null
  if (link && (event.currentTarget as HTMLElement).contains(link)) {
    event.preventDefault()
    event.stopPropagation()
    const href = link.dataset.fileLink || ''
    const context = fileContext.value
    let file = parseFileLink(href)
    if (!file && href.startsWith('#') && context.basePath) {
      try { file = { path: context.basePath, ...fileFragment(decodeURIComponent(href.slice(1))) } } catch { return }
    }
    if (file) preview?.open({ ...context, ...file }, link)
    return
  }
  void copyCode(event)
}
async function copyCode(event: MouseEvent) {
  const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('button[data-copy-code]') : null
  if (!button || !(event.currentTarget as HTMLElement).contains(button)) return
  const source = button.parentElement?.nextElementSibling?.textContent
  if (source === undefined || source === null) return
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(source)
    button.textContent = t('copied')
  } catch (error) { copyError.value = errorMessage(error) }
}
</script>
<!-- Local file destinations are converted to preview actions, never file: navigation. -->
<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="markdown" :data-part-index="partIndex" @click="click" v-html="html" />
  <p v-if="copyError" class="error-text" role="alert">{{ copyError }}</p>
</template>
