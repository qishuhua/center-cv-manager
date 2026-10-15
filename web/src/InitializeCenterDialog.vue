<script setup lang="ts">
import {ref, reactive, computed, nextTick} from 'vue'
import {ElMessage} from 'element-plus'
import http from './api'
import type {Center, Source, ModelVersion, RegistrySoftwareVersion, RegistrySourceVersions, Task} from './types'
const emit=defineEmits<{task:[task:Task]}>()
const visible=ref(false), loading=ref(false), submitting=ref(false), center=ref<Center>()
const sources=ref<Source[]>([]), models=ref<ModelVersion[]>([]), modelKeys=ref<string[]>([])
const registryVersions=ref<RegistrySourceVersions[]>([]), selected=reactive<Record<string,string>>({})
const error=ref('')
const modelNames=computed(()=>[...new Set(models.value.map(m=>m.model))])
function chooseModel(name:string,value:string){modelKeys.value=modelKeys.value.filter(k=>!models.value.some(m=>m.model===name&&m.manifest_key===k));if(value)modelKeys.value.push(value)}
// 中心只认镜像版本与模型版本：业务软件从镜像仓库已发布版本中选择
const versionsFor=(id:string)=>registryVersions.value.find(item=>item.source_id===id)?.versions||[]
const versionKey=(v:RegistrySoftwareVersion)=>`${v.ref}:${v.commit}`
const versionLabel=(v:RegistrySoftwareVersion)=>`${v.version} · ${v.platforms.map(tag=>tag.platform).join('/')} · ${v.created_at?new Date(v.created_at).toLocaleString():'-'}`
function defaultVersions(){for(const item of registryVersions.value){if(!item.source_id||selected[item.source_id])continue;const latest=item.versions[0];if(latest)selected[item.source_id]=versionKey(latest)}}
function defaultModels(){const names=[...new Set(models.value.map(m=>m.model))];for(const name of names){const latest=models.value.filter(m=>m.model===name).sort((a,b)=>(b.created_at||'').localeCompare(a.created_at||''))[0];if(latest&&!modelKeys.value.includes(latest.manifest_key))modelKeys.value.push(latest.manifest_key)}}
async function open(value:Center){
 center.value=value;visible.value=true;loading.value=true;error.value='';modelKeys.value=[]
 sources.value=[];models.value=[];registryVersions.value=[]
 Object.keys(selected).forEach(id=>delete selected[id])
 await nextTick()
 try{
  const results=await Promise.allSettled([http.get('/sources'),http.get('/models'),http.get('/registry/versions')])
  const [software,model,versions]=results
  sources.value=software.status==='fulfilled'&&Array.isArray(software.value)?software.value:[]
  models.value=model.status==='fulfilled'&&Array.isArray(model.value)?model.value:[]
  registryVersions.value=versions.status==='fulfilled'&&Array.isArray(versions.value)?versions.value:[]
  error.value=results.flatMap((r,i)=>r.status==='rejected'?[`${['读取软件仓库','读取模型版本','读取镜像仓库版本'][i]}失败：${r.reason?.message||r.reason}`]:[]).join('；')
  defaultVersions()
  defaultModels()
 }finally{loading.value=false}
}
async function submit(){
 if(submitting.value)return
 const items=sources.value.flatMap(s=>{const v=versionsFor(s.id!).find(v=>versionKey(v)===selected[s.id!]);return v?[{source_id:s.id,ref:v.ref,commit:v.commit}]:[]})
 const model_selections=models.value.filter(m=>modelKeys.value.includes(m.manifest_key)).map(m=>({manifest_key:m.manifest_key,manifest_digest:m.manifest_digest}))
 if(!items.length||!model_selections.length){ElMessage.warning('请选择业务镜像版本和模型版本');return}
 submitting.value=true;error.value=''
 try{const task:Task=await http.post(`/centers/${center.value!.id}/initialize`,{items,model_selections});visible.value=false;emit('task',task)}catch(e:any){error.value=e.message}finally{submitting.value=false}
}
defineExpose({open})
</script>
<template>
 <el-dialog v-model="visible" :title="`${center?.name||''} · 初始化部署`" width="900px" :close-on-click-modal="false" :close-on-press-escape="!submitting" :show-close="!submitting" append-to-body>
  <div v-loading="loading">
   <el-alert title="先选择业务镜像版本和模型版本。确认后自动检测硬件、准备配置与基础镜像、从镜像仓库分发业务镜像并部署全部服务。" type="info" :closable="false"/>
   <el-alert v-if="error" :title="error" type="error" :closable="false"/>
   <h3>业务镜像版本</h3>
   <el-empty v-if="!loading&&!sources.length" description="请先在软件仓库中添加业务软件"/>
   <el-empty v-else-if="!loading&&!registryVersions.length" description="镜像仓库尚无已发布版本，请先在软件仓库页构建发布"/>
   <el-form label-width="160px" :disabled="submitting">
    <el-form-item v-for="s in sources" :key="s.id" :label="s.name">
     <el-select v-if="versionsFor(s.id!).length" v-model="selected[s.id!]" clearable placeholder="选择要部署的镜像版本" style="width:100%">
      <el-option v-for="v in versionsFor(s.id!)" :key="versionKey(v)" :value="versionKey(v)" :label="versionLabel(v)"/>
     </el-select>
     <span v-else class="form-tip">镜像仓库暂无该软件的已发布版本</span>
    </el-form-item>
   </el-form>
   <h3>模型版本</h3>
   <el-empty v-if="!loading&&!models.length" description="请先在模型仓库中上传模型版本"/>
   <el-form label-width="160px" :disabled="submitting">
    <el-form-item v-for="name in modelNames" :key="name" :label="name">
     <el-select :model-value="modelKeys.find(k=>models.some(m=>m.model===name&&m.manifest_key===k))||''" clearable placeholder="选择模型版本" style="width:100%" @update:model-value="(value:string)=>chooseModel(name,value)">
      <el-option v-for="m in models.filter(m=>m.model===name)" :key="m.manifest_key" :value="m.manifest_key" :label="m.version"/>
     </el-select>
    </el-form-item>
   </el-form>
  </div>
  <template #footer><el-button :disabled="submitting" @click="visible=false">取消</el-button><el-button type="primary" :loading="submitting" :disabled="loading||!sources.length||!models.length" @click="submit">开始初始化部署</el-button></template>
 </el-dialog>
</template>
