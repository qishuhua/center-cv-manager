<script setup lang="ts">
import {ref, reactive, computed, nextTick} from 'vue'
import {ElMessage} from 'element-plus'
import http from './api'
import type {Center, Source, Version, ModelVersion, Task} from './types'
const emit=defineEmits<{task:[task:Task]}>()
const visible=ref(false), loading=ref(false), submitting=ref(false), center=ref<Center>()
const sources=ref<Source[]>([]), models=ref<ModelVersion[]>([]), modelKeys=ref<string[]>([])
const versions=reactive<Record<string,Version[]>>({}), selected=reactive<Record<string,string>>({})
const error=ref('')
const modelNames=computed(()=>[...new Set(models.value.map(m=>m.model))])
function chooseModel(name:string,value:string){modelKeys.value=modelKeys.value.filter(k=>!models.value.some(m=>m.model===name&&m.manifest_key===k));if(value)modelKeys.value.push(value)}
const key=(v:Version)=>JSON.stringify([v.kind,v.ref,v.commit])
// 用户未手动选择前，分支默认 master/main，模型默认最新日期的版本
const branchDefaulted=new Set<string>()
function defaultBranch(id:string){if(branchDefaulted.has(id)||selected[id])return;branchDefaulted.add(id);const list=versions[id]||[];const def=list.find(v=>v.kind==='branch'&&(v.ref==='master'||v.ref==='main'))||list[0];if(def)selected[id]=key(def)}
function defaultModels(){const names=[...new Set(models.value.map(m=>m.model))];for(const name of names){const latest=models.value.filter(m=>m.model===name).sort((a,b)=>(b.created_at||'').localeCompare(a.created_at||''))[0];if(latest&&!modelKeys.value.includes(latest.manifest_key))modelKeys.value.push(latest.manifest_key)}}
async function refreshVersions(id:string){try{const data=await http.get(`/sources/${id}/versions`);versions[id]=Array.isArray(data)?data:[]}catch(e:any){versions[id]=[];error.value=`读取代码分支失败：${e.message}`};defaultBranch(id)}
async function open(value:Center){
 center.value=value;visible.value=true;loading.value=true;error.value='';modelKeys.value=[]
 sources.value=[];models.value=[]
 branchDefaulted.clear()
 Object.keys(selected).forEach(id=>delete selected[id])
 Object.keys(versions).forEach(id=>delete versions[id])
 await nextTick()
 try{
  const results=await Promise.allSettled([http.get('/sources'),http.get('/models')])
  const [software,model]=results
  sources.value=software.status==='fulfilled'&&Array.isArray(software.value)?software.value:[]
  models.value=model.status==='fulfilled'&&Array.isArray(model.value)?model.value:[]
  error.value=results.flatMap((r,i)=>r.status==='rejected'?[`${i===0?'读取代码仓库':'读取模型版本'}失败：${r.reason?.message||r.reason}`]:[]).join('；')
  await Promise.all(sources.value.filter(s=>s.id).map(s=>refreshVersions(s.id!)))
  defaultModels()
 }finally{loading.value=false}
}
async function submit(){
 if(submitting.value)return
 const items=sources.value.flatMap(s=>{const v=(versions[s.id!]||[]).find(v=>key(v)===selected[s.id!]);return v?[{source_id:s.id,...v}]:[]})
 const model_selections=models.value.filter(m=>modelKeys.value.includes(m.manifest_key)).map(m=>({manifest_key:m.manifest_key,manifest_digest:m.manifest_digest}))
 if(!items.length||!model_selections.length){ElMessage.warning('请选择业务代码分支和模型版本');return}
 submitting.value=true;error.value=''
 try{const task:Task=await http.post(`/centers/${center.value!.id}/initialize`,{items,model_selections});visible.value=false;emit('task',task)}catch(e:any){error.value=e.message}finally{submitting.value=false}
}
defineExpose({open})
</script>
<template>
 <el-dialog v-model="visible" :title="`${center?.name||''} · 初始化部署`" width="900px" :close-on-click-modal="false" :close-on-press-escape="!submitting" :show-close="!submitting" append-to-body>
  <div v-loading="loading">
   <el-alert title="先选择业务代码分支和模型版本。确认后自动检测硬件、准备配置与基础镜像、构建业务镜像并部署全部服务。" type="info" :closable="false"/>
   <el-alert v-if="error" :title="error" type="error" :closable="false"/>
   <h3>业务代码分支 / 标签</h3>
   <el-empty v-if="!loading&&!sources.length" description="请先在代码仓库中添加业务代码仓库"/>
   <el-form label-width="160px" :disabled="submitting">
    <el-form-item v-for="s in sources" :key="s.id" :label="s.name">
     <el-select v-model="selected[s.id!]" clearable placeholder="选择要部署的分支或标签" style="width:100%" @visible-change="(show:boolean)=>show&&refreshVersions(s.id!)">
      <el-option v-for="v in versions[s.id!]||[]" :key="key(v)" :value="key(v)" :label="`${v.kind==='branch'?'分支':'标签'} ${v.ref} · ${v.commit.slice(0,8)}`"/>
     </el-select>
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
