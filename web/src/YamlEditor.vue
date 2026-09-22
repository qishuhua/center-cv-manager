<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, basicSetup } from 'codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'

const props=withDefaults(defineProps<{modelValue:string;disabled?:boolean}>(),{disabled:false})
const emit=defineEmits<{(event:'update:modelValue',value:string):void}>()
const host=ref<HTMLElement|null>(null)
let editor:EditorView|undefined
const access=new Compartment()

onMounted(()=>{
  editor=new EditorView({
    parent:host.value!,
    doc:props.modelValue,
    extensions:[
      basicSetup,
      yaml(),
      oneDark,
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({'aria-label':'Compose YAML 编辑器','spellcheck':'false'}),
      access.of([EditorState.readOnly.of(props.disabled),EditorView.editable.of(!props.disabled)]),
      EditorView.updateListener.of(update=>{
        if(update.docChanged)emit('update:modelValue',update.state.doc.toString())
      }),
    ],
  })
})

watch(()=>props.modelValue,value=>{
  if(!editor||editor.state.doc.toString()===value)return
  editor.dispatch({changes:{from:0,to:editor.state.doc.length,insert:value}})
})
watch(()=>props.disabled,value=>editor?.dispatch({effects:access.reconfigure([EditorState.readOnly.of(value),EditorView.editable.of(!value)])}))

onBeforeUnmount(()=>editor?.destroy())
</script>

<template><div ref="host" class="yaml-editor"/></template>

<style scoped>
.yaml-editor{overflow:hidden;border:1px solid #26364b;border-radius:10px;background:#111827}
.yaml-editor:focus-within{border-color:#4f8cff;box-shadow:0 0 0 2px rgba(79,140,255,.18)}
.yaml-editor :deep(.cm-editor){height:min(68vh,760px);min-height:500px;font:13px/1.6 Consolas,"Cascadia Code","Courier New",monospace}
.yaml-editor :deep(.cm-scroller){overflow:auto}
.yaml-editor :deep(.cm-gutters){border-right-color:#26364b}
</style>
