import {useCallback,useEffect,useRef,useState} from 'react';
import {list,token,type Run} from './api';

interface Snapshot {scope:string;runs:Run[];selected:string;updated:string;error:string}
const empty=(scope:string):Snapshot=>({scope,runs:[],selected:'',updated:'',error:''});

// Identity + project scope guards responses even before passive-effect cleanup runs.
export function useProjectRuns(logged:boolean,project:string){
  const scope=logged?`${token}:${project}`:'logged-out';
  const currentScope=useRef(scope),generation=useRef(0);
  const pending=useRef<{scope:string;generation:number}|null>(null);
  const [snapshot,setSnapshot]=useState<Snapshot>(()=>empty(scope));
  currentScope.current=scope;
  const refresh=useCallback(async(preferred?:string)=>{
    if(!logged||!project||currentScope.current!==scope)return;
    const requestGeneration=++generation.current;
    pending.current={scope,generation:requestGeneration};
    const current=()=>currentScope.current===scope&&generation.current===requestGeneration;
    try{
      const items=await list<Run>(`/projects/${project}/runs`);
      if(!current())return;
      setSnapshot(previous=>{
        if(!current())return previous;
        const selected=preferred||(previous.scope===scope?previous.selected:'');
        return {scope,runs:items,selected:items.some(run=>run.id===selected)?selected:items[0]?.id||'',
          updated:new Date().toLocaleTimeString('ru'),error:''};
      });
    }catch(cause){
      if(current())setSnapshot(previous=>current()?{...(previous.scope===scope?previous:empty(scope)),
        error:cause instanceof Error?cause.message:'Не удалось загрузить запуски.'}:previous);
    }finally{if(pending.current?.generation===requestGeneration)pending.current=null}
  },[logged,project,scope]);
  useEffect(()=>{
    setSnapshot(empty(scope));
    if(!logged||!project)return;
    void refresh();
    const timer=setInterval(()=>{if(pending.current?.scope!==scope)void refresh()},3000);
    return()=>{clearInterval(timer);generation.current++;pending.current=null};
  },[logged,project,scope,refresh]);
  const setSelected=useCallback((id:string)=>{
    if(currentScope.current!==scope)return;
    setSnapshot(previous=>previous.scope===scope&&previous.runs.some(run=>run.id===id)?{...previous,selected:id}:previous);
  },[scope]);
  const visible=snapshot.scope===scope?snapshot:empty(scope);
  return {...visible,refresh,setSelected};
}
