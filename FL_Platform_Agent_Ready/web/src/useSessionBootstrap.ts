import {useEffect,useState} from 'react';
import {ApiError,list,request,type Project} from './api';

export interface SessionUser {id:string;name:string;role:string}

// Retry only the initial read operations. A failed write is never replayed here.
export function useSessionBootstrap(
  logged:boolean,
  onMe:(user:SessionUser)=>void,
  onProjects:(projects:Project[])=>void,
){
  const [error,setError]=useState('');
  useEffect(()=>{
    setError('');
    if(!logged)return;
    let current=true;
    const timers=new Set<ReturnType<typeof setTimeout>>();
    const errors=new Map<string,string>();
    async function load<T>(key:string,read:()=>Promise<T>,apply:(value:T)=>void,attempt=0){
      try{
        const value=await read();
        if(!current)return;
        apply(value);
        errors.delete(key);
        setError(errors.values().next().value||'');
      }catch(cause){
        if(!current)return;
        const network=cause instanceof TypeError ||
          (cause instanceof DOMException&&cause.name==='TimeoutError');
        errors.set(key,network?'Не удалось связаться с сервером. Проверяем подключение…':
          cause instanceof Error?cause.message:'Не удалось загрузить рабочее пространство.');
        setError(errors.values().next().value||'');
        if(!network&&!(cause instanceof ApiError&&cause.status===503))return;
        const delay=Math.min(1000*2**Math.min(attempt,5),30000);
        const timer=setTimeout(()=>{
          timers.delete(timer);
          if(current)void load(key,read,apply,attempt+1);
        },delay);
        timers.add(timer);
      }
    }
    void load('me',()=>request<SessionUser>('/me'),onMe);
    void load('projects',()=>list<Project>('/projects'),onProjects);
    return()=>{current=false;timers.forEach(clearTimeout)};
  },[logged,onMe,onProjects]);
  return error;
}
