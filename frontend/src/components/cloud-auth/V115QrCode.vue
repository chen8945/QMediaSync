<script setup lang="ts">
import * as QRCode from 'qrcode'
import { nextTick, onMounted, shallowRef, useTemplateRef, watch } from 'vue'

const props = defineProps<{
  content: string
}>()

const canvasRef = useTemplateRef<HTMLCanvasElement>('canvas')
const renderError = shallowRef('')

const renderQrCode = async () => {
  await nextTick()
  const canvas = canvasRef.value
  if (!canvas || !props.content) return

  try {
    await QRCode.toCanvas(canvas, props.content, {
      width: 220,
      margin: 2,
      errorCorrectionLevel: 'M',
      color: {
        dark: '#1f2937',
        light: '#ffffff',
      },
    })
    renderError.value = ''
  } catch (error) {
    renderError.value = error instanceof Error ? error.message : '二维码生成失败'
  }
}

onMounted(() => void renderQrCode())

watch(
  () => props.content,
  () => void renderQrCode(),
  { flush: 'post' },
)
</script>

<template>
  <div class="v115-qr-code">
    <canvas
      ref="canvas"
      class="v115-qr-code__canvas"
      width="220"
      height="220"
      role="img"
      aria-label="115 授权二维码"
    />
    <div v-if="renderError" class="v115-qr-code__error">
      {{ renderError }}
    </div>
  </div>
</template>

<style scoped>
.v115-qr-code {
  position: relative;
  width: 220px;
  height: 220px;
}

.v115-qr-code__canvas {
  display: block;
  width: 220px;
  height: 220px;
}

.v115-qr-code__error {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 16px;
  text-align: center;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  font-size: 13px;
  line-height: 1.4;
}
</style>
