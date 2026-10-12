<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">学习分析</h2>
      <div>
        <el-select v-model="query.userId" placeholder="选择孩子" style="width: 160px" @change="loadAll">
          <el-option
            v-for="u in users"
            :key="u.id"
            :label="u.nickname || u.username"
            :value="u.id"
          />
        </el-select>
        <el-date-picker
          v-model="dateRange"
          type="daterange"
          range-separator="至"
          start-placeholder="开始日期"
          end-placeholder="结束日期"
          value-format="YYYY-MM-DD"
          @change="loadAll"
        />
        <el-button type="primary" :icon="MagicStick" :loading="analyzing" @click="analyze">
          AI 分析
        </el-button>
      </div>
    </div>

    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col :span="6">
        <div class="card-box stat-card">
          <div class="stat-value">{{ overview.totalMinutes || 0 }}</div>
          <div class="stat-label">总学习时长（分钟）</div>
        </div>
      </el-col>
      <el-col :span="6">
        <div class="card-box stat-card">
          <div class="stat-value">{{ overview.totalDays || 0 }}</div>
          <div class="stat-label">学习天数</div>
        </div>
      </el-col>
      <el-col :span="6">
        <div class="card-box stat-card">
          <div class="stat-value">{{ overview.finishedResources || 0 }}</div>
          <div class="stat-label">完成资源数</div>
        </div>
      </el-col>
      <el-col :span="6">
        <div class="card-box stat-card">
          <div class="stat-value">{{ overview.inProgressResources || 0 }}</div>
          <div class="stat-label">进行中资源</div>
        </div>
      </el-col>
    </el-row>

    <!-- 图表 -->
    <el-row :gutter="16" class="mt-16">
      <el-col :span="14">
        <div class="card-box">
          <h3 class="section-title">每日学习时长趋势</h3>
          <div ref="trendChart" class="chart"></div>
        </div>
      </el-col>
      <el-col :span="10">
        <div class="card-box">
          <h3 class="section-title">各科目学习分布</h3>
          <div ref="pieChart" class="chart"></div>
        </div>
      </el-col>
    </el-row>

    <el-row :gutter="16" class="mt-16">
      <el-col :span="24">
        <div class="card-box">
          <h3 class="section-title">学习时段分布</h3>
          <div ref="hourChart" class="chart-sm"></div>
        </div>
      </el-col>
    </el-row>

    <!-- AI 报告列表 -->
    <div class="card-box mt-16">
      <div class="flex justify-between items-center mb-16">
        <h3 class="section-title" style="margin: 0">AI 学情报告</h3>
      </div>
      <el-table :data="reports" v-loading="reportLoading" empty-text="暂无报告，点击右上角「AI 分析」生成">
        <el-table-column label="报告周期" width="220">
          <template #default="{ row }">
            {{ formatDate(row.periodStart) }} ~ {{ formatDate(row.periodEnd) }}
          </template>
        </el-table-column>
        <el-table-column prop="summary" label="总体评价" min-width="300" show-overflow-tooltip />
        <el-table-column label="生成时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="viewReport(row)">查看</el-button>
            <el-button link type="danger" size="small" @click="removeReport(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 报告详情 -->
    <el-dialog v-model="reportDialog" title="学习分析报告" width="760px">
      <div v-if="currentReport" class="report-body">
        <div class="report-section">
          <h4>📊 总体评价</h4>
          <p class="report-text">{{ currentReport.summary }}</p>
        </div>
        <div class="report-section" v-if="currentReport.highlights?.length">
          <h4>✨ 学习亮点</h4>
          <ul class="report-list success">
            <li v-for="(h, i) in currentReport.highlights" :key="i">{{ h }}</li>
          </ul>
        </div>
        <div class="report-section" v-if="currentReport.weaknesses?.length">
          <h4>⚠️ 待改进</h4>
          <ul class="report-list warning">
            <li v-for="(w, i) in currentReport.weaknesses" :key="i">{{ w }}</li>
          </ul>
        </div>
        <div class="report-section" v-if="currentReport.suggestions?.length">
          <h4>💡 学习建议</h4>
          <ul class="report-list primary">
            <li v-for="(s, i) in currentReport.suggestions" :key="i">{{ s }}</li>
          </ul>
        </div>
      </div>
    </el-dialog>
  </div>
</template>

<script setup>
import { nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { MagicStick } from '@element-plus/icons-vue'
// ECharts 按需引入。
// 原来写的是 `import * as echarts from 'echarts'`，会把整个 echarts 打进
// 本页 chunk（实测 1,020 KB）。而本页只用 3 种图 + 4 类组件，
// 按需引入后体积可降约 700 KB（gzip 后约 250 KB）。
//
// 维护提示：下面这份清单必须与 renderCharts() 里的用法保持一致。
// 漏一个模块不会报错，只会在运行时图不出来（或控制台报
// "Component xxx is used but not imported"）。新增图表类型/组件时记得同步。
import * as echarts from 'echarts/core'
import { LineChart, PieChart, BarChart } from 'echarts/charts'
import {
  TooltipComponent,   // tooltip: { trigger: 'axis' | 'item' }
  GridComponent,      // grid + xAxis/yAxis
  LegendComponent,    // legend: { bottom: 0, type: 'scroll' }
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  LineChart, PieChart, BarChart,
  TooltipComponent, GridComponent, LegendComponent,
  CanvasRenderer,
])
import { studyApi, userApi } from '@/api'

const users = ref([])
const dateRange = ref([])
const overview = ref({})
const reports = ref([])
const reportLoading = ref(false)
const analyzing = ref(false)

const trendChart = ref()
const pieChart = ref()
const hourChart = ref()
let charts = []

const reportDialog = ref(false)
const currentReport = ref(null)

const query = reactive({ userId: null })

function defaultRange() {
  const end = new Date()
  const start = new Date()
  start.setDate(start.getDate() - 30)
  const fmt = (d) => d.toISOString().slice(0, 10)
  return [fmt(start), fmt(end)]
}

function formatDate(t) {
  if (!t) return '-'
  return new Date(t).toLocaleDateString('zh-CN')
}

function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function loadUsers() {
  try {
    const res = await userApi.list({ page: 1, pageSize: 100, role: 'kid' })
    users.value = res.list || []
    if (users.value.length && !query.userId) {
      query.userId = users.value[0].id
    }
  } catch (e) {
    /* 忽略 */
  }
}

function dateParams() {
  return {
    userId: query.userId,
    startDate: dateRange.value?.[0] || defaultRange()[0],
    endDate: dateRange.value?.[1] || defaultRange()[1],
  }
}

async function loadOverview() {
  if (!query.userId) return
  try {
    overview.value = await studyApi.overview(dateParams())
    renderCharts()
  } catch (e) {
    /* 忽略 */
  }
}

async function loadReports() {
  reportLoading.value = true
  try {
    const res = await studyApi.reports({ userId: query.userId, page: 1, pageSize: 20 })
    reports.value = res.list || []
  } catch (e) {
    /* 忽略 */
  } finally {
    reportLoading.value = false
  }
}

function renderCharts() {
  const byDay = overview.value.byDay || []
  const byCategory = overview.value.byCategory || []
  const byHour = overview.value.byHour || []

  nextTick(() => {
    // 趋势图
    if (trendChart.value) {
      let c = echarts.getInstanceByDom(trendChart.value)
      if (!c) c = echarts.init(trendChart.value)
      c.setOption({
        tooltip: { trigger: 'axis' },
        grid: { left: 40, right: 20, top: 20, bottom: 30 },
        xAxis: { type: 'category', data: byDay.map((d) => d.date) },
        yAxis: { type: 'value', name: '分钟' },
        series: [
          {
            data: byDay.map((d) => d.minutes),
            type: 'line',
            smooth: true,
            areaStyle: { opacity: 0.2 },
            itemStyle: { color: '#4a90d9' },
          },
        ],
      })
    }

    // 饼图
    if (pieChart.value) {
      let c = echarts.getInstanceByDom(pieChart.value)
      if (!c) c = echarts.init(pieChart.value)
      c.setOption({
        tooltip: { trigger: 'item', formatter: '{b}: {c} 分钟 ({d}%)' },
        legend: { bottom: 0, type: 'scroll' },
        series: [
          {
            type: 'pie',
            radius: ['40%', '65%'],
            data: byCategory.map((c) => ({ name: c.categoryName || '未分类', value: c.minutes })),
            label: { formatter: '{b}\n{d}%' },
          },
        ],
      })
    }

    // 时段图
    if (hourChart.value) {
      let c = echarts.getInstanceByDom(hourChart.value)
      if (!c) c = echarts.init(hourChart.value)
      const hours = Array.from({ length: 24 }, (_, i) => `${i}时`)
      const hourMap = {}
      byHour.forEach((h) => (hourMap[h.hour] = h.minutes))
      c.setOption({
        tooltip: { trigger: 'axis' },
        grid: { left: 40, right: 20, top: 20, bottom: 30 },
        xAxis: { type: 'category', data: hours },
        yAxis: { type: 'value', name: '分钟' },
        series: [
          {
            data: hours.map((_, i) => hourMap[i] || 0),
            type: 'bar',
            itemStyle: { color: '#5ad8a6', borderRadius: [4, 4, 0, 0] },
          },
        ],
      })
    }

    charts = [trendChart.value, pieChart.value, hourChart.value].filter(Boolean)
  })
}

async function analyze() {
  if (!query.userId) {
    ElMessage.warning('请先选择孩子')
    return
  }
  analyzing.value = true
  try {
    await studyApi.analyze({
      userId: query.userId,
      startDate: dateParams().startDate,
      endDate: dateParams().endDate,
    })
    ElMessage.success('报告生成成功')
    loadReports()
  } catch (e) {
    /* 忽略 */
  } finally {
    analyzing.value = false
  }
}

async function viewReport(row) {
  try {
    currentReport.value = await studyApi.reportDetail(row.id)
    reportDialog.value = true
  } catch (e) {
    /* 忽略 */
  }
}

async function removeReport(row) {
  try {
    await ElMessageBox.confirm('确定删除这份报告吗？', '删除确认', { type: 'warning' })
    await studyApi.deleteReport(row.id)
    ElMessage.success('已删除')
    loadReports()
  } catch (e) {
    /* 取消 */
  }
}

function loadAll() {
  loadOverview()
  loadReports()
}

function handleResize() {
  charts.forEach((c) => {
    const inst = echarts.getInstanceByDom(c)
    inst?.resize()
  })
}

onMounted(async () => {
  dateRange.value = defaultRange()
  await loadUsers()
  loadAll()
  window.addEventListener('resize', handleResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  charts.forEach((c) => {
    const inst = echarts.getInstanceByDom(c)
    inst?.dispose()
  })
})
</script>

<style scoped>
.stat-card {
  text-align: center;
}

.stat-value {
  font-size: 30px;
  font-weight: 600;
  color: var(--ls-primary);
}

.stat-label {
  font-size: 13px;
  color: var(--ls-text-secondary);
  margin-top: 4px;
}

.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 12px;
}

.chart {
  height: 280px;
}

.chart-sm {
  height: 180px;
}

.report-body {
  padding: 0 8px;
}

.report-section {
  margin-bottom: 20px;
}

.report-section h4 {
  margin: 0 0 8px;
  font-size: 15px;
}

.report-text {
  line-height: 1.9;
  margin: 0;
  color: #303133;
}

.report-list {
  margin: 0;
  padding-left: 20px;
  line-height: 2;
}

.report-list.success li::marker {
  color: #67c23a;
}
.report-list.warning li::marker {
  color: #e6a23c;
}
.report-list.primary li::marker {
  color: #409eff;
}
</style>
