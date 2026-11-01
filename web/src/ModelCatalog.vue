<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Delete, FolderOpened, Plus, Refresh, Switch } from '@element-plus/icons-vue'
import type { EmptyModelFolder, ModelFormat, ModelVersion } from './types'
import { groupModels } from './modelGroups'

const props = withDefaults(defineProps<{models: ModelVersion[]; modelValue?: string[]; loading: boolean; selectable?: boolean; emptyFolders?: EmptyModelFolder[]; convertEnabled?: boolean}>(), {modelValue:()=>[],selectable:true,emptyFolders:()=>[],convertEnabled:false})
const emit = defineEmits<{
  'update:modelValue': [keys: string[]]
  refresh: []
  uploadVersion: [model: string]
  deleteModel: [model: string]
  convert: [version: ModelVersion]
}>()
const groups = computed(() => {
  const grouped = groupModels(props.models)
  // 空文件夹（有残留对象但没有可用版本）以 0 版本分组展示，只在这些分组上提供删除入口。
  const empty = props.emptyFolders.filter(folder => !grouped.some(group => group.model === folder.model)).map(folder => ({model: folder.model, versions: [] as ModelVersion[]}))
  return [...grouped, ...empty].sort((left, right) => left.model.localeCompare(right.model))
})
const expanded = ref<string[]>([])
const selected = computed(() => new Set(props.modelValue))
watch(groups, value => {
  if (!expanded.value.length && value.length) expanded.value = [value[0].model]
}, {immediate: true})

const versionFiles = (item: ModelVersion) => item.formats.flatMap(format=>format.artifacts)
const versionSize = (item: ModelVersion) => versionFiles(item).reduce((size, file) => size + file.size, 0)
const groupSize = (items: ModelVersion[]) => items.reduce((size, item) => size + versionSize(item), 0)
function folderSubtitle(group: {model:string;versions:ModelVersion[]}) {
  if (group.versions.length) return `${group.versions.length} 个版本 · ${sizeLabel(groupSize(group.versions))}`
  const folder = props.emptyFolders.find(item => item.model === group.model)
  return folder ? `空文件夹 · ${folder.files} 个残留文件 · ${sizeLabel(folder.size)}` : '空文件夹'
}
const formatName = (format: ModelFormat) => format.platform==='onnx'?'ONNX':format.platform==='denglin'?'登临 engine':`NVIDIA plan · ${format.hardware_profile}`
const formatType = (format: ModelFormat) => format.platform==='onnx'?'info':format.platform==='denglin'?'warning':'success'
const formatSummary = (item: ModelVersion) => item.formats.map(format=>format.hardware_profile?`${format.platform}-${format.hardware_profile}`:format.platform).join(' / ')
const selectedVersion = (items: ModelVersion[]) => items.find(item => selected.value.has(item.manifest_key))
const hasOnnx = (item: ModelVersion) => item.formats.some(format => format.platform === 'onnx')

function selectLatest(items: ModelVersion[]) {
  const latest = new Map<string, ModelVersion>()
  for (const item of items) {
    const previous = latest.get(item.model)
    if (!previous || Date.parse(item.created_at) > Date.parse(previous.created_at)) latest.set(item.model, item)
  }
  const keys = new Set(props.modelValue)
  for (const item of props.models) if (latest.has(item.model)) keys.delete(item.manifest_key)
  for (const item of latest.values()) keys.add(item.manifest_key)
  emit('update:modelValue', props.models.filter(item => keys.has(item.manifest_key)).map(item => item.manifest_key))
}
function toggle(item: ModelVersion, checked: boolean | string | number) {
  const keys = new Set(props.modelValue)
  if (checked === true) {
    for (const version of props.models) if (version.model === item.model) keys.delete(version.manifest_key)
    keys.add(item.manifest_key)
  } else {
    keys.delete(item.manifest_key)
  }
  emit('update:modelValue', props.models.filter(version => keys.has(version.manifest_key)).map(version => version.manifest_key))
}
function sizeLabel(value: number) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let index = 0
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index++ }
  return `${value.toFixed(1)} ${units[index]}`
}
</script>

<template>
  <div class="panel model-catalog" v-loading="loading">
    <div class="panel-title">
      <div><h2>模型文件夹</h2><p>每个文件夹是一个独立模型，展开后选择或上传其中的版本。</p></div>
      <el-button :icon="Refresh" :disabled="loading" @click="emit('refresh')">刷新</el-button>
    </div>
    <div v-if="selectable" class="model-select-all">
      <div class="model-selection-summary">
        <strong>已选 {{modelValue.length}} / {{groups.length}} 个模型</strong>
        <span>共 {{models.length}} 个版本，可一键选择每个模型最新上传的版本</span>
      </div>
      <div class="model-selection-actions">
        <el-button type="primary" size="large" :disabled="!models.length || loading" @click="selectLatest(models)">选择全部模型的最新版本</el-button>
        <el-button :disabled="!modelValue.length || loading" @click="emit('update:modelValue', [])">清空选择</el-button>
      </div>
    </div>

    <el-empty v-if="!loading&&!groups.length" description="还没有模型，请先新建模型并上传首个版本"/>
    <el-collapse v-else v-model="expanded" class="model-folders">
      <el-collapse-item v-for="group in groups" :key="group.model" :name="group.model">
        <template #title>
          <div class="model-folder-title">
            <el-icon class="folder-icon"><FolderOpened/></el-icon>
            <div class="folder-name"><strong>{{group.model}}</strong><span>{{folderSubtitle(group)}}</span></div>
            <el-tag v-if="selectable&&selectedVersion(group.versions)" type="primary" effect="plain">已选 v{{selectedVersion(group.versions)?.version}}</el-tag>
            <el-tag v-else-if="selectable" type="info" effect="plain">未选版本</el-tag>
            <el-button class="folder-upload" type="primary" plain size="small" :icon="Plus" :disabled="loading" @click.stop="emit('uploadVersion', group.model)">上传新版本</el-button>
            <el-button v-if="!selectable && !group.versions.length" type="danger" plain size="small" :icon="Delete" :disabled="loading" @click.stop="emit('deleteModel', group.model)">删除文件夹</el-button>
          </div>
        </template>

        <div class="version-list-head">
          <div><strong>版本列表</strong><span v-if="selectable">同一模型只能选择一个版本发布</span></div>
          <el-button v-if="selectable" link :disabled="loading" @click="selectLatest(group.versions)">选择最新版本</el-button>
        </div>
        <el-table :data="group.versions" row-key="manifest_key" class="version-table" empty-text="该文件夹没有可用版本，只包含残留文件，可整体删除">
          <el-table-column type="expand">
            <template #default="{row}">
              <div class="version-formats">
                <p>该版本的文件组会整体发布，并根据中心硬件自动选择匹配的格式。</p>
                <section v-for="format in row.formats" :key="format.id" class="format-group">
                  <div class="format-title">
                    <el-tag :type="formatType(format)" effect="plain">{{formatName(format)}}</el-tag>
                    <span>{{format.artifacts.length}} 个文件 · {{sizeLabel(format.artifacts.reduce((sum:number,file:any)=>sum+file.size,0))}}</span>
                  </div>
                  <el-table :data="format.artifacts" size="small">
                    <el-table-column prop="path" label="OSS 相对路径" min-width="240"/>
                    <el-table-column prop="target_path" label="中心相对路径" min-width="240"/>
                    <el-table-column label="大小" width="115"><template #default="{row:file}">{{sizeLabel(file.size)}}</template></el-table-column>
                    <el-table-column label="SHA-256" min-width="160"><template #default="{row:file}"><code :title="file.sha256">{{file.sha256.slice(0,16)}}</code></template></el-table-column>
                  </el-table>
                </section>
              </div>
            </template>
          </el-table-column>
          <el-table-column v-if="selectable" width="64">
            <template #default="{row}"><el-radio :model-value="selectedVersion(group.versions)?.manifest_key" :value="row.manifest_key" :disabled="loading" :aria-label="`选择 ${row.model} ${row.version}`" @change="toggle(row, true)"><span/></el-radio></template>
          </el-table-column>
          <el-table-column prop="version" label="版本号" min-width="150"><template #default="{row}"><strong>v{{row.version}}</strong></template></el-table-column>
          <el-table-column label="模型格式" min-width="220"><template #default="{row}">{{formatSummary(row)}}</template></el-table-column>
          <el-table-column label="文件" width="100"><template #default="{row}">{{versionFiles(row).length}} 个</template></el-table-column>
          <el-table-column label="总大小" width="120"><template #default="{row}">{{sizeLabel(versionSize(row))}}</template></el-table-column>
          <el-table-column label="上传时间" width="190"><template #default="{row}">{{row.created_at ? new Date(row.created_at).toLocaleString() : '-'}}</template></el-table-column>
          <el-table-column label="Manifest" min-width="130"><template #default="{row}"><code>{{row.manifest_digest.slice(0,12)}}</code></template></el-table-column>
          <el-table-column v-if="convertEnabled" label="操作" width="110">
            <template #default="{row}">
              <el-button size="small" type="primary" plain :icon="Switch" :disabled="loading || !hasOnnx(row)" :title="hasOnnx(row) ? '在构建机上转换为 TensorRT/登临格式并作为新版本入仓' : '该版本没有 ONNX 格式，无法转换'" @click="emit('convert', row)">转换</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-collapse-item>
    </el-collapse>
  </div>
</template>

<style scoped>
.model-select-all{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:16px 18px;margin-bottom:18px;border:1px solid #2c3235;border-radius:10px;background:#1f2125;flex-wrap:wrap}
.model-selection-summary{display:flex;flex-direction:column;gap:6px}
.model-selection-summary strong{color:#fff;font-size:15px}
.model-selection-summary span{color:#8e9297;font-size:13px}
.model-selection-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap}
.model-selection-actions>.el-button{margin-left:0}
.model-selection-actions>.el-button--primary{font-weight:600}
@media(max-width:600px){.model-selection-actions{width:100%}.model-selection-actions>.el-button--primary{width:100%}}
.model-folders{border-top:0}
.model-folders :deep(.el-collapse-item){border:1px solid #22252b;border-radius:12px;margin-bottom:12px;overflow:hidden}
.model-folders :deep(.el-collapse-item__header){height:72px;padding:0 18px;border-bottom:0;background:#1f2125;color:#d8d9da}
.model-folders :deep(.el-collapse-item__header.is-active){border-bottom:1px solid #22252b}
.model-folders :deep(.el-collapse-item__wrap){border-bottom:0}
.model-folders :deep(.el-collapse-item__content){padding:0 18px 18px}
.model-folder-title{display:flex;align-items:center;gap:13px;width:100%;min-width:0;padding-right:12px}
.folder-icon{width:34px;height:34px;border-radius:9px;background:rgba(51,162,229,.15);color:#33a2e5;font-size:19px;flex:0 0 auto}
.folder-name{display:flex;flex-direction:column;align-items:flex-start;line-height:1.45;min-width:160px;margin-right:auto}
.folder-name strong{font-size:16px;color:#e0e1e2}
.folder-name span,.version-list-head span,.format-title span{font-size:12px;color:#8e9297}
.folder-upload{margin-left:6px}
.version-list-head{display:flex;align-items:center;justify-content:space-between;padding:15px 2px 10px}
.version-list-head>div{display:flex;align-items:center;gap:12px}
.version-formats{padding:2px 20px 18px}
.version-formats>p{color:#8e9297;margin:6px 0 12px}
.format-group{border:1px solid #22252b;border-radius:9px;overflow:hidden;margin-top:10px;background:#181b1f}
.format-title{display:flex;align-items:center;gap:12px;padding:10px 12px;background:#1f2125}
.version-table :deep(.el-radio__label){display:none}
@media(max-width:800px){.model-folder-title>.el-tag{display:none}.folder-upload{margin-left:auto}.version-list-head span{display:none}}
</style>
