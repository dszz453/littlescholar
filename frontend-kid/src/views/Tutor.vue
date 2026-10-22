<template>
  <div class="page tutor-page">
    <header class="bar" :style="barStyle">
      <div class="head">
        <span class="avatar">🤖</span>
        <div class="meta">
          <div class="name">小书童</div>
          <div class="status">你的学习小伙伴</div>
        </div>
      </div>
    </header>

    <div class="chat" ref="chatEl">
      <!-- 空态：引导问题 -->
      <div v-if="!messages.length" class="intro">
        <span class="big">🤖</span>
        <h2>你好，我是小书童</h2>
        <p>我可以帮你想想学什么，也能推荐乐园里的内容</p>

        <div class="suggests">
          <button
            v-for="s in suggestions"
            :key="s"
            class="suggest pressable"
            @click="send(s)"
          >{{ s }}</button>
        </div>
      </div>

      <!-- 消息流 -->
      <div
        v-for="(m, i) in messages"
        :key="i"
        class="msg"
        :class="m.role"
      >
        <span v-if="m.role === 'assistant'" class="m-avatar">🤖</span>
        <div class="bubble">
          <div class="text">{{ m.content }}</div>

          <!-- AI 推荐了站内资源 → 可点直接播放 -->
          <div v-if="m.resources?.length" class="recs">
            <div
              v-for="r in m.resources"
              :key="r.id"
              class="rec pressable"
              @click="goPlay(r)"
            >
              <span class="rec-ico">{{ recEmoji(r) }}</span>
              <span class="rec-title ellipsis">{{ r.title }}</span>
              <span class="rec-go">›</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 正在思考 -->
      <div v-if="thinking" class="msg assistant">
        <span class="m-avatar">🤖</span>
        <div class="bubble typing">
          <span></span><span></span><span></span>
        </div>
      </div>
    </div>

    <!-- 输入区 -->
    <div class="composer">
      <div class="input-wrap">
        <input
          v-model="draft"
          type="text"
          placeholder="问问小书童…"
          enterkeyhint="send"
          @keyup.enter="onEnter"
        />
        <button
          class="send pressable"
          :disabled="!draft.trim() || thinking"
          @click="send()"
        >发送</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { aiApi } from '@/api'
import { useUserStore } from '@/stores/user'

const router = useRouter()
const userStore = useUserStore()

const chatEl = ref(null)
const draft = ref('')
const thinking = ref(false)
const messages = ref([])

const barStyle = computed(() => ({
  paddingTop: 'calc(12px + var(--safe-top))',
}))

// 引导问题会带上孩子年级（若后台已设置）
const suggestions = computed(() => {
  const g = userStore.grade
  const list = []
  if (g) list.push(`${g}适合学什么？`)
  list.push('推荐一些英语内容')
  list.push('今天学点什么好？')
  return list.slice(0, 4)
})

function recEmoji(r) {
  const map = { audio: '🎧', video: '🎬', pdf: '📄', image: '🖼️', ebook: '📖' }
  return map[r.mediaType] || '📚'
}

async function scrollToBottom() {
  await nextTick()
  const el = chatEl.value
  if (el) el.scrollTop = el.scrollHeight
}

async function send(text) {
  const content = (text ?? draft.value).trim()
  if (!content || thinking.value) return

  draft.value = ''
  messages.value.push({ role: 'user', content })
  thinking.value = true
  await scrollToBottom()

  try {
    // 只带最近若干轮，避免上下文过长
    const history = messages.value.slice(-8).map((m) => ({
      role: m.role,
      content: m.content,
    }))
    const data = await aiApi.chat({ messages: history })

    messages.value.push({
      role: 'assistant',
      content: data?.content || data?.message || '我想想…',
      resources: data?.resources || [],
    })
  } catch (e) {
    messages.value.push({
      role: 'assistant',
      content:
        '抱歉，我暂时想不出来 😅 你可以先去看看乐园里的内容，或者稍后再问我。',
    })
  } finally {
    thinking.value = false
    await scrollToBottom()
  }
}

function onEnter() {
  send()
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

onMounted(() => {
  // 确保有年级信息用于推荐
  userStore.loadProfile().catch(() => {})
})
</script>

<style scoped>
.tutor-page {
  display: flex;
  flex-direction: column;
  height: 100vh;
  /* 底部给 TabBar 让位。
     这里以前写的是 padding-bottom: 0（覆盖 .page 的 24px），而 TabBar 是
     fixed + z-index:100，正好压在输入框上 —— 孩子看到的输入框其实点不到，
     所以"没法打字"。58px 是 TabBar 的实测高度，safe-area 另计。 */
  padding-bottom: calc(var(--tabbar-h) + var(--safe-bottom));
}

.bar {
  flex: 0 0 auto;
  padding: 0 16px 12px;
  background: linear-gradient(180deg, #e8f0ff 0%, var(--c-bg) 100%);
}
.head {
  display: flex;
  align-items: center;
  gap: 11px;
}
.avatar {
  width: 42px;
  height: 42px;
  border-radius: 50%;
  background: #fff;
  box-shadow: var(--shadow-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 23px;
}
.meta .name {
  font-size: 16.5px;
  font-weight: 700;
}
.meta .status {
  font-size: 12.5px;
  color: var(--c-text-sub);
}

/* ===== 聊天区 ===== */

.chat {
  flex: 1;
  overflow-y: auto;
  padding: 16px 16px 8px;
  -webkit-overflow-scrolling: touch;
}

.intro {
  text-align: center;
  padding: 32px 16px;
}
.big {
  display: inline-block;
  font-size: 58px;
  line-height: 1;
  margin-bottom: 12px;
}
.intro h2 {
  margin: 0 0 6px;
  font-size: 19px;
  font-weight: 700;
}
.intro p {
  margin: 0 0 20px;
  font-size: 14px;
  color: var(--c-text-sub);
}

.suggests {
  display: flex;
  flex-direction: column;
  gap: 9px;
  max-width: 300px;
  margin: 0 auto;
}
.suggest {
  padding: 12px 16px;
  border: 1px solid #e3ecfb;
  border-radius: var(--r-md);
  background: #f5f9ff;
  color: var(--c-primary);
  font-size: 14.5px;
  font-family: inherit;
  text-align: left;
}

/* ===== 消息气泡 ===== */

.msg {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}
.msg.user {
  justify-content: flex-end;
}

.m-avatar {
  flex: 0 0 auto;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #fff;
  box-shadow: var(--shadow-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
}

.bubble {
  max-width: 76%;
  padding: 11px 14px;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
  animation: popIn 0.28s cubic-bezier(0.34, 1.56, 0.64, 1) both;
}
.msg.user .bubble {
  background: linear-gradient(135deg, #6b9bff, #5b8ff9);
  color: #fff;
}

.text {
  font-size: 15px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

/* 推荐资源 */
.recs {
  margin-top: 11px;
  padding-top: 11px;
  border-top: 1px solid var(--c-border);
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.rec {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 11px;
  background: #f5f9ff;
  border-radius: var(--r-sm);
}
.rec-ico {
  font-size: 17px;
  line-height: 1;
}
.rec-title {
  flex: 1;
  min-width: 0;
  font-size: 13.5px;
  color: var(--c-text);
}
.rec-go {
  color: var(--c-primary);
  font-size: 17px;
  line-height: 1;
}

/* 打字动画 */
.typing {
  display: flex;
  gap: 5px;
  padding: 14px;
}
.typing span {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #c8d3e0;
  animation: blink 1.3s infinite ease-in-out;
}
.typing span:nth-child(2) { animation-delay: 0.2s; }
.typing span:nth-child(3) { animation-delay: 0.4s; }

@keyframes blink {
  0%, 80%, 100% { opacity: 0.35; transform: translateY(0); }
  40% { opacity: 1; transform: translateY(-3px); }
}

/* ===== 输入区 ===== */

.composer {
  flex: 0 0 auto;
  /* safe-area 由 .tutor-page 的 padding-bottom 统一让出，这里不再重复加，
     否则底部会出现两倍安全区的空白。 */
  padding: 10px 16px;
  background: rgba(255, 255, 255, 0.96);

  border-top: 1px solid var(--c-border);
}
.input-wrap {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 5px 5px 5px 16px;
  background: #f3f6fa;
  border-radius: 999px;
}
.input-wrap input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: 0;
  background: transparent;
  font-size: 15.5px;
  font-family: inherit;
  color: var(--c-text);
  height: 38px;
}
.input-wrap input::placeholder {
  color: #aab6c4;
}

.send {
  flex: 0 0 auto;
  height: 38px;
  padding: 0 20px;
  border: 0;
  border-radius: 999px;
  background: var(--c-primary);
  color: #fff;
  font-size: 14.5px;
  font-weight: 600;
  font-family: inherit;
}
.send:disabled {
  opacity: 0.4;
}
</style>
