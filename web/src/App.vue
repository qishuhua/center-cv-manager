<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, DataLine, Delete, Download, Files, Lock, Monitor, Operation, Plus, Promotion, Refresh, SwitchButton, UploadFilled, User, VideoPlay } from '@element-plus/icons-vue'
import http,{eventURL} from './api'
import ModelCatalog from './ModelCatalog.vue'
import ModelUploadDialog from './ModelUploadDialog.vue'
import TerminalPage from './TerminalPage.vue'
import ModuleManager from './ModuleManager.vue'
import InitializeCenterDialog from './InitializeCenterDialog.vue'
const initializationDialog=ref<InstanceType<typeof InitializeCenterDialog>>()
const initializationTask=(c:Center)=>tasks.value.find(t=>t.type==='INITIALIZE_CENTER'&&t.params?.center_id===c.id&&['PENDING','RUNNING'].includes(t.status))
async function initializationSubmitted(task:Task){tasks.value.unshift(task);await openTask(task);await loadAll()}
import UserManagement from './UserManagement.vue'
import type {AlertCredential,BaseImageArchive,Center,CenterOverview,Checkout,ComposeDocument,CurrentModelVersion,EmptyModelFolder,GitCredential,HardwareInfo,ModelVersion,MonitoringCenterStatus,MonitoringStatus,OSSCredential,OverviewContainer,OverviewVersion,RegistrySettings,RegistrySoftwareVersion,RegistrySourceVersions,RegistryStorage,SoftwareImage,Source,Task,UserRole,Version} from './types'

const YamlEditor=defineAsyncComponent(()=>import('./YamlEditor.vue'))

interface DeployItem {source_id:string;ref:string;commit:string}
interface ModelReleasePlan {ready:boolean;error?:string;action:'blocked'|'skip'|'download_and_activate'|'keep_current';download_bytes:number;current:Record<string,{version:string;platform:string;manifest_digest:string}>;targets:ModelVersion[]}
interface DeploymentPlan {service_mappings?:Record<string,string[]>;center:Center;ready:boolean;build_required:boolean;error?:string;images:Record<string,string>;missing_images:string[];missing_services:string[];model_plan:ModelReleasePlan}
interface CenterSoftwareRow {key:string;source?:Source;current:OverviewVersion[]}
interface SourcePublishDialog {visible:boolean;source:Source|null;selectedKey:string;loading:boolean}
interface ImageManageDialog {visible:boolean;loading:boolean;images:SoftwareImage[];deleting:string}
interface CenterModelRow {model:string;current?:CurrentModelVersion;options:ModelVersion[]}

const section=ref('overview'),loading=ref(false),centers=ref<Center[]>([]),sources=ref<Source[]>([]),tasks=ref<Task[]>([])
const authState=reactive({authenticated:false,username:'',role:'' as UserRole|'',must_change_password:false}),authLoading=ref(true)
const isAdmin=computed(()=>authState.role==='admin')
// 内建 admin 账号专属：用户增删改与任务记录删除不随管理权限下放。
const isSuperAdmin=computed(()=>authState.username==='admin')
// 普通用户点击发布、删除等操作按钮时统一弹窗提示，不再隐藏界面。
function guardAdminAction(){
  if(isAdmin.value)return true
  ElMessageBox.alert('当前账号是普通用户，仅可查看各中心状态。如需执行该操作，请联系管理员处理。','没有操作权限',{type:'warning',confirmButtonText:'知道了'})
  return false
}
const loginForm=reactive({username:'',password:''}),passwordForm=reactive({old_password:'',new_password:'',confirm_password:''})
const passwordDialog=ref(false),passwordSaving=ref(false)
const centerDialog=ref(false),sourceDialog=ref(false),gitCredentialDialog=ref(false),ossCredentialDialog=ref(false),alertCredentialDialog=ref(false),modelUploadDialog=ref(false),modelUploadTarget=ref(''),logDrawer=ref(false),centerOverviewDrawer=ref(false),centerOperationDialog=ref(false),centerReleaseDialog=ref(false),composeDrawer=ref(false),hardwareDialog=ref(false)
const centerForm=reactive<Center>({name:'',host:'',port:22,console_port:8080,username:'root',auth_type:'password',password:'',private_key:'',passphrase:'',docker_socket:'/var/run/docker.sock',accelerator:'auto',cpu_count:0,gpu_count:0,compose_file:'~/.local/share/center-manager/deploy/docker-compose.yml',description:'',wms_url:'',mis_auth:'',mis_sort_enabled:false,test_mode:false,debug_log_enabled:false,save_image_enabled:false})
const misSortEnabled=ref(false)
// 开关只控制启用状态：关闭时 WMS/misAuth 原值保留（表单隐藏但提交原值），
// 重新打开即可恢复使用，不丢失任何配置。
function onMisSortToggle(enabled:string|number|boolean){if(enabled&&!centerForm.mis_auth)centerForm.mis_auth='20260606123a'}
const sourceForm=reactive<Source>({name:'',repository:'',description:''})
const gitCredential=reactive<GitCredential>({base_url:'',username:'oauth2',token:'',configured:false})
const gitCredentialForm=reactive<GitCredential>({base_url:'',username:'oauth2',token:'',configured:false})
const ossCredential=reactive<OSSCredential>({endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false})
const ossCredentialForm=reactive<OSSCredential>({endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false})
const alertCredential=reactive<AlertCredential>({feishu_webhook:'',configured:false})
const alertCredentialForm=reactive<AlertCredential>({feishu_webhook:'',configured:false})
const registryDialog=ref(false),registryTesting=ref(false)
const registrySettings=reactive<RegistrySettings>({endpoint:'http://127.0.0.1:5000',configured:false})
const registrySettingsForm=reactive<RegistrySettings>({endpoint:'http://127.0.0.1:5000',configured:false})
const registryStorageForm=reactive<RegistryStorage>({root_directory:'registry',exists:false})
const gitVersions=reactive<Record<string,Version[]>>({}),gitVersionErrors=reactive<Record<string,string>>({})
const deployForm=reactive<{center_ids:string[];source_ids:string[];items:DeployItem[];model_keys:string[]}>({center_ids:[],source_ids:[],items:[],model_keys:[]})
const deploymentPlan=ref<DeploymentPlan[]>([]),deploymentPlanning=ref(false),deploymentVersionsLoading=ref(false),deploymentTasks=ref<Task[]>([])
const deploymentFailuresNotified=new Set<string>()
let deploymentPollTimer:number|undefined
const baseImages=ref<BaseImageArchive[]>([]),baseImagesLoading=ref(false),baseImageTasks=ref<Task[]>([])
const baseImageForm=reactive<{center_ids:string[];restart_stack:boolean}>({center_ids:[],restart_stack:false})
const baseImageFailuresNotified=new Set<string>()
let baseImagePollTimer:number|undefined
const models=ref<ModelVersion[]>([]),modelsLoading=ref(false),emptyModelFolders=ref<EmptyModelFolder[]>([])
const overview=ref<CenterOverview[]>([]),overviewLoading=ref(false)
const activeCenterOverview=ref<CenterOverview|null>(null),centerOverviewLoading=ref(false)
const centerModelSelections=reactive<Record<string,string>>({})
const sourcePublishDialog=reactive<SourcePublishDialog>({visible:false,source:null,selectedKey:'',loading:false})
const centerRegistryVersions=ref<RegistrySourceVersions[]>([]),centerRegistryError=ref('')
const centerSoftwareSelections=reactive<Record<string,string>>({})
const imageManageDialog=reactive<ImageManageDialog>({visible:false,loading:false,images:[],deleting:''})
const centerVersionApplying=ref(''),centerVersionApplyingCenter=ref(''),centerPinBusy=ref('')
const centerReleaseTask=ref<Task|null>(null),centerReleaseLogs=ref<string[]>([]),centerReleaseTitle=ref(''),centerReleaseStage=ref(''),centerReleaseProgress=ref(0),centerReleaseMessage=ref(''),centerReleaseError=ref('')
let centerReleaseWorkflowID=0
const centerOperationSubmitting=ref(false)
const centerOperationTask=ref<Task|null>(null),centerOperationLogs=ref<string[]>([]),centerOperationEvents=ref<EventSource|null>(null)
const composeDocument=reactive<ComposeDocument>({center_id:'',path:'',content:''}),composeSaving=ref(false)
const hardwareInfo=ref<HardwareInfo|null>(null),hardwareCenterName=ref(''),hardwareDetectingId=ref('')
const activeTask=ref<Task|null>(null),taskLogs=ref<string[]>([]),sourceEvents=ref<EventSource|null>(null)
const selectedTasks=ref<Task[]>([]),taskActionBusy=ref(false)
const isActiveTask=(task:Task)=>['PENDING','RUNNING'].includes(task.status)
const selectedActiveTasks=computed(()=>selectedTasks.value.filter(isActiveTask))
const selectedFinishedTasks=computed(()=>selectedTasks.value.filter(task=>!isActiveTask(task)))
let taskPollTimer:number|undefined
async function refreshTasks(){
  const data:Task[]=await http.get('/tasks')
  tasks.value=data
  selectedTasks.value=selectedTasks.value.map(task=>data.find(current=>current.id===task.id)).filter((task):task is Task=>!!task)
  if(activeTask.value){const id=activeTask.value.id;const current:Task=await http.get(`/tasks/${id}`);if(activeTask.value?.id===id)Object.assign(activeTask.value,current)}
}
async function pollTasks(){
  try{if(authState.authenticated&&(section.value==='tasks'||logDrawer.value))await refreshTasks()}catch{}finally{taskPollTimer=window.setTimeout(pollTasks,2000)}
}
async function stopTasks(targets:Task[],all=false){if(!guardAdminAction())return;
  if(taskActionBusy.value||(!all&&!targets.length))return
  taskActionBusy.value=true
  try{
    await ElMessageBox.confirm(all?'确定停止所有等待中和进行中的任务？':`确定停止选中的 ${targets.length} 个任务？`,'确认停止任务',{type:'warning',confirmButtonText:'确认停止',cancelButtonText:'取消',closeOnClickModal:false})
    if(all){const count:number=await http.post('/tasks/cancel-all');ElMessage.success(`已向 ${count} 个任务发送停止请求`)}
    else{const results=await Promise.allSettled(targets.map(task=>http.post(`/tasks/${task.id}/cancel`)));reportTaskResults(results,'停止请求已发送')}
    await refreshTasks()
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{taskActionBusy.value=false}
}
function reportTaskResults(results:PromiseSettledResult<any>[],message:string){
  const failed=results.filter((result):result is PromiseRejectedResult=>result.status==='rejected')
  if(failed.length)ElMessage.warning(`${results.length-failed.length} 个成功，${failed.length} 个失败：${failed[0].reason.message}`)
  else ElMessage.success(message)
}
async function deleteTasks(targets:Task[]){if(!guardAdminAction())return;
  if(taskActionBusy.value||!targets.length)return
  taskActionBusy.value=true
  try{
    await ElMessageBox.confirm(`确定永久删除 ${targets.length} 条已结束任务记录及其全部日志？删除后无法恢复。`,'确认删除任务记录',{type:'warning',confirmButtonText:'确认删除',cancelButtonText:'取消',closeOnClickModal:false})
    const results=await Promise.allSettled(targets.map(task=>http.delete(`/tasks/${task.id}`)))
    reportTaskResults(results,'任务记录已删除')
    const deletedIds=new Set(targets.filter((_,index)=>results[index].status==='fulfilled').map(task=>task.id))
    if(activeTask.value&&deletedIds.has(activeTask.value.id)){closeLogs();logDrawer.value=false;activeTask.value=null;taskLogs.value=[]}
    selectedTasks.value=selectedTasks.value.filter(task=>!deletedIds.has(task.id))
    await refreshTasks()
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{taskActionBusy.value=false}
}
const onlineCount=computed(()=>overview.value.filter(v=>v.online).length)
const offlineCount=computed(()=>Math.max(0,overview.value.length-onlineCount.value))
const initializedCount=computed(()=>centers.value.filter(v=>v.initialized_at).length)
const activeTaskCount=computed(()=>tasks.value.filter(v=>['PENDING','RUNNING'].includes(v.status)).length)
const onlineRate=computed(()=>overview.value.length?Math.round(onlineCount.value/overview.value.length*100):0)
const initializedRate=computed(()=>centers.value.length?Math.round(initializedCount.value/centers.value.length*100):0)
const recentTasks=computed(()=>[...tasks.value].sort((a,b)=>new Date(b.created_at).getTime()-new Date(a.created_at).getTime()).slice(0,6))
const acceleratorStats=computed(()=>{const counts=new Map<string,number>();for(const center of centers.value){const key=center.detected_accelerator||center.accelerator||'unknown';counts.set(key,(counts.get(key)||0)+1)}return [...counts.entries()].map(([type,count])=>({type,count}))})
const acceleratorName=(value?:string)=>value==='nvidia'?'NVIDIA':value==='denglin'?'登临':value==='cpu'?'CPU':value==='unknown'?'识别失败':'自动识别'
const centerTransferData=computed(()=>centers.value.map(center=>{const detected=center.detected_accelerator||overview.value.find(item=>item.center.id===center.id)?.accelerator;return {key:center.id!,label:`${center.name} · ${acceleratorName(detected||center.accelerator)} (${center.host}:${center.port})`}}))
const unavailableDeploymentCenters=computed(()=>deploymentPlan.value.filter(item=>!item.ready))
const activeComposeCenter=computed(()=>centers.value.find(item=>item.id===composeDocument.center_id))
const statusType=(s:string)=>s==='SUCCESS'?'success':s==='FAILED'?'danger':s==='RUNNING'?'primary':'info'
const centerReleaseStatus=computed(()=>centerReleaseError.value?'FAILED':centerReleaseTask.value?.status||(centerReleaseProgress.value>=100?'SUCCESS':'RUNNING'))
const centerActionName=(value?:string)=>value==='restart_container'?'重启容器':value==='restart_compose'?'重启 Compose':value==='redeploy_compose'?'重新部署 Compose':value==='stop_compose'?'停止 Compose':value==='remove_all_containers'?'删除全部容器':value||'-'
const taskTypeName=(value:string)=>value==='RESET_CENTER'?'重置中心':value==='PAUSE_CENTER'?'暂停中心':value==='RESUME_CENTER'?'恢复中心':value==='COMPOSE_PUBLISH'?'Compose 发布':value==='DETECT_CENTER'?'硬件检测':value==='INITIALIZE_CENTER'?'中心初始化':value==='BASE_IMAGE_UPDATE'?'基础镜像更新':value==='MODEL_RELEASE'?'模型发布':value==='MODEL_SYNC'?'模型同步（历史）':value==='DOWNLOAD_SOURCE'?'源码下载':value==='PUBLISH_IMAGE'?'镜像构建发布':value==='RELEASE_SOFTWARE'?'软件版本切换':value==='RELEASE_VERSION'?'版本发布':value==='BUILD_IMAGE'?'镜像构建（历史）':value==='DEPLOY_STACK'?'服务部署（历史）':value==='CENTER_OPERATION'?'中心操作':value==='MODULE_OPERATION'?'模块操作':value==='MODULE_MIGRATION'?'模块迁移':value==='PREPARE_MODULES'?'准备模块文件':value
const taskTarget=(task:Task)=>task.type==='COMPOSE_PUBLISH'||task.type==='MODULE_OPERATION'?`${task.params?.module_id} · ${task.params?.center_name||task.params?.center_id}`:task.type==='MODULE_MIGRATION'?`${task.params?.action==='rollback'?'迁移回退':'模块迁移'} · ${task.params?.center_name||task.params?.center_id}`:task.type==='RELEASE_VERSION'||task.type==='DEPLOY_STACK'||task.type==='MODEL_RELEASE'||task.type==='RELEASE_SOFTWARE'?`${task.params?.center_name||task.params?.center_id}${task.params?.versions?`：${task.params?.versions}`:''}`:task.type==='PUBLISH_IMAGE'?`${task.params?.source_name||task.params?.source_id}${task.params?.versions?`：${task.params?.versions}`:''}`:task.type==='BUILD_IMAGE'?`${task.params?.source_name||task.params?.source_id} → ${task.params?.center_name||task.params?.center_id}`:task.type==='BASE_IMAGE_UPDATE'?task.params?.center_name||task.params?.center_id:task.type==='MODEL_SYNC'?`${task.params?.model_count||0} 个模型 → ${task.params?.center_name||task.params?.center_id}`:task.type==='DETECT_CENTER'||task.type==='INITIALIZE_CENTER'||task.type==='RESET_CENTER'||task.type==='PAUSE_CENTER'||task.type==='RESUME_CENTER'||task.type==='PREPARE_MODULES'?task.params?.center_name||task.params?.center_id:task.type==='CENTER_OPERATION'?`${centerActionName(task.params?.action)} · ${task.params?.center_name||task.params?.center_id}`:task.params?.ref||'-'
// 任务目标统一显示中心名称；没有中心名称的任务（Compose 发布、源码下载）沿用原文，版本等明细放悬浮提示。
const taskTargetSummary=(task:Task)=>task.params?.center_name||taskTarget(task)
const taskLogLine=(task:Task)=>task.error||task.message||'暂无日志'
const taskLogTooltip=(task:Task)=>taskLogLine(task)+(task.log_count?`（共 ${task.log_count} 条日志，点击行查看完整日志）`:'')
const fmt=(v?:string)=>v?new Date(v).toLocaleString():'-'
const formatBytes=(value:number)=>{const units=['B','KiB','MiB','GiB','TiB'];let size=value,index=0;while(size>=1024&&index<units.length-1){size/=1024;index++}return `${size.toFixed(1)} ${units[index]}`}

async function loadOverview(showError=true){overviewLoading.value=true;try{const data=await http.get('/overview');overview.value=Array.isArray(data)?data:[]}catch(e:any){if(showError)ElMessage.error(e.message)}finally{overviewLoading.value=false}}
const monitoringStatus=ref<MonitoringStatus|null>(null),monitoringLoading=ref(false)
async function loadMonitoringStatus(){monitoringLoading.value=true;try{monitoringStatus.value=await http.get('/monitoring/status')}catch(e:any){ElMessage.error(e.message)}finally{monitoringLoading.value=false}}
// Grafana 与管理端同机不同端口（compose 默认 0.0.0.0:3000）；每中心仪表盘
// uid 由 dashboard-generator 固定为 center-cv-<中心ID>。
const grafanaBase=()=>`${window.location.protocol}//${window.location.hostname}:3000`
function openGrafanaDashboard(site?:string){window.open(grafanaBase()+(site?`/d/center-cv-${site}`:'/dashboards'),'_blank','noopener')}
const monitorStateText=(item:MonitoringCenterStatus)=>!item.monitored?'未纳入监控':item.up===undefined?'等待采集':item.up?'健康信号正常':'无法连接'
const monitorStateType=(item:MonitoringCenterStatus)=>!item.monitored?'info':item.up===undefined?'warning':item.up?'success':'danger'
const softwareOptionKey=(item:Pick<Version,'kind'|'ref'|'commit'>)=>`${item.kind}:${item.ref}:${item.commit}`
const softwareImageKey=(item:SoftwareImage)=>`${item.ref}:${item.commit}:${item.image}`
const centerSoftwareRows=computed<CenterSoftwareRow[]>(()=>{
  const deployed=activeCenterOverview.value?.versions||[],matched=new Set<number>(),rows:CenterSoftwareRow[]=[]
  for(const source of sources.value){
    const current=deployed.filter((item,index)=>{const same=item.source_id===source.id||(!item.source_id&&item.source_name===source.name);if(same)matched.add(index);return same})
    rows.push({key:source.id||source.name,source,current})
  }
  const orphaned=new Map<string,OverviewVersion[]>()
  deployed.forEach((item,index)=>{if(matched.has(index))return;const key=item.source_id||item.source_name;orphaned.set(key,[...(orphaned.get(key)||[]),item])})
  for(const [key,current] of orphaned)rows.push({key:`deployed:${key}`,current})
  return rows
})
const deployedCenterModels=computed(()=>(activeCenterOverview.value?.models||[]))
// 镜像仓库版本按 source_id 建索引，中心详情页软件区据此提供可切换版本。
const centerRegistryBySource=computed(()=>{const map:Record<string,RegistrySourceVersions>={};for(const item of centerRegistryVersions.value)if(item.source_id)map[item.source_id]=item;return map})
const centerSoftwarePlatform=computed(()=>{const overview=activeCenterOverview.value;const accelerator=overview?.accelerator||overview?.center.detected_accelerator||overview?.center.accelerator||'';return accelerator==='auto'?'':accelerator})
const centerModelRows=computed<CenterModelRow[]>(()=>{
  const byModel=new Map<string,CenterModelRow>()
  for(const current of deployedCenterModels.value)byModel.set(current.model,{model:current.model,current,options:[]})
  for(const option of models.value){const row=byModel.get(option.model)||{model:option.model,options:[]};row.options.push(option);byModel.set(option.model,row)}
  return [...byModel.values()].sort((a,b)=>a.model.localeCompare(b.model))
})
const centerModelSetPinned=computed(()=>deployedCenterModels.value.some(item=>item.pinned))
// 版本切换/模型发布是长任务（发布对话框可「转到后台」）：进行中状态只属于
// 发起它的中心，其他中心详情页的按钮不应显示切换中。
const centerVersionApplyingHere=computed(()=>centerVersionApplyingCenter.value&&centerVersionApplyingCenter.value===activeCenterOverview.value?.center.id?centerVersionApplying.value:'')
function centerModelCompatible(item:ModelVersion){const accelerator=activeCenterOverview.value?.accelerator||activeCenterOverview.value?.center.detected_accelerator||activeCenterOverview.value?.center.accelerator;return item.formats.some(format=>format.platform==='onnx'||format.platform===accelerator)}
function modelFormatSummary(item:ModelVersion){return item.formats.map(format=>format.hardware_profile?`${format.platform}-${format.hardware_profile}`:format.platform).join(' / ')}
function selectedCenterModel(row:CenterModelRow){return models.value.find(item=>item.manifest_key===centerModelSelections[row.model])}
function selectedCenterModelCompatible(row:CenterModelRow){const selected=selectedCenterModel(row);return !!selected&&centerModelCompatible(selected)}
const changedCenterModelRows=computed(()=>centerModelRows.value.filter(row=>{const target=selectedCenterModel(row);return !!target&&target.manifest_digest!==row.current?.manifest_digest}))
// 软件版本与模型版本同口径：选择与运行版本不同即待发布变更，多个软件
// 累计到底部发布条，与模型变更合并成一个统一发布任务。
function selectedCenterSoftwareVersion(row:CenterSoftwareRow){if(!row.source?.id)return undefined;const entry=centerRegistryBySource.value[row.source.id];return entry?.versions.find(item=>item.version===centerSoftwareSelections[row.key])}
const changedCenterSoftwareRows=computed(()=>centerSoftwareRows.value.filter(row=>{const selected=selectedCenterSoftwareVersion(row);return !!selected&&!row.current.some(item=>item.version===selected.ref&&(!item.commit||!selected.commit||item.commit===selected.commit))}))
const hasCenterVersionChanges=computed(()=>changedCenterSoftwareRows.value.length>0||changedCenterModelRows.value.length>0)
const centerVersionChangesBlocked=computed(()=>changedCenterModelRows.value.some(row=>!selectedCenterModelCompatible(row))||(centerModelSetPinned.value&&changedCenterModelRows.value.length>0))
function syncCenterVersionSelections(){
  for(const row of centerModelRows.value){const currentMatch=row.current?row.options.find(item=>item.manifest_digest===row.current!.manifest_digest):undefined;const selected=row.options.find(item=>item.manifest_key===centerModelSelections[row.model]&&centerModelCompatible(item));centerModelSelections[row.model]=currentMatch?.manifest_key||selected?.manifest_key||''}
}
async function loadGitVersions(sourceID:string){try{const data=await http.get(`/sources/${sourceID}/versions`);gitVersions[sourceID]=Array.isArray(data)?data:[];delete gitVersionErrors[sourceID]}catch(e:any){gitVersions[sourceID]=[];gitVersionErrors[sourceID]=e.message||'读取 Git 版本失败'}}
async function loadCenterRegistryVersions(){const centerID=activeCenterOverview.value?.center.id;if(!centerID)return;centerRegistryError.value='';try{const data=await http.get(`/centers/${centerID}/software/registry-versions`);centerRegistryVersions.value=Array.isArray(data)?data:[];syncCenterSoftwareSelections()}catch(e:any){centerRegistryVersions.value=[];centerRegistryError.value=e.message||'读取镜像仓库版本失败'}}
function syncCenterSoftwareSelections(){
  for(const row of centerSoftwareRows.value){
    const available=centerRegistryBySource.value[row.source?.id||'']?.versions||[]
    const current=row.current[0]
    const match=current?available.find(item=>item.ref===current.version&&(!current.commit||!item.commit||item.commit===current.commit)):undefined
    centerSoftwareSelections[row.key]=match?.version||available[0]?.version||''
  }
}
function registryVersionLabel(item:RegistrySoftwareVersion){return `${item.version} · ${item.platforms.map(tag=>tag.platform==='shared'?'通用':tag.platform).join('/')} · ${fmt(item.created_at)}`}
async function loadCenterVersionOptions(){if(ossCredential.configured)await loadModels();else models.value=[];syncCenterVersionSelections()}
async function refreshActiveCenterOverview(){if(!activeCenterOverview.value?.center.id)return;centerOverviewLoading.value=true;try{activeCenterOverview.value=await http.get(`/centers/${activeCenterOverview.value.center.id}/overview`);syncCenterVersionSelections()}catch(e:any){ElMessage.error(e.message)}finally{centerOverviewLoading.value=false}}
async function refreshCenterManagement(){await Promise.all([refreshActiveCenterOverview(),loadCenterVersionOptions(),registrySettings.configured?loadCenterRegistryVersions():Promise.resolve()]);syncCenterVersionSelections();syncCenterSoftwareSelections()}
async function openCenterOverview(item:CenterOverview){for(const key of Object.keys(centerModelSelections))delete centerModelSelections[key];activeCenterOverview.value=item;centerOverviewDrawer.value=true;await Promise.all([refreshActiveCenterOverview(),loadCenterVersionOptions(),registrySettings.configured?loadCenterRegistryVersions():Promise.resolve()]);syncCenterVersionSelections();syncCenterSoftwareSelections()}
async function openCenterFromCard(center:Center){await openCenterOverview({center,online:false,containers:[],versions:[],models:[]})}
// 中心网关 Nginx 在中心本机监听 8080，公网端口由防火墙映射，各中心可能不同（console_port）；
// Grafana 监控与 Kibana 日志页面均经它对外提供。
function openCenterConsole(center:Center,path:string){window.open(`http://${center.host}:${center.console_port||8080}${path}`,'_blank','noopener')}
function closeCenterOperationStream(){centerOperationEvents.value?.close();centerOperationEvents.value=null}
async function finishCenterOperation(status:string){
  closeCenterOperationStream()
  try{
    if(centerOperationTask.value){
      const current:Task=await http.get(`/tasks/${centerOperationTask.value.id}`)
      if(centerOperationTask.value?.id===current.id)Object.assign(centerOperationTask.value,current)
    }
  }catch{}
  await refreshActiveCenterOverview()
  if(status==='SUCCESS')ElMessage.success('中心操作完成，实际容器状态已刷新')
  else if(status==='FAILED')ElMessage.error(centerOperationTask.value?.error||'中心操作失败，请查看实时输出')
  else if(status==='CANCELLED')ElMessage.warning('中心操作已停止')
}
async function showCenterOperation(task:Task){
  closeCenterOperationStream()
  centerOperationTask.value={...task,params:{...(task.params||{})}}
  centerOperationLogs.value=[]
  centerOperationDialog.value=true
  try{const logs=await http.get(`/tasks/${task.id}/logs`);centerOperationLogs.value=Array.isArray(logs)?logs:[];for(const message of centerOperationLogs.value)captureCenterOperationCommand(message)}catch{}
  const es=new EventSource(eventURL(task.id))
  centerOperationEvents.value=es
  es.addEventListener('log',(event:any)=>{try{const message=JSON.parse(event.data).message;centerOperationLogs.value.push(message);captureCenterOperationCommand(message)}catch{}})
  es.addEventListener('progress',(event:any)=>{try{if(centerOperationTask.value?.id===task.id)Object.assign(centerOperationTask.value,JSON.parse(event.data))}catch{}})
  es.addEventListener('status',async(event:any)=>{try{const data=JSON.parse(event.data);if(centerOperationTask.value?.id===task.id)Object.assign(centerOperationTask.value,data);if(['SUCCESS','FAILED','CANCELLED'].includes(data.status))await finishCenterOperation(data.status)}catch{}})
  es.onerror=async()=>{es.close();if(centerOperationEvents.value===es)centerOperationEvents.value=null;try{const current:Task=await http.get(`/tasks/${task.id}`);if(centerOperationTask.value?.id===task.id)Object.assign(centerOperationTask.value,current);if(['SUCCESS','FAILED','CANCELLED'].includes(current.status))await finishCenterOperation(current.status)}catch{}}
}
function captureCenterOperationCommand(message:string){
  const match=message.match(/\$ (docker compose .*)$/)
  if(match&&centerOperationTask.value){centerOperationTask.value.params={...(centerOperationTask.value.params||{}),command:match[1]}}
}
async function restartContainer(container:OverviewContainer){if(!guardAdminAction())return
  const center=activeCenterOverview.value?.center
  if(!center?.id)return
  const name=(container.names||[]).join(', ')||container.id.slice(0,12)
  try{
    await ElMessageBox.confirm(`确认重启容器“${name}”？`,'重启容器',{type:'warning',confirmButtonText:'确认重启',cancelButtonText:'取消',closeOnClickModal:false})
    centerOperationSubmitting.value=true
    const task:Task=await http.post(`/centers/${center.id}/actions`,{action:'restart_container',container_id:container.id})
    // restart_container 走 Docker API，没有 "$ docker compose" 日志行，直接预设展示等价指令。
    task.params={...(task.params||{}),command:'docker restart '+container.id.slice(0,12)}
    await showCenterOperation(task)
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{centerOperationSubmitting.value=false}
}
async function loadAll(){loading.value=true;try{
  const [centerData,sourceData,taskData]=await Promise.all([http.get('/centers'),http.get('/sources'),http.get('/tasks')])
  centers.value=Array.isArray(centerData)?centerData:[];sources.value=Array.isArray(sourceData)?sourceData:[];tasks.value=Array.isArray(taskData)?taskData:[]
  // 凭据接口仅管理员可见；普通用户改用配置状态布尔值渲染页面提示。
  if(isAdmin.value){const [credentialData,ossData,alertData,registryData]=await Promise.all([http.get('/git-credential'),http.get('/oss-credential'),http.get('/alert-credential'),http.get('/registry')]);Object.assign(gitCredential,credentialData||{base_url:'',username:'oauth2',token:'',configured:false});Object.assign(ossCredential,{models_prefix:'center-models/'},ossData||{endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false});Object.assign(alertCredential,alertData||{feishu_webhook:'',configured:false});Object.assign(registrySettings,{endpoint:'http://127.0.0.1:5000'},registryData||{endpoint:'http://127.0.0.1:5000',configured:false})}
  else{const flags=await http.get('/settings/status');Object.assign(gitCredential,{configured:flags.git_configured});Object.assign(ossCredential,{configured:flags.oss_configured});Object.assign(alertCredential,{configured:flags.alert_configured});Object.assign(registrySettings,{configured:flags.registry_configured})}
  if(section.value==='overview')void loadOverview(false)}catch(e:any){if(authState.authenticated)ElMessage.error(e.message)}finally{loading.value=false}}
async function selectSection(value:string){if(!isAdmin.value&&['terminal','users'].includes(value)){guardAdminAction();return}if(value==='releases'){await openDeploy();return}if(value==='base-images'){await openBaseImages();return}if(value==='models'){await openModels();return}section.value=value;if(value==='overview'&&!overviewLoading.value)void loadOverview();if(value==='monitoring')void loadMonitoringStatus()}
async function refreshCurrent(){await loadAll();if(section.value==='overview'&&!overview.value.length&&!overviewLoading.value)void loadOverview();if(section.value==='base-images'&&ossCredential.configured)await loadBaseImages();if(section.value==='models'&&ossCredential.configured)await loadModels();if(section.value==='releases'){await Promise.all([loadDeploymentVersions(),ossCredential.configured?loadModels():Promise.resolve()]);syncDeployItems(deployForm.source_ids)}}
async function checkAuth(){authLoading.value=true;try{Object.assign(authState,await http.get('/auth/status'))}catch{authState.authenticated=false}finally{authLoading.value=false}}
async function login(){authLoading.value=true;try{const status:any=await http.post('/auth/login',loginForm);Object.assign(authState,status);resetPasswordForm();if(status.must_change_password)passwordForm.old_password=loginForm.password;loginForm.password='';if(!status.must_change_password)await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{authLoading.value=false}}
function resetPasswordForm(){Object.assign(passwordForm,{old_password:'',new_password:'',confirm_password:''})}
function openPasswordDialog(){resetPasswordForm();passwordDialog.value=true}
async function changePassword(){
  if(passwordSaving.value)return
  if(!passwordForm.old_password){ElMessage.warning('请输入当前密码');return}
  if(passwordForm.new_password.length<4){ElMessage.warning('新密码至少需要 4 个字符');return}
  if(passwordForm.new_password!==passwordForm.confirm_password){ElMessage.warning('两次输入的新密码不一致');return}
  const firstLogin=authState.must_change_password
  passwordSaving.value=true
  try{
    await http.post('/auth/change-password',{old_password:passwordForm.old_password,new_password:passwordForm.new_password})
    authState.must_change_password=false
    passwordDialog.value=false
    resetPasswordForm()
    ElMessage.success('密码修改成功')
    if(firstLogin)await loadAll()
  }catch(e:any){ElMessage.error(e.message)}finally{passwordSaving.value=false}
}
async function skipPasswordChange(){authLoading.value=true;try{await http.post('/auth/skip-password-change');authState.must_change_password=false;resetPasswordForm();await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{authLoading.value=false}}
async function logout(){try{await http.post('/auth/logout')}finally{Object.assign(authState,{authenticated:false,username:'',role:'',must_change_password:false});loginForm.password='';passwordDialog.value=false;resetPasswordForm()}}
function resetCenter(){Object.assign(centerForm,{id:undefined,name:'',host:'',port:22,console_port:8080,username:'root',auth_type:'password',password:'',private_key:'',passphrase:'',host_key:'',docker_socket:'/var/run/docker.sock',accelerator:'auto',detected_accelerator:'',cpu_count:0,gpu_count:0,compose_file:'~/.local/share/center-manager/deploy/docker-compose.yml',initialized_at:undefined,description:'',wms_url:'',mis_auth:'',mis_sort_enabled:false,test_mode:false,debug_log_enabled:false,save_image_enabled:false});misSortEnabled.value=false}
function editCenter(v?:Center){if(!guardAdminAction())return;resetCenter();if(v)Object.assign(centerForm,v);misSortEnabled.value=!!centerForm.wms_url&&centerForm.mis_sort_enabled===true;if(misSortEnabled.value&&!centerForm.mis_auth)centerForm.mis_auth='20260606123a';centerDialog.value=true}
async function saveCenter(){if(!guardAdminAction())return;centerForm.mis_sort_enabled=misSortEnabled.value;try{const isNew=!centerForm.id;const path=isNew?'/centers':`/centers/${centerForm.id}`;const result:Center=isNew?await http.post(path,centerForm):await http.put(path,centerForm);centerDialog.value=false;await loadAll();ElMessage.success('中心已保存');if(isNew)await initializationDialog.value?.open(result)}catch(e:any){ElMessage.error(e.message)}}
async function removeCenter(v:Center){if(!guardAdminAction())return;await ElMessageBox.confirm(`确定删除中心“${v.name}”？`,'确认');try{await http.delete(`/centers/${v.id}`);ElMessage.success('已删除');await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function detectCenter(v:Center){if(!v.id||hardwareDetectingId.value)return;hardwareDetectingId.value=v.id;try{ElMessage.info('正在读取中心硬件信息…');hardwareInfo.value=await http.post(`/centers/${v.id}/detect`);hardwareCenterName.value=v.name;hardwareDialog.value=true;await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{hardwareDetectingId.value=''}}
async function initializeCenter(v:Center){if(!guardAdminAction())return;const running=initializationTask(v);if(running){await openTask(running);return};await initializationDialog.value?.open(v)}
const composeSelectedFile=ref('')
async function resetCenterDeployment(v:Center){if(!guardAdminAction())return
 try{
  await ElMessageBox.confirm(`确认重置中心“${v.name}”（${v.host}）？将删除该中心受管 Compose 容器和部署配置。镜像、数据卷、模型文件保留。不生成备份、不支持回退；重置后不会自动启动服务，需要先选择业务镜像版本与模型版本，再开始完整初始化部署。`,'重置中心 · 第 1 步 / 共 3 步',{type:'warning',confirmButtonText:'继续',cancelButtonText:'取消',closeOnClickModal:false})
  await ElMessageBox.confirm(`再次确认：重置不可逆、不生成备份。中心全部服务将立即下线，监控、网关与外部访问随之中断；已发布的部署配置会被删除，之后必须重新走完整的初始化部署。确定继续？`,'重置中心 · 第 2 步 / 共 3 步',{type:'warning',confirmButtonText:'仍要继续',cancelButtonText:'取消',closeOnClickModal:false})
  await ElMessageBox.confirm(`最后确认：是否立即重置中心“${v.name}”（${v.host}）的受管部署？`,'重置中心 · 第 3 步 / 共 3 步',{type:'error',confirmButtonText:'确认重置受管部署',cancelButtonText:'取消',closeOnClickModal:false})
  centerOperationSubmitting.value=true
  const task:Task=await http.post(`/centers/${v.id}/reset`,{confirm_reset:true})
  await openTask(task)
 }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{centerOperationSubmitting.value=false}
}
async function pauseCenter(v:Center){if(!guardAdminAction())return
 try{
  await ElMessageBox.confirm(`确认暂停中心“${v.name}”（${v.host}）？全部受管服务将按依赖反序停止并移除容器，监控、网关与外部访问随之中断；数据卷、镜像与配置保留，可随时恢复。`,'暂停中心',{type:'warning',confirmButtonText:'确认暂停',cancelButtonText:'取消',closeOnClickModal:false})
  centerOperationSubmitting.value=true
  const task:Task=await http.post(`/centers/${v.id}/pause`,{confirm:true})
  await openTask(task)
 }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{centerOperationSubmitting.value=false}
}
async function resumeCenter(v:Center){if(!guardAdminAction())return
 try{
  await ElMessageBox.confirm(`确认恢复中心“${v.name}”（${v.host}）？将按模块依赖顺序重新启动全部服务并等待就绪，期间服务分批恢复。`,'恢复中心',{type:'info',confirmButtonText:'确认恢复',cancelButtonText:'取消',closeOnClickModal:false})
  centerOperationSubmitting.value=true
  const task:Task=await http.post(`/centers/${v.id}/resume`,{confirm:true})
  await openTask(task)
 }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{centerOperationSubmitting.value=false}
}
async function openCompose(v:Center,file=''){composeSelectedFile.value=file;composeDrawer.value=true;composeSaving.value=true;Object.assign(composeDocument,{center_id:v.id!,path:v.compose_file,content:''});try{Object.assign(composeDocument,await http.get(`/centers/${v.id}/compose?file=${encodeURIComponent(file)}`))}catch(e:any){ElMessage.error(e.message)}finally{composeSaving.value=false}}
async function saveCompose(){if(!guardAdminAction())return;if(!composeDocument.content.trim()){ElMessage.warning('Compose 内容不能为空');return};composeSaving.value=true;try{Object.assign(composeDocument,await http.put(`/centers/${composeDocument.center_id}/compose?file=${encodeURIComponent(composeSelectedFile.value)}`,{content:composeDocument.content}));ElMessage.success('Compose 校验通过并已保存，旧文件已备份');await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{composeSaving.value=false}}
function resetSource(){Object.assign(sourceForm,{id:undefined,name:'',repository:'',description:''})}
function editSource(v?:Source){if(!guardAdminAction())return;resetSource();if(v)Object.assign(sourceForm,v);sourceDialog.value=true}
async function saveSource(){if(!guardAdminAction())return;try{const path=sourceForm.id?`/sources/${sourceForm.id}`:'/sources';sourceForm.id?await http.put(path,sourceForm):await http.post(path,sourceForm);ElMessage.success('软件仓库已保存');sourceDialog.value=false;await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function removeSource(v:Source){if(!guardAdminAction())return;await ElMessageBox.confirm(`删除“${v.name}”及其本地源码缓存？`,'确认');try{await http.delete(`/sources/${v.id}`);ElMessage.success('已删除');await loadAll()}catch(e:any){ElMessage.error(e.message)}}
function editGitCredential(){if(!guardAdminAction())return;Object.assign(gitCredentialForm,gitCredential);gitCredentialDialog.value=true}
async function saveGitCredential(){try{const out:any=await http.put('/git-credential',gitCredentialForm);Object.assign(gitCredential,out);ElMessage.success('全局 Git 访问令牌已保存');gitCredentialDialog.value=false}catch(e:any){ElMessage.error(e.message)}}
function editOSSCredential(){if(!guardAdminAction())return;Object.assign(ossCredentialForm,ossCredential);ossCredentialDialog.value=true}
async function saveOSSCredential(){try{const out:any=await http.put('/oss-credential',ossCredentialForm);Object.assign(ossCredential,out);ElMessage.success('OSS 资源仓库配置已保存');ossCredentialDialog.value=false;if(section.value==='models'||section.value==='releases')await loadModels();else await loadBaseImages()}catch(e:any){ElMessage.error(e.message)}}
function editAlertCredential(){if(!guardAdminAction())return;Object.assign(alertCredentialForm,alertCredential);alertCredentialDialog.value=true}
async function saveAlertCredential(){try{const out:any=await http.put('/alert-credential',alertCredentialForm);Object.assign(alertCredential,out);ElMessage.success('全局飞书告警 Webhook 已保存');alertCredentialDialog.value=false}catch(e:any){ElMessage.error(e.message)}}
async function editRegistry(){if(!guardAdminAction())return;Object.assign(registrySettingsForm,registrySettings);Object.assign(registryStorageForm,{root_directory:'registry',exists:false});try{const storage=await http.get('/registry/storage');if(storage?.root_directory)registryStorageForm.root_directory=storage.root_directory}catch{/* 未渲染过配置时使用默认值 */}registryDialog.value=true}
async function saveRegistry(){try{const [settingsOut,storageOut]=await Promise.all([http.put('/registry',registrySettingsForm),http.put('/registry/storage',registryStorageForm)]);Object.assign(registrySettings,settingsOut);Object.assign(registryStorageForm,storageOut);ElMessage.success('镜像仓库配置已保存；registry 容器需重启后生效（docker compose restart registry）');registryDialog.value=false}catch(e:any){ElMessage.error(e.message)}}
async function testRegistry(){if(registryTesting.value)return;registryTesting.value=true;try{await http.post('/registry/test');ElMessage.success('镜像仓库连接正常')}catch(e:any){ElMessage.error(e.message)}finally{registryTesting.value=false}}
async function loadBaseImages(){baseImagesLoading.value=true;try{const data=await http.get('/base-images');baseImages.value=Array.isArray(data)?data:[]}catch(e:any){baseImages.value=[];if(ossCredential.configured)ElMessage.error(e.message)}finally{baseImagesLoading.value=false}}
async function openBaseImages(){section.value='base-images';baseImageForm.center_ids=[];baseImageForm.restart_stack=false;baseImageTasks.value=[];if(ossCredential.configured)await loadBaseImages()}
async function refreshBaseImageTasks(){window.clearTimeout(baseImagePollTimer);if(!baseImageTasks.value.length)return;try{baseImageTasks.value=await Promise.all(baseImageTasks.value.map(task=>http.get(`/tasks/${task.id}`)));for(const task of baseImageTasks.value){if(task.status==='FAILED'&&!baseImageFailuresNotified.has(task.id)){baseImageFailuresNotified.add(task.id);ElMessage.error(`中心 ${task.params?.center_name||task.params?.center_id} 基础镜像更新失败：${task.error||'请查看日志'}`)}}if(baseImageTasks.value.some(task=>['PENDING','RUNNING'].includes(task.status)))baseImagePollTimer=window.setTimeout(refreshBaseImageTasks,2000)}catch(e:any){ElMessage.error(e.message)}}
async function upgradeBaseImages(){if(!guardAdminAction())return;if(!baseImageForm.center_ids.length){ElMessage.warning('请至少选择一台中心服务器');return};if(baseImageForm.restart_stack)await ElMessageBox.confirm('加载新基础镜像后将重建实际使用变化镜像的服务，会产生短暂中断。确认继续？','确认重建服务',{type:'warning'});try{baseImageFailuresNotified.clear();baseImageTasks.value=await http.post('/base-images/upgrade',baseImageForm);ElMessage.success(`已创建 ${baseImageTasks.value.length} 个基础镜像更新任务`);await refreshBaseImageTasks();await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function loadModels(){modelsLoading.value=true;try{const data=await http.get('/models');models.value=Array.isArray(data)?data:[];const available=new Set(models.value.map(item=>item.manifest_key));deployForm.model_keys=deployForm.model_keys.filter(key=>available.has(key))}catch(e:any){models.value=[];deployForm.model_keys=[];if(ossCredential.configured)ElMessage.error(e.message)}finally{modelsLoading.value=false}}
async function deleteModelFolder(model:string){if(!guardAdminAction())return;
  if(modelsLoading.value)return
  const count=models.value.filter(item=>item.model===model).length
  if(count){ElMessage.warning('该文件夹包含可用版本，不支持在管理端删除');return}
  const folder=emptyModelFolders.value.find(item=>item.model===model)
  const detail=folder?`其中仅含 ${folder.files} 个残留文件（${formatBytes(folder.size)}）`:'其中没有可用版本'
  try{await ElMessageBox.confirm(`确定删除模型文件夹“${model}”？${detail}，删除仅清理残留文件。此操作不可恢复。`,'删除空模型文件夹',{type:'warning',confirmButtonText:'确认删除',cancelButtonText:'取消'})}catch{return}
  modelsLoading.value=true
  try{await http.delete(`/models/${encodeURIComponent(model)}`);ElMessage.success('空模型文件夹已删除')}catch(e:any){ElMessage.error(e.message)}finally{await refreshModelsPage()}
}
async function loadEmptyModelFolders(){try{const data=await http.get('/models/empty-folders');emptyModelFolders.value=Array.isArray(data)?data:[]}catch{emptyModelFolders.value=[]}}
async function refreshModelsPage(){await Promise.all([loadModels(),loadEmptyModelFolders()])}
async function openModels(){section.value='models';if(ossCredential.configured)await refreshModelsPage()}
function openModelUpload(model=''){if(!guardAdminAction())return;modelUploadTarget.value=model;modelUploadDialog.value=true}
function modelSelections(){return models.value.filter(item=>deployForm.model_keys.includes(item.manifest_key)).map(item=>({manifest_key:item.manifest_key,manifest_digest:item.manifest_digest}))}
function beginCenterRelease(title:string,stage:string){centerReleaseWorkflowID++;centerReleaseTitle.value=title;centerReleaseStage.value=stage;centerReleaseProgress.value=0;centerReleaseMessage.value='正在准备发布…';centerReleaseError.value='';centerReleaseTask.value=null;centerReleaseLogs.value=[];centerReleaseDialog.value=true;return centerReleaseWorkflowID}
function prepareCenterReleaseStage(stage:string,progress:number,message:string){centerReleaseTask.value=null;centerReleaseStage.value=stage;centerReleaseProgress.value=progress;centerReleaseMessage.value=message}
async function followCenterReleaseTask(task:Task,stage:string,start:number,end:number,workflowID:number){
  const prefix=[...centerReleaseLogs.value,...(centerReleaseLogs.value.length?['']:[]),`=== ${stage} ===`]
  centerReleaseTask.value={...task,params:{...(task.params||{})}};centerReleaseStage.value=stage;centerReleaseMessage.value=task.message||'任务已创建，等待执行…'
  while(workflowID===centerReleaseWorkflowID){
    const current:Task=await http.get(`/tasks/${task.id}`)
    let logs:string[]=[];try{const data=await http.get(`/tasks/${task.id}/logs`);logs=Array.isArray(data)?data:[]}catch{}
    if(workflowID!==centerReleaseWorkflowID)throw new Error('发布流程已停止')
    centerReleaseTask.value={...current,params:{...(current.params||{})}};centerReleaseLogs.value=[...prefix,...logs];centerReleaseMessage.value=current.message||stage;centerReleaseProgress.value=Math.round(start+(end-start)*(current.progress||0)/100)
    if(!isActiveTask(current))return current
    await new Promise(resolve=>window.setTimeout(resolve,1000))
  }
  throw new Error('发布流程已停止')
}
function requireReleaseSuccess(task:Task){if(task.status!=='SUCCESS')throw new Error(task.error||task.message||`任务状态异常：${task.status}`)}
// applyCenterVersionChanges 把中心详情页累计的软件与模型变更合成一个统一
// 发布任务（单中心）：软件经反向隧道按 digest 分发镜像并重建映射服务，
// 模型随后同步/校验；只选软件或只选模型时即对应纯软件/纯模型发布。
async function applyCenterVersionChanges(){if(!guardAdminAction())return;
  const centerID=activeCenterOverview.value?.center.id,softwareRows=changedCenterSoftwareRows.value,modelRows=changedCenterModelRows.value
  if(!centerID||(!softwareRows.length&&!modelRows.length))return
  if(centerVersionChangesBlocked.value){ElMessage.warning(centerModelSetPinned.value?'当前模型版本组已固定，请先整体解绑':'所选模型版本与中心平台不兼容');return}
  const items=softwareRows.map(row=>{const selected=selectedCenterSoftwareVersion(row)!;return {source_id:row.source!.id,ref:selected.ref,commit:selected.commit||''}})
  const modelSelections=modelRows.map(row=>{const target=selectedCenterModel(row)!;return {manifest_key:target.manifest_key,manifest_digest:target.manifest_digest}})
  const parts=[];if(items.length)parts.push(`${items.length} 个软件版本`);if(modelSelections.length)parts.push(`${modelSelections.length} 个模型版本`)
  const summary=parts.join('、')
  const detail=softwareRows.map(row=>`${row.source!.name}→${selectedCenterSoftwareVersion(row)!.version}`).join('、')
  try{
    await ElMessageBox.confirm(`将发布 ${summary}${detail?`（${detail}）`:''}，软件经反向隧道拉取镜像并重建对应服务${modelSelections.length&&!items.length?'，模型随后同步并仅重启使用模型的服务':''}，服务会短暂中断。是否继续？`,'确认发布版本',{type:'warning',confirmButtonText:'确认发布',cancelButtonText:'取消',closeOnClickModal:false})
    centerVersionApplying.value='publish'
    centerVersionApplyingCenter.value=centerID
    const workflowID=beginCenterRelease(`发布 ${summary}`,'统一发布预检')
    prepareCenterReleaseStage('统一发布预检',5,'正在检查中心 Compose、软件镜像、模型版本与硬件兼容性')
    const payload={center_ids:[centerID],items,model_selections:modelSelections}
    const plans:DeploymentPlan[]=await http.post('/releases/plan',payload)
    const plan=plans[0];if(!plan?.ready)throw new Error(plan?.error||plan?.missing_services?.join('；')||plan?.missing_images?.join('；')||'中心不满足发布条件')
    centerReleaseMessage.value=plan.build_required?'预检提示中心缺少镜像，将从镜像仓库分发':'预检通过，正在创建统一发布任务'
    const created:Task[]=await http.post('/releases',payload)
    if(!created.length)throw new Error('没有创建版本发布任务')
    deploymentTasks.value=created
    const released=await followCenterReleaseTask(created[0],`发布 ${summary}`,12,100,workflowID);requireReleaseSuccess(released)
    centerReleaseProgress.value=100;centerReleaseMessage.value=`${summary}已发布到该中心`;ElMessage.success(centerReleaseMessage.value)
    if(activeCenterOverview.value?.center.id===centerID)await Promise.all([refreshActiveCenterOverview(),loadCenterRegistryVersions()])
    await refreshTasks()
  }catch(e:any){if(e!=='cancel'&&e!=='close'){centerReleaseError.value=e.message||String(e);centerReleaseMessage.value=centerReleaseError.value;ElMessage.error(centerReleaseError.value)}}finally{centerVersionApplying.value='';centerVersionApplyingCenter.value=''}
}
async function openSourcePublish(source:Source){if(!guardAdminAction()||!source.id)return;sourcePublishDialog.source=source;sourcePublishDialog.selectedKey='';sourcePublishDialog.visible=true;sourcePublishDialog.loading=true;try{await loadGitVersions(source.id)}finally{sourcePublishDialog.loading=false}}
const sourceVersionsDialog=reactive<{visible:boolean;source:Source|null;loading:boolean;deleting:string}>({visible:false,source:null,loading:false,deleting:''})
// 镜像版本对话框自持一份仓库清单（打开时刷新，含 repository/版本/平台标签）
const sourceRegistryList=ref<RegistrySourceVersions[]>([])
const sourceRegistryEntry=computed(()=>{const source=sourceVersionsDialog.source;return sourceRegistryList.value.find(item=>item.source_id===source?.id)})
async function openSourceVersions(source:Source){if(!guardAdminAction()||!source.id)return;sourceVersionsDialog.source=source;sourceVersionsDialog.visible=true;await loadSourceRegistryVersions()}
async function loadSourceRegistryVersions(){sourceVersionsDialog.loading=true;try{const data=await http.get('/registry/versions');sourceRegistryList.value=Array.isArray(data)?data:[]}catch(e:any){sourceRegistryList.value=[];ElMessage.error(e.message)}finally{sourceVersionsDialog.loading=false}}
async function removeRegistryVersion(version:RegistrySoftwareVersion){
  const entry=sourceRegistryEntry.value
  if(!guardAdminAction()||!entry)return
  try{
    await ElMessageBox.confirm(`确定删除镜像版本 ${version.version}（${version.platforms.map(tag=>tag.tag).join('、')}）？各中心已运行的实例不受影响，但该版本将不能再发布；占用的存储由镜像仓库 GC 回收。`,'删除镜像版本',{type:'warning',confirmButtonText:'删除',cancelButtonText:'取消',closeOnClickModal:false})
    sourceVersionsDialog.deleting=version.version
    await http.delete(`/registry/versions?repository=${encodeURIComponent(entry.repository)}&version=${encodeURIComponent(version.version)}`)
    ElMessage.success(`镜像版本 ${version.version} 已删除`)
    await loadSourceRegistryVersions()
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}finally{sourceVersionsDialog.deleting=''}
}
async function submitSourcePublish(){
  const source=sourcePublishDialog.source
  const selected=(gitVersions[source?.id||'']||[]).find(item=>softwareOptionKey(item)===sourcePublishDialog.selectedKey)
  if(!source?.id||!selected)return
  try{
    await ElMessageBox.confirm(`将在管理端本地构建 ${source.name} 的${selected.kind==='tag'?'标签':'分支'} ${selected.ref}（${selected.commit.slice(0,8)}）全部平台镜像并发布到镜像仓库。存在 Dockerfile.nvidia / Dockerfile.denglin 时两个变体都构建成功才入库；只有通用 Dockerfile 时一次构建、各平台中心通用。是否继续？`,'确认构建发布',{type:'primary',confirmButtonText:'开始构建',cancelButtonText:'取消',closeOnClickModal:false})
    sourcePublishDialog.visible=false
    const task:Task=await http.post(`/sources/${source.id}/publish`,{ref:selected.ref,commit:selected.commit,kind:selected.kind})
    ElMessage.success('构建发布任务已创建')
    await openTask(task)
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}
}
const fmtImageTime=(ts:number)=>ts?new Date(ts*1000).toLocaleString():'-'
// 对中心只存在"镜像版本"概念：ref 与 commit 合并展示为镜像版本主体
const imageVersionLabel=(ref?:string,commit?:string)=>ref?(commit?`${ref}-${commit.slice(0,8)}`:ref):'-'
async function loadCenterSoftwareImages(){const centerID=activeCenterOverview.value?.center.id;if(!centerID)return;imageManageDialog.loading=true;try{const data=await http.get(`/centers/${centerID}/software/images`);imageManageDialog.images=Array.isArray(data)?data:[]}catch(e:any){imageManageDialog.images=[];ElMessage.error(e.message)}finally{imageManageDialog.loading=false}}
function openImageManage(){imageManageDialog.images=[];imageManageDialog.visible=true;loadCenterSoftwareImages()}
async function removeCenterSoftwareImage(img:SoftwareImage){
  const centerID=activeCenterOverview.value?.center.id
  if(!guardAdminAction()||!centerID||img.in_use)return
  try{
    await ElMessageBox.confirm(`确定删除镜像 ${img.image}（构建于 ${fmtImageTime(img.created_at)}）？删除后需要重新构建才能再次发布该版本。`,'删除镜像',{type:'warning',confirmButtonText:'删除',cancelButtonText:'取消',closeOnClickModal:false})
    imageManageDialog.deleting=img.id
    await http.delete(`/centers/${centerID}/software/images`,{id:img.id})
    ElMessage.success('镜像已删除')
    await loadCenterSoftwareImages()
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}finally{imageManageDialog.deleting=''}
}
async function toggleCenterModelPin(){if(!guardAdminAction())return;const centerID=activeCenterOverview.value?.center.id,pinned=centerModelSetPinned.value;if(!centerID)return;try{if(pinned)await ElMessageBox.confirm('解绑后才能新增、删除或切换任一模型版本，确定整体解绑？','解绑模型版本组',{type:'warning',confirmButtonText:'确认解绑',cancelButtonText:'取消'});else await ElMessageBox.confirm(`将固定当前 ${deployedCenterModels.value.length} 个模型组成的版本快照。固定后任何模型调整都必须先整体解绑，是否继续？`,'固定模型版本组',{type:'warning',confirmButtonText:'固定当前版本组',cancelButtonText:'取消'});centerPinBusy.value='model-set';await http.put(`/centers/${centerID}/models/pin`,{pinned:!pinned});ElMessage.success(pinned?'模型版本组已解绑':'当前模型版本组已固定');await refreshActiveCenterOverview()}catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}finally{centerPinBusy.value=''}}
function invalidateDeploymentPlan(){deploymentPlan.value=[]}
// 统一发布只认镜像仓库已发布的版本：源码不再发送到中心构建。
const registryAllVersions=ref<RegistrySourceVersions[]>([]),registryVersionsError=ref('')
const registryVersionsFor=(sourceID:string)=>registryAllVersions.value.find(item=>item.source_id===sourceID)?.versions||[]
const deployVersionKey=(item:{ref:string;commit:string})=>`${item.ref}:${item.commit}`
const selectedDeployVersion=(item:DeployItem)=>registryVersionsFor(item.source_id).find(version=>version.ref===item.ref&&version.commit===item.commit)
function selectDeployVersion(item:DeployItem,key:string){const version=registryVersionsFor(item.source_id).find(candidate=>deployVersionKey(candidate)===key);if(version)Object.assign(item,{ref:version.ref,commit:version.commit});else Object.assign(item,{ref:'',commit:''});invalidateDeploymentPlan()}
// 默认选择各软件最新发布的版本（列表按创建时间倒序）
function syncDeployItems(selected:string[],preferredSourceID?:string,preferredVersion?:{ref:string;commit:string}){const existing=new Map(deployForm.items.map(item=>[item.source_id,item]));deployForm.items=selected.map(id=>{const available=registryVersionsFor(id),old=existing.get(id),current=old&&available.find(version=>version.ref===old.ref&&version.commit===old.commit);const preferred=id===preferredSourceID&&preferredVersion?available.find(version=>version.ref===preferredVersion.ref&&version.commit===preferredVersion.commit):undefined;const version=preferred||current||available[0];return version?{source_id:id,ref:version.ref,commit:version.commit}:{source_id:id,ref:'',commit:''}});invalidateDeploymentPlan()}
async function loadDeploymentVersions(){deploymentVersionsLoading.value=true;try{const data=await http.get('/registry/versions');registryAllVersions.value=Array.isArray(data)?data:[];registryVersionsError.value='';syncDeployItems(deployForm.source_ids)}catch(e:any){registryAllVersions.value=[];registryVersionsError.value=e.message||'读取镜像仓库版本失败'}finally{deploymentVersionsLoading.value=false}}
async function openDeploy(source?:Source,preferredVersion?:{ref:string;commit:string}){section.value='releases';deployForm.center_ids=[];deployForm.source_ids=source?.id?[source.id]:[];deployForm.items=[];deployForm.model_keys=[];deploymentPlan.value=[];deploymentTasks.value=[];await Promise.all([sources.value.length?loadDeploymentVersions():Promise.resolve(),ossCredential.configured?loadModels():Promise.resolve()]);syncDeployItems(deployForm.source_ids,source?.id,preferredVersion)}
function deploySourcesChanged(value:string[]){syncDeployItems(value)}
function deploymentIssue(item:DeploymentPlan){return item.error||'未满足发布条件'}
function modelPlanSummary(item:DeploymentPlan){const plan=item.model_plan;if(!plan)return '-';if(!plan.ready)return plan.error||'模型检查失败';const count=plan.targets?.length||Object.keys(plan.current||{}).length;if(plan.action==='keep_current')return `沿用中心当前 ${count} 个健康模型`;if(plan.action==='skip')return `${count} 个模型已是目标版本`;return `更新 ${count} 个模型，下载 ${formatBytes(plan.download_bytes||0)}`}
async function planDeployment(){if(!guardAdminAction())return;if(!deployForm.items.length&&!deployForm.model_keys.length){ElMessage.warning('请至少选择一个软件版本或模型版本');return};if(deployForm.items.some(item=>!item.ref||!item.commit)){ElMessage.warning('每个软件都必须选择一个已发布的镜像版本');return};if(!deployForm.center_ids.length){ElMessage.warning('请至少选择一个发布中心');return};deploymentPlanning.value=true;try{const result:DeploymentPlan[]=await http.post('/releases/plan',{center_ids:deployForm.center_ids,items:deployForm.items,model_selections:modelSelections()});deploymentPlan.value=Array.isArray(result)?result:[];const ready=deploymentPlan.value.filter(item=>item.ready).length;const distributes=deploymentPlan.value.filter(item=>item.ready&&item.build_required).length;if(ready)ElMessage.success(`发布计划已生成：${ready} 个中心可发布，其中 ${distributes} 个将从镜像仓库分发镜像`);else ElMessage.warning('当前没有满足发布条件的中心，请查看下方原因')}catch(e:any){deploymentPlan.value=[];ElMessage.error(e.message)}finally{deploymentPlanning.value=false}}
async function refreshDeploymentTasks(){window.clearTimeout(deploymentPollTimer);if(!deploymentTasks.value.length)return;try{deploymentTasks.value=await Promise.all(deploymentTasks.value.map(task=>http.get(`/tasks/${task.id}`)));for(const task of deploymentTasks.value){if(task.status==='FAILED'&&!deploymentFailuresNotified.has(task.id)){deploymentFailuresNotified.add(task.id);ElMessage.error(`中心 ${task.params?.center_name||task.params?.center_id} 部署失败：${task.error||task.message||'请查看任务日志'}`)}}if(deploymentTasks.value.some(task=>['PENDING','RUNNING'].includes(task.status)))deploymentPollTimer=window.setTimeout(refreshDeploymentTasks,2000)}catch(e:any){ElMessage.error(e.message)}}
async function startDeployment(){if(!guardAdminAction())return;if(!deploymentPlan.value.length){ElMessage.warning('请先生成发布计划');return};if(!deploymentPlan.value.some(item=>item.ready)){ElMessage.warning('当前没有通过巡检的发布中心');return};try{deploymentFailuresNotified.clear();deploymentTasks.value=await http.post('/releases',{center_ids:deployForm.center_ids,items:deployForm.items,model_selections:modelSelections()});const skipped=Math.max(0,deployForm.center_ids.length-deploymentTasks.value.length);if(skipped)ElMessage.warning(`已创建 ${deploymentTasks.value.length} 个发布任务，${skipped} 个中心未通过最新复检并已跳过`);else ElMessage.success(`已创建 ${deploymentTasks.value.length} 个中心版本发布任务`);await refreshDeploymentTasks();await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function openTask(v:Task){sourceEvents.value?.close();activeTask.value=v;logDrawer.value=true;try{taskLogs.value=await http.get(`/tasks/${v.id}/logs`)}catch{taskLogs.value=[]};if(['RUNNING','PENDING'].includes(v.status)){const es=new EventSource(eventURL(v.id));sourceEvents.value=es;es.addEventListener('log',(ev:any)=>{try{taskLogs.value.push(JSON.parse(ev.data).message)}catch{}});es.addEventListener('progress',(ev:any)=>{try{Object.assign(activeTask.value!,JSON.parse(ev.data))}catch{}});es.addEventListener('status',async(ev:any)=>{try{Object.assign(activeTask.value!,JSON.parse(ev.data))}catch{};const status=JSON.parse(ev.data).status;if(['SUCCESS','FAILED','CANCELLED'].includes(status)){es.close();await loadAll();if(['CENTER_OPERATION','MODEL_RELEASE','RELEASE_VERSION','BUILD_IMAGE','PUBLISH_IMAGE','RELEASE_SOFTWARE','COMPOSE_PUBLISH','MODULE_OPERATION','RESET_CENTER','PAUSE_CENTER','RESUME_CENTER','INITIALIZE_CENTER'].includes(v.type)&&activeCenterOverview.value?.center.id===v.params?.center_id)await refreshActiveCenterOverview()}});es.onerror=()=>es.close()}}
function closeLogs(){sourceEvents.value?.close();sourceEvents.value=null}
onMounted(async()=>{window.addEventListener('manager-auth-required',()=>{authState.authenticated=false;authState.role='';passwordDialog.value=false;resetPasswordForm()});await checkAuth();if(authState.authenticated&&!authState.must_change_password)await loadAll();taskPollTimer=window.setTimeout(pollTasks,2000)})
onUnmounted(()=>{centerReleaseWorkflowID++;window.clearTimeout(deploymentPollTimer);window.clearTimeout(baseImagePollTimer);window.clearTimeout(taskPollTimer);closeLogs();closeCenterOperationStream()})
</script>

<template>
  <div v-if="authLoading" class="auth-loading"><div class="logo">CV</div><span>正在加载中心数字化管理平台…</span></div>
  <div v-else-if="!authState.authenticated" class="login-page">
    <div class="login-card">
      <div class="login-brand"><div class="logo">CV</div><h1>中心数字化管理平台</h1></div>
      <el-form :model="loginForm" @submit.prevent="login">
        <el-form-item><el-input v-model="loginForm.username" size="large" placeholder="用户名" :prefix-icon="User" autocomplete="username"/></el-form-item>
        <el-form-item><el-input v-model="loginForm.password" size="large" type="password" show-password placeholder="密码" :prefix-icon="Lock" autocomplete="current-password" @keyup.enter="login"/></el-form-item>
        <el-button type="primary" size="large" native-type="submit" class="login-submit" :loading="authLoading" @click="login">登录</el-button>
      </el-form>
    </div>
  </div>
  <div v-else-if="authState.must_change_password" class="login-page">
    <div class="login-card password-card">
      <div class="login-brand"><div class="logo">CV</div><h1>设置新密码</h1><p>首次登录建议修改管理员密码</p></div>
      <el-alert title="为了中心服务器凭据安全，建议立即修改；也可以像 Grafana 一样暂时跳过。" type="warning" :closable="false"/>
      <el-form :model="passwordForm" :disabled="passwordSaving" @submit.prevent="changePassword" class="password-form">
        <el-form-item><el-input v-model="passwordForm.old_password" size="large" type="password" show-password placeholder="当前密码" :prefix-icon="Lock"/></el-form-item>
        <el-form-item><el-input v-model="passwordForm.new_password" size="large" type="password" show-password placeholder="新密码（至少 4 位）" :prefix-icon="Lock"/></el-form-item>
        <el-form-item><el-input v-model="passwordForm.confirm_password" size="large" type="password" show-password placeholder="再次输入新密码" :prefix-icon="Lock"/></el-form-item>
        <el-button type="primary" size="large" native-type="submit" class="login-submit" :loading="passwordSaving">保存密码并进入</el-button>
        <el-button size="large" class="login-submit skip-button" :loading="authLoading" :disabled="passwordSaving" @click="skipPasswordChange">暂时跳过</el-button>
      </el-form>
    </div>
  </div>
  <el-container v-else class="shell">
    <el-aside width="280px" class="aside">
      <div class="brand"><div class="logo">CV</div><div><strong>中心数字化管理平台</strong><small>Digital Management</small></div></div>
      <el-menu v-model:default-active="section" @select="selectSection" class="menu">
        <el-menu-item index="overview"><el-icon><Monitor/></el-icon>总览</el-menu-item>
        <el-menu-item index="centers"><el-icon><Connection/></el-icon>中心服务器</el-menu-item>
        <el-menu-item index="monitoring"><el-icon><DataLine/></el-icon>统一监控</el-menu-item>
        <el-menu-item index="terminal"><el-icon><Operation/></el-icon>SSH 终端</el-menu-item>
        <el-menu-item index="base-images"><el-icon><UploadFilled/></el-icon>基础镜像</el-menu-item>
        <el-menu-item index="models"><el-icon><Download/></el-icon>模型仓库</el-menu-item>
        <el-menu-item index="sources"><el-icon><Files/></el-icon>软件仓库</el-menu-item>
        <el-menu-item index="releases"><el-icon><Promotion/></el-icon>版本发布</el-menu-item>
        <el-menu-item index="tasks"><el-icon><VideoPlay/></el-icon>任务与日志</el-menu-item>
        <el-menu-item v-if="isAdmin" index="users"><el-icon><User/></el-icon>用户管理</el-menu-item>
      </el-menu>
      <div class="account"><span><el-icon><User/></el-icon>{{authState.username}}<el-tag size="small" :type="isAdmin?'primary':'info'" effect="plain">{{isAdmin?'管理员':'查看用户'}}</el-tag></span><button title="修改密码" aria-label="修改密码" @click="openPasswordDialog"><el-icon><Lock/></el-icon><span class="account-label">修改密码</span></button><button title="退出" aria-label="退出" @click="logout"><el-icon><SwitchButton/></el-icon>退出</button></div>
    </el-aside>
    <el-main class="main" :class="{'terminal-main':section==='terminal'}" v-loading="loading">
      <header v-if="section!=='terminal'"><div><h1>{{section==='overview'?'运行总览':section==='centers'?'中心服务器':section==='terminal'?'SSH 终端':section==='base-images'?'基础镜像':section==='models'?'模型仓库':section==='sources'?'软件仓库':section==='releases'?'版本发布':section==='users'?'用户管理':'任务与日志'}}</h1><p v-if="['terminal','releases','base-images','models','sources','users'].includes(section)">{{section==='terminal'?'连接已配置的中心服务器并进行交互式运维':section==='releases'?'从镜像仓库选择软件镜像版本，与模型版本一起发布到各中心':section==='base-images'?'从 OSS 按中心显卡类型下载并更新基础镜像':section==='models'?'上传和发布按硬件格式组织的模型版本':section==='sources'?'管理软件源码地址，构建并发布镜像版本到镜像仓库':section==='users'?'管理登录账号与角色；普通用户仅可查看各中心状态':''}}</p></div><el-button :icon="Refresh" circle @click="refreshCurrent"/></header>

      <template v-if="section==='overview'">
        <div class="overview-dashboard" v-loading="overviewLoading">
          <div class="metrics overview-metrics"><div class="metric accent"><span>集群在线率</span><b>{{onlineCount}} / {{overview.length}}</b><small>{{offlineCount?`${offlineCount} 个中心连接异常`:'全部中心连接正常'}}</small></div><div class="metric"><span>初始化就绪</span><b>{{initializedCount}} / {{centers.length}}</b><small>硬件检测与 Compose 已准备</small></div><div class="metric"><span>执行中任务</span><b>{{activeTaskCount}}</b><small>包含等待与运行中的任务</small></div><div class="metric"><span>软件仓库</span><b>{{sources.length}}</b><small>已纳入版本管理</small></div></div>
          <div class="overview-grid">
            <div class="panel health-panel">
              <div class="panel-title"><div><h2>集群健康</h2><p>聚合展示可用性和资源准备情况。</p></div><el-button type="primary" link @click="selectSection('centers')">管理中心服务器</el-button></div>
              <div class="health-row"><div><strong>连接可用性</strong><small>SSH 与 Docker Engine</small></div><el-progress :percentage="onlineRate" :status="offlineCount?'exception':'success'"/></div>
              <div class="health-row"><div><strong>初始化完成度</strong><small>基础模块、业务就绪与监控启动</small></div><el-progress :percentage="initializedRate" :status="initializedRate===100?'success':undefined"/></div>
              <div class="resource-summary"><span><small>逻辑 CPU</small><b>{{centers.reduce((sum,c)=>sum+(c.cpu_count||0),0)}}</b></span><span><small>物理 GPU</small><b>{{centers.reduce((sum,c)=>sum+(c.gpu_count||0),0)}}</b></span><div><small>计算平台</small><div class="platform-tags"><el-tag v-for="item in acceleratorStats" :key="item.type" size="small" effect="plain">{{acceleratorName(item.type)}} × {{item.count}}</el-tag><span v-if="!acceleratorStats.length" class="muted">暂无数据</span></div></div></div>
            </div>
            <div class="panel recent-task-panel">
              <div class="panel-title"><div><h2>最近任务</h2><p>只保留运行动态摘要，点击任务查看日志。</p></div><el-button type="primary" link @click="selectSection('tasks')">查看全部任务</el-button></div>
              <el-table :data="recentTasks" @row-click="openTask" empty-text="暂无任务" size="small"><el-table-column label="类型" width="130"><template #default="{row}">{{taskTypeName(row.type)}}</template></el-table-column><el-table-column label="目标" min-width="190"><template #default="{row}"><el-tooltip :content="taskTarget(row)" placement="top" :show-after="400"><span class="task-target">{{taskTargetSummary(row)}}</span></el-tooltip></template></el-table-column><el-table-column label="状态" width="100"><template #default="{row}"><el-tag :type="statusType(row.status)" size="small">{{row.status}}</el-tag></template></el-table-column><el-table-column label="时间" width="170"><template #default="{row}">{{fmt(row.created_at)}}</template></el-table-column></el-table>
            </div>
          </div>
        </div>
      </template>

      <template v-else-if="section==='centers'">
        <div class="toolbar"><div><p>SSH 凭据加密保存在开发机；所有中心共用一套飞书告警 Webhook。</p><el-tag :type="alertCredential.configured?'success':'warning'">{{alertCredential.configured?'飞书告警已配置':'尚未配置飞书告警'}}</el-tag></div><div><el-button :icon="Lock" @click="editAlertCredential">配置飞书告警</el-button><el-button type="primary" :icon="Plus" @click="editCenter()">添加中心</el-button></div></div>
        <div class="cards"><div class="server-card" v-for="c in centers" :key="c.id"><div class="server-head"><div class="server-icon"><Connection/></div><div><h3>{{c.name}}</h3><p>{{c.username}}@{{c.host}}:{{c.port}}</p></div><el-tag size="small" :type="c.deployment?(c.deployment.status==='ready'?'success':c.deployment.status==='paused'?'warning':c.deployment.status==='waiting_business'?'warning':c.deployment.status==='failed'?'danger':'info'):(c.initialized_at?'success':'warning')">{{c.deployment?(c.deployment.status==='ready'?'已就绪':c.deployment.status==='paused'?'已暂停':c.deployment.status==='waiting_business'?'等待业务发布':c.deployment.status==='failed'?'初始化失败':'待初始化'):(c.initialized_at?'已初始化':'待初始化')}}</el-tag></div><p class="desc">{{c.description||'暂无说明'}}</p><div class="hardware-strip"><span><small>检测平台</small><b>{{acceleratorName(c.detected_accelerator||c.accelerator)}}</b></span><span><small>逻辑 CPU</small><b>{{c.cpu_count||'-'}}</b></span><span><small>物理 GPU</small><b>{{c.gpu_count||'-'}}</b></span></div><div class="detail"><span>最近初始化</span><code>{{fmt(c.initialized_at)}}</code></div><div class="detail"><span>访问端口</span><code>{{c.console_port||8080}}</code></div><div class="detail"><span>错分检测</span><code>{{!c.wms_url?'未启用':c.mis_sort_enabled===false?'已配置·未启用':'已启用'}}</code></div><div v-if="c.test_mode" class="detail"><span>告警控制</span><code>飞书通知静默</code></div><div class="actions center-actions"><el-button type="primary" @click="openCenterFromCard(c)">运行详情</el-button><el-button plain :loading="hardwareDetectingId===c.id" :disabled="!!hardwareDetectingId&&hardwareDetectingId!==c.id" @click="detectCenter(c)">检测硬件</el-button><el-button v-if="!c.initialized_at" type="primary" plain :loading="!!initializationTask(c)" :disabled="!!initializationTask(c)" @click="initializeCenter(c)">初始化部署</el-button><el-button @click="editCenter(c)">编辑连接</el-button><el-button type="danger" plain @click="removeCenter(c)">删除</el-button></div></div><el-empty v-if="!centers.length" description="尚未添加中心服务器"/></div>
      </template>

      <template v-else-if="section==='monitoring'">
        <div class="toolbar"><div><p>开发机统一监控：每个中心一条健康信号与失联告警，指标和日志仍保存在中心本地，Grafana 经中心网关（SSH 主机 + 访问端口，与中心 Grafana 入口同址）直接查询。完成硬件检测的中心自动纳入；开启告警控制的中心不探测。</p><el-tag :type="monitoringStatus?.stack_online?'success':'warning'">{{monitoringStatus?.stack_online?'监控栈运行中':'监控栈未启动或不可达'}}</el-tag></div><div><el-button :icon="Refresh" :loading="monitoringLoading" @click="loadMonitoringStatus">刷新状态</el-button><el-button type="primary" @click="openGrafanaDashboard()">打开 Grafana</el-button></div></div>
        <el-alert v-if="monitoringStatus?.error&&!monitoringStatus.stack_online" :title="monitoringStatus.error" type="warning" :closable="false" show-icon style="margin-bottom:16px"/>
        <div class="cards" v-loading="monitoringLoading">
          <div class="server-card" v-for="item in monitoringStatus?.centers||[]" :key="item.center_id">
            <div class="server-head"><div class="server-icon"><DataLine/></div><div><h3>{{item.name}}</h3><p>{{item.target||item.reason}}</p></div><el-tag size="small" :type="monitorStateType(item)">{{monitorStateText(item)}}</el-tag></div>
            <div class="detail"><span>拉取地址</span><code>{{item.target||'-'}}</code></div>
            <div class="detail"><span>失联告警</span><code>{{!item.monitored?'-':item.test_mode?'静默（告警控制）':'开启（连续 3 分钟失联通知飞书）'}}</code></div>
            <div v-if="!item.monitored&&item.reason" class="detail"><span>未纳入原因</span><code>{{item.reason}}</code></div>
            <div class="actions center-actions"><el-button type="primary" :disabled="!item.monitored" @click="openGrafanaDashboard(item.site)">查看仪表盘</el-button></div>
          </div>
          <el-empty v-if="monitoringStatus&&!monitoringStatus.centers.length&&!monitoringLoading" description="尚未登记中心服务器"/>
        </div>
      </template>

      <template v-else-if="section==='terminal'">
        <TerminalPage :centers="centers"/>
      </template>

      <template v-else-if="section==='base-images'">
        <el-alert class="compat-alert" title="安全升级：下载并 docker load 新镜像不会影响正在运行的旧容器；勾选立即应用时，只重建实际使用变化镜像的运行服务。" type="success" :closable="false" show-icon/>
        <div class="toolbar"><div><p>公共目录镜像会下发到所有中心；nvidia/ 与 denglin/ 目录只下发给对应显卡中心。</p><el-tag :type="ossCredential.configured?'success':'warning'">{{ossCredential.configured?(ossCredential.bucket?`${ossCredential.bucket}/${ossCredential.images_prefix}`:'已配置 OSS'):'尚未配置 OSS'}}</el-tag></div><el-button :icon="Lock" @click="editOSSCredential">配置 OSS</el-button></div>
        <el-alert v-if="!ossCredential.configured" title="请先配置 OSS Endpoint、Bucket 和访问凭据。" type="warning" :closable="false" show-icon/>
        <template v-else><div class="panel base-image-catalog" v-loading="baseImagesLoading"><div class="panel-title"><div><h2>OSS 基础镜像清单</h2><p>对象 ETag 变化时，即使文件名不变也会重新下载并加载。</p></div><el-button :icon="Refresh" @click="loadBaseImages">刷新清单</el-button></div><el-table :data="baseImages" empty-text="OSS 前缀下没有镜像 tar 包"><el-table-column prop="name" label="镜像包" min-width="300"><template #default="{row}"><code>{{row.name}}</code></template></el-table-column><el-table-column label="适用平台" width="130"><template #default="{row}"><el-tag :type="row.platform==='common'?'info':row.platform==='nvidia'?'success':'warning'">{{row.platform==='common'?'公共':acceleratorName(row.platform)}}</el-tag></template></el-table-column><el-table-column label="大小" width="130"><template #default="{row}">{{formatBytes(row.size)}}</template></el-table-column><el-table-column label="OSS 更新时间" width="190"><template #default="{row}">{{fmt(row.last_modified)}}</template></el-table-column><el-table-column label="ETag" min-width="180"><template #default="{row}"><code>{{row.etag}}</code></template></el-table-column></el-table></div><div class="panel base-image-targets"><div class="panel-title"><div><h2>批量更新中心</h2><p>每个中心会自动检测显卡，只接收公共镜像和自身平台镜像。</p></div></div><el-transfer v-model="baseImageForm.center_ids" class="center-transfer" :data="centerTransferData" :titles="['所有中心','更新目标']" :button-texts="['移除','选择']" filterable/><div class="base-image-options"><el-checkbox v-model="baseImageForm.restart_stack">加载完成后应用变化镜像的使用服务</el-checkbox><span v-if="!baseImageForm.restart_stack">推荐：仅加载镜像，现有容器保持运行</span><span v-else class="danger-text">仅对镜像已变化的运行服务执行重建，相关服务会短暂中断</span><el-button type="primary" size="large" :disabled="!baseImageForm.center_ids.length||!baseImages.length" @click="upgradeBaseImages">开始批量更新</el-button></div></div><div v-if="baseImageTasks.length" class="panel deployment-results"><div class="panel-title"><div><h2>本次基础镜像更新</h2><p>各中心独立执行，点击行查看实时下载和加载日志。</p></div><el-button :icon="Refresh" @click="refreshBaseImageTasks">刷新状态</el-button></div><el-table :data="baseImageTasks" @row-click="openTask"><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.params?.center_name}}</strong></template></el-table-column><el-table-column label="模式" width="160"><template #default="{row}">{{row.params?.restart_stack==='true'?'加载并重建':'仅加载镜像'}}</template></el-table-column><el-table-column label="进度" width="200"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)" :status="row.status==='FAILED'?'exception':row.status==='SUCCESS'?'success':undefined"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)" effect="dark">{{row.status}}</el-tag></template></el-table-column><el-table-column label="结果 / 错误" min-width="280"><template #default="{row}"><span :class="{'deploy-error':row.status==='FAILED'}">{{row.error||row.message||'等待执行'}}</span></template></el-table-column></el-table></div></template>
      </template>

      <template v-else-if="section==='models'">
        <el-alert class="compat-alert" title="模型仓库只负责上传和管理版本；需要部署或更新模型时，请前往版本发布。" type="info" :closable="false" show-icon/>
        <div class="toolbar">
          <div><p>模型与基础镜像共用 OSS Endpoint、Bucket 和加密访问凭据。</p><el-tag :type="ossCredential.configured?'success':'warning'">{{ossCredential.configured?(ossCredential.bucket?`${ossCredential.bucket}/${ossCredential.models_prefix}`:'已配置 OSS'):'尚未配置 OSS'}}</el-tag></div>
          <div><el-button :icon="Lock" @click="editOSSCredential">配置 OSS</el-button><el-button type="primary" :icon="UploadFilled" :disabled="!ossCredential.configured" @click="openModelUpload()">新建模型</el-button></div>
        </div>
        <el-alert v-if="!ossCredential.configured" title="请先配置 OSS Endpoint、Bucket、访问凭据和模型前缀。" type="warning" :closable="false" show-icon/>
        <template v-else><ModelCatalog :models="models" :loading="modelsLoading" :selectable="false" :empty-folders="emptyModelFolders" @delete-model="deleteModelFolder" @refresh="refreshModelsPage" @upload-version="openModelUpload"/></template>
      </template>
      <template v-else-if="section==='sources'">
        <div class="toolbar"><div><p>所有同一 Git 服务下的软件仓库共用一套加密访问令牌。</p><el-tag :type="gitCredential.configured?'success':'warning'" effect="plain">{{gitCredential.configured?(gitCredential.base_url?`已配置：${gitCredential.base_url}`:'已配置访问令牌'):'尚未配置访问令牌'}}</el-tag>&nbsp;<el-tag :type="registrySettings.configured?'success':'info'" effect="plain">{{registrySettings.configured?(registrySettings.endpoint||'镜像仓库已配置'):'镜像仓库未启用'}}</el-tag></div><div><el-button :icon="Lock" @click="editRegistry">配置镜像仓库</el-button><el-button :icon="Lock" @click="editGitCredential">配置 Git 令牌</el-button><el-button type="primary" :icon="Plus" @click="editSource()">添加软件仓库</el-button></div></div>
        <div class="panel"><el-table :data="sources" empty-text="暂无软件仓库"><el-table-column prop="name" label="软件" width="180"/><el-table-column prop="repository" label="Git 仓库" min-width="240"><template #default="{row}"><code>{{row.repository}}</code></template></el-table-column><el-table-column prop="description" label="说明"/><el-table-column v-if="isAdmin" label="操作" width="300"><template #default="{row}"><el-button @click="editSource(row)">编辑</el-button><el-button type="primary" link @click="openSourcePublish(row)">构建发布</el-button><el-button link @click="openSourceVersions(row)">镜像版本</el-button><el-button type="danger" link @click="removeSource(row)">删除</el-button></template></el-table-column><el-table-column v-else label="操作" width="80"><template #default="{row}"><el-button @click="editSource(row)">编辑</el-button></template></el-table-column></el-table></div>
      </template>

      <template v-else-if="section==='releases'">
        <el-alert class="compat-alert" title="软件和模型统一发布：至少选择一种。只选择模型时执行纯模型更新；只选择软件时会校验中心已有模型是否健康。" type="success" :closable="false" show-icon/>
        <div class="panel build-page" v-loading="deploymentVersionsLoading" element-loading-text="正在读取镜像仓库版本…">
          <el-alert v-if="!sources.length" title="尚未添加软件仓库，仍可只选择模型执行纯模型更新。" type="info" :closable="false" show-icon/>
          <el-alert v-else-if="registryVersionsError" :title="registryVersionsError" type="error" :closable="false" show-icon/>
          <el-form :model="deployForm" label-position="top" class="build-form">
            <div class="build-step"><div class="step-number">1</div><div class="step-content"><h3>选择需要发布的软件（可选）</h3><p class="table-subtitle">版本来自镜像仓库；新版本先在「软件仓库」页构建发布。不选择软件、只选择模型时，本次发布就是纯模型更新。</p><el-checkbox-group v-model="deployForm.source_ids" class="software-check-list" @change="deploySourcesChanged"><div v-for="software in sources" :key="software.id" class="software-check-row" :class="{disabled:!registryVersionsFor(software.id!).length}"><el-checkbox :value="software.id!" :disabled="!registryVersionsFor(software.id!).length"><span class="software-name">{{software.name}}</span></el-checkbox><code>{{software.repository}}</code><el-tag size="small" :type="registryVersionsFor(software.id!).length?'success':'warning'">{{registryVersionsFor(software.id!).length}} 个已发布版本</el-tag></div></el-checkbox-group></div></div>
            <div class="build-step"><div class="step-number">2</div><div class="step-content"><div class="step-title-bar"><h3>选择镜像版本</h3><el-button :icon="Refresh" :disabled="deploymentVersionsLoading" @click="loadDeploymentVersions">刷新</el-button></div><el-table :data="deployForm.items" empty-text="请先从上方列表勾选软件" border><el-table-column label="软件" width="220"><template #default="{row}"><strong>{{sources.find(v=>v.id===row.source_id)?.name}}</strong></template></el-table-column><el-table-column label="镜像仓库版本" min-width="420"><template #default="{row}"><el-select :model-value="deployVersionKey(row)" filterable style="width:100%" placeholder="选择已发布的镜像版本" @change="selectDeployVersion(row,$event)"><el-option v-for="version in registryVersionsFor(row.source_id)" :key="deployVersionKey(version)" :label="registryVersionLabel(version)" :value="deployVersionKey(version)"/></el-select></template></el-table-column></el-table><p class="table-subtitle">中心缺少所选镜像时经反向隧道从镜像仓库拉取（镜像层从 OSS 直下），不再向中心发送源码构建。</p></div></div>
            <div class="build-step"><div class="step-number">3</div><div class="step-content"><h3>选择模型版本（可选）</h3><el-alert v-if="!ossCredential.configured" title="OSS 尚未配置，不能选择新模型；发布软件时将校验并沿用中心当前健康模型。" type="warning" :closable="false"/><ModelCatalog v-else v-model="deployForm.model_keys" :models="models" :loading="modelsLoading" @update:model-value="invalidateDeploymentPlan" @refresh="loadModels" @upload-version="openModelUpload"/><p class="table-subtitle">选择模型后会按中心硬件自动匹配 ONNX、NVIDIA plan 或登临 engine，并在发布结束时重启 Compose。</p></div></div>
            <div class="build-step"><div class="step-number">4</div><div class="step-content"><h3>选择发布中心</h3><el-transfer v-model="deployForm.center_ids" class="center-transfer" :data="centerTransferData" :titles="['所有中心','发布目标']" :button-texts="['移除','选择']" filterable filter-placeholder="搜索中心" @change="invalidateDeploymentPlan"/><div class="deploy-plan-action"><el-button type="primary" plain :loading="deploymentPlanning" :disabled="(!deployForm.items.length&&!deployForm.model_keys.length)||!deployForm.center_ids.length" @click="planDeployment">生成发布计划</el-button><span>只读检查软件镜像、Compose 映射、模型完整性和硬件兼容性。</span></div></div></div>
            <div v-if="deploymentPlan.length" class="build-step"><div class="step-number">5</div><div class="step-content"><h3>确认发布计划</h3><el-table :data="deploymentPlan" border><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.center.name}}</strong><small class="table-subtitle">{{row.center.host}}:{{row.center.port}}</small></template></el-table-column><el-table-column label="执行方式" width="150"><template #default="{row}"><el-tag v-if="!row.ready" type="info">本次跳过</el-tag><el-tag v-else-if="!deployForm.items.length" type="warning">纯模型更新</el-tag><el-tag v-else-if="deployForm.model_keys.length" type="primary">软件 + 模型</el-tag><el-tag v-else type="success">仅软件</el-tag></template></el-table-column><el-table-column label="目标模块 / 服务" min-width="220"><template #default="{row}">{{Object.values(row.service_mappings||{}).flat().join('、')||'模型使用服务'}}</template></el-table-column><el-table-column label="软件检查" min-width="240"><template #default="{row}"><span v-if="!deployForm.items.length">未选择软件</span><span v-else-if="row.build_required">从镜像仓库分发：{{row.missing_images.join('、')}}</span><span v-else>所选版本的镜像均已就绪</span></template></el-table-column><el-table-column label="模型检查" min-width="260"><template #default="{row}"><span :class="{'deploy-error':!row.model_plan?.ready}">{{modelPlanSummary(row)}}</span></template></el-table-column><el-table-column label="结果" min-width="280"><template #default="{row}"><span v-if="!row.ready" class="deploy-error">{{deploymentIssue(row)}}</span><span v-else>可以发布</span></template></el-table-column></el-table><div class="build-options"><span>通过复检的中心会分别创建独立任务，不通过的中心自动跳过。</span><el-tag type="primary">{{deploymentPlan.length-unavailableDeploymentCenters.length}} 个可发布 / {{unavailableDeploymentCenters.length}} 个跳过</el-tag></div></div></div>
            <div class="build-submit"><el-button type="primary" size="large" :disabled="!deploymentPlan.some(item=>item.ready)" @click="startDeployment">开始版本发布</el-button></div>
          </el-form>
        </div>
        <div v-if="deploymentTasks.length" class="panel deployment-results"><div class="panel-title"><div><h2>本次版本发布</h2><p>点击中心查看构建与部署各阶段日志。</p></div><el-button :icon="Refresh" @click="refreshDeploymentTasks">刷新状态</el-button></div><el-table :data="deploymentTasks" @row-click="openTask"><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.params?.center_name||row.params?.center_id}}</strong></template></el-table-column><el-table-column label="发布版本" min-width="300"><template #default="{row}">{{row.params?.versions}}</template></el-table-column><el-table-column label="进度" width="180"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)" :status="row.status==='FAILED'?'exception':row.status==='SUCCESS'?'success':undefined"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)" effect="dark">{{row.status}}</el-tag></template></el-table-column><el-table-column label="当前阶段 / 失败原因" min-width="320"><template #default="{row}"><span :class="{'deploy-error':row.status==='FAILED'}">{{row.error||row.message||'等待执行'}}</span></template></el-table-column></el-table></div>
      </template>

      <template v-else-if="section==='users'">
        <UserManagement :current-username="authState.username" :super-admin="isSuperAdmin" @role-changed="checkAuth"/>
      </template>

      <template v-else>
        <div class="toolbar task-toolbar"><p>已选 {{selectedTasks.length}} 项：{{selectedActiveTasks.length}} 个可停止，{{selectedFinishedTasks.length}} 条记录可删除。</p><div><el-button type="warning" :disabled="!selectedActiveTasks.length||taskActionBusy" @click="stopTasks(selectedActiveTasks)">停止选中任务</el-button><el-button type="danger" :disabled="taskActionBusy" @click="stopTasks([],true)">停止所有任务</el-button><el-button :disabled="!isSuperAdmin||!selectedFinishedTasks.length||taskActionBusy" @click="deleteTasks(selectedFinishedTasks)">删除记录</el-button></div></div><div class="panel"><el-table :data="tasks" row-key="id" @row-click="(row:Task,column:any)=>{if(column?.type!=='selection'&&column?.label!=='操作')openTask(row)}" @selection-change="(rows:Task[])=>selectedTasks=rows" empty-text="暂无任务"><el-table-column type="selection" width="48" :reserve-selection="true"/><el-table-column label="类型" width="190"><template #default="{row}">{{taskTypeName(row.type)}}</template></el-table-column><el-table-column label="目标" min-width="150"><template #default="{row}"><el-tooltip :content="taskTarget(row)" placement="top" :show-after="400"><div class="task-list-target"><span>{{taskTargetSummary(row)}}</span></div></el-tooltip></template></el-table-column><el-table-column label="日志" min-width="300"><template #default="{row}"><el-tooltip :content="taskLogTooltip(row)" placement="top" :show-after="400"><span class="task-log-line" :class="{'deploy-error':row.status==='FAILED'}">{{taskLogLine(row)}}</span></el-tooltip></template></el-table-column><el-table-column label="进度" width="180"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)">{{row.status}}</el-tag></template></el-table-column><el-table-column label="创建时间" width="190"><template #default="{row}">{{fmt(row.created_at)}}</template></el-table-column><el-table-column label="操作" width="130"><template #default="{row}"><el-button v-if="isActiveTask(row)" type="warning" link :disabled="taskActionBusy" @click.stop="stopTasks([row])">停止</el-button><span v-if="!isActiveTask(row)" class="muted">-</span></template></el-table-column></el-table></div>
      </template>
    </el-main>
  </el-container>

  <el-dialog v-if="authState.authenticated&&!authState.must_change_password" v-model="passwordDialog" title="修改密码" width="460px" style="max-width:calc(100vw - 32px)" :close-on-click-modal="false" :close-on-press-escape="!passwordSaving" :show-close="!passwordSaving" @closed="resetPasswordForm">
    <el-form :model="passwordForm" :disabled="passwordSaving" label-position="top" @submit.prevent="changePassword">
      <el-form-item label="当前密码"><el-input v-model="passwordForm.old_password" type="password" show-password autocomplete="current-password" placeholder="请输入当前密码"/></el-form-item>
      <el-form-item label="新密码"><el-input v-model="passwordForm.new_password" type="password" show-password autocomplete="new-password" placeholder="至少 4 个字符"/></el-form-item>
      <el-form-item label="确认新密码"><el-input v-model="passwordForm.confirm_password" type="password" show-password autocomplete="new-password" placeholder="请再次输入新密码"/></el-form-item>
      <div class="password-actions"><el-button :disabled="passwordSaving" @click="passwordDialog=false">取消</el-button><el-button type="primary" native-type="submit" :loading="passwordSaving">保存密码</el-button></div>
    </el-form>
  </el-dialog>

  <el-dialog v-model="centerDialog" :title="centerForm.id?'编辑中心':'添加中心'" width="620px">
    <el-form :model="centerForm" label-width="110px">
      <div class="form-grid"><el-form-item label="名称"><el-input v-model="centerForm.name"/></el-form-item><el-form-item label="SSH 主机"><el-input v-model="centerForm.host"/></el-form-item><el-form-item label="SSH 端口"><el-input-number v-model="centerForm.port" :min="1" :max="65535"/></el-form-item><el-form-item label="用户名"><el-input v-model="centerForm.username"/></el-form-item><el-form-item label="访问端口"><el-input-number v-model="centerForm.console_port" :min="1" :max="65535"/><div class="form-tip">Nginx 对外映射端口（默认 8080），防火墙映射不同时填映射后的端口，用于打开 Grafana / Kibana</div></el-form-item></div>
      <el-form-item label="认证方式"><el-radio-group v-model="centerForm.auth_type"><el-radio-button value="password">密码</el-radio-button><el-radio-button value="private_key">私钥</el-radio-button></el-radio-group></el-form-item>
      <el-form-item v-if="centerForm.auth_type==='password'" label="SSH 密码"><el-input v-model="centerForm.password" type="password" show-password placeholder="编辑时留空保留原密码"/></el-form-item>
      <template v-else><el-form-item label="私钥 PEM"><el-input v-model="centerForm.private_key" type="textarea" :rows="5" placeholder="编辑时留空保留原私钥"/></el-form-item><el-form-item label="私钥口令"><el-input v-model="centerForm.passphrase" type="password" show-password/></el-form-item></template>
      <el-form-item label="Docker Socket"><el-input v-model="centerForm.docker_socket"/></el-form-item>
      <el-form-item label="加速卡类型"><el-select v-model="centerForm.accelerator" style="width:100%"><el-option label="自动识别（推荐）" value="auto"/><el-option label="NVIDIA" value="nvidia"/><el-option label="登临" value="denglin"/><el-option label="仅 CPU" value="cpu"/></el-select></el-form-item>
      <el-form-item label="告警控制"><el-switch v-model="centerForm.test_mode"/><div class="form-tip">开启后该中心的管理端飞书通知全部静默：软件/模型/统一发布的开始与结束、初始化开始与结果、暂停/恢复、模型固定/解绑等；同时不纳入统一监控探测（失联告警静默）。中心侧监控告警不受影响。</div></el-form-item>
      <el-form-item label="Debug 模式"><el-switch v-model="centerForm.debug_log_enabled"/><div class="form-tip">开启后业务 Worker（center-cv-service）日志等级切换为 debug（LOG_LEVEL 环境变量），关闭恢复默认 info。仅模块化中心支持；排障时临时开启，日志量会显著增大。修改后需在模块管理中整体发布 business 模块生效。</div></el-form-item>
      <el-form-item label="存图"><el-switch v-model="centerForm.save_image_enabled"/><div class="form-tip">开启后业务 Worker（center-cv-service）每轮推理保存调试图片（原始帧与带检测框结果帧，CV_SAVE_DEBUG_IMAGE 环境变量），关闭恢复默认不存图。仅模块化中心支持；持续写盘、占存储，排障时临时开启。修改后需在模块管理中整体发布 business 模块生效。</div></el-form-item>
      <el-form-item label="错分检测"><el-switch v-model="misSortEnabled" @change="onMisSortToggle"/><div class="form-tip">开关仅控制 ingress 摄像机配置主动轮询（错分实时任务来源）；关闭后 WMS 地址与鉴权保留，重新打开即可恢复。落格核验服务始终部署，未启用时使用模板默认 WMS 参数。需在软件仓库发布 chute-verification-service 与新版 center-cv-ingress 镜像。配置修改后需在模块管理中整体发布 business 模块生效。</div></el-form-item>
      <template v-if="misSortEnabled">
        <el-form-item label="WMS 服务地址"><el-input v-model="centerForm.wms_url" placeholder="请输入 WMS 服务基地址，含协议、主机和端口，不含路径"/><div class="form-tip">WMS 基础地址，仅协议、主机和端口；具体接口路径由服务自动拼接。</div></el-form-item>
        <el-form-item label="WMS 鉴权 misAuth"><el-input v-model="centerForm.mis_auth" placeholder="留空使用默认 20260606123a"/><div class="form-tip">落格核验与 ingress 轮询共用的 misAuth 请求头，已预填公开约定默认值，站点不同时再修改。</div></el-form-item>
      </template>
      <el-form-item :label="centerForm.deployment?'部署目录锚点':'Compose 文件'"><el-input v-model="centerForm.compose_file" placeholder="例如 ~/.local/share/center-manager/deploy/docker-compose.yml"/><div class="form-tip">{{centerForm.deployment?'模块化中心此路径仅作部署目录锚点，实际入口为部署目录下各模块的 compose.yml + module.env，修改需谨慎。':'默认使用 SSH 用户家目录；初始化后会保存解析出的绝对路径。文件已存在时只导入，不会自动覆盖。'}}</div></el-form-item>
      <el-form-item label="说明"><el-input v-model="centerForm.description" type="textarea"/></el-form-item>
    </el-form>
    <template #footer><el-button @click="centerDialog=false">取消</el-button><el-button type="primary" @click="saveCenter">保存</el-button></template>
  </el-dialog>
  <el-dialog v-model="sourceDialog" :title="sourceForm.id?'编辑软件仓库':'添加软件仓库'" width="620px"><el-form :model="sourceForm" label-width="120px"><el-form-item label="软件名称"><el-input v-model="sourceForm.name"/><div class="form-tip">软件名即默认镜像名，需与中心 Compose 模板引用的镜像名一致，例如 center-cv-service。</div></el-form-item><el-form-item label="Git 地址"><el-input v-model="sourceForm.repository" placeholder="ssh://git@host:port/group/repo.git 或 https://..."/></el-form-item><el-form-item label="说明"><el-input v-model="sourceForm.description" type="textarea"/></el-form-item></el-form><template #footer><el-button @click="sourceDialog=false">取消</el-button><el-button type="primary" @click="saveSource">保存</el-button></template></el-dialog>
  <el-dialog v-model="gitCredentialDialog" title="全局 Git 访问令牌" width="620px"><el-alert title="SSH 仓库地址会按路径自动转换到这里配置的 HTTP(S) Git 服务；令牌仅发送给相同主机。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="gitCredentialForm" label-width="110px"><el-form-item label="Git 服务地址"><el-input v-model="gitCredentialForm.base_url" placeholder="例如 https://code.jms.com"/></el-form-item><el-form-item label="Git 用户名"><el-input v-model="gitCredentialForm.username" placeholder="GitLab 令牌通常可使用 oauth2"/></el-form-item><el-form-item label="访问令牌"><el-input v-model="gitCredentialForm.token" type="password" show-password placeholder="编辑时保留掩码即可沿用原令牌"/></el-form-item></el-form><template #footer><el-button @click="gitCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveGitCredential">保存全局令牌</el-button></template></el-dialog>
  <el-dialog v-model="ossCredentialDialog" title="OSS 资源仓库" width="650px"><el-alert title="基础镜像和模型共用同一套加密凭据，分别从各自前缀读取。前缀需以 / 结尾。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="ossCredentialForm" label-width="130px"><el-form-item label="Endpoint"><el-input v-model="ossCredentialForm.endpoint" placeholder="例如 https://oss-cn-hangzhou.aliyuncs.com"/></el-form-item><el-form-item label="Bucket"><el-input v-model="ossCredentialForm.bucket"/></el-form-item><el-form-item label="AccessKey ID"><el-input v-model="ossCredentialForm.access_key_id"/></el-form-item><el-form-item label="AccessKey Secret"><el-input v-model="ossCredentialForm.access_key_secret" type="password" show-password placeholder="编辑时保留掩码即可沿用"/></el-form-item><el-form-item label="镜像前缀"><el-input v-model="ossCredentialForm.images_prefix" placeholder="center-images/"/></el-form-item><el-form-item label="模型前缀"><el-input v-model="ossCredentialForm.models_prefix" placeholder="center-models/"/></el-form-item></el-form><template #footer><el-button @click="ossCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveOSSCredential">保存并读取清单</el-button></template></el-dialog>
  <el-dialog v-model="alertCredentialDialog" title="全局飞书告警配置" width="650px"><el-alert title="所有中心共用这个机器人 Webhook；告警中的服务器名称仍自动使用各中心的名称。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="alertCredentialForm" label-width="130px"><el-form-item label="机器人 Webhook"><el-input v-model="alertCredentialForm.feishu_webhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."/><div class="form-tip">飞书群 → 设置 → 群机器人 → 添加机器人 → 自定义机器人。Webhook 会加密保存。</div></el-form-item></el-form><template #footer><el-button @click="alertCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveAlertCredential">保存全局 Webhook</el-button></template></el-dialog>
  <el-dialog v-model="registryDialog" title="镜像仓库" width="680px">
    <el-alert title="启用后软件发布改为：本地构建一次推送仓库，各中心经 SSH 反向隧道按 digest 拉取（镜像层从 OSS 直下）。仓库未启用或不可达时发布会被阻止，请先在软件仓库页构建发布版本。" type="info" :closable="false" style="margin-bottom:18px"/>
    <el-form :model="registrySettingsForm" label-width="130px">
      <el-form-item label="仓库地址"><el-input v-model="registrySettingsForm.endpoint" placeholder="http://127.0.0.1:5000"/><div class="form-tip">registry sidecar 与管理端共享网络，默认 127.0.0.1:5000。清空地址即关闭镜像仓库模式。</div></el-form-item>
    </el-form>
    <el-divider content-position="left">存储（复用模型仓库的 OSS）</el-divider>
    <el-form :model="registryStorageForm" label-width="130px">
      <el-form-item label="存储前缀"><el-input v-model="registryStorageForm.root_directory" placeholder="registry"/><div class="form-tip">镜像存储复用「模型仓库」配置的 OSS Endpoint、Bucket 与凭据（AK 需具备 multipart 七项权限），此前缀是镜像在共享 Bucket 内的隔离目录，与 center-images/、center-models/ 并列。保存后需重启 registry 容器生效（docker compose restart registry）。</div></el-form-item>
    </el-form>
    <template #footer><el-button @click="registryDialog=false">取消</el-button><el-button :loading="registryTesting" @click="testRegistry">测试连接</el-button><el-button type="primary" @click="saveRegistry">保存配置</el-button></template>
  </el-dialog>
  <el-dialog v-model="hardwareDialog" :title="`${hardwareCenterName} · 硬件信息`" width="min(920px,92vw)" destroy-on-close>
    <div v-if="hardwareInfo" class="hardware-dialog">
      <el-descriptions :column="2" border>
        <el-descriptions-item label="CPU 型号" :span="2">{{hardwareInfo.cpu.model}}</el-descriptions-item>
        <el-descriptions-item label="CPU 数量">{{hardwareInfo.cpu.sockets}} 路</el-descriptions-item>
        <el-descriptions-item label="核心">{{hardwareInfo.cpu.physical_cores}} 个物理核心 / {{hardwareInfo.cpu.logical_cores}} 个逻辑核心</el-descriptions-item>
        <el-descriptions-item label="内存">{{hardwareInfo.memory.total_bytes?formatBytes(hardwareInfo.memory.total_bytes):'-'}}</el-descriptions-item>
        <el-descriptions-item label="加速平台">{{acceleratorName(hardwareInfo.accelerator)}}</el-descriptions-item>
      </el-descriptions>
      <section><h4>GPU</h4><el-table :data="hardwareInfo.gpus" empty-text="未检测到 GPU"><el-table-column prop="model" label="型号" min-width="360"/><el-table-column prop="count" label="数量" width="100"/><el-table-column label="单卡显存" width="150"><template #default="{row}">{{row.memory_bytes?formatBytes(row.memory_bytes):'-'}}</template></el-table-column></el-table></section>
      <section><h4>磁盘</h4><el-table :data="hardwareInfo.disks" empty-text="未读取到磁盘信息" max-height="280"><el-table-column prop="device" label="设备" min-width="150"/><el-table-column prop="mount_point" label="挂载点" min-width="150"/><el-table-column label="已用 / 总量" min-width="210"><template #default="{row}">{{formatBytes(row.used_bytes)}} / {{formatBytes(row.total_bytes)}}</template></el-table-column><el-table-column prop="usage" label="使用率" width="100"/></el-table></section>
      <p class="hardware-detected-at">检测时间：{{fmt(hardwareInfo.detected_at)}}。此次检测不会生成任务记录或任务日志。</p>
    </div>
    <template #footer><el-button type="primary" @click="hardwareDialog=false">关闭</el-button></template>
  </el-dialog>
  <el-drawer v-model="composeDrawer" class="compose-drawer" size="72%"><template #header><div><h3>{{activeComposeCenter?.name||'编辑中心 Compose'}}</h3><span>{{composeDocument.path}}</span></div></template><div class="compose-editor" v-loading="composeSaving"><el-alert title="保存前会在中心服务器上执行 docker compose config 校验；校验失败不会替换现有文件，成功后会自动备份旧文件。保存配置不会自动重新部署。" type="info" :closable="false" show-icon/><YamlEditor v-model="composeDocument.content" :disabled="composeSaving"/><div class="compose-actions"><span>YAML 高亮、行号、折叠和括号匹配已启用</span><div><el-button @click="composeDrawer=false">关闭</el-button><el-button type="primary" :loading="composeSaving" @click="saveCompose">校验并保存</el-button></div></div></div></el-drawer>
  <el-drawer v-model="centerOverviewDrawer" size="76%">
    <template #header><div><h3>{{activeCenterOverview?.center.name||'中心详情'}}</h3><span v-if="activeCenterOverview">{{activeCenterOverview.center.username}}@{{activeCenterOverview.center.host}}:{{activeCenterOverview.center.port}}</span></div></template>
    <div v-loading="centerOverviewLoading||centerOperationSubmitting" class="center-detail">
      <el-alert v-if="activeCenterOverview?.error" :title="activeCenterOverview.error" type="error" :closable="false" show-icon/>
      <template v-else-if="activeCenterOverview">
        <el-descriptions :column="3" border><el-descriptions-item label="Docker 状态"><el-tag :type="activeCenterOverview.online?'success':'danger'">{{activeCenterOverview.online?'在线':'异常'}}</el-tag></el-descriptions-item><el-descriptions-item label="加速卡">{{acceleratorName(activeCenterOverview.accelerator)}}</el-descriptions-item><el-descriptions-item label="Compose 配置"><span>{{activeCenterOverview.center.deployment ? `${activeCenterOverview.center.deployment.modules.length} 个独立模块` : '待同步为独立模块'}}</span></el-descriptions-item><el-descriptions-item label="错分检测"><el-tag :type="activeCenterOverview.center.wms_url?'success':'info'" size="small">{{activeCenterOverview.center.wms_url?'已启用':'未启用'}}</el-tag></el-descriptions-item><el-descriptions-item label="告警控制"><el-tag :type="activeCenterOverview.center.test_mode?'warning':'info'" size="small">{{activeCenterOverview.center.test_mode?'通知静默':'否'}}</el-tag></el-descriptions-item><el-descriptions-item v-if="activeCenterOverview.center.wms_url" label="WMS 地址"><code>{{activeCenterOverview.center.wms_url}}</code></el-descriptions-item></el-descriptions>
        <div class="center-runtime-actions">
          <div class="center-runtime-actions-head">
            <strong>中心状态</strong>
            <small>各 Compose 配置请在下方分别查看和编辑。</small>
            <el-button :icon="Monitor" @click="openCenterConsole(activeCenterOverview.center,'/grafana/')">Grafana 监控</el-button>
            <el-button :icon="Files" @click="openCenterConsole(activeCenterOverview.center,'/kibana/')">Kibana 日志</el-button>
            <el-button v-if="isAdmin&&(activeCenterOverview.center.deployment?.status==='paused'||!activeCenterOverview.center.deployment)" type="primary" plain :disabled="centerOperationSubmitting" @click="resumeCenter(activeCenterOverview.center)">恢复中心</el-button><el-button v-if="isAdmin&&(!activeCenterOverview.center.deployment||(activeCenterOverview.center.deployment.status!=='reset'&&activeCenterOverview.center.deployment.status!=='paused'))" type="warning" plain :disabled="centerOperationSubmitting" @click="pauseCenter(activeCenterOverview.center)">暂停中心</el-button><el-button v-if="isAdmin" type="danger" plain :disabled="centerOperationSubmitting" @click="resetCenterDeployment(activeCenterOverview.center)">重置中心</el-button><el-button :icon="Refresh" :loading="centerOverviewLoading||modelsLoading" @click="refreshCenterManagement">刷新中心状态</el-button>
          </div>
        </div>
        <template v-if="activeCenterOverview.center.deployment">
          <el-alert :title="activeCenterOverview.center.deployment.status==='reset'?'中心已重置，待重新初始化':activeCenterOverview.center.deployment.status==='paused'?'中心已暂停：全部受管服务已停止、容器已移除，数据卷与配置保留；点击上方「恢复中心」按模块依赖顺序重新启动，或在「Compose 配置列表」中单独启动部分模块调试':'模块初始化状态：'+activeCenterOverview.center.deployment.status" :type="activeCenterOverview.center.deployment.status==='paused'?'warning':'info'" :closable="false"/>
          <el-button v-if="!activeCenterOverview.center.initialized_at" :disabled="!!initializationTask(activeCenterOverview.center)" :loading="!!initializationTask(activeCenterOverview.center)" style="margin-top:12px" @click="initializeCenter(activeCenterOverview.center)">初始化部署</el-button>
        </template>
        <ModuleManager v-if="activeCenterOverview.center.deployment?.status!=='reset'" :key="activeCenterOverview.center.id" :center="activeCenterOverview.center" :active="centerOverviewDrawer" :admin="isAdmin" @task="openTask" @changed="refreshCenterManagement" @legacy-edit="(file:string)=>openCompose(activeCenterOverview!.center,file)"/>
        <section class="detail-section">
          <div class="detail-section-title"><div><h3>软件版本</h3><p>可切换版本来自镜像仓库并按本机显卡平台（{{acceleratorName(centerSoftwarePlatform||'auto')}}）过滤；可选择多个软件版本，在底部与模型版本变更合并为一次发布。新版本在「软件仓库」页构建发布。</p></div><el-button v-if="isAdmin" :icon="Delete" @click="openImageManage">管理镜像</el-button></div>
          <el-alert v-if="centerRegistryError" :title="centerRegistryError" type="warning" :closable="false" show-icon/>
          <el-table :data="centerSoftwareRows" class="center-version-table" empty-text="尚未配置软件仓库，且中心没有可识别的已部署软件">
            <el-table-column label="软件" min-width="170"><template #default="{row}"><strong>{{row.source?.name||row.current[0]?.source_name}}</strong><small v-if="row.source&&centerRegistryBySource[row.source.id!]?.versions.length" class="table-subtitle">镜像仓库 {{centerRegistryBySource[row.source.id!].versions.length}} 个版本</small><small v-else-if="row.source" class="table-subtitle">镜像仓库暂无该软件版本</small><small v-else class="table-subtitle">仅在中心检测到，软件仓库已被移除或未配置</small></template></el-table-column>
            <el-table-column label="中心已部署" min-width="210"><template #default="{row}"><template v-if="row.current.length"><div v-for="item in row.current" :key="`${item.version}-${item.commit}`" class="deployed-version"><el-tag size="small" type="success">已部署</el-tag><span>{{imageVersionLabel(item.version,item.commit)}}</span><small>{{item.container_count}} 个容器</small></div></template><span v-else class="muted">尚未部署</span></template></el-table-column>
            <el-table-column label="镜像仓库可切换版本" min-width="360"><template #default="{row}">
              <el-select v-if="row.source&&centerRegistryBySource[row.source.id!]?.versions.length" v-model="centerSoftwareSelections[row.key]" filterable placeholder="选择镜像仓库版本" style="width:100%">
                <el-option v-for="item in centerRegistryBySource[row.source!.id!].versions" :key="item.version" :label="registryVersionLabel(item)" :value="item.version"/>
              </el-select>
              <span v-else class="muted">-</span>
            </template></el-table-column>
          </el-table>
        </section>
        <section class="detail-section">
          <div class="detail-section-title"><div><h3>模型版本</h3><p>同时显示该中心已部署模型与 OSS 中全部 Manifest；未部署过的模型可以直接发布。</p></div><el-button :type="centerModelSetPinned?'warning':'primary'" plain :loading="centerPinBusy==='model-set'" :disabled="!!centerPinBusy||(!centerModelSetPinned&&(!deployedCenterModels.length||deployedCenterModels.some(item=>!item.healthy)))" @click="toggleCenterModelPin">{{centerModelSetPinned?'解绑版本组':'固定当前版本组'}}</el-button></div>
          <el-alert v-if="activeCenterOverview.models_error" :title="activeCenterOverview.models_error" type="warning" :closable="false" show-icon/>
          <el-alert v-if="!ossCredential.configured" title="尚未配置 OSS 资源仓库，只能查看中心已部署模型。" type="info" :closable="false" show-icon/>
          <el-alert v-if="centerModelSetPinned" title="当前整组模型版本已固定：只允许相同 Manifest 的幂等检查，调整任一模型前请先解绑。" type="warning" :closable="false" show-icon/>
          <el-table :data="centerModelRows" class="center-version-table" empty-text="中心没有已部署模型，OSS 中也没有可发布模型">
            <el-table-column label="模型" min-width="160"><template #default="{row}"><strong>{{row.model}}</strong><small v-if="row.current" class="table-subtitle"><code>{{row.current.target_path}}</code></small><small v-else class="table-subtitle">OSS 可用 · 尚未部署</small></template></el-table-column>
            <el-table-column label="中心已部署" min-width="210"><template #default="{row}"><template v-if="row.current"><div class="deployed-version"><el-tag size="small" :type="row.current.healthy?'success':'danger'">{{row.current.healthy?'已部署':'异常'}}</el-tag><span>{{row.current.version}}</span><small>{{row.current.hardware_profile||row.current.platform}} · {{row.current.manifest_digest.slice(0,12)}}</small></div><small v-if="row.current.health_error" class="table-subtitle deploy-error">{{row.current.health_error}}</small></template><span v-else class="muted">尚未部署</span></template></el-table-column>
            <el-table-column label="OSS 可发布版本" min-width="420"><template #default="{row}"><el-select v-model="centerModelSelections[row.model]" filterable placeholder="选择 OSS 模型版本" style="width:100%"><el-option v-for="item in row.options" :key="item.manifest_key" :disabled="!centerModelCompatible(item)" :label="`${item.version} · ${modelFormatSummary(item)}${centerModelCompatible(item)?'':' · 平台不兼容'}`" :value="item.manifest_key"/></el-select></template></el-table-column>
          </el-table>
        </section>
        <div v-if="hasCenterVersionChanges" class="center-version-publish-bar"><span>已选择变更：{{changedCenterSoftwareRows.length}} 个软件版本、{{changedCenterModelRows.length}} 个模型版本</span><el-tooltip :disabled="!centerVersionChangesBlocked" :content="centerModelSetPinned?'模型版本组已固定，请先整体解绑':'所选模型版本与中心平台不兼容'"><span><el-button type="primary" size="large" :loading="centerVersionApplyingHere==='publish'" :disabled="!!centerVersionApplyingHere||centerVersionChangesBlocked" @click="applyCenterVersionChanges">{{changedCenterSoftwareRows.length?'切换并发布':'发布'}}</el-button></span></el-tooltip></div>
        <section class="detail-section"><h3>运行中的容器</h3><el-table :data="activeCenterOverview.containers" empty-text="当前没有运行中的容器"><el-table-column label="容器" min-width="170"><template #default="{row}"><strong>{{(row.names||[]).join(', ')||row.id.slice(0,12)}}</strong></template></el-table-column><el-table-column prop="image" label="镜像" min-width="240"><template #default="{row}"><code>{{row.image}}</code></template></el-table-column><el-table-column prop="status" label="Docker 状态" min-width="180"/><el-table-column v-if="isAdmin" label="操作" width="90" align="center"><template #default="{row}"><el-button size="small" type="primary" plain :icon="Refresh" :loading="centerOperationSubmitting" @click="restartContainer(row)">重启</el-button></template></el-table-column></el-table></section>
      </template>
    </div>
  </el-drawer>
  <el-dialog v-model="centerReleaseDialog" width="min(900px,94vw)" append-to-body :close-on-click-modal="false" class="center-release-dialog">
    <template #header><div class="center-operation-header"><div><h3>{{centerReleaseTitle}}</h3><span>{{centerReleaseStage}}<template v-if="centerReleaseTask"> · {{centerReleaseTask.id}}</template></span></div><el-tag :type="statusType(centerReleaseStatus)">{{centerReleaseStatus}}</el-tag></div></template>
    <div class="center-release-live">
      <el-alert v-if="centerReleaseError" :title="centerReleaseError" type="error" :closable="false" show-icon/>
      <div class="center-release-progress"><el-progress :percentage="Math.round(centerReleaseProgress)" :status="centerReleaseStatus==='FAILED'?'exception':centerReleaseStatus==='SUCCESS'?'success':undefined"/><strong>{{centerReleaseStage}}</strong><span>{{centerReleaseMessage}}</span></div>
      <pre class="center-operation-console">{{centerReleaseLogs.join('\n')||(centerReleaseStatus==='RUNNING'?'正在准备发布并等待任务输出…':'任务没有输出日志')}}</pre>
    </div>
    <template #footer><el-button @click="centerReleaseDialog=false">{{centerReleaseStatus==='RUNNING'?'转到后台':'关闭'}}</el-button></template>
  </el-dialog>
  <el-dialog v-model="sourcePublishDialog.visible" :title="`构建发布 ${sourcePublishDialog.source?.name||''}`" width="min(660px,94vw)" append-to-body :close-on-click-modal="false">
    <el-alert title="在管理端本地构建全部平台镜像并推送到镜像仓库：存在 Dockerfile.nvidia / Dockerfile.denglin 时两个变体都构建成功才入库；只有通用 Dockerfile 时一次构建、各平台中心通用。构建与中心无关，完成后各中心在详情页选择版本拉取运行。" type="info" :closable="false" style="margin-bottom:18px"/>
    <el-select v-model="sourcePublishDialog.selectedKey" :loading="sourcePublishDialog.loading" filterable placeholder="选择远端分支或标签" style="width:100%">
      <el-option v-for="item in gitVersions[sourcePublishDialog.source?.id||'']||[]" :key="softwareOptionKey(item)" :label="`${item.kind==='tag'?'标签':'分支'} · ${item.ref} · ${item.commit.slice(0,8)}`" :value="softwareOptionKey(item)"/>
    </el-select>
    <template #footer><el-button @click="sourcePublishDialog.visible=false">取消</el-button><el-button type="primary" :disabled="!sourcePublishDialog.selectedKey" @click="submitSourcePublish">开始构建发布</el-button></template>
  </el-dialog>
  <el-dialog v-model="sourceVersionsDialog.visible" :title="`${sourceVersionsDialog.source?.name||''} · 镜像版本`" width="min(760px,94vw)" append-to-body>
    <div class="panel-title"><div><h3>镜像仓库已发布版本</h3><p>删除版本会移除其全部平台标签：各中心已运行实例不受影响，但该版本不能再发布；占用的存储由镜像仓库 GC 回收。</p></div><el-button :icon="Refresh" :disabled="sourceVersionsDialog.loading" @click="loadSourceRegistryVersions">刷新</el-button></div>
    <el-table :data="sourceRegistryEntry?.versions||[]" v-loading="sourceVersionsDialog.loading" empty-text="镜像仓库暂无该软件的已发布版本" max-height="420">
      <el-table-column label="版本" min-width="240"><template #default="{row}"><code>{{row.version}}</code></template></el-table-column>
      <el-table-column label="平台标签" min-width="260"><template #default="{row}"><el-tag v-for="tag in row.platforms" :key="tag.tag" size="small" style="margin-right:6px">{{tag.tag}}</el-tag></template></el-table-column>
      <el-table-column label="发布时间" width="180"><template #default="{row}">{{fmt(row.created_at)}}</template></el-table-column>
      <el-table-column v-if="isAdmin" label="操作" width="90" fixed="right"><template #default="{row}"><el-button type="danger" link :loading="sourceVersionsDialog.deleting===row.version" @click="removeRegistryVersion(row)">删除</el-button></template></el-table-column>
    </el-table>
    <template #footer><el-button @click="sourceVersionsDialog.visible=false">关闭</el-button></template>
  </el-dialog>
  <el-dialog v-model="imageManageDialog.visible" title="管理业务镜像" width="min(1120px,96vw)" append-to-body :close-on-click-modal="false">
    <div class="panel-title"><div><h3>中心已构建镜像</h3><p>仅显示本管理端构建的业务镜像，按构建时间倒序；被容器使用的镜像不可删除。</p></div><el-button :icon="Refresh" :disabled="imageManageDialog.loading" @click="loadCenterSoftwareImages">刷新</el-button></div>
    <el-table :data="imageManageDialog.images" v-loading="imageManageDialog.loading" empty-text="该中心尚无本管理端构建的业务镜像" max-height="420">
      <el-table-column label="软件" min-width="120" show-overflow-tooltip><template #default="{row}"><strong>{{row.source_name||row.source_id}}</strong></template></el-table-column>
      <el-table-column label="版本" min-width="110" show-overflow-tooltip><template #default="{row}">{{row.ref}}</template></el-table-column>
      <el-table-column label="Commit" width="115"><template #default="{row}"><code>{{row.commit?.slice(0,12)||'-'}}</code></template></el-table-column>
      <el-table-column label="镜像" min-width="200" show-overflow-tooltip><template #default="{row}"><code>{{row.image}}</code></template></el-table-column>
      <el-table-column label="构建时间" width="160"><template #default="{row}">{{fmtImageTime(row.created_at)}}</template></el-table-column>
      <el-table-column label="大小" width="90"><template #default="{row}">{{formatBytes(row.size)}}</template></el-table-column>
      <el-table-column label="状态" width="95"><template #default="{row}"><el-tooltip v-if="row.in_use" content="镜像正被容器引用（含已停止容器），不能删除" placement="top"><el-tag size="small" type="success">使用中</el-tag></el-tooltip><el-tag v-else size="small" type="info">可删除</el-tag></template></el-table-column>
      <el-table-column label="操作" width="85"><template #default="{row}"><el-button type="danger" link :disabled="row.in_use" :loading="imageManageDialog.deleting===row.id" @click="removeCenterSoftwareImage(row)">删除</el-button></template></el-table-column>
    </el-table>
  </el-dialog>
  <el-dialog v-model="centerOperationDialog" width="min(980px,94vw)" append-to-body :close-on-click-modal="false" class="center-operation-dialog" @closed="closeCenterOperationStream">
    <template #header>
      <div class="center-operation-header">
        <div><h3>{{centerActionName(centerOperationTask?.params?.action)}} · {{centerOperationTask?.params?.center_name}}</h3><span>{{centerOperationTask?.id}}</span></div>
        <el-tag v-if="centerOperationTask" :type="statusType(centerOperationTask.status)">{{centerOperationTask.status}}</el-tag>
      </div>
    </template>
    <div v-if="centerOperationTask" class="center-operation-live">
      <el-alert v-if="centerOperationTask.status==='FAILED'" :title="centerOperationTask.error||'操作失败'" type="error" :closable="false" show-icon/>
      <div class="center-operation-command"><span>远端实际指令</span><code>{{centerOperationTask.params?.command||'正在准备远端指令…'}}</code></div>
      <div class="center-operation-progress"><el-progress :percentage="Math.round(centerOperationTask.progress||0)" :status="centerOperationTask.status==='FAILED'?'exception':centerOperationTask.status==='SUCCESS'?'success':undefined"/><span>{{centerOperationTask.message||'正在等待执行…'}}</span></div>
      <div class="center-operation-state">
        <span>中心 Docker：<b :class="activeCenterOverview?.online?'state-ok':'state-error'">{{activeCenterOverview?.online?'在线':'异常'}}</b></span>
        <span>当前运行容器：<b>{{(activeCenterOverview?.containers||[]).length}}</b></span>
        <el-button size="small" :icon="Refresh" :loading="centerOverviewLoading" @click="refreshActiveCenterOverview">刷新实际状态</el-button>
      </div>
      <pre class="center-operation-console">{{(centerOperationLogs||[]).join('\n')||(['PENDING','RUNNING'].includes(centerOperationTask.status)?'正在连接中心并等待命令输出…':'远端命令没有输出')}}</pre>
    </div>
    <template #footer><el-button @click="centerOperationDialog=false">{{centerOperationTask&&['PENDING','RUNNING'].includes(centerOperationTask.status)?'转到后台':'关闭'}}</el-button><el-button v-if="centerOperationTask" type="primary" plain @click="centerOperationDialog=false;openTask(centerOperationTask)">查看完整任务</el-button></template>
  </el-dialog>
  <InitializeCenterDialog ref="initializationDialog" @task="initializationSubmitted"/>
  <ModelUploadDialog v-model="modelUploadDialog" :model-name="modelUploadTarget" @uploaded="loadModels"/>
  <el-drawer v-model="logDrawer" size="58%" @closed="closeLogs"><template #header><div><h3>{{activeTask?.type}}</h3><span>{{activeTask?.id}}</span></div></template><div class="task-summary"><el-tag :type="statusType(activeTask?.status||'')">{{activeTask?.status}}</el-tag><el-progress :percentage="Math.round(activeTask?.progress||0)"/><span>{{activeTask?.message}}</span></div><div v-if="activeTask&&isAdmin" class="task-actions"><el-button v-if="isActiveTask(activeTask)" type="warning" :disabled="taskActionBusy" @click="stopTasks([activeTask])">停止任务</el-button></div><pre class="logs">{{taskLogs.join('\n')||(activeTask&&isActiveTask(activeTask)?'等待日志…':'暂无日志')}}</pre></el-drawer>
</template>
