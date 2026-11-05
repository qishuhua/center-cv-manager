<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import http from './api'
import type { Center, CenterTask } from './types'

const props = defineProps<{ centers: Center[]; initialCenterId?: string }>()

const selectedId = ref(props.initialCenterId || props.centers[0]?.id || '')
const tasks = ref<CenterTask[]>([])
const loading = ref(false)
const error = ref('')
const lastRefresh = ref('')
const filter = ref<'all' | 'running' | 'ended'>('all')
// 客户端分页：整包拉取全量清单，本地切片展示（任务量千级）。
const page = ref(1)
const pageSize = ref(50)
const pageSizes = [20, 50, 100]

// 任务类型文案全量对应 Worker 的 ViolationType 枚举（yl-alg-alg-center-cv-service
// src/common/utils.h，1~13，与其 docs/API_PROTOCOL.md 同口径）；未登记的类型回退为「类型 N」。
const typeNames: Record<number, string> = {
  1: '暴力分拣',
  2: '车辆倒车人员安全检测',
  3: '倚靠护栏',
  4: '翻越皮带线',
  5: '长发',
  6: '在岗检测',
  7: '快件拥堵',
  8: '卡包挂包',
  9: '交叉带错分检测',
  10: '末端错分检测',
  11: '堆场检测',
  12: '火情检测',
  13: '倒地未起',
}
const statusNames: Record<string, string> = { created: '待执行', running: '运行中', finished: '已结束', succeeded: '已结束', failed: '失败' }
const statusTagTypes: Record<string, 'success' | 'danger' | 'info'> = { created: 'info', running: 'success', finished: 'info', succeeded: 'info', failed: 'danger' }

function taskName(t: CenterTask) {
  if (!t.violation_type) return '-'
  return typeNames[t.violation_type] || `类型 ${t.violation_type}`
}
function streamType(t: CenterTask) {
  return t.video_type === '2' ? '实时' : t.video_type === '1' ? '历史' : '-'
}

// 心跳时间为 Worker 上报心跳：活任务约每 5 秒前进，Worker 崩溃/重启后
// 冻结。相对展示比绝对时间更直观（悬停查看绝对时间），未上报（旧版
// 中心、待执行任务）显示 -。
function heartbeatAgo(t: CenterTask) {
  if (!t.heartbeat_time) return '-'
  const seconds = Math.max(0, Math.round((Date.now() - t.heartbeat_time) / 1000))
  if (seconds < 5) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  return new Date(t.heartbeat_time).toLocaleString('zh-CN', { hour12: false })
}

// 失联判定（ingress api.md 约定由查询方完成）：运行中任务心跳超 30 秒
// 未更新即疑似失联——Worker 崩溃/重启后不回写旧任务终态，台账保持
// running 且心跳冻结，直到窗口结束 + 10 分钟 TTL 清理。未上报心跳的
// 运行中任务（旧版中心）无法判定，不标记。
const LOST_CONTACT_MS = 30_000
function lostContact(t: CenterTask) {
  return t.status === 'running' && !!t.heartbeat_time && Date.now() - t.heartbeat_time > LOST_CONTACT_MS
}

// 任务控制（预留）：后续做任务停止等操作，当前只对进行中的任务渲染禁用
// 的停止入口，已结束/失败的任务显示占位。
function controllable(t: CenterTask) {
  return t.status === 'running' || t.status === 'created'
}

async function load(silent = false) {
  if (!selectedId.value) return
  if (!silent) loading.value = true
  try {
    tasks.value = await http.get(`/centers/${selectedId.value}/tasks`)
    error.value = ''
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
    lastRefresh.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  }
}

const filtered = computed(() =>
  filter.value === 'all' ? tasks.value : tasks.value.filter(t => (filter.value === 'running' ? t.status === 'running' : t.status !== 'running')))
const runningCount = computed(() => tasks.value.filter(t => t.status === 'running').length)
// 数据变短（轮询刷新/切换筛选）时钳制当前页，避免停在空页。
const safePage = computed(() => {
  const maxPage = Math.max(1, Math.ceil(filtered.value.length / pageSize.value))
  return Math.min(page.value, maxPage)
})
const paged = computed(() => filtered.value.slice((safePage.value - 1) * pageSize.value, safePage.value * pageSize.value))

function fmtTime(ms?: number) {
  if (!ms) return '-'
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

function onFilterChange() {
  page.value = 1
}

// 页面显示期间每 15 秒静默刷新（保持当前页）；离开页面组件卸载即停止。
let refreshTimer: number | undefined
let disposed = false
async function poll() {
  if (!loading.value) await load(true)
  if (!disposed) refreshTimer = window.setTimeout(poll, 15000)
}
onMounted(() => { load(); refreshTimer = window.setTimeout(poll, 15000) })
onUnmounted(() => { disposed = true; window.clearTimeout(refreshTimer) })
watch(selectedId, () => {
  tasks.value = []
  error.value = ''
  lastRefresh.value = ''
  page.value = 1
  load()
})
</script>

<template>
  <div class="panel center-tasks-page">
    <div class="panel-title">
      <div>
        <h2>任务状态</h2>
        <p>中心业务任务的运行清单（流开始/流结束为流窗口时间，与执行状态无关；任务开始时间为任务创建时间；任务结束时间与任务控制为预留；心跳时间为 Worker 上报心跳，运行中任务超 30 秒未更新标记疑似失联），经中心网关每 15 秒自动刷新，数据保存在中心本地。</p>
      </div>
      <div class="center-tasks-toolbar">
        <el-select v-model="selectedId" filterable placeholder="选择中心" style="width:240px">
          <el-option v-for="c in centers" :key="c.id" :label="c.name" :value="c.id"/>
        </el-select>
        <el-radio-group v-model="filter" size="small" @change="onFilterChange">
          <el-radio-button value="all">全部 {{ tasks.length }}</el-radio-button>
          <el-radio-button value="running">运行中 {{ runningCount }}</el-radio-button>
          <el-radio-button value="ended">已结束 {{ tasks.length - runningCount }}</el-radio-button>
        </el-radio-group>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="() => load()">刷新</el-button>
      </div>
    </div>
    <el-alert v-if="error" :title="error" type="warning" :closable="false" show-icon style="margin-bottom:12px"/>
    <el-table :data="paged" v-loading="loading" empty-text="当前没有任务记录" max-height="calc(100vh - 320px)">
      <el-table-column label="摄像机名称" min-width="220"><template #default="{row}">{{ row.camera || '-' }}</template></el-table-column>
      <el-table-column label="任务名称" min-width="165" show-overflow-tooltip><template #default="{row}">{{ taskName(row) }}</template></el-table-column>
      <el-table-column label="流类型" width="90"><template #default="{row}">{{ streamType(row) }}</template></el-table-column>
      <el-table-column label="流开始时间" width="185"><template #default="{row}">{{ fmtTime(row.start_time) }}</template></el-table-column>
      <el-table-column label="流结束时间" width="185"><template #default="{row}">{{ fmtTime(row.end_time) }}</template></el-table-column>
      <el-table-column label="任务开始时间" width="185"><template #default="{row}">{{ fmtTime(row.created_time) }}</template></el-table-column>
      <el-table-column label="任务结束时间" width="185"><template #default="{row}">{{ fmtTime(row.finished_time) }}</template></el-table-column>
      <el-table-column label="心跳时间" width="110">
        <template #default="{row}">
          <el-tooltip :disabled="!row.heartbeat_time" :content="fmtTime(row.heartbeat_time)" placement="top">
            <span :class="{ 'heartbeat-stale': lostContact(row) }">{{ heartbeatAgo(row) }}</span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="任务状态" width="100">
        <template #default="{row}">
          <el-tooltip v-if="lostContact(row)" content="台账状态仍为运行中，但心跳超 30 秒未更新——Worker 可能已崩溃或重启（重启后不回写旧任务终态）" placement="top">
            <el-tag type="warning">疑似失联</el-tag>
          </el-tooltip>
          <el-tooltip v-else :disabled="!row.error" :content="row.error" placement="top">
            <el-tag :type="statusTagTypes[row.status] || 'info'">{{ statusNames[row.status] || row.status || '未知' }}</el-tag>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="任务控制" width="90" align="center">
        <template #default="{row}">
          <el-tooltip v-if="controllable(row)" content="任务控制功能预留，暂未开放" placement="top">
            <span><el-button size="small" type="danger" plain disabled>停止</el-button></span>
          </el-tooltip>
          <template v-else>-</template>
        </template>
      </el-table-column>
    </el-table>
    <div class="center-tasks-pager">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="filtered.length"
        :page-sizes="pageSizes"
        layout="total, sizes, prev, pager, next, jumper"
        background
        size="small"/>
      <small v-if="lastRefresh" class="center-tasks-refreshed-at">最后刷新：{{ lastRefresh }}</small>
    </div>
  </div>
</template>

<style scoped>
.center-tasks-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap;justify-content:flex-end}
.center-tasks-pager{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:14px;flex-wrap:wrap}
.center-tasks-refreshed-at{color:#8e9297}
.heartbeat-stale{color:#FF9830}
</style>
