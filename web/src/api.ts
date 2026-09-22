import axios from 'axios'
const raw=axios.create({baseURL:'/api/v1',timeout:60000,withCredentials:true})
raw.interceptors.response.use(r=>r.data.data,e=>{
  if(e.response?.status===401)window.dispatchEvent(new Event('manager-auth-required'))
  return Promise.reject(new Error(e.response?.data?.message||e.message))
})
const http={
  get:<T=any>(url:string):Promise<T>=>raw.get(url) as any,
  getFresh:<T=any>(url:string):Promise<T>=>raw.get(url,{params:{_fresh:Date.now()},headers:{'Cache-Control':'no-cache'}}) as any,
  post:<T=any>(url:string,data?:any):Promise<T>=>raw.post(url,data) as any,
  postLong:<T=any>(url:string,data?:any):Promise<T>=>raw.post(url,data,{timeout:0}) as any,
  put:<T=any>(url:string,data?:any):Promise<T>=>raw.put(url,data) as any,
  delete:<T=any>(url:string):Promise<T>=>raw.delete(url) as any,
}
export default http
export const eventURL=(id:string)=>`/api/v1/tasks/${id}/events`
