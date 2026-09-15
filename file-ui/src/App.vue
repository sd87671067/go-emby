<script setup lang="ts">
import {computed,nextTick,onMounted,onUnmounted,ref,watch} from 'vue'
import {Folder,Film,Image,Music,FileText,Paperclip,Search,ArrowLeft,ChevronRight,Sun,Moon,Pencil,Check,X,Trash2,LoaderCircle,ArrowUp,ArrowDown,Home,RefreshCw,TriangleAlert,HardDrive,LogIn,FolderSearch} from 'lucide-vue-next'
type Entry={name:string;path:string;isDir:boolean;size:number;modified:string}
const entries=ref<Entry[]>([]),path=ref('/'),search=ref(''),appliedSearch=ref(''),scope=ref('current'),sort=ref('name'),order=ref('asc'),loading=ref(false),error=ref(''),edit=ref(''),editName=ref(''),saving=ref(false),deleting=ref(false),target=ref<Entry|null>(null),dialog=ref<HTMLDialogElement|null>(null),auth=ref(true)
const dark=ref(localStorage.getItem('files-theme')==='dark'),toast=ref({text:'',bad:false}),editInput=ref<HTMLInputElement[]>([])
let timer:ReturnType<typeof setTimeout>,toastTimer:ReturnType<typeof setTimeout>,controller:AbortController|undefined,sequence=0,lastFocus:HTMLElement|null=null
watch(dark,v=>{document.documentElement.classList.toggle('dark',v);localStorage.setItem('files-theme',v?'dark':'light')},{immediate:true})
const crumbs=computed(()=>path.value.split('/').filter(Boolean).map((name,i,all)=>({name,path:'/'+all.slice(0,i+1).join('/')})))
const folderCount=computed(()=>entries.value.filter(x=>x.isDir).length)
const busy=computed(()=>saving.value||deleting.value)
function notify(text:string,bad=false){clearTimeout(toastTimer);toast.value={text,bad};toastTimer=setTimeout(()=>toast.value.text='',5000)}
async function api(url:string,method='GET',body?:unknown,signal?:AbortSignal){
 const response=await fetch(url,{method,headers:{'Content-Type':'application/json','X-Emby-Token':sessionStorage.getItem('token')||''},body:body===undefined?undefined:JSON.stringify(body),signal})
 const data=await response.json();if(!response.ok)throw Error(data.error||`HTTP ${response.status}`);return data
}
async function load(){
 controller?.abort();controller=new AbortController();const id=++sequence;loading.value=true;error.value='';edit.value=''
 try {const params=new URLSearchParams({path:path.value,search:search.value.trim(),scope:scope.value,sort:sort.value,order:order.value});const data=await api('/api/files?'+params,'GET',undefined,controller.signal);if(id===sequence){entries.value=data.entries;appliedSearch.value=search.value.trim()}}
 catch(e){if(id===sequence&&(e as Error).name!=='AbortError'){error.value=(e as Error).message;entries.value=[]}}
 finally{if(id===sequence)loading.value=false}
}
watch(search,()=>{clearTimeout(timer);timer=setTimeout(load,300)})
watch([scope,sort,order],()=>{clearTimeout(timer);load()})
async function navigate(next:string){if(busy.value)return;clearTimeout(timer);path.value=next;await load()}
function up(){navigate(path.value.substring(0,path.value.lastIndexOf('/'))||'/')}
function sortBy(key:string){if(sort.value===key)order.value=order.value==='asc'?'desc':'asc';else{sort.value=key;order.value='asc'}}
function parts(name:string){const term=appliedSearch.value.toLocaleLowerCase();if(!term)return[{text:name,hit:false}];const result:{text:string;hit:boolean}[]=[];const lower=name.toLocaleLowerCase();let start=0,i:number;while((i=lower.indexOf(term,start))>=0){if(i>start)result.push({text:name.slice(start,i),hit:false});result.push({text:name.slice(i,i+term.length),hit:true});start=i+term.length}if(start<name.length)result.push({text:name.slice(start),hit:false});return result}
function icon(e:Entry){if(e.isDir)return Folder;const ext=e.name.split('.').pop()?.toLowerCase()||'';if(['mp4','mkv','avi','mov','ts','webm','strm'].includes(ext))return Film;if(['jpg','jpeg','png','webp','gif','svg','avif'].includes(ext))return Image;if(['mp3','flac','wav','aac','m4a','ogg'].includes(ext))return Music;if(['txt','md','pdf','doc','docx','nfo','srt','json','xml'].includes(ext))return FileText;return Paperclip}
function size(n:number){if(n<1024)return n+' B';const units=['KB','MB','GB','TB'];let i=-1;do{n/=1024;i++}while(n>=1024&&i<3);return n.toFixed(1)+' '+units[i]}
function modified(s:string){return new Intl.DateTimeFormat('zh-CN',{dateStyle:'medium',timeStyle:'short'}).format(new Date(s))}
async function rename(e:Entry){edit.value=e.path;editName.value=e.name;await nextTick();editInput.value?.[0]?.focus();editInput.value?.[0]?.select()}
async function save(e:Entry){if(saving.value)return;const name=editName.value.trim();if(!name||/[\/\\:*?"<>|\x00-\x1f]/.test(name)||['.','..'].includes(name)){notify('重命名失败：名称不能为空或包含非法字符',true);return}if(name===e.name){edit.value='';return}
 saving.value=true;try{await api('/api/files/rename','POST',{oldPath:e.path,newPath:e.path.substring(0,e.path.lastIndexOf('/')+1)+name});await load();notify('重命名成功')}catch(err){notify('重命名失败：'+(err as Error).message,true)}finally{saving.value=false}}
function askDelete(e:Entry){target.value=e;lastFocus=document.activeElement as HTMLElement;dialog.value?.showModal()}
function closeDelete(){if(deleting.value)return;dialog.value?.close();target.value=null;lastFocus?.focus()}
async function remove(){if(!target.value||deleting.value)return;deleting.value=true;try{await api('/api/files','DELETE',{paths:[target.value.path]});deleting.value=false;closeDelete();await load();notify('删除成功')}catch(err){notify('删除失败：'+(err as Error).message,true);await load()}finally{deleting.value=false}}
onMounted(async()=>{try{const user=await api('/Users/Me');auth.value=!!user.Policy?.IsAdministrator;if(auth.value)await load()}catch{auth.value=false}})
onUnmounted(()=>{clearTimeout(timer);clearTimeout(toastTimer);controller?.abort()})
</script>

<template>
<div class="min-h-screen">
<header class="surface sticky top-0 z-20 border-x-0 border-t-0 shadow-sm">
 <div class="mx-auto flex max-w-7xl flex-wrap items-center gap-3 px-4 py-3 sm:px-8">
  <a href="/#admin" class="icon-btn" title="返回后台管理" aria-label="返回后台管理"><ArrowLeft/></a>
  <div class="mr-auto flex items-center gap-3"><span class="rounded-2xl bg-indigo-600 p-3 text-white"><Folder/></span><div><h1 class="text-lg font-semibold tracking-tight">文件管理</h1><p class="text-xs text-slate-500">go-emby · 媒体空间</p></div></div>
  <button class="icon-btn" :title="dark?'切换浅色主题':'切换深色主题'" :aria-label="dark?'切换浅色主题':'切换深色主题'" @click="dark=!dark"><Sun v-if="dark"/><Moon v-else/></button>
  <div v-if="auth" class="flex w-full items-center gap-2 rounded-2xl bg-slate-100 px-3 dark:bg-slate-800 sm:order-none sm:ml-6 sm:w-auto sm:flex-1 sm:max-w-xl">
   <Search class="text-slate-400"/><input v-model="search" :disabled="busy" aria-label="搜索文件或文件夹" placeholder="搜索文件或文件夹…" class="h-12 min-w-0 flex-1 bg-transparent text-sm outline-none" type="search"/>
   <button v-if="search" class="icon-btn" title="清空搜索" aria-label="清空搜索" :disabled="busy" @click="search='' "><X/></button>
   <select v-model="scope" :disabled="busy" class="h-11 max-w-24 rounded-lg bg-transparent text-xs dark:bg-slate-800" aria-label="搜索范围"><option value="current">当前目录</option><option value="global">全局搜索</option></select>
  </div>
 </div>
</header>
<main class="mx-auto max-w-7xl px-4 py-7 sm:px-8 sm:py-10">
 <div v-if="!auth" class="surface mx-auto mt-16 max-w-md rounded-3xl p-10 text-center"><LogIn class="mx-auto mb-4 text-indigo-500"/><h2 class="text-xl font-semibold">需要管理员登录</h2><p class="my-4 text-sm text-slate-500">请使用影库管理员账号登录后打开文件管理。</p><a href="/" class="inline-flex min-h-11 items-center rounded-xl bg-indigo-600 px-5 text-white">返回登录</a></div>
 <template v-else>
 <div class="mb-6 flex items-center gap-3"><div class="mr-auto"><h2 class="text-2xl font-semibold tracking-tight">{{search?'搜索文件':'我的文件'}}</h2><p class="mt-2 text-sm text-slate-500">{{entries.length}} 个项目<span v-if="!search"> · {{folderCount}} 个文件夹</span></p></div><HardDrive class="text-slate-400"/><span class="hidden text-sm text-slate-500 sm:inline">媒体目录</span></div>
 <section class="surface overflow-hidden rounded-2xl shadow-sm">
  <div class="flex items-center gap-2 border-b border-slate-200 px-3 py-2 dark:border-slate-800 sm:px-5">
   <button class="icon-btn" title="返回上级" aria-label="返回上级" :disabled="path==='/'||busy" @click="up"><ArrowLeft/></button>
   <nav aria-label="路径导航" class="flex min-w-0 flex-1 items-center overflow-x-auto whitespace-nowrap text-sm"><button class="icon-btn" title="媒体根目录" aria-label="媒体根目录" :disabled="busy" @click="navigate('/')"><Home/></button><template v-for="c in crumbs" :key="c.path"><ChevronRight class="text-slate-300"/><button :disabled="busy" class="min-h-11 rounded-lg px-3 hover:bg-slate-100 dark:hover:bg-slate-800" @click="navigate(c.path)">{{c.name}}</button></template></nav>
   <button class="icon-btn" title="刷新列表" aria-label="刷新列表" :disabled="busy||loading" @click="load"><RefreshCw :class="{'animate-spin':loading}"/></button>
  </div>
  <div v-if="error" role="alert" class="m-5 rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-950 dark:text-red-200">{{error}}</div>
  <div class="overflow-x-auto" :aria-busy="loading">
  <table class="w-full table-fixed text-left text-sm"><thead class="bg-slate-50 text-xs text-slate-500 dark:bg-slate-800/50"><tr><th class="px-4 sm:px-6" :aria-sort="sort==='name'?(order==='asc'?'ascending':'descending'):'none'"><button :disabled="busy" class="flex min-h-11 items-center gap-2" @click="sortBy('name')">名称<component v-if="sort==='name'" :is="order==='asc'?ArrowUp:ArrowDown" class="!h-3.5 !w-3.5"/></button></th><th class="hidden w-48 md:table-cell" :aria-sort="sort==='time'?(order==='asc'?'ascending':'descending'):'none'"><button :disabled="busy" class="flex min-h-11 items-center gap-2" @click="sortBy('time')">修改时间<component v-if="sort==='time'" :is="order==='asc'?ArrowUp:ArrowDown" class="!h-3.5 !w-3.5"/></button></th><th class="hidden w-28 sm:table-cell" :aria-sort="sort==='size'?(order==='asc'?'ascending':'descending'):'none'"><button :disabled="busy" class="flex min-h-11 items-center gap-2" @click="sortBy('size')">大小<component v-if="sort==='size'" :is="order==='asc'?ArrowUp:ArrowDown" class="!h-3.5 !w-3.5"/></button></th><th class="w-28"><span class="sr-only">操作</span></th></tr></thead>
  <tbody class="divide-y divide-slate-100 dark:divide-slate-800">
   <tr v-for="e in entries" :key="e.path" class="file-row group h-[76px] hover:bg-indigo-50/50 dark:hover:bg-slate-800/60">
    <td class="px-4 py-3 sm:px-6"><div class="flex min-w-0 items-center gap-3"><span class="hidden rounded-xl p-2.5 min-[380px]:block" :class="e.isDir?'bg-amber-50 text-amber-500 dark:bg-amber-950/40':'bg-indigo-50 text-indigo-500 dark:bg-indigo-950/40'"><component :is="icon(e)"/></span>
     <div v-if="edit===e.path" class="min-w-0 flex-1"><input ref="editInput" v-model="editName" :disabled="saving" class="field w-full" aria-label="文件夹新名称" @keydown.enter.prevent="save(e)" @keydown.esc="!saving&&(edit='')"/></div>
     <div v-else class="min-w-0 flex-1"><button v-if="e.isDir" :disabled="busy" class="min-h-11 w-full truncate text-left font-medium hover:text-indigo-600 dark:hover:text-indigo-300" :title="e.name" @click="navigate(e.path)"><template v-for="(p,i) in parts(e.name)" :key="i"><mark v-if="p.hit">{{p.text}}</mark><template v-else>{{p.text}}</template></template></button><div v-else class="truncate font-medium" :title="e.name"><template v-for="(p,i) in parts(e.name)" :key="i"><mark v-if="p.hit">{{p.text}}</mark><template v-else>{{p.text}}</template></template></div><p v-if="appliedSearch&&scope==='global'" class="truncate text-xs text-slate-500" :title="e.path">{{e.path}}</p><p class="truncate text-xs text-slate-500 md:hidden">{{modified(e.modified)}}<span v-if="!e.isDir" class="sm:hidden"> · {{size(e.size)}}</span></p></div>
    </div></td>
    <td class="hidden text-xs text-slate-500 md:table-cell">{{modified(e.modified)}}</td><td class="hidden text-xs tabular-nums text-slate-500 sm:table-cell">{{e.isDir?'':size(e.size)}}</td>
    <td class="pr-3"><div class="flex justify-end"><template v-if="edit===e.path"><button class="icon-btn !text-emerald-600" title="保存重命名" aria-label="保存重命名" :disabled="saving" @click="save(e)"><LoaderCircle v-if="saving" class="animate-spin"/><Check v-else/></button><button class="icon-btn" title="取消重命名" aria-label="取消重命名" :disabled="saving" @click="edit='' "><X/></button></template><template v-else><button v-if="e.isDir" class="icon-btn" :title="'重命名 '+e.name" :aria-label="'重命名 '+e.name" :disabled="busy||loading" @click="rename(e)"><Pencil/></button><button class="icon-btn hover:!bg-red-50 hover:!text-red-600 dark:hover:!bg-red-950" :title="'删除 '+e.name" :aria-label="'删除 '+e.name" :disabled="busy||loading" @click="askDelete(e)"><Trash2/></button></template></div></td>
   </tr>
  </tbody></table></div>
  <div v-if="loading&&!entries.length" class="flex justify-center gap-3 p-16 text-sm text-slate-500" role="status"><LoaderCircle class="animate-spin"/>正在加载…</div>
  <div v-else-if="!entries.length&&!error" class="p-16 text-center text-slate-400"><FolderSearch class="mx-auto mb-3 !h-10 !w-10"/><p>{{search?'没有匹配的文件':'此文件夹为空'}}</p></div>
 </section>
 <div class="mt-4 flex items-center gap-2 text-xs text-slate-500 md:hidden"><label for="mobile-sort">排序</label><select id="mobile-sort" v-model="sort" :disabled="busy" class="field"><option value="name">名称</option><option value="time">修改时间</option><option value="size">大小</option></select><button class="icon-btn" :disabled="busy" title="切换排序方向" aria-label="切换排序方向" @click="order=order==='asc'?'desc':'asc'"><component :is="order==='asc'?ArrowUp:ArrowDown"/></button></div>
 </template>
</main>
<dialog ref="dialog" class="surface w-[calc(100%-2rem)] max-w-md rounded-3xl p-6 text-slate-800 shadow-2xl dark:text-slate-100" aria-labelledby="delete-title" aria-describedby="delete-description" @cancel.prevent="closeDelete">
 <div class="mb-4 flex items-center gap-3"><span class="rounded-2xl bg-red-100 p-3 text-red-600 dark:bg-red-950"><TriangleAlert/></span><h2 id="delete-title" class="text-lg font-semibold">删除确认</h2></div>
 <p id="delete-description" class="break-words text-sm leading-7">确定要删除 <strong>{{target?.name}}</strong> 吗？此操作不可恢复。</p>
 <div class="mt-6 flex justify-end gap-3"><button class="field px-5" :disabled="deleting" autofocus @click="closeDelete">取消</button><button class="danger-btn flex items-center gap-2" :disabled="deleting" @click="remove"><LoaderCircle v-if="deleting" class="animate-spin"/><Trash2 v-else/>{{deleting?'删除中…':'确认删除'}}</button></div>
</dialog>
<Transition name="fade"><div v-if="toast.text" role="status" aria-live="polite" class="fixed bottom-6 left-1/2 z-50 flex w-max max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-3 rounded-2xl px-5 py-4 text-sm text-white shadow-lg" :class="toast.bad?'bg-red-600':'bg-emerald-600'"><TriangleAlert v-if="toast.bad"/><Check v-else/>{{toast.text}}</div></Transition>
</div>
</template>
