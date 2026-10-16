<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowRight, CircleClose, Connection, CopyDocument, Delete, Refresh, VideoPlay } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import type { Center } from './types'

const props=defineProps<{centers:Center[]}>()
const selectedCenterId=ref('')
const terminalHost=ref<HTMLElement|null>(null)
const status=ref<'idle'|'connecting'|'connected'|'closed'|'error'>('idle')
const statusMessage=ref('选择一台中心服务器开始连接')
let terminal:Terminal|undefined
let fitAddon:FitAddon|undefined
let socket:WebSocket|undefined
let resizeObserver:ResizeObserver|undefined

const activeCenter=computed(()=>props.centers.find(center=>center.id===selectedCenterId.value))
const isConnected=computed(()=>status.value==='connected'&&socket?.readyState===WebSocket.OPEN)
const statusLabel=computed(()=>({idle:'未连接',connecting:'连接中',connected:'已连接',closed:'已断开',error:'连接失败'}[status.value]))
function selectCenter(id?:string){
  if(!id)return
  if(isConnected.value&&selectedCenterId.value!==id)disconnect()
  selectedCenterId.value=id
  statusMessage.value='已选择服务器，点击连接打开终端'
}

async function connect(){
  const center=activeCenter.value
  if(!center?.id||status.value==='connecting')return
  disconnect(false)
  await nextTick()
  terminal?.reset()
  terminal?.writeln(`\x1b[38;2;102;209;255m正在连接 ${center.username}@${center.host}:${center.port}…\x1b[0m`)
  status.value='connecting'
  statusMessage.value='正在建立安全会话'
  const protocol=window.location.protocol==='https:'?'wss':'ws'
  socket=new WebSocket(`${protocol}://${window.location.host}/api/v1/centers/${encodeURIComponent(center.id)}/terminal`)
  socket.binaryType='arraybuffer'
  socket.onopen=()=>{
    status.value='connected'
    statusMessage.value=`${center.username}@${center.host}:${center.port}`
    fitTerminal()
    sendResize()
    terminal?.focus()
  }
  socket.onmessage=event=>{
    if(typeof event.data==='string')terminal?.write(event.data)
    else terminal?.write(new Uint8Array(event.data))
  }
  socket.onerror=()=>{
    status.value='error'
    statusMessage.value='无法建立 SSH 会话，请检查中心配置'
  }
  socket.onclose=()=>{
    if(status.value!=='error')status.value='closed'
    if(status.value==='closed')statusMessage.value='连接已断开，可以重新连接'
    socket=undefined
  }
}

function disconnect(showMessage=true){
  if(socket){socket.onclose=null;socket.close();socket=undefined}
  if(status.value==='connected'||status.value==='connecting'){
    status.value='closed'
    statusMessage.value='连接已断开，可以重新连接'
    terminal?.writeln('\r\n\x1b[90m已断开 SSH 会话\x1b[0m')
    if(showMessage)ElMessage.info('SSH 会话已断开')
  }
}

function send(payload:object){
  if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify(payload))
}
function sendResize(){if(terminal)send({type:'resize',cols:terminal.cols,rows:terminal.rows})}
function fitTerminal(){requestAnimationFrame(()=>{fitAddon?.fit();sendResize()})}
function clearTerminal(){terminal?.clear();terminal?.focus()}
async function copySelection(){
  const selection=terminal?.getSelection()
  if(!selection){ElMessage.info('请先在终端中选择内容');return}
  try{await navigator.clipboard.writeText(selection);ElMessage.success('已复制终端内容')}
  catch{
    // 局域网 IP 的 HTTP 访问没有 navigator.clipboard（仅安全上下文可用），
    // 退回 execCommand 方案。
    try{
      const textarea=document.createElement('textarea');textarea.value=selection;textarea.style.position='fixed';textarea.style.opacity='0'
      document.body.appendChild(textarea);textarea.select();document.execCommand('copy');document.body.removeChild(textarea)
      ElMessage.success('已复制终端内容')
    }catch{ElMessage.error('复制失败，请使用 Ctrl+C')}
  }
}

watch(()=>props.centers,values=>{if(!selectedCenterId.value&&values[0]?.id)selectCenter(values[0].id)},{immediate:true})

onMounted(()=>{
  terminal=new Terminal({
    cursorBlink:true,
    cursorStyle:'bar',
    fontFamily:'"Cascadia Code","JetBrains Mono",Consolas,monospace',
    fontSize:14,
    lineHeight:1.35,
    scrollback:5000,
    theme:{background:'#071019',foreground:'#d8e7ef',cursor:'#64d7ff',cursorAccent:'#071019',selectionBackground:'#21445e',black:'#15232d',red:'#ff6b7a',green:'#7ee787',yellow:'#e3c66f',blue:'#6cb6ff',magenta:'#c99bff',cyan:'#56d4dd',white:'#d8e7ef',brightBlack:'#67808e'}
  })
  fitAddon=new FitAddon()
  terminal.loadAddon(fitAddon)
  terminal.open(terminalHost.value!)
  terminal.onData(data=>{if(isConnected.value)send({type:'input',data})})
  terminal.attachCustomKeyEventHandler(event=>{
    if(event.type==='keydown'&&event.ctrlKey&&event.shiftKey&&event.key.toLowerCase()==='c'){copySelection();return false}
    return true
  })
  terminal.writeln('\x1b[38;2;100;215;255m中心数字化管理平台 SSH Console\x1b[0m')
  terminal.writeln('\x1b[90m选择左侧服务器并连接，终端会在这里打开。\x1b[0m')
  resizeObserver=new ResizeObserver(fitTerminal)
  resizeObserver.observe(terminalHost.value!)
  fitTerminal()
})

onBeforeUnmount(()=>{disconnect(false);resizeObserver?.disconnect();terminal?.dispose()})
</script>

<template>
  <div class="terminal-page">
    <aside class="terminal-sidebar">
      <div class="terminal-sidebar-head">
        <span>中心服务器</span>
        <el-tag size="small" effect="plain">{{centers.length}}</el-tag>
      </div>
      <div v-if="centers.length" class="terminal-server-list">
        <button v-for="center in centers" :key="center.id" class="terminal-server" :class="{active:center.id===selectedCenterId}" @click="selectCenter(center.id)" @dblclick="connect">
          <span class="server-status-dot"></span>
          <span><strong>{{center.name}}</strong><small>{{center.username}}@{{center.host}}:{{center.port}}</small></span>
          <el-icon><ArrowRight/></el-icon>
        </button>
      </div>
      <el-empty v-else description="暂无中心服务器" :image-size="64"/>
      <div v-if="activeCenter" class="terminal-connection-card">
        <span>当前目标</span>
        <strong>{{activeCenter.name}}</strong>
        <code>{{activeCenter.host}}:{{activeCenter.port}}</code>
      </div>
    </aside>

    <section class="terminal-workspace">
      <div class="terminal-topbar">
        <div class="terminal-breadcrumb">
          <span class="terminal-mark"><Connection/></span>
          <div><strong>{{activeCenter?.name||'SSH 终端'}}</strong><small>{{statusMessage}}</small></div>
        </div>
        <div class="terminal-status" :class="status"><i></i>{{statusLabel}}</div>
        <div class="terminal-tools">
          <el-button text :icon="CopyDocument" @click="copySelection">复制</el-button>
          <el-button text :icon="Delete" @click="clearTerminal">清屏</el-button>
          <el-button v-if="!isConnected" type="primary" :icon="status==='closed'||status==='error'?Refresh:VideoPlay" :loading="status==='connecting'" :disabled="!activeCenter" @click="connect">{{status==='closed'||status==='error'?'重新连接':'连接 SSH'}}</el-button>
          <el-button v-else type="danger" plain :icon="CircleClose" @click="disconnect()">断开连接</el-button>
        </div>
      </div>
      <div class="terminal-window">
        <div ref="terminalHost" class="terminal-host" aria-label="SSH 交互终端"></div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.terminal-page{display:grid;grid-template-columns:220px minmax(0,1fr);height:100%;min-height:0;margin:0;background:#111217;border:1px solid #22252b;border-radius:10px;overflow:hidden;box-shadow:0 18px 44px rgba(0,0,0,.35)}
.terminal-sidebar{display:flex;flex-direction:column;background:#181b1f;border-right:1px solid #22252b;padding:14px 8px;min-height:0}.terminal-sidebar-head{display:flex;align-items:center;justify-content:space-between;padding:0 8px 14px;color:#d8d9da;font-size:14px;font-weight:700}.terminal-server-list{display:grid;gap:6px;overflow:auto}.terminal-server{width:100%;display:grid;grid-template-columns:9px minmax(0,1fr) 18px;gap:10px;align-items:center;text-align:left;border:1px solid transparent;border-radius:11px;padding:12px 10px;background:transparent;color:#c3c6c9;cursor:pointer;transition:.18s ease}.terminal-server:hover{background:#1f2125}.terminal-server.active{background:#22252b;border-color:#3d434d}.terminal-server strong,.terminal-server small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.terminal-server strong{font-size:14px}.terminal-server small{margin-top:4px;color:#8e9297;font:12px Consolas,monospace}.terminal-server .el-icon{color:#8e9297}.server-status-dot{width:8px;height:8px;border-radius:50%;background:#73bf69;box-shadow:0 0 0 3px rgba(115,191,105,.14)}.terminal-connection-card{margin-top:auto;display:grid;gap:7px;padding:16px 12px 2px;border-top:1px solid #22252b}.terminal-connection-card>span{color:#8e9297;font-size:12px}.terminal-connection-card strong{color:#d8d9da}.terminal-connection-card code{color:#8e9297;font-size:12px}.terminal-workspace{display:grid;grid-template-rows:auto minmax(0,1fr);min-height:0;min-width:0;background:#111217}.terminal-topbar{min-height:52px;display:flex;align-items:center;gap:10px;padding:8px 12px;background:#181b1f;border-bottom:1px solid #22252b;color:#d8d9da}.terminal-breadcrumb{display:flex;align-items:center;gap:11px;min-width:0}.terminal-breadcrumb>div{min-width:0}.terminal-breadcrumb strong,.terminal-breadcrumb small{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.terminal-breadcrumb strong{font-size:14px}.terminal-breadcrumb small{color:#8e9297;font-size:12px;margin-top:3px}.terminal-mark{width:34px;height:34px;display:grid;place-items:center;border:1px solid #3d434d;border-radius:9px;color:#66d8ff;background:#1f2125}.terminal-mark svg{width:18px}.terminal-status{display:flex;align-items:center;gap:7px;color:#8e9297;font-size:12px;margin-left:auto}.terminal-status i{width:8px;height:8px;border-radius:50%;background:#5a5f66}.terminal-status.connected{color:#8fd6a0}.terminal-status.connected i{background:#73bf69;box-shadow:0 0 0 4px rgba(115,191,105,.12)}.terminal-status.connecting i{background:#ff9830;animation:pulse 1s infinite}.terminal-status.error i{background:#f5495c}.terminal-tools{display:flex;align-items:center;gap:4px;border-left:1px solid #22252b;padding-left:8px}.terminal-tools :deep(.el-button.is-text){color:#8e9297}.terminal-tools :deep(.el-button--primary),.terminal-tools :deep(.el-button--danger){margin-left:8px}.terminal-window{display:grid;grid-template-rows:minmax(0,1fr);min-height:0;background:#071019}.terminal-host{min-width:0;min-height:0;overflow:hidden;padding:6px}.terminal-host :deep(.xterm){height:100%}@keyframes pulse{50%{opacity:.35}}@media(max-width:1100px){.terminal-page{grid-template-columns:180px minmax(0,1fr)}.terminal-status{display:none}.terminal-tools{margin-left:auto}.terminal-tools :deep(.el-button.is-text){display:none}}@media(max-width:820px){.terminal-page{grid-template-columns:minmax(0,1fr);grid-template-rows:auto minmax(0,1fr)}.terminal-sidebar{padding:8px;border-right:0;border-bottom:1px solid #22252b}.terminal-sidebar-head,.terminal-connection-card{display:none}.terminal-server-list{display:flex;overflow:auto;max-height:72px}.terminal-server{flex:0 0 190px;padding:8px}.terminal-topbar{gap:8px;padding:6px 8px}.terminal-mark{display:none}.terminal-tools .el-button{padding:8px}.terminal-sidebar :deep(.el-empty){padding:8px}.terminal-sidebar :deep(.el-empty__image){display:none}.terminal-sidebar :deep(.el-empty__description){margin-top:0}}
</style>
