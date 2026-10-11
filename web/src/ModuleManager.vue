<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from './api'
import type { Center, ComposeModule, Task } from './types'
const YamlEditor = defineAsyncComponent(() => import('./YamlEditor.vue'))
const props = defineProps<{ center: Center }>()
const emit = defineEmits<{ task: [task: Task]; changed: [] }>()
const modules = ref<ComposeModule[]>([]), busy = ref(false), editor = ref(false), metadata = ref(false)
const selected = ref<ComposeModule>(), file = ref('compose.yml'), content = ref(''), original = ref(''), revision = ref(''), drift = ref(false), diff = ref(false)
const form = reactive({ id: '', name: '', project: '', enabled: true, timeout: 600, dependencies: '', init: '', models: '', urls: '' })
const adding = ref(false), history = ref<string[]>([]), historySelection = ref('')
const statusNames: Record<string,string> = { partial:'部分就绪', pending:'待配置', prepared:'配置待应用', starting:'启动中', healthy:'已就绪', failed:'失败', stopped:'已停止', disabled:'已禁用' }
const base = computed(() => `/centers/${props.center.id}/modules`)
const words = (text: string) => text.split(/[\s,]+/).filter(Boolean)
async function load() { busy.value=true; try { modules.value=await http.get(base.value) } catch(e:any) { ElMessage.error(e.message) } finally { busy.value=false } }
async function readFile() {
  if(!selected.value) return
  busy.value=true
  try { const doc:any=await http.get(`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(file.value)}`); content.value=doc.content; original.value=doc.content; revision.value=doc.revision; drift.value=doc.drift; diff.value=false }
  catch(e:any) { ElMessage.error(e.message) }
  finally { busy.value=false }
}
async function edit(m:ComposeModule) { selected.value=m; file.value='compose.yml'; content.value=''; original.value=''; revision.value=''; editor.value=true; await readFile(); history.value=await http.get(`${base.value}/${m.id}/revisions`); historySelection.value='' }
async function loadHistorical() {
 if(!selected.value||!historySelection.value)return
 try{
  const current:any=await http.get(`${base.value}/${selected.value.id}/compose`)
  const old:any=await http.get(`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(historySelection.value)}`)
  file.value='compose.yml';original.value=current.content;revision.value=current.revision;content.value=old.content;diff.value=true
  ElMessage.info('历史内容已加载到编辑器，校验并保存后再部署生效')
 }catch(e:any){ElMessage.error(e.message)}
}
async function createFile() {
  if(!selected.value) return
  const { value } = await ElMessageBox.prompt('输入模块内的相对路径，例如 config/app.yml。已有文件不会被覆盖。','新增配置文件',{inputPattern:/^[\w./-]+$/,inputErrorMessage:'请输入相对文件路径'})
  file.value=value; content.value=''; original.value=''; revision.value=''; drift.value=false; diff.value=false
}
async function save() {
  if(!selected.value) return
  busy.value=true
  try { const doc:any=await http.put(`${base.value}/${selected.value.id}/compose?file=${encodeURIComponent(file.value)}`,{content:content.value,revision:revision.value}); revision.value=doc.revision; original.value=content.value; drift.value=false; diff.value=false; ElMessage.success('配置已保存并备份原文件；部署后生效'); await load(); emit('changed') }
  catch(e:any) { ElMessage.error(e.message) } finally { busy.value=false }
}
function configure(m?:ComposeModule) {
  adding.value=!m
  Object.assign(form,{id:m?.id||'',name:m?.name||'',project:m?.project||'',enabled:m?.enabled??true,timeout:m?.timeout||600,dependencies:(m?.depends_on||[]).join(', '),init:(m?.init_services||[]).join(', '),models:(m?.model_services||[]).join(', '),urls:(m?.ready_urls||[]).join('\n')})
  metadata.value=true
}
async function saveMetadata() {
  busy.value=true
  try { const data={id:form.id,name:form.name,project:form.project||`center-cv-${form.id}`,enabled:form.enabled,timeout:form.timeout,depends_on:words(form.dependencies),init_services:words(form.init),model_services:words(form.models),ready_urls:words(form.urls)}; await http[adding.value?'post':'put'](adding.value?base.value:`${base.value}/${form.id}`,data); metadata.value=false; await load(); emit('changed') }
  catch(e:any) { ElMessage.error(e.message) } finally { busy.value=false }
}
async function action(m:ComposeModule, action:string) {
  try {
    const { value: services } = await ElMessageBox.prompt('留空操作整个模块，或填写服务名（逗号分隔）。仅作用于此模块。','选择操作范围',{inputPlaceholder:'例如 center-cv-ingress',confirmButtonText:'生成计划'})
    const plan:any=await http.post(`${base.value}/${m.id}/plan`,{action,services:words(services||'')})
    const { value: password } = await ElMessageBox.prompt(`模块：${m.name}\n服务：${plan.services.join(', ')}\n${action==='stop'?'停止所选服务':action==='restart'?'重启所选服务':'应用当前配置，变化的容器可能重建'}。请输入管理员密码。`,'执行模块计划',{inputType:'password',inputValidator:(v:string)=>!!v||'请输入密码',type:'warning'})
    const task:Task=await http.post(`${base.value}/${m.id}/actions`,{action,services:words(services||''),revision:plan.revision,admin_password:password}); emit('task',task)
  } catch(e:any) { if(e!=='cancel'&&e!=='close') ElMessage.error(e.message||String(e)) }
}
onMounted(load)
watch(()=>props.center.updated_at,load)
</script>

<template>
  <section class="detail-section" v-loading="busy">
    <div class="module-toolbar"><div><h3>部署模块</h3><small>分别管理 Compose、环境变量和配置文件。业务发布只更新目标服务。</small></div><el-button @click="load">刷新</el-button><el-button type="primary" @click="configure()">新增模块</el-button></div>
    <el-table :data="modules">
      <el-table-column label="模块" min-width="140"><template #default="{row}"><strong>{{row.name}}</strong><div><small>{{row.project}}</small></div></template></el-table-column>
      <el-table-column label="配置" min-width="220"><template #default="{row}"><code>{{row.compose_file}}</code><div v-if="row.revision!==row.applied_revision"><small>已保存配置与上次整模块应用版本不同</small></div></template></el-table-column>
      <el-table-column label="状态" width="120"><template #default="{row}"><el-tooltip :content="row.error||statusNames[row.status]||row.status"><el-tag :type="row.status==='healthy'?'success':row.status==='failed'?'danger':'info'">{{row.enabled?(statusNames[row.status]||row.status):'已禁用'}}</el-tag></el-tooltip></template></el-table-column>
      <el-table-column label="操作" width="330"><template #default="{row}"><el-button link @click="edit(row)">编辑文件</el-button><el-button link @click="configure(row)">设置</el-button><el-button type="primary" link :disabled="!row.enabled" @click="action(row,'deploy')">部署</el-button><el-button type="warning" link :disabled="!row.enabled" @click="action(row,'restart')">重启</el-button><el-button type="danger" link :disabled="!row.enabled" @click="action(row,'stop')">停止</el-button></template></el-table-column>
    </el-table>
  </section>
  <el-drawer v-model="editor" size="80%" :title="`${selected?.name||''} · 配置文件`" append-to-body>
    <div class="module-file-toolbar"><el-input v-model="file" placeholder="模块内文件路径"/><el-button :disabled="busy" @click="readFile">读取</el-button><el-button @click="createFile">新增文件</el-button></div>
    <el-alert v-if="drift" title="远端文件已被修改，本次读取的是远端最新内容。" type="warning" :closable="false"/>
    <el-alert title="保存不会自动部署。Compose 保存前会在目标机校验；环境变量使用 module.env。部署前可选择服务范围。" type="info" :closable="false"/>
    <div class="module-file-toolbar"><el-select v-model="historySelection" placeholder="Compose 历史备份"><el-option v-for="name in history" :key="name" :label="name" :value="name"/></el-select><el-button :disabled="!historySelection" @click="loadHistorical">载入历史版本</el-button></div>
    <div class="module-editor"><YamlEditor v-model="content" :disabled="busy"/></div>
    <div class="module-toolbar"><el-button @click="diff=!diff">{{diff?'收起':'查看保存前后'}}</el-button><el-button type="primary" :loading="busy" @click="save">校验并保存</el-button></div>
    <div v-if="diff" class="module-diff"><div><b>原文件</b><pre>{{original}}</pre></div><div><b>待保存</b><pre>{{content}}</pre></div></div>
  </el-drawer>
  <el-dialog v-model="metadata" :title="adding?'新增部署模块':'模块设置'" width="660px" append-to-body>
    <el-form label-width="130px">
      <el-form-item label="模块标识"><el-input v-model="form.id" :disabled="!adding" placeholder="如 recording"/></el-form-item>
      <el-form-item label="显示名称"><el-input v-model="form.name"/></el-form-item>
      <el-form-item label="Compose 项目名"><el-input v-model="form.project" :disabled="!adding" placeholder="默认 center-cv-模块标识"/></el-form-item>
      <el-form-item label="参与初始化"><el-switch v-model="form.enabled"/><small>禁用不停止现有容器</small></el-form-item>
      <el-form-item label="依赖模块"><el-input v-model="form.dependencies" placeholder="模块标识，逗号分隔"/></el-form-item>
      <el-form-item label="一次性初始化服务"><el-input v-model="form.init" placeholder="成功退出码必须为 0"/></el-form-item>
      <el-form-item label="模型使用服务"><el-input v-model="form.models" placeholder="模型升级只重启这些服务"/></el-form-item>
      <el-form-item label="HTTP 就绪探测"><el-input v-model="form.urls" type="textarea" placeholder="每行一个 http://127.0.0.1:端口/就绪路径"/></el-form-item>
      <el-form-item label="等待秒数"><el-input-number v-model="form.timeout" :min="10" :max="3600"/></el-form-item>
    </el-form>
    <template #footer><el-button @click="metadata=false">取消</el-button><el-button type="primary" :loading="busy" @click="saveMetadata">保存设置</el-button></template>
  </el-dialog>
</template>

<style scoped>
.module-toolbar,.module-file-toolbar{display:flex;align-items:center;gap:12px;margin:12px 0}.module-toolbar>div:first-child{flex:1}.module-toolbar h3{margin:0 0 6px}.module-editor{height:55vh;margin:14px 0}.module-diff{display:grid;grid-template-columns:1fr 1fr;gap:16px}.module-diff pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--el-fill-color-light);padding:12px}small{color:var(--el-text-color-secondary)}code{word-break:break-all}
</style>
