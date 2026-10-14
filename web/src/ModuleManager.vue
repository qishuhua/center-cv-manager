<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from './api'
import type { Center, ComposeModule, Task } from './types'
const YamlEditor = defineAsyncComponent(() => import('./YamlEditor.vue'))
const props = defineProps<{ center: Center; admin?: boolean; active?: boolean }>()
const emit = defineEmits<{ task: [task: Task]; changed: []; legacyEdit: [file: string] }>()
// 普通用户点击发布、保存等按钮时统一弹窗提示，不再隐藏按钮。
function guardAdmin(){
  if(props.admin)return true
  ElMessageBox.alert('当前账号是普通用户，仅可查看状态。如需执行该操作，请联系管理员处理。','没有操作权限',{type:'warning',confirmButtonText:'知道了'})
  return false
}
const modules = ref<ComposeModule[]>([]), busy = ref(false), editor = ref(false)
const selected = ref<ComposeModule>(), file = ref('compose.yml'), content = ref(''), original = ref(''), revision = ref(''), drift = ref(false), diff = ref(false)
const loaded = ref(false), loadedFile = ref(''), documentPath = ref('')
const history = ref<string[]>([]), historySelection = ref('')
const statusNames: Record<string,string> = { published:'已发布', unpublished:'有修改未发布', unknown:'状态读取失败', existing:'现有配置', partial:'部分就绪', pending:'待配置', prepared:'配置待应用', starting:'启动中', healthy:'已就绪', failed:'失败', stopped:'已停止', disabled:'已禁用' }
const base = computed(() => `/centers/${props.center.id}/modules`)
async function load() { busy.value=true; try { modules.value=await http.get(base.value) } catch(e:any) { ElMessage.error(e.message) } finally { busy.value=false } }
async function readFile() {
  if(!selected.value) return
  busy.value=true; loaded.value=false
  try { const doc:any=await http.get(props.center.deployment?`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(file.value)}`:`/centers/${props.center.id}/compose?file=${encodeURIComponent(selected.value.id)}`); content.value=doc.content; original.value=doc.content; loadedFile.value=file.value; documentPath.value=doc.path; loaded.value=true; revision.value=doc.revision||''; drift.value=doc.drift; diff.value=false }
  catch(e:any) { ElMessage.error(e.message) }
  finally { busy.value=false }
}
async function edit(m:ComposeModule) {  loaded.value=false; selected.value=m; file.value=props.center.deployment?'compose.yml':m.id; content.value=''; original.value=''; revision.value=''; editor.value=true; await readFile(); try{history.value=props.center.deployment?await http.get(`${base.value}/${m.id}/revisions`):[]}catch{history.value=[]}; historySelection.value='' }
async function loadHistorical() {
 if(!selected.value||!historySelection.value)return
 try{
  const current:any=await http.get(`${base.value}/${selected.value.id}/compose`)
  const old:any=await http.get(`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(historySelection.value)}`)
  file.value='compose.yml';loadedFile.value=file.value;loaded.value=true;documentPath.value=current.path;original.value=current.content;revision.value=current.revision;content.value=old.content;diff.value=true
  ElMessage.info('历史内容已加载到编辑器，校验并保存后再部署生效')
 }catch(e:any){ElMessage.error(e.message)}
}
async function createFile() {
  if(!guardAdmin())return
  if(!selected.value) return
  const { value } = await ElMessageBox.prompt('输入模块内的相对路径，例如 config/app.yml。已有文件不会被覆盖。','新增配置文件',{inputPattern:/^[\w./-]+$/,inputErrorMessage:'请输入相对文件路径'})
  file.value=value; loadedFile.value=value; loaded.value=true; documentPath.value=''; content.value=''; original.value=''; revision.value=''; drift.value=false; diff.value=false
}
async function save() {
  if(!guardAdmin())return
  if(!selected.value||!loaded.value||loadedFile.value!==file.value) return
  busy.value=true
  try { const doc:any=await http.put(props.center.deployment?`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(file.value)}`:`/centers/${props.center.id}/compose?file=${encodeURIComponent(selected.value.id)}`,{content:content.value,revision:revision.value}); revision.value=doc.revision||''; original.value=content.value; drift.value=false; diff.value=false; ElMessage.success('配置已校验并保存；点击发布后生效'); await load(); emit('changed') }
  catch(e:any) { ElMessage.error(e.message) } finally { busy.value=false }
}
async function publish(m:ComposeModule) {
 if(!guardAdmin())return
 busy.value=true
 try {
  const plan:any=await http.post(`${base.value}/${encodeURIComponent(m.id)}/publish-plan`,{})
  await ElMessageBox.confirm(`Compose：${m.name}\n项目：${plan.project}\n服务：${plan.services.join(', ')}\n${plan.changed?'将发布整个 Compose 的已保存配置。':'内容未修改，将重启此 Compose 中的全部常驻服务。'}`,'发布 Compose',{type:plan.changed?'primary':'warning',confirmButtonText:plan.changed?'确认发布':'确认重启',cancelButtonText:'取消',closeOnClickModal:false})
  const task:Task=await http.post(`${base.value}/${encodeURIComponent(m.id)}/publish`,{revision:plan.revision,confirm_unchanged:!plan.changed})
  emit('task',task)
 }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{busy.value=false}
}
let refreshTimer:number|undefined,disposed=false
async function pollChanges(){
 const currentBase=base.value
 if(props.active&&!busy.value&&!editor.value){
  try{const result:ComposeModule[]=await http.get(currentBase);if(!disposed&&base.value===currentBase)modules.value=result}
  catch(e:any){if(!disposed&&base.value===currentBase)modules.value=modules.value.map(m=>({...m,status:'unknown',error:e.message||'配置变化检测失败'}))}
 }
 if(!disposed)refreshTimer=window.setTimeout(pollChanges,15000)
}
onMounted(()=>{load();refreshTimer=window.setTimeout(pollChanges,15000)})
onUnmounted(()=>{disposed=true;window.clearTimeout(refreshTimer)})
watch(()=>props.center,load)
</script>

<template>
  <section class="detail-section" v-loading="busy">
    <div class="module-toolbar"><div><h3>Compose 配置列表</h3><small>通过 SSH 或编辑器修改配置，刷新检测变化后点击重新发布。采用新内置模板请重置并重新初始化。</small></div><el-button @click="load">刷新</el-button></div>
    <el-table :data="modules">
      <el-table-column label="模块" min-width="140"><template #default="{row}"><strong>{{row.name}}</strong><div><small>{{row.project}}</small></div></template></el-table-column>
      <el-table-column label="配置" min-width="220"><template #default="{row}"><code>{{row.compose_file}}</code></template></el-table-column>
      <el-table-column label="发布状态" width="160"><template #default="{row}"><el-tooltip :content="row.error||statusNames[row.status]||row.status||'未登记模块'"><el-tag :type="row.status==='published'?'success':row.status==='unpublished'?'warning':row.status==='unknown'?'danger':'info'">{{row.enabled?(statusNames[row.status]||row.status||'原有配置'):'已禁用'}}</el-tag></el-tooltip></template></el-table-column>
      <el-table-column label="操作" width="180"><template #default="{row}"><el-button link @click="edit(row)">编辑</el-button><el-button type="primary" link :disabled="!row.enabled || busy || row.status==='unknown'" @click="publish(row)">{{row.status==='unpublished'?'重新发布':'发布'}}</el-button></template></el-table-column>
    </el-table>
  </section>
  <el-drawer v-model="editor" size="80%" :title="`${selected?.name||''} · 配置文件`" append-to-body>
    <p><strong>{{selected?.name}}</strong> · <code>{{documentPath || selected?.compose_file}}</code></p>
    <div v-if="center.deployment" class="module-file-toolbar"><el-input v-model="file" :disabled="busy" placeholder="模块内文件路径"/><el-button :disabled="busy" @click="readFile">读取</el-button><el-button :disabled="busy" @click="createFile">新增文件</el-button></div>
    <el-alert v-if="drift" title="远端文件已被修改，本次读取的是远端最新内容。" type="warning" :closable="false"/>
    <el-alert title="保存只校验并保存配置，不启动或重启服务。返回列表点击“发布”后，整体应用此 Compose。" type="info" :closable="false"/>
    <div v-if="center.deployment" class="module-file-toolbar"><el-select v-model="historySelection" placeholder="Compose 历史备份"><el-option v-for="name in history" :key="name" :label="name" :value="name"/></el-select><el-button :disabled="busy || !historySelection" @click="loadHistorical">载入历史版本</el-button></div>
    <div class="module-editor"><YamlEditor v-model="content" :disabled="busy||!admin"/></div>
    <div class="module-toolbar"><el-button @click="diff=!diff">{{diff?'收起':'查看保存前后'}}</el-button><el-button type="primary" :loading="busy" :disabled="!loaded || loadedFile!==file" @click="save">校验并保存</el-button></div>
    <div v-if="diff" class="module-diff"><div><b>原文件</b><pre>{{original}}</pre></div><div><b>待保存</b><pre>{{content}}</pre></div></div>
  </el-drawer>
</template>

<style scoped>
.module-toolbar,.module-file-toolbar{display:flex;align-items:center;gap:12px;margin:12px 0}.module-toolbar>div:first-child{flex:1}.module-toolbar h3{margin:0 0 6px}.module-editor{height:55vh;margin:14px 0}.module-diff{display:grid;grid-template-columns:1fr 1fr;gap:16px}.module-diff pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--el-fill-color-light);padding:12px}small{color:var(--el-text-color-secondary)}code{word-break:break-all}
</style>
