<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from './api'
import type { Center } from './types'
defineProps<{ isAdmin: boolean }>()
interface Token {id:string;name:string;prefix:string;scopes:string[];center_ids:string[];all_centers:boolean;expires_at:string;last_used_at?:string}
const tokens=ref<Token[]>([]), centers=ref<Center[]>([]), visible=ref(false), secret=ref(''), saving=ref(false)
const form=ref({name:'',days:30,scopes:['read'],all_centers:false,center_ids:[] as string[]})
async function load(){try{tokens.value=await http.get('/mcp-tokens');centers.value=await http.get('/centers')}catch(e:any){ElMessage.error(e.message)}}
async function create(){saving.value=true;try{const result:any=await http.post('/mcp-tokens',form.value);secret.value=result.token;visible.value=false;await load()}catch(e:any){ElMessage.error(e.message)}finally{saving.value=false}}
async function revoke(t:Token){try{await ElMessageBox.confirm(`撤销令牌“${t.name}”？撤销后立即失效。`,'确认撤销',{type:'warning'});await http.delete(`/mcp-tokens/${encodeURIComponent(t.id)}`);await load()}catch(e:any){if(e!=='cancel'&&e!=='close')ElMessage.error(e.message)}}
onMounted(load)
</script>
<template>
  <section class="panel">
    <div class="panel-title"><div><h2>个人 MCP 令牌</h2><p>令牌仅可访问 MCP 接口，权限随账号角色变化。请妥善保存令牌。</p></div><el-button type="primary" @click="visible=true">创建令牌</el-button></div>
    <el-table :data="tokens" empty-text="暂无令牌"><el-table-column prop="name" label="名称"/><el-table-column prop="prefix" label="前缀"/><el-table-column label="权限"><template #default="{row}">{{row.scopes.join('、')}}</template></el-table-column><el-table-column label="发布中心"><template #default="{row}">{{row.all_centers?'全部':(row.center_ids||[]).map((id:string)=>centers.find(c=>c.id===id)?.name||id).join('、') || '无'}}</template></el-table-column><el-table-column label="到期时间"><template #default="{row}">{{new Date(row.expires_at).toLocaleString()}}</template></el-table-column><el-table-column label="最近使用"><template #default="{row}">{{row.last_used_at ? new Date(row.last_used_at).toLocaleString() : '从未使用'}}</template></el-table-column><el-table-column label="操作"><template #default="{row}"><el-button type="danger" link @click="revoke(row)">撤销</el-button></template></el-table-column></el-table>
  </section>
  <el-dialog v-model="visible" title="创建 MCP 令牌" width="520px"><el-form label-position="top"><el-form-item label="名称"><el-input v-model="form.name" maxlength="100"/></el-form-item><el-form-item label="有效期（天，最多 365）"><el-input-number v-model="form.days" :min="1" :max="365"/></el-form-item><el-form-item label="权限"><el-checkbox-group v-model="form.scopes"><el-checkbox value="read" disabled>读取</el-checkbox><el-checkbox v-if="isAdmin" value="build">构建</el-checkbox><el-checkbox v-if="isAdmin" value="release">发布</el-checkbox></el-checkbox-group></el-form-item><template v-if="form.scopes.includes('release')"><el-form-item><el-checkbox v-model="form.all_centers">允许发布到全部中心</el-checkbox></el-form-item><el-form-item v-if="!form.all_centers" label="允许发布的中心"><el-select v-model="form.center_ids" multiple placeholder="请选择中心"><el-option v-for="c in centers" :key="c.id" :label="c.name" :value="c.id"/></el-select></el-form-item></template></el-form><template #footer><el-button @click="visible=false">取消</el-button><el-button type="primary" :loading="saving" @click="create">创建</el-button></template></el-dialog>
  <el-dialog :model-value="!!secret" title="复制令牌" :close-on-click-modal="false" @close="secret=''" width="560px"><p>令牌只显示这一次，关闭后无法再次查看。</p><el-input :model-value="secret" readonly type="textarea" :rows="3"/><template #footer><el-button type="primary" @click="secret=''">已保存</el-button></template></el-dialog>
</template>
