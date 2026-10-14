<template>
  <div class="page">
    <!-- 顶部：返回 + 面包屑（统一走 PageBar，安全区与返回兜底只有一处实现） -->
    <PageBar>
      <div class="crumb">
        <span class="crumb-item" @click="goRoot">首页</span>
        <template v-for="c in crumbs" :key="c.id">
          <span class="sep">›</span>
          <span
            class="crumb-item"
            :class="{ current: c.id === node?.id }"
            @click="goCrumb(c)"
          >{{ c.name }}</span>
        </template>
      </div>
      <template #right>
        <span class="ico-lg">{{ icon.emoji }}</span>
      </template>
    </PageBar>

    <div class="page-pad">
      <!-- 小书童说：把这个目录读成一段人话。
           由后台每天定时生成（见后端 DigestHandler），没生成过就不显示。 -->
      <section v-if="digest" class="digest-card pop-in">
        <div class="d-head">
          <span class="d-ava">🤖</span>
          <span class="d-name">小书童说</span>
          <span v-if="digest.resourceCount" class="d-count">{{ digest.resourceCount }} 个内容</span>
        </div>
        <p class="d-intro">{{ digest.intro }}</p>
        <ul v-if="digest.points && digest.points.length" class="d-points">
          <li v-for="(p, i) in digest.points" :key="i">{{ p }}</li>
        </ul>
        <div v-if="digest.keywords && digest.keywords.length" class="d-tags">
          <span v-for="k in digest.keywords" :key="k" class="d-tag">{{ k }}</span>
        </div>
      </section>

      <!-- 子目录 -->
      <template v-if="children.length">
        <div class="section-title"><h2>分类</h2></div>
        <CategoryCard
          v-for="(c, i) in children"
          :key="c.id"
          :node="c"
          class="pop-in"
          :style="{ animationDelay: i * 40 + 'ms' }"
          @click="goCategory(c)"
        />
      </template>

      <!-- 资源列表 -->
      <template v-if="resources.length">
        <div class="section-title">
          <h2>{{ children.length ? '内容' : '全部内容' }}</h2>
          <span v-if="total > resources.length" class="more">
            共 {{ total }} 个
          </span>
        </div>

        <ResourceCard
          v-for="(r, i) in resources"
          :key="r.id"
          :node="r"
          class="pop-in"
          :style="{ animationDelay: i * 40 + 'ms' }"
          @click="goPlay(r)"
        />

        <button v-if="hasMore" class="more-btn pressable" @click="loadMore">
          {{ loadingMore ? '加载中…' : '看更多' }}
        </button>
      </template>

      <!-- 加载 / 出错 / 空态 -->
      <SkeletonList v-if="loading" :count="4" />

      <div v-else-if="failed" class="error-box">
        <span class="emoji">😵</span>
        <p>内容没能加载出来</p>
        <button class="retry pressable" @click="load">再试一次</button>
      </div>

      <div v-else-if="!children.length && !resources.length" class="empty">
        <span class="emoji">🍃</span>
        <p>这个分类还是空的</p>
        <p class="text-sub">等爸爸妈妈放点内容进来吧</p>
      </div>
    </div>

    <BackTop />
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import CategoryCard from '@/components/CategoryCard.vue'
import ResourceCard from '@/components/ResourceCard.vue'
import PageBar from '@/components/PageBar.vue'
import SkeletonList from '@/components/SkeletonList.vue'
import BackTop from '@/components/BackTop.vue'
import { useUserStore } from '@/stores/user'
import { resourceApi, digestApi } from '@/api'
import { resolveIcon } from '@/assets/icons'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const loading = ref(true)
const failed = ref(false)
const loadingMore = ref(false)
const resources = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const digest = ref(null)

const nodeId = computed(() => route.params.id)

const node = computed(() => userStore.findNode(nodeId.value))

const children = computed(() => node.value?.children || [])

const crumbs = computed(() => userStore.pathOf(nodeId.value) || [])

const icon = computed(() => resolveIcon(node.value?.icon, node.value?.color))

const hasMore = computed(() => resources.value.length < total.value)

async function fetchResources(reset = true) {
  if (reset) {
    page.value = 1
    resources.value = []
    failed.value = false
  }
  try {
    const data = await resourceApi.list({
      categoryId: nodeId.value,
      page: page.value,
      pageSize,
      // 只列**本层直属**资源，不递归子目录 —— 子目录已经在上面「分类」
      // 里单独列成卡片了，再带上它们的资源会重复、页面长一倍。
      //
      // 这里原先传的是 includeChildren（后端只认 includeSub），从来就没
      // 生效过。不生效恰好就是想要的行为，所以直接把参数删掉，
      // 别留一个看着像"功能没开"的悬空参数。
    })
    const list = data?.list || []
    total.value = data?.total ?? list.length
    resources.value = reset ? list : [...resources.value, ...list]
  } catch {
    // 列表加载失败要能看出来：静默清空会让页面显示成"这个分类是空的"，
    // 家长会以为内容被删了。
    if (reset) {
      resources.value = []
      failed.value = true
    }
  }
}

async function load() {
  loading.value = true
  try {
    await userStore.loadTree()
    await fetchResources(true)
  } finally {
    loading.value = false
  }
  // 沉淀单独取：失败不该影响目录内容（它是锦上添花的一张卡片）
  loadDigest()
}

// loadDigest 取当前目录的「小书童说」。
// 后端在没有沉淀时返回 null 而不是报错，所以这里静默即可。
async function loadDigest() {
  digest.value = null
  try {
    const d = await digestApi.forCategory(nodeId.value)
    // 切目录时旧请求可能后到，回来时已不是当前目录就丢弃
    digest.value = d && String(d.categoryId) === String(nodeId.value) ? d : null
  } catch {
    digest.value = null
  }
}

async function loadMore() {
  if (loadingMore.value) return
  loadingMore.value = true
  page.value += 1
  try {
    await fetchResources(false)
  } finally {
    loadingMore.value = false
  }
}

function goRoot() {
  router.push({ name: 'home' })
}

function goCrumb(c) {
  if (c.id === node.value?.id) return
  router.push({ name: 'category', params: { id: c.id } })
}

function goCategory(c) {
  router.push({ name: 'category', params: { id: c.id } })
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

onMounted(load)
watch(nodeId, load)
</script>

<style scoped>
/* 顶部返回栏与安全区已统一由 PageBar 承担，这里只剩面包屑自身的排版 */

.crumb {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  overflow-x: auto;
  scrollbar-width: none;
  font-size: 14.5px;
}
.crumb::-webkit-scrollbar {
  display: none;
}
.crumb-item {
  flex: 0 0 auto;
  color: var(--c-text-sub);
  white-space: nowrap;
}
.crumb-item.current {
  color: var(--c-text);
  font-weight: 700;
}
.sep {
  flex: 0 0 auto;
  color: #c8d3e0;
}

.ico-lg {
  flex: 0 0 auto;
  font-size: 22px;
  line-height: 1;
}

.more-btn {
  width: 100%;
  margin: 6px 0 12px;
  padding: 13px;
  border: 0;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
  color: var(--c-primary);
  font-size: 15px;
  font-weight: 600;
  font-family: inherit;
}

/* ===== 小书童说 ===== */

.digest-card {
  margin-top: 4px;
  padding: 15px 16px;
  border-radius: var(--r-lg);
  /* 与普通白卡片区分开：孩子要一眼认出"这是小书童写的" */
  background: linear-gradient(150deg, #eef4ff 0%, #f7fbff 100%);
  border: 1px solid #dbe7ff;
  box-shadow: var(--shadow-sm);
}

.d-head {
  display: flex;
  align-items: center;
  gap: 7px;
}
.d-ava {
  font-size: 17px;
  line-height: 1;
}
.d-name {
  font-size: 14px;
  font-weight: 700;
  color: #3d6fe0;
}
.d-count {
  margin-left: auto;
  font-size: 11.5px;
  color: #7f93b8;
}

.d-intro {
  margin: 9px 0 0;
  font-size: 14px;
  line-height: 1.62;
  color: #33465e;
}

.d-points {
  margin: 9px 0 0;
  padding: 0;
  list-style: none;
}
.d-points li {
  position: relative;
  padding-left: 15px;
  margin-top: 5px;
  font-size: 13px;
  line-height: 1.55;
  color: #4a5c73;
}
.d-points li::before {
  content: '';
  position: absolute;
  left: 3px;
  top: 7px;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #7ea6f5;
}

.d-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 11px;
}
.d-tag {
  font-size: 11.5px;
  color: #4a7ef0;
  background: rgba(91, 143, 249, 0.12);
  padding: 2px 9px;
  border-radius: 999px;
}
</style>
