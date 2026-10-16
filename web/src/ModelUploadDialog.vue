<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { ModelPlatform, ModelUpload, ModelUploadFormat } from './types'
import http from './api'

const props=withDefaults(defineProps<{modelValue:boolean;modelName?:string}>(),{modelName:''})
const emit=defineEmits<{ 'update:modelValue':[value:boolean]; uploaded:[] }>()
type FormatForm={key:string;platform:ModelPlatform;hardwareProfile:string;modelFile?:File}
const visible=computed({get:()=>props.modelValue,set:value=>emit('update:modelValue',value)})
const form=reactive({model:'',version:''})
const formats=ref<FormatForm[]>([])
const uploading=ref(false),progress=ref(0),message=ref('')

function defaultVersion(){const d=new Date();const p=(v:number)=>String(v).padStart(2,'0');return `${d.getFullYear()}.${p(d.getMonth()+1)}.${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`}
// crypto.randomUUID 仅安全上下文（https / localhost）可用，局域网 IP 的
// HTTP 访问下为 undefined——用降级实现，否则对话框在挂载时即抛错打不开。
function newKey(){return typeof crypto!=='undefined'&&typeof crypto.randomUUID==='function'?crypto.randomUUID():`key-${Date.now().toString(36)}-${Math.random().toString(36).slice(2,10)}`}
function reset(){form.model=props.modelName;form.version=defaultVersion();formats.value=[{key:newKey(),platform:'onnx',hardwareProfile:''}];progress.value=0;message.value=''}
watch(()=>props.modelValue,value=>{if(value)reset()})
function addFormat(platform:ModelPlatform){formats.value.push({key:newKey(),platform,hardwareProfile:''})}
function removeFormat(key:string){formats.value=formats.value.filter(item=>item.key!==key)}
const modelExtension=(platform:ModelPlatform)=>platform==='onnx'?'onnx':platform==='denglin'?'engine':'plan'
const platformName=(platform:ModelPlatform)=>platform==='onnx'?'ONNX':platform==='denglin'?'登临 engine':'NVIDIA plan'
function selectedFile(event:Event,row:FormatForm){row.modelFile=(event.target as HTMLInputElement).files?.[0]}
function safeProfile(value:string){return value.trim()}
function uploadRequest():{request:ModelUpload;files:Map<string,File>} {
  if(!form.model.trim()||!form.version.trim())throw new Error('请填写模型名称和版本')
  if(!formats.value.length)throw new Error('请至少添加一种模型格式')
	const files=new Map<string,File>(),seen=new Set<string>()
	const requestFormats:ModelUploadFormat[]=formats.value.map(row=>{
    const profile=safeProfile(row.hardwareProfile)
    const id=row.platform==='nvidia'?`nvidia-${profile}`:row.platform
		if(seen.has(id))throw new Error(`模型格式重复：${id}`)
    seen.add(id)
    if(row.platform==='nvidia'&&!profile)throw new Error('NVIDIA plan 必须填写显卡型号')
		const extension=modelExtension(row.platform)
		if(!row.modelFile)throw new Error(`${platformName(row.platform)} 必须选择 .${extension} 文件`)
		const filename=row.modelFile.name
		if(!filename.toLowerCase().endsWith(`.${extension}`))throw new Error(`${platformName(row.platform)} 必须选择 .${extension} 文件`)
		files.set(`${id}/${filename}`,row.modelFile)
		return {id,platform:row.platform,hardware_profile:profile||undefined,files:[{path:filename,size:row.modelFile.size}]}
	})
	return {request:{model:form.model.trim(),version:form.version.trim(),formats:requestFormats},files}
}
function xhrError(xhr:XMLHttpRequest){
  try{const message=JSON.parse(xhr.responseText)?.message;if(message)return `上传失败：${message}`}catch{}
  return `上传失败（HTTP ${xhr.status}）`
}
function xhrPut(url:string,file:File,onProgress:(loaded:number)=>void,credentials=false){return new Promise<void>((resolve,reject)=>{const xhr=new XMLHttpRequest();xhr.open('PUT',url);xhr.withCredentials=credentials;xhr.upload.onprogress=event=>onProgress(event.loaded);xhr.onerror=()=>reject(new Error('上传连接失败'));xhr.onload=()=>xhr.status>=200&&xhr.status<300?resolve():reject(new Error(xhrError(xhr)));xhr.send(file)})}
async function upload(){
  let draft:ModelUpload|undefined
  try{
    const {request,files}=uploadRequest();uploading.value=true;progress.value=0;message.value='正在创建上传草稿'
    const activeDraft:ModelUpload=await http.post('/model-uploads',request);draft=activeDraft
		const all=activeDraft.formats.flatMap(format=>format.files.map(file=>({format,file,blob:files.get(`${format.id}/${file.path}`)!})))
    const total=all.reduce((sum,item)=>sum+item.blob.size,0);let completed=0
    for(const item of all){
			message.value=`正在上传 ${item.format.id}/${item.file.path}`
      const report=(loaded:number)=>{progress.value=Math.min(95,Math.round((completed+loaded)/total*95))}
      try{await xhrPut(item.file.upload_url!,item.blob,report)}catch{
				const query=new URLSearchParams({format_id:item.format.id,path:item.file.path,size:String(item.blob.size)})
        await xhrPut(`/api/v1/model-uploads/${activeDraft.upload_id}/file?${query}`,item.blob,report,true)
      }
      completed+=item.blob.size;report(0)
    }
    message.value='正在校验 SHA-256 并发布 Manifest'
    await http.postLong(`/model-uploads/${activeDraft.upload_id}/finalize`,activeDraft)
    progress.value=100;message.value='模型版本上传完成';ElMessage.success(`模型 ${activeDraft.model}:${activeDraft.version} 已发布`)
    emit('uploaded');visible.value=false;reset()
  }catch(error:any){if(draft?.upload_id){try{await http.delete(`/model-uploads/${draft.upload_id}`)}catch{}}ElMessage.error(error.message||String(error));message.value=error.message||String(error)}finally{uploading.value=false}
}
reset()
</script>

<template>
  <el-dialog v-model="visible" :title="modelName?`为 ${modelName} 上传新版本`:'新建模型'" width="860px" :close-on-click-modal="!uploading" :close-on-press-escape="!uploading" :show-close="!uploading">
    <el-alert :title="modelName?'新版本会保存到当前模型文件夹；可同时包含 ONNX、登临 engine 和一个或多个 NVIDIA plan。':'输入模型名称并上传首个版本后，系统会创建对应的模型文件夹。'" type="info" :closable="false"/>
    <el-form class="model-upload-form" label-width="105px">
      <div class="form-grid">
        <el-form-item label="模型文件夹"><el-input v-model="form.model" placeholder="例如 fire" :disabled="uploading||!!modelName"/></el-form-item>
        <el-form-item label="版本号"><el-input v-model="form.version" placeholder="例如 1.0.0" :disabled="uploading"/></el-form-item>
      </div>
	  <div class="upload-variant" v-for="row in formats" :key="row.key">
		<div class="upload-variant-head"><strong>{{platformName(row.platform)}}</strong><el-button type="danger" link :disabled="uploading||formats.length===1" @click="removeFormat(row.key)">移除</el-button></div>
		<el-form-item v-if="row.platform==='nvidia'" label="显卡型号"><el-input v-model="row.hardwareProfile" placeholder="例如 3050、V100" :disabled="uploading"/></el-form-item>
		<el-form-item label="模型文件"><input type="file" :accept="`.${modelExtension(row.platform)}`" :disabled="uploading" @change="selectedFile($event,row)"/><span class="upload-file-name">{{row.modelFile?`${row.modelFile.name} · ${(row.modelFile.size/1024/1024).toFixed(1)} MiB`:`请选择 .${modelExtension(row.platform)} 文件，文件名不限`}}</span></el-form-item>
	  </div>
	  <div class="upload-add"><span>添加模型格式</span><el-button :disabled="uploading||formats.some(v=>v.platform==='onnx')" @click="addFormat('onnx')">ONNX</el-button><el-button :disabled="uploading||formats.some(v=>v.platform==='denglin')" @click="addFormat('denglin')">登临 engine</el-button><el-button :disabled="uploading" @click="addFormat('nvidia')">NVIDIA plan</el-button></div>
      <div v-if="uploading||message" class="upload-progress"><el-progress :percentage="progress"/><span>{{message}}</span></div>
    </el-form>
    <template #footer><el-button :disabled="uploading" @click="visible=false">取消</el-button><el-button type="primary" :loading="uploading" @click="upload">{{modelName?'上传新版本':'创建模型并上传'}}</el-button></template>
  </el-dialog>
</template>
