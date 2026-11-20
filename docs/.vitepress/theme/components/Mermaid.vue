<script setup lang="ts">
// Renders one Mermaid diagram in the browser, again whenever the reader
// switches between light and dark. Mermaid is loaded only on pages that have
// a diagram.
import { onMounted, ref, watch } from 'vue'
import { useData } from 'vitepress'

const props = defineProps<{ code: string }>()
const { isDark } = useData()
const svg = ref('')
const error = ref('')
let seq = 0

async function render() {
  const source = decodeURIComponent(props.code)
  const { default: mermaid } = await import('mermaid')
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: isDark.value ? 'dark' : 'default',
    // A concrete font stack: Mermaid measures labels with it, so it must match what is drawn.
    fontFamily: 'ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif',
    flowchart: { padding: 12 },
  })
  try {
    const id = `mermaid-${Math.random().toString(36).slice(2)}-${seq++}`
    const out = await mermaid.render(id, source)
    svg.value = out.svg
    error.value = ''
  } catch (e: any) {
    error.value = String(e?.message || e)
  }
}

onMounted(render)
watch(isDark, render)
</script>

<template>
  <div class="mermaid-diagram" :data-mermaid-error="error ? 'true' : null">
    <div v-if="error" class="mermaid-error" role="alert">Diagram error: {{ error }}</div>
    <div v-else-if="svg" v-html="svg" />
    <pre v-else class="mermaid-source"><code>{{ decodeURIComponent(code) }}</code></pre>
  </div>
</template>

<style scoped>
.mermaid-diagram { margin: 16px 0; overflow-x: auto; text-align: center; }
.mermaid-diagram :deep(svg) { max-width: 100%; height: auto; }
/* Labels were measured without the page's text styles; keep them out. */
.mermaid-diagram :deep(foreignObject) :is(div, span, p) { line-height: 1.5 !important; margin: 0 !important; font-size: 14px; }
.mermaid-error { color: var(--vp-c-danger-1); font-family: var(--vp-font-family-mono); white-space: pre-wrap; text-align: left; }
.mermaid-source { text-align: left; opacity: .6; }
</style>
