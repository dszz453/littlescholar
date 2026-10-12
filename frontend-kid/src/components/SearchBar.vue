<template>
  <div class="searchbar" :class="{ focused }">
    <span class="ico">🔍</span>
    <input
      ref="inputEl"
      v-model="inner"
      class="input"
      type="search"
      :placeholder="placeholder"
      enterkeyhint="search"
      @focus="focused = true"
      @blur="focused = false"
      @keyup.enter="submit"
    />
    <span v-if="inner" class="clear" @click="clear">✕</span>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '' },
  placeholder: { type: String, default: '想听什么？想学什么？' },
})

const emit = defineEmits(['update:modelValue', 'search'])

const inner = ref(props.modelValue)
const focused = ref(false)
const inputEl = ref(null)

watch(
  () => props.modelValue,
  (v) => {
    if (v !== inner.value) inner.value = v
  }
)

function submit() {
  const v = inner.value.trim()
  if (!v) return
  emit('search', v)
  inputEl.value?.blur()
}

function clear() {
  inner.value = ''
  emit('update:modelValue', '')
  emit('search', '')
}

function focus() {
  inputEl.value?.focus()
}

defineExpose({ focus })
</script>

<style scoped>
.searchbar {
  display: flex;
  align-items: center;
  gap: 9px;
  height: 48px;
  padding: 0 16px;
  background: #fff;
  border-radius: 999px;
  box-shadow: 0 3px 14px rgba(44, 62, 80, 0.09);
  transition: box-shadow 0.2s ease, transform 0.2s ease;
}
.searchbar.focused {
  box-shadow: 0 5px 20px rgba(91, 143, 249, 0.22);
}

.ico {
  font-size: 17px;
  line-height: 1;
  flex: 0 0 auto;
}

.input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: 0;
  background: transparent;
  font-size: 15.5px;
  color: var(--c-text);
  font-family: inherit;
}
.input::placeholder {
  color: #aab6c4;
}
/* 去掉 Safari 自带的清除按钮，用自定义的 */
.input::-webkit-search-cancel-button {
  display: none;
}

.clear {
  flex: 0 0 auto;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: #dfe6ee;
  color: #fff;
  font-size: 11px;
  display: flex;
  align-items: center;
  justify-content: center;
}
</style>
