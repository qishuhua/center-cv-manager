<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, Download, Files, Lock, Monitor, Operation, Plus, Promotion, Refresh, SwitchButton, UploadFilled, User, VideoPlay } from '@element-plus/icons-vue'
import http,{eventURL} from './api'
import ModelCatalog from './ModelCatalog.vue'
import ModelUploadDialog from './ModelUploadDialog.vue'
import TerminalPage from './TerminalPage.vue'
import type {AlertCredential,BaseImageArchive,Center,CenterOverview,Checkout,ComposeDocument,CurrentModelVersion,GitCredential,HardwareInfo,ModelVersion,OSSCredential,OverviewContainer,OverviewVersion,Source,Task,Version} from './types'

const YamlEditor=defineAsyncComponent(()=>import('./YamlEditor.vue'))

interface DeployItem {source_id:string;ref:string;commit:string;kind:'branch'|'tag'}
interface ModelReleasePlan {ready:boolean;error?:string;action:'blocked'|'skip'|'download_and_activate'|'keep_current';download_bytes:number;current:Record<string,{version:string;platform:string;manifest_digest:string}>;targets:ModelVersion[]}
interface DeploymentPlan {center:Center;ready:boolean;build_required:boolean;error?:string;images:Record<string,string>;missing_images:string[];missing_services:string[];model_plan:ModelReleasePlan}
interface CenterSoftwareOption extends Version {key:string;downloaded:boolean;origin:'git'|'local'}
interface CenterSoftwareRow {key:string;source?:Source;current:OverviewVersion[];options:CenterSoftwareOption[];gitError?:string}
interface CenterModelRow {model:string;current?:CurrentModelVersion;options:ModelVersion[]}

const section=ref('overview'),loading=ref(false),centers=ref<Center[]>([]),sources=ref<Source[]>([]),tasks=ref<Task[]>([])
const authState=reactive({authenticated:false,username:'',must_change_password:false}),authLoading=ref(true)
const loginForm=reactive({username:'',password:''}),passwordForm=reactive({old_password:'',new_password:'',confirm_password:''})
const passwordDialog=ref(false),passwordSaving=ref(false)
const centerDialog=ref(false),sourceDialog=ref(false),gitCredentialDialog=ref(false),ossCredentialDialog=ref(false),alertCredentialDialog=ref(false),modelUploadDialog=ref(false),modelUploadTarget=ref(''),logDrawer=ref(false),centerOverviewDrawer=ref(false),centerOperationDialog=ref(false),centerReleaseDialog=ref(false),composeDrawer=ref(false),hardwareDialog=ref(false)
const centerForm=reactive<Center>({name:'',host:'',port:22,username:'root',auth_type:'password',password:'',private_key:'',passphrase:'',docker_socket:'/var/run/docker.sock',accelerator:'auto',cpu_count:0,gpu_count:0,compose_file:'~/.local/share/center-manager/deploy/docker-compose.yml',description:''})
const sourceForm=reactive<Source>({name:'',repository:'',description:''})
const gitCredential=reactive<GitCredential>({base_url:'',username:'oauth2',token:'',configured:false})
const gitCredentialForm=reactive<GitCredential>({base_url:'',username:'oauth2',token:'',configured:false})
const ossCredential=reactive<OSSCredential>({endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false})
const ossCredentialForm=reactive<OSSCredential>({endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false})
const alertCredential=reactive<AlertCredential>({feishu_webhook:'',configured:false})
const alertCredentialForm=reactive<AlertCredential>({feishu_webhook:'',configured:false})
const buildCheckouts=reactive<Record<string,Checkout[]>>({}),gitVersions=reactive<Record<string,Version[]>>({}),gitVersionErrors=reactive<Record<string,string>>({})
const deployForm=reactive<{center_ids:string[];source_ids:string[];items:DeployItem[];model_keys:string[]}>({center_ids:[],source_ids:[],items:[],model_keys:[]})
const deploymentPlan=ref<DeploymentPlan[]>([]),deploymentPlanning=ref(false),deploymentVersionsLoading=ref(false),deploymentTasks=ref<Task[]>([])
const deploymentFailuresNotified=new Set<string>()
let deploymentPollTimer:number|undefined
const baseImages=ref<BaseImageArchive[]>([]),baseImagesLoading=ref(false),baseImageTasks=ref<Task[]>([])
const baseImageForm=reactive<{center_ids:string[];restart_stack:boolean}>({center_ids:[],restart_stack:false})
const baseImageFailuresNotified=new Set<string>()
let baseImagePollTimer:number|undefined
const models=ref<ModelVersion[]>([]),modelsLoading=ref(false)
const overview=ref<CenterOverview[]>([]),overviewLoading=ref(false)
const activeCenterOverview=ref<CenterOverview|null>(null),centerOverviewLoading=ref(false)
const centerSoftwareSelections=reactive<Record<string,string>>({}),centerModelSelections=reactive<Record<string,string>>({})
const centerVersionApplying=ref(''),centerPinBusy=ref('')
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
async function stopTasks(targets:Task[],all=false){
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
async function deleteTasks(targets:Task[]){
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
async function retryTask(task:Task){
  if(taskActionBusy.value)return
  taskActionBusy.value=true
  try{
    const created:Task=await http.post(`/tasks/${task.id}/retry`)
    ElMessage.success('重试任务已创建')
    await refreshTasks()
    await openTask(created)
  }catch(e:any){ElMessage.error(e.message||String(e))}finally{taskActionBusy.value=false}
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
const taskTypeName=(value:string)=>value==='DETECT_CENTER'?'硬件检测':value==='INITIALIZE_CENTER'?'中心初始化':value==='BASE_IMAGE_UPDATE'?'基础镜像更新':value==='MODEL_RELEASE'?'模型发布':value==='MODEL_SYNC'?'模型同步（历史）':value==='DOWNLOAD_SOURCE'?'源码下载':value==='RELEASE_VERSION'?'版本发布':value==='BUILD_IMAGE'?'镜像构建（历史）':value==='DEPLOY_STACK'?'服务部署（历史）':value==='CENTER_OPERATION'?'中心操作':value
const taskTarget=(task:Task)=>task.type==='RELEASE_VERSION'||task.type==='DEPLOY_STACK'?`${task.params?.versions} → ${task.params?.center_name||task.params?.center_id}`:task.type==='BUILD_IMAGE'?`${task.params?.source_name||task.params?.source_id} → ${task.params?.center_name||task.params?.center_id}`:task.type==='BASE_IMAGE_UPDATE'?task.params?.center_name||task.params?.center_id:task.type==='MODEL_RELEASE'?`${task.params?.versions} → ${task.params?.center_name||task.params?.center_id}`:task.type==='MODEL_SYNC'?`${task.params?.model_count||0} 个模型 → ${task.params?.center_name||task.params?.center_id}`:task.type==='DETECT_CENTER'||task.type==='INITIALIZE_CENTER'?task.params?.center_name||task.params?.center_id:task.type==='CENTER_OPERATION'?`${centerActionName(task.params?.action)} · ${task.params?.center_name||task.params?.center_id}`:task.params?.ref||'-'
const taskVersions=(task:Task)=>(task.params?.versions||'').split('、').filter(Boolean)
const taskTargetSummary=(task:Task)=>{
  if(task.type!=='MODEL_RELEASE'&&task.type!=='RELEASE_VERSION'&&task.type!=='DEPLOY_STACK')return taskTarget(task)
  const versions=taskVersions(task),center=task.params?.center_name||task.params?.center_id||'-'
  if(versions.length<=1)return `${versions[0]||'-'} → ${center}`
  return `${task.type==='MODEL_RELEASE'?`${task.params?.model_count||versions.length} 个模型`:`${versions.length} 个版本`} → ${center}`
}
const taskTargetDetail=(task:Task)=>{
  const versions=taskVersions(task)
  return versions.length>1?versions.map(item=>item.split(':')[0]).join('、'):''
}
const fmt=(v?:string)=>v?new Date(v).toLocaleString():'-'
const formatBytes=(value:number)=>{const units=['B','KiB','MiB','GiB','TiB'];let size=value,index=0;while(size>=1024&&index<units.length-1){size/=1024;index++}return `${size.toFixed(1)} ${units[index]}`}

async function loadOverview(showError=true){overviewLoading.value=true;try{const data=await http.get('/overview');overview.value=Array.isArray(data)?data:[]}catch(e:any){if(showError)ElMessage.error(e.message)}finally{overviewLoading.value=false}}
const softwareOptionKey=(item:Pick<Version,'kind'|'ref'|'commit'>)=>`${item.kind}:${item.ref}:${item.commit}`
function softwareOptions(sourceID:string){
  const remote=gitVersions[sourceID]||[],local=buildCheckouts[sourceID]||[]
  const options:CenterSoftwareOption[]=remote.map(item=>({...item,key:softwareOptionKey(item),downloaded:local.some(checkout=>checkout.ref===item.ref&&checkout.commit===item.commit),origin:'git'}))
  for(const checkout of local){if(options.some(item=>item.ref===checkout.ref&&item.commit===checkout.commit))continue;const kind=remote.find(item=>item.ref===checkout.ref)?.kind||'branch';options.push({ref:checkout.ref,commit:checkout.commit,kind,key:softwareOptionKey({ref:checkout.ref,commit:checkout.commit,kind}),downloaded:true,origin:'local'})}
  return options
}
const centerSoftwareRows=computed<CenterSoftwareRow[]>(()=>{
  const deployed=activeCenterOverview.value?.versions||[],matched=new Set<number>(),rows:CenterSoftwareRow[]=[]
  for(const source of sources.value){
    const current=deployed.filter((item,index)=>{const same=item.source_id===source.id||(!item.source_id&&item.source_name===source.name);if(same)matched.add(index);return same})
    const sourceID=source.id||''
    rows.push({key:sourceID||source.name,source,current,options:softwareOptions(sourceID),gitError:gitVersionErrors[sourceID]})
  }
  const orphaned=new Map<string,OverviewVersion[]>()
  deployed.forEach((item,index)=>{if(matched.has(index))return;const key=item.source_id||item.source_name;orphaned.set(key,[...(orphaned.get(key)||[]),item])})
  for(const [key,current] of orphaned)rows.push({key:`deployed:${key}`,current,options:[]})
  return rows
})
const deployedCenterModels=computed(()=>(activeCenterOverview.value?.models||[]))
const centerModelRows=computed<CenterModelRow[]>(()=>{
  const byModel=new Map<string,CenterModelRow>()
  for(const current of deployedCenterModels.value)byModel.set(current.model,{model:current.model,current,options:[]})
  for(const option of models.value){const row=byModel.get(option.model)||{model:option.model,options:[]};row.options.push(option);byModel.set(option.model,row)}
  return [...byModel.values()].sort((a,b)=>a.model.localeCompare(b.model))
})
const centerModelSetPinned=computed(()=>deployedCenterModels.value.some(item=>item.pinned))
function centerModelCompatible(item:ModelVersion){const accelerator=activeCenterOverview.value?.accelerator||activeCenterOverview.value?.center.detected_accelerator||activeCenterOverview.value?.center.accelerator;return item.formats.some(format=>format.platform==='onnx'||format.platform===accelerator)}
function modelFormatSummary(item:ModelVersion){return item.formats.map(format=>format.hardware_profile?`${format.platform}-${format.hardware_profile}`:format.platform).join(' / ')}
function selectedCenterSoftware(row:CenterSoftwareRow){return row.options.find(item=>item.key===centerSoftwareSelections[row.key])}
function selectedCenterModel(row:CenterModelRow){return models.value.find(item=>item.manifest_key===centerModelSelections[row.model])}
function selectedCenterModelCompatible(row:CenterModelRow){const selected=selectedCenterModel(row);return !!selected&&centerModelCompatible(selected)}
const changedCenterSoftwareRows=computed(()=>centerSoftwareRows.value.filter(row=>{const target=selectedCenterSoftware(row);return !!target&&!!row.source?.id&&(!row.current.length||row.current.some(current=>current.version!==target.ref||!!current.commit&&current.commit!==target.commit))}))
const changedCenterModelRows=computed(()=>centerModelRows.value.filter(row=>{const target=selectedCenterModel(row);return !!target&&target.manifest_digest!==row.current?.manifest_digest}))
const hasCenterVersionChanges=computed(()=>changedCenterSoftwareRows.value.length+changedCenterModelRows.value.length>0)
const centerVersionChangesBlocked=computed(()=>changedCenterModelRows.value.some(row=>!selectedCenterModelCompatible(row))||(centerModelSetPinned.value&&changedCenterModelRows.value.length>0))
function syncCenterVersionSelections(){
  for(const row of centerSoftwareRows.value){const current=row.current[0],match=current?row.options.find(item=>item.ref===current.version&&(!current.commit||item.commit===current.commit)):undefined;if(match)centerSoftwareSelections[row.key]=match.key;else if(!row.options.some(item=>item.key===centerSoftwareSelections[row.key]))centerSoftwareSelections[row.key]=''}
  for(const row of centerModelRows.value){const currentMatch=row.current?row.options.find(item=>item.manifest_digest===row.current!.manifest_digest):undefined;const selected=row.options.find(item=>item.manifest_key===centerModelSelections[row.model]&&centerModelCompatible(item));centerModelSelections[row.model]=currentMatch?.manifest_key||selected?.manifest_key||''}
}
async function loadGitVersions(sourceID:string){try{const data=await http.get(`/sources/${sourceID}/versions`);gitVersions[sourceID]=Array.isArray(data)?data:[];delete gitVersionErrors[sourceID]}catch(e:any){gitVersions[sourceID]=[];gitVersionErrors[sourceID]=e.message||'读取 Git 版本失败'}}
async function loadCenterVersionOptions(){await Promise.all(sources.value.filter(item=>item.id).map(item=>Promise.all([loadBuildCheckouts(item.id!,true),loadGitVersions(item.id!)])));if(ossCredential.configured)await loadModels();else models.value=[];syncCenterVersionSelections()}
async function refreshActiveCenterOverview(){if(!activeCenterOverview.value?.center.id)return;centerOverviewLoading.value=true;try{activeCenterOverview.value=await http.get(`/centers/${activeCenterOverview.value.center.id}/overview`);syncCenterVersionSelections()}catch(e:any){ElMessage.error(e.message)}finally{centerOverviewLoading.value=false}}
async function refreshCenterManagement(){await Promise.all([refreshActiveCenterOverview(),loadCenterVersionOptions()]);syncCenterVersionSelections()}
async function openCenterOverview(item:CenterOverview){for(const key of Object.keys(centerSoftwareSelections))delete centerSoftwareSelections[key];for(const key of Object.keys(centerModelSelections))delete centerModelSelections[key];activeCenterOverview.value=item;centerOverviewDrawer.value=true;await Promise.all([refreshActiveCenterOverview(),loadCenterVersionOptions()]);syncCenterVersionSelections()}
async function openCenterFromCard(center:Center){await openCenterOverview({center,online:false,containers:[],versions:[],models:[]})}
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
async function runCenterAction(action:'restart_container'|'restart_compose'|'redeploy_compose'|'stop_compose'|'remove_all_containers',container?:OverviewContainer){
  const center=activeCenterOverview.value?.center
  if(!center?.id)return
  const definitions={restart_container:{title:'重启容器',message:`确认重启容器“${(container?.names||[]).join(', ')||container?.id.slice(0,12)}”？`,confirm:'验证并重启'},restart_compose:{title:'重启整套 Compose',message:`将重启中心“${center.name}”Compose 中的全部服务。`,confirm:'验证并重启'},redeploy_compose:{title:'重新部署 Compose',message:`将按当前 Compose 配置更新中心“${center.name}”的服务，并清理配置中已移除的容器。`,confirm:'验证并重新部署'},stop_compose:{title:'停止整套 Compose',message:`将停止中心“${center.name}”Compose 中的全部服务，容器会保留。`,confirm:'验证并停止'},remove_all_containers:{title:'删除中心全部容器',message:`高风险操作：将强制删除中心“${center.name}”中的全部 Docker 容器，包括不属于当前 Compose 的容器；镜像和数据卷保留。`,confirm:'验证并全部删除'}} as const
  const definition=definitions[action]
  try{
    const {value}=await ElMessageBox.prompt(`${definition.message}\n请输入当前管理员密码继续。`,definition.title,{type:action==='restart_container'?'warning':'error',inputType:'password',inputPlaceholder:'管理员密码',inputValidator:(input:string)=>input?.length>0||'请输入管理员密码',confirmButtonText:definition.confirm,cancelButtonText:'取消',closeOnClickModal:false})
    centerOperationSubmitting.value=true
    const task:Task=await http.post(`/centers/${center.id}/actions`,{action,container_id:container?.id||'',admin_password:value})
    await showCenterOperation(task)
  }catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}finally{centerOperationSubmitting.value=false}
}
async function loadAll(){loading.value=true;try{const [centerData,sourceData,taskData,credentialData,ossData,alertData]=await Promise.all([http.get('/centers'),http.get('/sources'),http.get('/tasks'),http.get('/git-credential'),http.get('/oss-credential'),http.get('/alert-credential')]);centers.value=Array.isArray(centerData)?centerData:[];sources.value=Array.isArray(sourceData)?sourceData:[];tasks.value=Array.isArray(taskData)?taskData:[];Object.assign(gitCredential,credentialData||{base_url:'',username:'oauth2',token:'',configured:false});Object.assign(ossCredential,{models_prefix:'center-models/'},ossData||{endpoint:'',bucket:'',access_key_id:'',access_key_secret:'',images_prefix:'center-images/',models_prefix:'center-models/',configured:false});Object.assign(alertCredential,alertData||{feishu_webhook:'',configured:false});if(section.value==='overview')await loadOverview(false)}catch(e:any){if(authState.authenticated)ElMessage.error(e.message)}finally{loading.value=false}}
async function selectSection(value:string){if(value==='releases'){await openDeploy();return}if(value==='base-images'){await openBaseImages();return}if(value==='models'){await openModels();return}section.value=value;if(value==='overview')await loadOverview()}
async function refreshCurrent(){await loadAll();if(section.value==='overview'&&!overview.value.length)await loadOverview();if(section.value==='base-images'&&ossCredential.configured)await loadBaseImages();if(section.value==='models'&&ossCredential.configured)await loadModels();if(section.value==='releases'){await Promise.all([loadDeploymentVersions(),ossCredential.configured?loadModels():Promise.resolve()]);syncDeployItems(deployForm.source_ids)}}
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
async function logout(){try{await http.post('/auth/logout')}finally{Object.assign(authState,{authenticated:false,username:'',must_change_password:false});loginForm.password='';passwordDialog.value=false;resetPasswordForm()}}
function resetCenter(){Object.assign(centerForm,{id:undefined,name:'',host:'',port:22,username:'root',auth_type:'password',password:'',private_key:'',passphrase:'',host_key:'',docker_socket:'/var/run/docker.sock',accelerator:'auto',detected_accelerator:'',cpu_count:0,gpu_count:0,compose_file:'~/.local/share/center-manager/deploy/docker-compose.yml',initialized_at:undefined,description:''})}
function editCenter(v?:Center){resetCenter();if(v)Object.assign(centerForm,v);centerDialog.value=true}
async function saveCenter(){try{const path=centerForm.id?`/centers/${centerForm.id}`:'/centers';const result:any=centerForm.id?await http.put(path,centerForm):await http.post(path,centerForm);centerDialog.value=false;await loadAll();if(result?.task){ElMessage.success('中心已保存，正在自动检测硬件并初始化 Compose');await openTask(result.task)}else ElMessage.success('中心已保存')}catch(e:any){ElMessage.error(e.message)}}
async function removeCenter(v:Center){await ElMessageBox.confirm(`确定删除中心“${v.name}”？`,'确认');try{await http.delete(`/centers/${v.id}`);ElMessage.success('已删除');await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function detectCenter(v:Center){if(!v.id||hardwareDetectingId.value)return;hardwareDetectingId.value=v.id;try{ElMessage.info('正在读取中心硬件信息…');hardwareInfo.value=await http.post(`/centers/${v.id}/detect`);hardwareCenterName.value=v.name;hardwareDialog.value=true;await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{hardwareDetectingId.value=''}}
async function initializeCenter(v:Center,forceTemplate=false,downloadImages=!forceTemplate){try{if(forceTemplate)await ElMessageBox.confirm(`将根据 ${v.name} 当前硬件重新生成 Compose，并为旧文件创建备份。生成后需要在运行详情中执行“重新部署 Compose”才会应用新配置。是否继续？`,'重新生成硬件模板',{type:'warning',confirmButtonText:'确认重新生成',cancelButtonText:'取消',closeOnClickModal:false});const task:Task=await http.post(`/centers/${v.id}/initialize`,{force_template:forceTemplate,download_images:downloadImages});ElMessage.success(forceTemplate?'模板重新生成任务已创建，完成后请重新部署 Compose':downloadImages?'已创建硬件检测、模板和基础镜像同步任务':'已创建内置配置同步任务，完成后请重启 Compose 应用监控配置');await openTask(task);await loadAll()}catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}}
async function syncCenterTemplates(v:Center){try{await ElMessageBox.confirm(`将保留 ${v.name} 当前 Compose 和中心上的飞书密钥，同步最新 Nginx、Prometheus、Grafana、告警及日志模板。完成后需重启 Compose 才会应用配置。是否继续？`,'同步内置配置',{type:'warning',confirmButtonText:'确认同步',cancelButtonText:'取消',closeOnClickModal:false});await initializeCenter(v,false,false)}catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message||String(e))}}
async function openCompose(v:Center){composeDrawer.value=true;composeSaving.value=true;Object.assign(composeDocument,{center_id:v.id!,path:v.compose_file,content:''});try{Object.assign(composeDocument,await http.get(`/centers/${v.id}/compose`))}catch(e:any){ElMessage.error(e.message)}finally{composeSaving.value=false}}
async function saveCompose(){if(!composeDocument.content.trim()){ElMessage.warning('Compose 内容不能为空');return};composeSaving.value=true;try{Object.assign(composeDocument,await http.put(`/centers/${composeDocument.center_id}/compose`,{content:composeDocument.content}));ElMessage.success('Compose 校验通过并已保存，旧文件已备份');await loadAll()}catch(e:any){ElMessage.error(e.message)}finally{composeSaving.value=false}}
function resetSource(){Object.assign(sourceForm,{id:undefined,name:'',repository:'',description:''})}
function editSource(v?:Source){resetSource();if(v)Object.assign(sourceForm,v);sourceDialog.value=true}
async function saveSource(){try{const path=sourceForm.id?`/sources/${sourceForm.id}`:'/sources';sourceForm.id?await http.put(path,sourceForm):await http.post(path,sourceForm);ElMessage.success('软件仓库已保存');sourceDialog.value=false;await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function removeSource(v:Source){await ElMessageBox.confirm(`删除“${v.name}”及其本地源码缓存？`,'确认');try{await http.delete(`/sources/${v.id}`);ElMessage.success('已删除');await loadAll()}catch(e:any){ElMessage.error(e.message)}}
function editGitCredential(){Object.assign(gitCredentialForm,gitCredential);gitCredentialDialog.value=true}
async function saveGitCredential(){try{const out:any=await http.put('/git-credential',gitCredentialForm);Object.assign(gitCredential,out);ElMessage.success('全局 Git 访问令牌已保存');gitCredentialDialog.value=false}catch(e:any){ElMessage.error(e.message)}}
function editOSSCredential(){Object.assign(ossCredentialForm,ossCredential);ossCredentialDialog.value=true}
async function saveOSSCredential(){try{const out:any=await http.put('/oss-credential',ossCredentialForm);Object.assign(ossCredential,out);ElMessage.success('OSS 资源仓库配置已保存');ossCredentialDialog.value=false;if(section.value==='models'||section.value==='releases')await loadModels();else await loadBaseImages()}catch(e:any){ElMessage.error(e.message)}}
function editAlertCredential(){Object.assign(alertCredentialForm,alertCredential);alertCredentialDialog.value=true}
async function saveAlertCredential(){try{const out:any=await http.put('/alert-credential',alertCredentialForm);Object.assign(alertCredential,out);ElMessage.success('全局飞书告警 Webhook 已保存');alertCredentialDialog.value=false}catch(e:any){ElMessage.error(e.message)}}
async function loadBaseImages(){baseImagesLoading.value=true;try{const data=await http.get('/base-images');baseImages.value=Array.isArray(data)?data:[]}catch(e:any){baseImages.value=[];if(ossCredential.configured)ElMessage.error(e.message)}finally{baseImagesLoading.value=false}}
async function openBaseImages(){section.value='base-images';baseImageForm.center_ids=[];baseImageForm.restart_stack=false;baseImageTasks.value=[];if(ossCredential.configured)await loadBaseImages()}
async function refreshBaseImageTasks(){window.clearTimeout(baseImagePollTimer);if(!baseImageTasks.value.length)return;try{baseImageTasks.value=await Promise.all(baseImageTasks.value.map(task=>http.get(`/tasks/${task.id}`)));for(const task of baseImageTasks.value){if(task.status==='FAILED'&&!baseImageFailuresNotified.has(task.id)){baseImageFailuresNotified.add(task.id);ElMessage.error(`中心 ${task.params?.center_name||task.params?.center_id} 基础镜像更新失败：${task.error||'请查看日志'}`)}}if(baseImageTasks.value.some(task=>['PENDING','RUNNING'].includes(task.status)))baseImagePollTimer=window.setTimeout(refreshBaseImageTasks,2000)}catch(e:any){ElMessage.error(e.message)}}
async function upgradeBaseImages(){if(!baseImageForm.center_ids.length){ElMessage.warning('请至少选择一台中心服务器');return};if(baseImageForm.restart_stack)await ElMessageBox.confirm('加载新基础镜像后将强制重建所选中心的整套服务，会产生短暂中断。确认继续？','确认重建服务',{type:'warning'});try{baseImageFailuresNotified.clear();baseImageTasks.value=await http.post('/base-images/upgrade',baseImageForm);ElMessage.success(`已创建 ${baseImageTasks.value.length} 个基础镜像更新任务`);await refreshBaseImageTasks();await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function loadModels(){modelsLoading.value=true;try{const data=await http.get('/models');models.value=Array.isArray(data)?data:[];const available=new Set(models.value.map(item=>item.manifest_key));deployForm.model_keys=deployForm.model_keys.filter(key=>available.has(key))}catch(e:any){models.value=[];deployForm.model_keys=[];if(ossCredential.configured)ElMessage.error(e.message)}finally{modelsLoading.value=false}}
async function deleteModelFolder(model:string){
  if(modelsLoading.value)return
  const count=models.value.filter(item=>item.model===model).length
  try{await ElMessageBox.confirm(`确定删除模型文件夹“${model}”及其中全部 ${count} 个版本和文件？此操作不可恢复。已部署到中心的文件会保留，但仓库版本将无法再次发布。`,'删除模型文件夹',{type:'warning',confirmButtonText:'确认删除',cancelButtonText:'取消'})}catch{return}
  modelsLoading.value=true
  try{await http.delete(`/models/${encodeURIComponent(model)}`);invalidateDeploymentPlan();ElMessage.success('模型文件夹已删除')}catch(e:any){ElMessage.error(e.message)}finally{await loadModels()}
}
async function openModels(){section.value='models';if(ossCredential.configured)await loadModels()}
function openModelUpload(model=''){modelUploadTarget.value=model;modelUploadDialog.value=true}
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
async function applyCenterVersionChanges(){
  const centerID=activeCenterOverview.value?.center.id,softwareRows=changedCenterSoftwareRows.value,modelRows=changedCenterModelRows.value
  if(!centerID||!softwareRows.length&&!modelRows.length)return
  if(centerVersionChangesBlocked.value){ElMessage.warning(centerModelSetPinned.value?'当前模型版本组已固定，请先整体解绑':'所选模型版本与中心平台不兼容');return}
  const items=softwareRows.map(row=>{const target=selectedCenterSoftware(row)!;return {source_id:row.source!.id!,ref:target.ref,commit:target.commit}})
  const modelSelections=modelRows.map(row=>{const target=selectedCenterModel(row)!;return {manifest_key:target.manifest_key,manifest_digest:target.manifest_digest}})
  const summary=[items.length?`${items.length} 个软件版本`:null,modelSelections.length?`${modelSelections.length} 个模型版本`:null].filter(Boolean).join('和')
  try{
    if(modelSelections.length)await ElMessageBox.confirm(`将发布 ${summary}，随后重启该中心整套 Compose 以加载模型，服务会短暂中断。是否继续？`,'确认发布版本',{type:'warning',confirmButtonText:'确认发布',cancelButtonText:'取消',closeOnClickModal:false})
    centerVersionApplying.value='all'
    const workflowID=beginCenterRelease(`发布 ${summary}`,'统一发布预检')
    prepareCenterReleaseStage('统一发布预检',5,'正在检查中心 Compose、软件镜像、模型版本和硬件兼容性')
    const payload={center_ids:[centerID],items,model_selections:modelSelections}
    const plans:DeploymentPlan[]=await http.post('/releases/plan',payload)
    const plan=plans[0];if(!plan?.ready)throw new Error(plan?.error||plan?.missing_services?.join('；')||'中心不满足发布条件')
    centerReleaseMessage.value=plan.build_required?'预检通过，缺少的软件镜像将先构建再统一发布':'预检通过，正在创建统一发布任务'
    const created:Task[]=await http.post('/releases',payload)
    if(!created.length)throw new Error('没有创建版本发布任务')
    deploymentTasks.value=created
    const released=await followCenterReleaseTask(created[0],`发布 ${summary}`,12,100,workflowID);requireReleaseSuccess(released)
    centerReleaseProgress.value=100;centerReleaseMessage.value=`${summary}已发布到该中心`;ElMessage.success(centerReleaseMessage.value)
    await Promise.all([refreshActiveCenterOverview(),refreshTasks()])
  }catch(e:any){if(e!=='cancel'&&e!=='close'){centerReleaseError.value=e.message||String(e);centerReleaseMessage.value=centerReleaseError.value;ElMessage.error(centerReleaseError.value)}}finally{centerVersionApplying.value=''}
}
async function toggleCenterModelPin(){const centerID=activeCenterOverview.value?.center.id,pinned=centerModelSetPinned.value;if(!centerID)return;try{if(pinned)await ElMessageBox.confirm('解绑后才能新增、删除或切换任一模型版本，确定整体解绑？','解绑模型版本组',{type:'warning',confirmButtonText:'确认解绑',cancelButtonText:'取消'});else await ElMessageBox.confirm(`将固定当前 ${deployedCenterModels.value.length} 个模型组成的版本快照。固定后任何模型调整都必须先整体解绑，是否继续？`,'固定模型版本组',{type:'warning',confirmButtonText:'固定当前版本组',cancelButtonText:'取消'});centerPinBusy.value='model-set';await http.put(`/centers/${centerID}/models/pin`,{pinned:!pinned});ElMessage.success(pinned?'模型版本组已解绑':'当前模型版本组已固定');await refreshActiveCenterOverview()}catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}finally{centerPinBusy.value=''}}
async function loadBuildCheckouts(sourceID:string,silent=false){try{const data=await http.get(`/sources/${sourceID}/checkouts`);buildCheckouts[sourceID]=Array.isArray(data)?data:[]}catch(e:any){buildCheckouts[sourceID]=[];if(!silent)ElMessage.error(e.message)}}
function invalidateDeploymentPlan(){deploymentPlan.value=[]}
const deployItemKey=(item:DeployItem)=>item.commit?softwareOptionKey(item):''
const selectedDeployVersion=(item:DeployItem)=>(gitVersions[item.source_id]||[]).find(version=>softwareOptionKey(version)===deployItemKey(item))
function selectDeployVersion(item:DeployItem,key:string){const version=(gitVersions[item.source_id]||[]).find(candidate=>softwareOptionKey(candidate)===key);if(version)Object.assign(item,{ref:version.ref,commit:version.commit,kind:version.kind});else Object.assign(item,{ref:'',commit:''});invalidateDeploymentPlan()}
function syncDeployItems(selected:string[],preferredSourceID?:string,preferredVersion?:Version){const existing=new Map(deployForm.items.map(item=>[item.source_id,item]));deployForm.items=selected.map(id=>{const available=gitVersions[id]||[],old=existing.get(id),current=old&&available.find(version=>softwareOptionKey(version)===deployItemKey(old));const preferred=id===preferredSourceID&&preferredVersion?available.find(version=>softwareOptionKey(version)===softwareOptionKey(preferredVersion)):undefined;const version=preferred||current||available[0];return version?{source_id:id,ref:version.ref,commit:version.commit,kind:version.kind}:{source_id:id,ref:'',commit:'',kind:'branch'}});invalidateDeploymentPlan()}
async function loadDeploymentVersions(){deploymentVersionsLoading.value=true;try{await Promise.all(sources.value.filter(item=>item.id).map(item=>loadGitVersions(item.id!)))}finally{deploymentVersionsLoading.value=false}}
async function openDeploy(source?:Source,preferredVersion?:Version){section.value='releases';deployForm.center_ids=[];deployForm.source_ids=source?.id?[source.id]:[];deployForm.items=[];deployForm.model_keys=[];deploymentPlan.value=[];deploymentTasks.value=[];await Promise.all([sources.value.length?loadDeploymentVersions():Promise.resolve(),ossCredential.configured?loadModels():Promise.resolve()]);syncDeployItems(deployForm.source_ids,source?.id,preferredVersion)}
function deploySourcesChanged(value:string[]){syncDeployItems(value)}
function deploymentIssue(item:DeploymentPlan){return item.error||'未满足发布条件'}
function modelPlanSummary(item:DeploymentPlan){const plan=item.model_plan;if(!plan)return '-';if(!plan.ready)return plan.error||'模型检查失败';const count=plan.targets?.length||Object.keys(plan.current||{}).length;if(plan.action==='keep_current')return `沿用中心当前 ${count} 个健康模型`;if(plan.action==='skip')return `${count} 个模型已是目标版本`;return `更新 ${count} 个模型，下载 ${formatBytes(plan.download_bytes||0)}`}
async function planDeployment(){if(!deployForm.items.length&&!deployForm.model_keys.length){ElMessage.warning('请至少选择一个软件版本或模型版本');return};if(deployForm.items.some(item=>!item.ref||!item.commit)){ElMessage.warning('每个软件都必须选择一个 Git 分支或标签');return};if(!deployForm.center_ids.length){ElMessage.warning('请至少选择一个发布中心');return};deploymentPlanning.value=true;try{const result:DeploymentPlan[]=await http.post('/releases/plan',{center_ids:deployForm.center_ids,items:deployForm.items,model_selections:modelSelections()});deploymentPlan.value=Array.isArray(result)?result:[];const ready=deploymentPlan.value.filter(item=>item.ready).length;const builds=deploymentPlan.value.filter(item=>item.ready&&item.build_required).length;if(ready)ElMessage.success(`发布计划已生成：${ready} 个中心可发布，其中 ${builds} 个需要先构建`);else ElMessage.warning('当前没有满足发布条件的中心，请查看下方原因')}catch(e:any){deploymentPlan.value=[];ElMessage.error(e.message)}finally{deploymentPlanning.value=false}}
async function refreshDeploymentTasks(){window.clearTimeout(deploymentPollTimer);if(!deploymentTasks.value.length)return;try{deploymentTasks.value=await Promise.all(deploymentTasks.value.map(task=>http.get(`/tasks/${task.id}`)));for(const task of deploymentTasks.value){if(task.status==='FAILED'&&!deploymentFailuresNotified.has(task.id)){deploymentFailuresNotified.add(task.id);ElMessage.error(`中心 ${task.params?.center_name||task.params?.center_id} 部署失败：${task.error||task.message||'请查看任务日志'}`)}}if(deploymentTasks.value.some(task=>['PENDING','RUNNING'].includes(task.status)))deploymentPollTimer=window.setTimeout(refreshDeploymentTasks,2000)}catch(e:any){ElMessage.error(e.message)}}
async function startDeployment(){if(!deploymentPlan.value.length){ElMessage.warning('请先生成发布计划');return};if(!deploymentPlan.value.some(item=>item.ready)){ElMessage.warning('当前没有通过巡检的发布中心');return};try{deploymentFailuresNotified.clear();deploymentTasks.value=await http.post('/releases',{center_ids:deployForm.center_ids,items:deployForm.items,model_selections:modelSelections()});const skipped=Math.max(0,deployForm.center_ids.length-deploymentTasks.value.length);if(skipped)ElMessage.warning(`已创建 ${deploymentTasks.value.length} 个发布任务，${skipped} 个中心未通过最新复检并已跳过`);else ElMessage.success(`已创建 ${deploymentTasks.value.length} 个中心版本发布任务`);await refreshDeploymentTasks();await loadAll()}catch(e:any){ElMessage.error(e.message)}}
async function openTask(v:Task){sourceEvents.value?.close();activeTask.value=v;logDrawer.value=true;try{taskLogs.value=await http.get(`/tasks/${v.id}/logs`)}catch{taskLogs.value=[]};if(['RUNNING','PENDING'].includes(v.status)){const es=new EventSource(eventURL(v.id));sourceEvents.value=es;es.addEventListener('log',(ev:any)=>{try{taskLogs.value.push(JSON.parse(ev.data).message)}catch{}});es.addEventListener('progress',(ev:any)=>{try{Object.assign(activeTask.value!,JSON.parse(ev.data))}catch{}});es.addEventListener('status',async(ev:any)=>{try{Object.assign(activeTask.value!,JSON.parse(ev.data))}catch{};const status=JSON.parse(ev.data).status;if(['SUCCESS','FAILED','CANCELLED'].includes(status)){es.close();if(status==='SUCCESS'&&v.type==='DOWNLOAD_SOURCE'&&v.params?.source_id)delete buildCheckouts[v.params.source_id];await loadAll();if(['CENTER_OPERATION','MODEL_RELEASE','RELEASE_VERSION'].includes(v.type)&&activeCenterOverview.value?.center.id===v.params?.center_id)await refreshActiveCenterOverview()}});es.onerror=()=>es.close()}}
function closeLogs(){sourceEvents.value?.close();sourceEvents.value=null}
onMounted(async()=>{window.addEventListener('manager-auth-required',()=>{authState.authenticated=false;passwordDialog.value=false;resetPasswordForm()});await checkAuth();if(authState.authenticated&&!authState.must_change_password)await loadAll();taskPollTimer=window.setTimeout(pollTasks,2000)})
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
        <el-menu-item index="terminal"><el-icon><Operation/></el-icon>SSH 终端</el-menu-item>
        <el-menu-item index="base-images"><el-icon><UploadFilled/></el-icon>基础镜像</el-menu-item>
        <el-menu-item index="models"><el-icon><Download/></el-icon>模型仓库</el-menu-item>
        <el-menu-item index="sources"><el-icon><Files/></el-icon>源码仓库</el-menu-item>
        <el-menu-item index="releases"><el-icon><Promotion/></el-icon>版本发布</el-menu-item>
        <el-menu-item index="tasks"><el-icon><VideoPlay/></el-icon>任务与日志</el-menu-item>
      </el-menu>
      <div class="account"><span><el-icon><User/></el-icon>{{authState.username}}</span><button title="修改密码" aria-label="修改密码" @click="openPasswordDialog"><el-icon><Lock/></el-icon><span class="account-label">修改密码</span></button><button title="退出" aria-label="退出" @click="logout"><el-icon><SwitchButton/></el-icon>退出</button></div>
    </el-aside>
    <el-main class="main" :class="{'terminal-main':section==='terminal'}" v-loading="loading">
      <header v-if="section!=='terminal'"><div><h1>{{section==='overview'?'运行总览':section==='centers'?'中心服务器':section==='terminal'?'SSH 终端':section==='base-images'?'基础镜像':section==='models'?'模型仓库':section==='sources'?'源码仓库':section==='releases'?'版本发布':'任务与日志'}}</h1><p v-if="['terminal','releases','base-images','models','sources'].includes(section)">{{section==='terminal'?'连接已配置的中心服务器并进行交互式运维':section==='releases'?'选择远端分支或标签，锁定 commit 后自动准备源码并发布':section==='base-images'?'从 OSS 按中心显卡类型下载并更新基础镜像':section==='models'?'上传和发布按硬件格式组织的模型版本':section==='sources'?'管理源码地址并查看远端可发布版本':''}}</p></div><el-button :icon="Refresh" circle @click="refreshCurrent"/></header>

      <template v-if="section==='overview'">
        <div class="overview-dashboard" v-loading="overviewLoading">
          <div class="metrics overview-metrics"><div class="metric accent"><span>集群在线率</span><b>{{onlineCount}} / {{overview.length}}</b><small>{{offlineCount?`${offlineCount} 个中心连接异常`:'全部中心连接正常'}}</small></div><div class="metric"><span>初始化就绪</span><b>{{initializedCount}} / {{centers.length}}</b><small>硬件检测与 Compose 已准备</small></div><div class="metric"><span>执行中任务</span><b>{{activeTaskCount}}</b><small>包含等待与运行中的任务</small></div><div class="metric"><span>软件仓库</span><b>{{sources.length}}</b><small>已纳入版本管理</small></div></div>
          <div class="overview-grid">
            <div class="panel health-panel">
              <div class="panel-title"><div><h2>集群健康</h2><p>聚合展示可用性和资源准备情况。</p></div><el-button type="primary" link @click="selectSection('centers')">管理中心服务器</el-button></div>
              <div class="health-row"><div><strong>连接可用性</strong><small>SSH 与 Docker Engine</small></div><el-progress :percentage="onlineRate" :status="offlineCount?'exception':'success'"/></div>
              <div class="health-row"><div><strong>初始化完成度</strong><small>硬件检测与 Compose 模板</small></div><el-progress :percentage="initializedRate" :status="initializedRate===100?'success':undefined"/></div>
              <div class="resource-summary"><span><small>逻辑 CPU</small><b>{{centers.reduce((sum,c)=>sum+(c.cpu_count||0),0)}}</b></span><span><small>物理 GPU</small><b>{{centers.reduce((sum,c)=>sum+(c.gpu_count||0),0)}}</b></span><div><small>计算平台</small><div class="platform-tags"><el-tag v-for="item in acceleratorStats" :key="item.type" size="small" effect="plain">{{acceleratorName(item.type)}} × {{item.count}}</el-tag><span v-if="!acceleratorStats.length" class="muted">暂无数据</span></div></div></div>
            </div>
            <div class="panel recent-task-panel">
              <div class="panel-title"><div><h2>最近任务</h2><p>只保留运行动态摘要，点击任务查看日志。</p></div><el-button type="primary" link @click="selectSection('tasks')">查看全部任务</el-button></div>
              <el-table :data="recentTasks" @row-click="openTask" empty-text="暂无任务" size="small"><el-table-column label="类型" width="130"><template #default="{row}">{{taskTypeName(row.type)}}</template></el-table-column><el-table-column label="目标" min-width="190"><template #default="{row}"><span class="task-target">{{taskTarget(row)}}</span></template></el-table-column><el-table-column label="状态" width="100"><template #default="{row}"><el-tag :type="statusType(row.status)" size="small">{{row.status}}</el-tag></template></el-table-column><el-table-column label="时间" width="170"><template #default="{row}">{{fmt(row.created_at)}}</template></el-table-column></el-table>
            </div>
          </div>
        </div>
      </template>

      <template v-else-if="section==='centers'">
        <div class="toolbar"><div><p>SSH 凭据加密保存在开发机；所有中心共用一套飞书告警 Webhook。</p><el-tag :type="alertCredential.configured?'success':'warning'">{{alertCredential.configured?'飞书告警已配置':'尚未配置飞书告警'}}</el-tag></div><div><el-button :icon="Lock" @click="editAlertCredential">配置飞书告警</el-button><el-button type="primary" :icon="Plus" @click="editCenter()">添加中心</el-button></div></div>
        <div class="cards"><div class="server-card" v-for="c in centers" :key="c.id"><div class="server-head"><div class="server-icon"><Connection/></div><div><h3>{{c.name}}</h3><p>{{c.username}}@{{c.host}}:{{c.port}}</p></div><el-tag size="small" :type="c.initialized_at?'success':'warning'">{{c.initialized_at?'已初始化':'待初始化'}}</el-tag></div><p class="desc">{{c.description||'暂无说明'}}</p><div class="hardware-strip"><span><small>检测平台</small><b>{{acceleratorName(c.detected_accelerator||c.accelerator)}}</b></span><span><small>逻辑 CPU</small><b>{{c.cpu_count||'-'}}</b></span><span><small>物理 GPU</small><b>{{c.gpu_count||'-'}}</b></span></div><div class="detail"><span>Docker Socket</span><code>{{c.docker_socket}}</code><span>Compose 文件</span><code>{{c.compose_file}}</code><span>最近初始化</span><code>{{fmt(c.initialized_at)}}</code></div><div class="actions center-actions"><el-button type="primary" @click="openCenterFromCard(c)">运行详情</el-button><el-button plain :loading="hardwareDetectingId===c.id" :disabled="!!hardwareDetectingId&&hardwareDetectingId!==c.id" @click="detectCenter(c)">检测硬件</el-button><el-button v-if="!c.initialized_at" type="primary" plain @click="initializeCenter(c)">初始化部署</el-button><el-button @click="editCenter(c)">编辑连接</el-button><el-button type="danger" plain @click="removeCenter(c)">删除</el-button></div></div><el-empty v-if="!centers.length" description="尚未添加中心服务器"/></div>
      </template>

      <template v-else-if="section==='terminal'">
        <TerminalPage :centers="centers"/>
      </template>

      <template v-else-if="section==='base-images'">
        <el-alert class="compat-alert" title="安全升级：下载并 docker load 新镜像不会影响正在运行的旧容器；只有勾选“立即重建整套服务”才会切换运行中的容器。" type="success" :closable="false" show-icon/>
        <div class="toolbar"><div><p>公共目录镜像会下发到所有中心；nvidia/ 与 denglin/ 目录只下发给对应显卡中心。</p><el-tag :type="ossCredential.configured?'success':'warning'">{{ossCredential.configured?`${ossCredential.bucket}/${ossCredential.images_prefix}`:'尚未配置 OSS'}}</el-tag></div><el-button :icon="Lock" @click="editOSSCredential">配置 OSS</el-button></div>
        <el-alert v-if="!ossCredential.configured" title="请先配置 OSS Endpoint、Bucket 和访问凭据。" type="warning" :closable="false" show-icon/>
        <template v-else><div class="panel base-image-catalog" v-loading="baseImagesLoading"><div class="panel-title"><div><h2>OSS 基础镜像清单</h2><p>对象 ETag 变化时，即使文件名不变也会重新下载并加载。</p></div><el-button :icon="Refresh" @click="loadBaseImages">刷新清单</el-button></div><el-table :data="baseImages" empty-text="OSS 前缀下没有镜像 tar 包"><el-table-column prop="name" label="镜像包" min-width="300"><template #default="{row}"><code>{{row.name}}</code></template></el-table-column><el-table-column label="适用平台" width="130"><template #default="{row}"><el-tag :type="row.platform==='common'?'info':row.platform==='nvidia'?'success':'warning'">{{row.platform==='common'?'公共':acceleratorName(row.platform)}}</el-tag></template></el-table-column><el-table-column label="大小" width="130"><template #default="{row}">{{formatBytes(row.size)}}</template></el-table-column><el-table-column label="OSS 更新时间" width="190"><template #default="{row}">{{fmt(row.last_modified)}}</template></el-table-column><el-table-column label="ETag" min-width="180"><template #default="{row}"><code>{{row.etag}}</code></template></el-table-column></el-table></div><div class="panel base-image-targets"><div class="panel-title"><div><h2>批量更新中心</h2><p>每个中心会自动检测显卡，只接收公共镜像和自身平台镜像。</p></div></div><el-transfer v-model="baseImageForm.center_ids" class="center-transfer" :data="centerTransferData" :titles="['所有中心','更新目标']" :button-texts="['移除','选择']" filterable/><div class="base-image-options"><el-checkbox v-model="baseImageForm.restart_stack">加载完成后立即重建整套服务</el-checkbox><span v-if="!baseImageForm.restart_stack">推荐：仅加载镜像，现有容器保持运行</span><span v-else class="danger-text">会执行 compose up --force-recreate，服务将短暂中断</span><el-button type="primary" size="large" :disabled="!baseImageForm.center_ids.length||!baseImages.length" @click="upgradeBaseImages">开始批量更新</el-button></div></div><div v-if="baseImageTasks.length" class="panel deployment-results"><div class="panel-title"><div><h2>本次基础镜像更新</h2><p>各中心独立执行，点击行查看实时下载和加载日志。</p></div><el-button :icon="Refresh" @click="refreshBaseImageTasks">刷新状态</el-button></div><el-table :data="baseImageTasks" @row-click="openTask"><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.params?.center_name}}</strong></template></el-table-column><el-table-column label="模式" width="160"><template #default="{row}">{{row.params?.restart_stack==='true'?'加载并重建':'仅加载镜像'}}</template></el-table-column><el-table-column label="进度" width="200"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)" :status="row.status==='FAILED'?'exception':row.status==='SUCCESS'?'success':undefined"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)" effect="dark">{{row.status}}</el-tag></template></el-table-column><el-table-column label="结果 / 错误" min-width="280"><template #default="{row}"><span :class="{'deploy-error':row.status==='FAILED'}">{{row.error||row.message||'等待执行'}}</span></template></el-table-column></el-table></div></template>
      </template>

      <template v-else-if="section==='models'">
        <el-alert class="compat-alert" title="模型仓库只负责上传和管理版本；需要部署或更新模型时，请前往版本发布。" type="info" :closable="false" show-icon/>
        <div class="toolbar">
          <div><p>模型与基础镜像共用 OSS Endpoint、Bucket 和加密访问凭据。</p><el-tag :type="ossCredential.configured?'success':'warning'">{{ossCredential.configured?`${ossCredential.bucket}/${ossCredential.models_prefix}`:'尚未配置 OSS'}}</el-tag></div>
          <div><el-button :icon="Lock" @click="editOSSCredential">配置 OSS</el-button><el-button type="primary" :icon="UploadFilled" :disabled="!ossCredential.configured" @click="openModelUpload()">新建模型</el-button></div>
        </div>
        <el-alert v-if="!ossCredential.configured" title="请先配置 OSS Endpoint、Bucket、访问凭据和模型前缀。" type="warning" :closable="false" show-icon/>
        <template v-else><ModelCatalog :models="models" :loading="modelsLoading" :selectable="false" @delete-model="deleteModelFolder" @refresh="loadModels" @upload-version="openModelUpload"/></template>
      </template>
      <template v-else-if="section==='sources'">
        <div class="toolbar"><div><p>所有同一 Git 服务下的软件仓库共用一套加密访问令牌。</p><el-tag :type="gitCredential.configured?'success':'warning'" effect="plain">{{gitCredential.configured?`已配置：${gitCredential.base_url}`:'尚未配置访问令牌'}}</el-tag></div><div><el-button :icon="Lock" @click="editGitCredential">配置 Git 令牌</el-button><el-button type="primary" :icon="Plus" @click="editSource()">添加软件仓库</el-button></div></div>
        <div class="panel"><el-table :data="sources" empty-text="暂无软件仓库"><el-table-column prop="name" label="软件" width="180"/><el-table-column prop="repository" label="Git 仓库" min-width="260"><template #default="{row}"><code>{{row.repository}}</code></template></el-table-column><el-table-column prop="description" label="说明"/><el-table-column label="操作" width="160"><template #default="{row}"><el-button @click="editSource(row)">编辑</el-button><el-button type="danger" link @click="removeSource(row)">删除</el-button></template></el-table-column></el-table></div>
      </template>

      <template v-else-if="section==='releases'">
        <el-alert class="compat-alert" title="软件和模型统一发布：至少选择一种。只选择模型时执行纯模型更新；只选择软件时会校验中心已有模型是否健康。" type="success" :closable="false" show-icon/>
        <div class="panel build-page" v-loading="deploymentVersionsLoading" element-loading-text="正在读取 Git 分支和标签…">
          <el-alert v-if="!sources.length" title="尚未添加软件仓库，仍可只选择模型执行纯模型更新。" type="info" :closable="false" show-icon/>
          <el-form :model="deployForm" label-position="top" class="build-form">
            <div class="build-step"><div class="step-number">1</div><div class="step-content"><h3>选择需要发布的软件（可选）</h3><p class="table-subtitle">不选择软件、只选择模型时，本次发布就是纯模型更新。</p><el-checkbox-group v-model="deployForm.source_ids" class="software-check-list" @change="deploySourcesChanged"><div v-for="software in sources" :key="software.id" class="software-check-row" :class="{disabled:!(gitVersions[software.id!]||[]).length}"><el-checkbox :value="software.id!" :disabled="!(gitVersions[software.id!]||[]).length"><span class="software-name">{{software.name}}</span></el-checkbox><code>{{software.repository}}</code><el-tag v-if="gitVersionErrors[software.id!]" size="small" type="danger">读取失败</el-tag><el-tag v-else size="small" :type="(gitVersions[software.id!]||[]).length?'success':'warning'">{{(gitVersions[software.id!]||[]).length}} 个远端版本</el-tag></div></el-checkbox-group></div></div>
            <div class="build-step"><div class="step-number">2</div><div class="step-content"><h3>锁定源码版本</h3><el-table :data="deployForm.items" empty-text="请先从上方列表勾选软件" border><el-table-column label="软件" width="190"><template #default="{row}"><strong>{{sources.find(v=>v.id===row.source_id)?.name}}</strong></template></el-table-column><el-table-column label="Git 分支 / 标签" min-width="300"><template #default="{row}"><el-select :model-value="deployItemKey(row)" filterable style="width:100%" placeholder="选择远端版本" @change="selectDeployVersion(row,$event)"><el-option v-for="version in gitVersions[row.source_id]||[]" :key="softwareOptionKey(version)" :label="`${version.kind==='tag'?'标签':'分支'} · ${version.ref} · ${version.commit.slice(0,8)}`" :value="softwareOptionKey(version)"/></el-select></template></el-table-column><el-table-column label="锁定 Commit" min-width="180"><template #default="{row}"><code>{{selectedDeployVersion(row)?.commit.slice(0,8)||'-'}}</code></template></el-table-column></el-table><p class="table-subtitle">源码会在目标中心缺少精确镜像、需要构建时自动准备，无需提前下载。</p></div></div>
            <div class="build-step"><div class="step-number">3</div><div class="step-content"><h3>选择模型版本（可选）</h3><el-alert v-if="!ossCredential.configured" title="OSS 尚未配置，不能选择新模型；发布软件时将校验并沿用中心当前健康模型。" type="warning" :closable="false"/><ModelCatalog v-else v-model="deployForm.model_keys" :models="models" :loading="modelsLoading" @update:model-value="invalidateDeploymentPlan" @refresh="loadModels" @upload-version="openModelUpload"/><p class="table-subtitle">选择模型后会按中心硬件自动匹配 ONNX、NVIDIA plan 或登临 engine，并在发布结束时重启 Compose。</p></div></div>
            <div class="build-step"><div class="step-number">4</div><div class="step-content"><h3>选择发布中心</h3><el-transfer v-model="deployForm.center_ids" class="center-transfer" :data="centerTransferData" :titles="['所有中心','发布目标']" :button-texts="['移除','选择']" filterable filter-placeholder="搜索中心" @change="invalidateDeploymentPlan"/><div class="deploy-plan-action"><el-button type="primary" plain :loading="deploymentPlanning" :disabled="(!deployForm.items.length&&!deployForm.model_keys.length)||!deployForm.center_ids.length" @click="planDeployment">生成发布计划</el-button><span>只读检查软件镜像、Compose 映射、模型完整性和硬件兼容性。</span></div></div></div>
            <div v-if="deploymentPlan.length" class="build-step"><div class="step-number">5</div><div class="step-content"><h3>确认发布计划</h3><el-table :data="deploymentPlan" border><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.center.name}}</strong><small class="table-subtitle">{{row.center.host}}:{{row.center.port}}</small></template></el-table-column><el-table-column label="执行方式" width="150"><template #default="{row}"><el-tag v-if="!row.ready" type="info">本次跳过</el-tag><el-tag v-else-if="!deployForm.items.length" type="warning">纯模型更新</el-tag><el-tag v-else-if="deployForm.model_keys.length" type="primary">软件 + 模型</el-tag><el-tag v-else type="success">仅软件</el-tag></template></el-table-column><el-table-column label="软件检查" min-width="240"><template #default="{row}"><span v-if="!deployForm.items.length">未选择软件</span><span v-else-if="row.build_required">需要构建：{{row.missing_images.join('、')}}</span><span v-else>所选 commit 的镜像均已就绪</span></template></el-table-column><el-table-column label="模型检查" min-width="260"><template #default="{row}"><span :class="{'deploy-error':!row.model_plan?.ready}">{{modelPlanSummary(row)}}</span></template></el-table-column><el-table-column label="结果" min-width="280"><template #default="{row}"><span v-if="!row.ready" class="deploy-error">{{deploymentIssue(row)}}</span><span v-else>可以发布</span></template></el-table-column></el-table><div class="build-options"><span>通过复检的中心会分别创建独立任务，不通过的中心自动跳过。</span><el-tag type="primary">{{deploymentPlan.length-unavailableDeploymentCenters.length}} 个可发布 / {{unavailableDeploymentCenters.length}} 个跳过</el-tag></div></div></div>
            <div class="build-submit"><el-button type="primary" size="large" :disabled="!deploymentPlan.some(item=>item.ready)" @click="startDeployment">开始版本发布</el-button></div>
          </el-form>
        </div>
        <div v-if="deploymentTasks.length" class="panel deployment-results"><div class="panel-title"><div><h2>本次版本发布</h2><p>点击中心查看构建与部署各阶段日志。</p></div><el-button :icon="Refresh" @click="refreshDeploymentTasks">刷新状态</el-button></div><el-table :data="deploymentTasks" @row-click="openTask"><el-table-column label="中心" min-width="180"><template #default="{row}"><strong>{{row.params?.center_name||row.params?.center_id}}</strong></template></el-table-column><el-table-column label="发布版本" min-width="300"><template #default="{row}">{{row.params?.versions}}</template></el-table-column><el-table-column label="进度" width="180"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)" :status="row.status==='FAILED'?'exception':row.status==='SUCCESS'?'success':undefined"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)" effect="dark">{{row.status}}</el-tag></template></el-table-column><el-table-column label="当前阶段 / 失败原因" min-width="320"><template #default="{row}"><span :class="{'deploy-error':row.status==='FAILED'}">{{row.error||row.message||'等待执行'}}</span></template></el-table-column></el-table></div>
      </template>

      <template v-else>
        <div class="toolbar task-toolbar"><p>已选 {{selectedTasks.length}} 项：{{selectedActiveTasks.length}} 个可停止，{{selectedFinishedTasks.length}} 条记录可删除。</p><div><el-button type="warning" :disabled="!selectedActiveTasks.length||taskActionBusy" @click="stopTasks(selectedActiveTasks)">停止选中任务</el-button><el-button type="danger" :disabled="taskActionBusy" @click="stopTasks([],true)">停止所有任务</el-button><el-button :disabled="!selectedFinishedTasks.length||taskActionBusy" @click="deleteTasks(selectedFinishedTasks)">删除记录</el-button></div></div><div class="panel"><el-table :data="tasks" row-key="id" @row-click="(row:Task,column:any)=>{if(column?.type!=='selection'&&column?.label!=='操作')openTask(row)}" @selection-change="(rows:Task[])=>selectedTasks=rows" empty-text="暂无任务"><el-table-column type="selection" width="48" :reserve-selection="true"/><el-table-column label="类型" width="190"><template #default="{row}">{{taskTypeName(row.type)}}</template></el-table-column><el-table-column label="目标" min-width="340"><template #default="{row}"><el-tooltip :content="taskTarget(row)" placement="top" :show-after="400"><div class="task-list-target"><span>{{taskTargetSummary(row)}}</span><small v-if="taskTargetDetail(row)">{{taskTargetDetail(row)}}</small></div></el-tooltip></template></el-table-column><el-table-column label="进度" width="180"><template #default="{row}"><el-progress :percentage="Math.round(row.progress||0)"/></template></el-table-column><el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="statusType(row.status)">{{row.status}}</el-tag></template></el-table-column><el-table-column label="日志" width="100"><template #default="{row}"><el-tag v-if="row.log_count===0" type="info">无日志</el-tag><span v-else>{{row.log_count}} 条</span></template></el-table-column><el-table-column label="创建时间" width="190"><template #default="{row}">{{fmt(row.created_at)}}</template></el-table-column><el-table-column label="操作" width="130" fixed="right"><template #default="{row}"><el-button v-if="isActiveTask(row)" type="warning" link :disabled="taskActionBusy" @click.stop="stopTasks([row])">停止</el-button><el-button v-if="row.status==='FAILED'" type="primary" link :disabled="taskActionBusy" @click.stop="retryTask(row)">重试</el-button><span v-if="!isActiveTask(row)&&row.status!=='FAILED'" class="muted">-</span></template></el-table-column></el-table></div>
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
      <div class="form-grid"><el-form-item label="名称"><el-input v-model="centerForm.name"/></el-form-item><el-form-item label="SSH 主机"><el-input v-model="centerForm.host"/></el-form-item><el-form-item label="SSH 端口"><el-input-number v-model="centerForm.port" :min="1" :max="65535"/></el-form-item><el-form-item label="用户名"><el-input v-model="centerForm.username"/></el-form-item></div>
      <el-form-item label="认证方式"><el-radio-group v-model="centerForm.auth_type"><el-radio-button value="password">密码</el-radio-button><el-radio-button value="private_key">私钥</el-radio-button></el-radio-group></el-form-item>
      <el-form-item v-if="centerForm.auth_type==='password'" label="SSH 密码"><el-input v-model="centerForm.password" type="password" show-password placeholder="编辑时留空保留原密码"/></el-form-item>
      <template v-else><el-form-item label="私钥 PEM"><el-input v-model="centerForm.private_key" type="textarea" :rows="5" placeholder="编辑时留空保留原私钥"/></el-form-item><el-form-item label="私钥口令"><el-input v-model="centerForm.passphrase" type="password" show-password/></el-form-item></template>
      <el-form-item label="Docker Socket"><el-input v-model="centerForm.docker_socket"/></el-form-item>
      <el-form-item label="加速卡类型"><el-select v-model="centerForm.accelerator" style="width:100%"><el-option label="自动识别（推荐）" value="auto"/><el-option label="NVIDIA" value="nvidia"/><el-option label="登临" value="denglin"/><el-option label="仅 CPU" value="cpu"/></el-select></el-form-item>
      <el-form-item label="Compose 文件"><el-input v-model="centerForm.compose_file" placeholder="例如 ~/.local/share/center-manager/deploy/docker-compose.yml"/><div class="form-tip">默认使用 SSH 用户家目录；初始化后会保存解析出的绝对路径。文件已存在时只导入，不会自动覆盖。</div></el-form-item>
      <el-form-item label="说明"><el-input v-model="centerForm.description" type="textarea"/></el-form-item>
    </el-form>
    <template #footer><el-button @click="centerDialog=false">取消</el-button><el-button type="primary" @click="saveCenter">保存</el-button></template>
  </el-dialog>
  <el-dialog v-model="sourceDialog" :title="sourceForm.id?'编辑软件仓库':'添加软件仓库'" width="620px"><el-form :model="sourceForm" label-width="120px"><el-form-item label="软件名称"><el-input v-model="sourceForm.name"/><div class="form-tip">软件名即默认镜像名，需与中心 Compose 模板引用的镜像名一致，例如 center-cv-service。</div></el-form-item><el-form-item label="Git 地址"><el-input v-model="sourceForm.repository" placeholder="ssh://git@host:port/group/repo.git 或 https://..."/></el-form-item><el-form-item label="说明"><el-input v-model="sourceForm.description" type="textarea"/></el-form-item></el-form><template #footer><el-button @click="sourceDialog=false">取消</el-button><el-button type="primary" @click="saveSource">保存</el-button></template></el-dialog>
  <el-dialog v-model="gitCredentialDialog" title="全局 Git 访问令牌" width="620px"><el-alert title="SSH 仓库地址会按路径自动转换到这里配置的 HTTP(S) Git 服务；令牌仅发送给相同主机。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="gitCredentialForm" label-width="110px"><el-form-item label="Git 服务地址"><el-input v-model="gitCredentialForm.base_url" placeholder="例如 https://code.jms.com"/></el-form-item><el-form-item label="Git 用户名"><el-input v-model="gitCredentialForm.username" placeholder="GitLab 令牌通常可使用 oauth2"/></el-form-item><el-form-item label="访问令牌"><el-input v-model="gitCredentialForm.token" type="password" show-password placeholder="编辑时保留掩码即可沿用原令牌"/></el-form-item></el-form><template #footer><el-button @click="gitCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveGitCredential">保存全局令牌</el-button></template></el-dialog>
  <el-dialog v-model="ossCredentialDialog" title="OSS 资源仓库" width="650px"><el-alert title="基础镜像和模型共用同一套加密凭据，分别从各自前缀读取。前缀需以 / 结尾。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="ossCredentialForm" label-width="130px"><el-form-item label="Endpoint"><el-input v-model="ossCredentialForm.endpoint" placeholder="例如 https://oss-cn-hangzhou.aliyuncs.com"/></el-form-item><el-form-item label="Bucket"><el-input v-model="ossCredentialForm.bucket"/></el-form-item><el-form-item label="AccessKey ID"><el-input v-model="ossCredentialForm.access_key_id"/></el-form-item><el-form-item label="AccessKey Secret"><el-input v-model="ossCredentialForm.access_key_secret" type="password" show-password placeholder="编辑时保留掩码即可沿用"/></el-form-item><el-form-item label="镜像前缀"><el-input v-model="ossCredentialForm.images_prefix" placeholder="center-images/"/></el-form-item><el-form-item label="模型前缀"><el-input v-model="ossCredentialForm.models_prefix" placeholder="center-models/"/></el-form-item></el-form><template #footer><el-button @click="ossCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveOSSCredential">保存并读取清单</el-button></template></el-dialog>
  <el-dialog v-model="alertCredentialDialog" title="全局飞书告警配置" width="650px"><el-alert title="所有中心共用这个机器人 Webhook；告警中的服务器名称仍自动使用各中心的名称。" type="info" :closable="false" style="margin-bottom:18px"/><el-form :model="alertCredentialForm" label-width="130px"><el-form-item label="机器人 Webhook"><el-input v-model="alertCredentialForm.feishu_webhook" type="password" show-password placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."/><div class="form-tip">飞书群 → 设置 → 群机器人 → 添加机器人 → 自定义机器人。Webhook 会加密保存。</div></el-form-item></el-form><template #footer><el-button @click="alertCredentialDialog=false">取消</el-button><el-button type="primary" @click="saveAlertCredential">保存全局 Webhook</el-button></template></el-dialog>
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
        <el-descriptions :column="3" border><el-descriptions-item label="Docker 状态"><el-tag :type="activeCenterOverview.online?'success':'danger'">{{activeCenterOverview.online?'在线':'异常'}}</el-tag></el-descriptions-item><el-descriptions-item label="加速卡">{{acceleratorName(activeCenterOverview.accelerator)}}</el-descriptions-item><el-descriptions-item label="Compose 文件"><code>{{activeCenterOverview.center.compose_file}}</code></el-descriptions-item></el-descriptions>
        <div class="center-runtime-actions">
          <div class="center-runtime-actions-head">
            <strong>配置与运行控制</strong>
            <small>重新生成模板需要确认；运行操作还需验证管理员密码，所有变更都会记录为任务。</small>
          </div>
          <div class="center-runtime-toolbar">
            <div class="center-runtime-action-group">
              <el-button :icon="Refresh" :loading="centerOverviewLoading||modelsLoading" @click="refreshCenterManagement">刷新</el-button>
              <el-button @click="openCompose(activeCenterOverview.center)">编辑 Compose</el-button>
              <el-button plain @click="syncCenterTemplates(activeCenterOverview.center)">同步内置配置</el-button>
              <el-button type="warning" plain @click="initializeCenter(activeCenterOverview.center,true)">按硬件重新生成模板</el-button>
              <el-button type="primary" @click="runCenterAction('redeploy_compose')">重新部署 Compose</el-button>
              <el-button type="warning" plain @click="runCenterAction('restart_compose')">重启整套 Compose</el-button>
            </div>
            <div class="center-runtime-action-group center-runtime-action-group--danger">
              <el-button type="danger" plain @click="runCenterAction('stop_compose')">停止整套 Compose</el-button>
              <el-button type="danger" @click="runCenterAction('remove_all_containers')">删除中心全部容器</el-button>
            </div>
          </div>
        </div>
        <section class="detail-section">
          <div class="detail-section-title"><div><h3>软件版本</h3><p>同时显示该中心当前运行版本与 Git 分支/标签；未下载的 Git 版本会在发布前自动下载。</p></div></div>
          <el-table :data="centerSoftwareRows" class="center-version-table" empty-text="尚未配置软件仓库，且中心没有可识别的已部署软件">
            <el-table-column label="软件" min-width="170"><template #default="{row}"><strong>{{row.source?.name||row.current[0]?.source_name}}</strong><small v-if="row.gitError" class="table-subtitle deploy-error">Git：{{row.gitError}}</small><small v-else-if="row.source" class="table-subtitle">Git {{row.options.filter((item:any)=>item.origin==='git').length}} 个版本</small><small v-else class="table-subtitle">仅在中心检测到，软件仓库已被移除或未配置</small></template></el-table-column>
            <el-table-column label="中心已部署" min-width="210"><template #default="{row}"><template v-if="row.current.length"><div v-for="item in row.current" :key="`${item.version}-${item.commit}`" class="deployed-version"><el-tag size="small" type="success">已部署</el-tag><span>{{item.version}}</span><small>{{item.commit?.slice(0,8)||'-'}} · {{item.container_count}} 个容器</small></div></template><span v-else class="muted">尚未部署</span></template></el-table-column>
            <el-table-column label="Git / 本地可发布版本" min-width="420"><template #default="{row}"><el-select v-if="row.source" v-model="centerSoftwareSelections[row.key]" filterable placeholder="选择 Git 分支、标签或本地缓存" style="width:100%"><el-option v-for="item in row.options" :key="item.key" :label="`${item.ref} · ${item.commit.slice(0,8)} · ${item.kind==='tag'?'标签':item.origin==='local'?'本地缓存':'分支'}${item.downloaded?' · 已下载':''}`" :value="item.key"/></el-select><span v-else class="muted">请重新配置对应软件仓库后再发布</span></template></el-table-column>
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
        <div v-if="hasCenterVersionChanges" class="center-version-publish-bar"><span>已选择变更：{{changedCenterSoftwareRows.length}} 个软件版本、{{changedCenterModelRows.length}} 个模型版本</span><el-tooltip :disabled="!centerVersionChangesBlocked" :content="centerModelSetPinned?'模型版本组已固定，请先整体解绑':'所选模型版本与中心平台不兼容'"><span><el-button type="primary" size="large" :loading="centerVersionApplying==='all'" :disabled="!!centerVersionApplying||centerVersionChangesBlocked" @click="applyCenterVersionChanges">发布</el-button></span></el-tooltip></div>
        <section class="detail-section"><h3>运行中的容器</h3><el-table :data="activeCenterOverview.containers" empty-text="当前没有运行中的容器"><el-table-column label="容器" min-width="170"><template #default="{row}"><strong>{{(row.names||[]).join(', ')||row.id.slice(0,12)}}</strong></template></el-table-column><el-table-column prop="image" label="镜像" min-width="240"><template #default="{row}"><code>{{row.image}}</code></template></el-table-column><el-table-column prop="status" label="Docker 状态" min-width="180"/><el-table-column label="操作" width="110" fixed="right"><template #default="{row}"><el-button type="warning" link @click="runCenterAction('restart_container',row)">重启</el-button></template></el-table-column></el-table></section>
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
  <ModelUploadDialog v-model="modelUploadDialog" :model-name="modelUploadTarget" @uploaded="loadModels"/>
  <el-drawer v-model="logDrawer" size="58%" @closed="closeLogs"><template #header><div><h3>{{activeTask?.type}}</h3><span>{{activeTask?.id}}</span></div></template><div class="task-summary"><el-tag :type="statusType(activeTask?.status||'')">{{activeTask?.status}}</el-tag><el-progress :percentage="Math.round(activeTask?.progress||0)"/><span>{{activeTask?.message}}</span></div><div v-if="activeTask" class="task-actions"><el-button v-if="isActiveTask(activeTask)" type="warning" :disabled="taskActionBusy" @click="stopTasks([activeTask])">停止任务</el-button><el-button v-if="activeTask.status==='FAILED'" type="primary" :disabled="taskActionBusy" @click="retryTask(activeTask)">重试任务</el-button></div><pre class="logs">{{taskLogs.join('\n')||(activeTask&&isActiveTask(activeTask)?'等待日志…':'暂无日志')}}</pre></el-drawer>
</template>
