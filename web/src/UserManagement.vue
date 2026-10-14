<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Key, Plus, Refresh, User } from '@element-plus/icons-vue'
import http from './api'
import type { UserAccount, UserRole } from './types'

defineProps<{ currentUsername: string; superAdmin?: boolean }>()
const emit = defineEmits<{ roleChanged: [] }>()
const users = ref<UserAccount[]>([])
const loading = ref(false)
const creating = ref(false)
const createDialog = ref(false)
const createForm = ref({ username: '', password: '', confirm: '', role: 'viewer' as UserRole })

async function load() {
  loading.value = true
  try { users.value = await http.get('/users') }
  catch (e: any) { ElMessage.error(e.message) }
  finally { loading.value = false }
}
function openCreate() {
  createForm.value = { username: '', password: '', confirm: '', role: 'viewer' }
  createDialog.value = true
}
async function createUser() {
  const form = createForm.value
  if (!form.username) { ElMessage.warning('请输入用户名'); return }
  if (form.password.length < 4) { ElMessage.warning('初始密码至少 4 位'); return }
  if (form.password !== form.confirm) { ElMessage.warning('两次输入的密码不一致'); return }
  creating.value = true
  try {
    await http.post('/users', { username: form.username, password: form.password, role: form.role })
    ElMessage.success(`用户 ${form.username} 已创建，首次登录将要求修改密码`)
    createDialog.value = false
    await load()
  } catch (e: any) { ElMessage.error(e.message) } finally { creating.value = false }
}
async function toggleRole(user: UserAccount) {
  const role: UserRole = user.role === 'admin' ? 'viewer' : 'admin'
  const label = role === 'admin' ? '设为管理员（可执行发布、重启等全部操作）' : '设为普通用户（只读查看）'
  try {
    await ElMessageBox.confirm(`将用户 “${user.username}” ${label}？角色变更后立即生效。`, '确认修改角色', { type: 'warning', confirmButtonText: '确认', cancelButtonText: '取消', closeOnClickModal: false })
    await http.put(`/users/${encodeURIComponent(user.username)}/role`, { role })
    ElMessage.success('角色已更新')
    await load()
    emit('roleChanged')
  } catch (e: any) { if (e !== 'cancel' && e !== 'close') ElMessage.error(e.message) }
}
async function resetPassword(user: UserAccount) {
  try {
    const { value } = await ElMessageBox.prompt(`为用户 “${user.username}” 设置新密码。重置后该用户的所有登录会话会立即失效，并在下次登录时要求修改密码。`, '重置密码', { inputType: 'password', inputValidator: (s: string) => (s && s.length >= 4) || '密码至少 4 位', confirmButtonText: '重置', cancelButtonText: '取消', closeOnClickModal: false })
    await http.post(`/users/${encodeURIComponent(user.username)}/reset-password`, { password: value })
    ElMessage.success('密码已重置')
    await load()
  } catch (e: any) { if (e !== 'cancel' && e !== 'close') ElMessage.error(e.message) }
}
async function removeUser(user: UserAccount) {
  try {
    await ElMessageBox.confirm(`确定删除用户 “${user.username}”？该用户的登录会话会立即失效。`, '确认删除用户', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消', closeOnClickModal: false })
    await http.delete(`/users/${encodeURIComponent(user.username)}`)
    ElMessage.success('用户已删除')
    await load()
  } catch (e: any) { if (e !== 'cancel' && e !== 'close') ElMessage.error(e.message) }
}
onMounted(load)
</script>

<template>
  <section class="panel user-panel" v-loading="loading">
    <div class="panel-title">
      <div>
        <h2>用户与权限</h2>
        <p>管理员可发布软件和模型、重启服务等；普通用户只能查看各中心状态。admin 为超级管理员：用户增删、角色调整、重置密码与任务记录删除仅限该账号。</p>
      </div>
      <div>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button v-if="superAdmin" type="primary" :icon="Plus" @click="openCreate">新建用户</el-button>
      </div>
    </div>
    <el-alert v-if="!superAdmin" title="用户增删、角色调整与重置密码仅限内建 admin 账号操作，当前为只读视图。" type="info" :closable="false" show-icon style="margin-bottom:14px"/>
    <el-table :data="users" empty-text="暂无用户">
      <el-table-column label="用户名" min-width="180">
        <template #default="{row}"><span class="username"><el-icon><User/></el-icon><strong>{{row.username}}</strong><el-tag v-if="row.username===currentUsername" size="small" type="info">当前登录</el-tag></span></template>
      </el-table-column>
      <el-table-column label="角色" width="130">
        <template #default="{row}"><el-tag :type="row.role==='admin'?'primary':'info'" :effect="row.username==='admin'?'dark':'plain'">{{row.role==='admin'?(row.username==='admin'?'超级管理员':'管理员'):'普通用户'}}</el-tag></template>
      </el-table-column>
      <el-table-column label="首登改密" width="120">
        <template #default="{row}"><el-tag v-if="row.must_change_password" size="small" type="warning">待修改</el-tag><span v-else>已设置</span></template>
      </el-table-column>
      <el-table-column label="创建时间" width="190">
        <template #default="{row}">{{row.created_at ? new Date(row.created_at).toLocaleString() : '-'}}</template>
      </el-table-column>
      <el-table-column label="操作" min-width="280" fixed="right">
        <template #default="{row}">
          <template v-if="superAdmin">
            <el-button v-if="row.username!=='admin'" :type="row.role==='admin'?'warning':'primary'" link @click="toggleRole(row)">{{row.role==='admin'?'设为普通用户':'设为管理员'}}</el-button>
            <el-button :icon="Key" link @click="resetPassword(row)">重置密码</el-button>
            <el-button v-if="row.username!=='admin'&&row.username!==currentUsername" type="danger" link @click="removeUser(row)">删除</el-button>
          </template>
          <span v-else class="muted">仅内建 admin 可管理</span>
        </template>
      </el-table-column>
    </el-table>
  </section>

  <el-dialog v-model="createDialog" title="新建用户" width="480px" :close-on-click-modal="false">
    <el-form label-width="96px" @submit.prevent="createUser">
      <el-form-item label="用户名"><el-input v-model="createForm.username" placeholder="2-32 位字母、数字、_ . -" autocomplete="off"/></el-form-item>
      <el-form-item label="初始密码"><el-input v-model="createForm.password" type="password" show-password placeholder="至少 4 位，用户首次登录需修改" autocomplete="new-password"/></el-form-item>
      <el-form-item label="确认密码"><el-input v-model="createForm.confirm" type="password" show-password placeholder="再次输入初始密码" autocomplete="new-password"/></el-form-item>
      <el-form-item label="角色">
        <el-radio-group v-model="createForm.role">
          <el-radio value="viewer">普通用户（只读）</el-radio>
          <el-radio value="admin">管理员</el-radio>
        </el-radio-group>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createDialog=false">取消</el-button>
      <el-button type="primary" :loading="creating" @click="createUser">创建用户</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.user-panel{margin-top:4px}
.username{display:inline-flex;align-items:center;gap:8px}
</style>
