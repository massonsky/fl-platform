import {useCallback,useEffect,useState} from 'react';
import {ApiError,token} from './api';
import {useRequestScope} from './project-access-api';
import {legacyLabError,readLegacyAdmission,type LegacyLabAdmission} from './legacy-lab-api';

type State='idle'|'loading'|'allowed'|'denied'|'error';
interface Snapshot {key:string;state:State;admission:LegacyLabAdmission|null;message:string}
interface Options {enabled:boolean;user:{id:string;role:string}|null;scopeKey:string}

function valid(value:LegacyLabAdmission,userID:string){
  if(value.user_id!==userID||!Number.isInteger(value.revision)||value.revision<0||typeof value.can_create_legacy_work!=='boolean')return false;
  if(value.status==='not_admitted')return value.revision===0&&!value.can_create_legacy_work;
  return ['active','revoked'].includes(value.status)&&value.revision>0&&!!value.source&&!!value.updated_at&&
    (value.status==='active'||!value.can_create_legacy_work);
}

export function useLegacyAdmission({enabled,user,scopeKey}:Options){
  const userID=user?.id||'',key=[token,userID,user?.role,enabled,scopeKey].join(':');
  const begin=useRequestScope(`own-legacy-lab:${key}`);
  const [snapshot,setSnapshot]=useState<Snapshot|null>(null);
  const refresh=useCallback(async()=>{
    const current=begin('capability');
    if(!enabled||!userID||!token){setSnapshot({key,state:'idle',admission:null,message:''});return false}
    setSnapshot({key,state:'loading',admission:null,message:'Проверяем допуск к лаборатории…'});
    try{
      const result=await readLegacyAdmission();
      if(!current())return false;
      if(!valid(result,userID))throw new Error('Не удалось подтвердить допуск для текущей учётной записи.');
      setSnapshot({key,state:result.can_create_legacy_work?'allowed':'denied',admission:result,
        message:result.can_create_legacy_work?'':'Для новых проектов и запусков нужен действующий допуск к лаборатории. Обратитесь к администратору.'});
      return result.can_create_legacy_work;
    }catch(cause){if(current())setSnapshot({key,state:'error',admission:null,message:legacyLabError(cause)});return false}
  },[begin,enabled,key,userID]);
  useEffect(()=>{void refresh()},[refresh]);
  useEffect(()=>{
    if(!enabled||!userID)return;
    const focus=()=>{if(document.visibilityState==='visible')void refresh()};
    const changed=(event:Event)=>{if((event as CustomEvent<{userID:string}>).detail?.userID===userID)void refresh()};
    window.addEventListener('focus',focus);document.addEventListener('visibilitychange',focus);
    window.addEventListener('fl-legacy-lab-changed',changed);
    return()=>{window.removeEventListener('focus',focus);document.removeEventListener('visibilitychange',focus);window.removeEventListener('fl-legacy-lab-changed',changed)};
  },[enabled,userID,refresh]);
  const invalidate=useCallback((cause:unknown)=>{
    if(cause instanceof ApiError&&cause.code==='LEGACY_LAB_NOT_ADMITTED'){
      begin('capability');setSnapshot({key,state:'denied',admission:null,message:legacyLabError(cause)});void refresh();
    }
  },[begin,key,refresh]);
  // A scope change denies synchronously, before effect cleanup or a new GET.
  const current=snapshot?.key===key?snapshot:null;
  return {state:current?.state||(enabled?'loading':'idle') as State,admission:current?.admission||null,
    message:current?.message||(enabled?'Проверяем допуск к лаборатории…':''),
    canCreate:enabled&&current?.state==='allowed',refresh,requireCurrent:refresh,invalidate};
}
export type LegacyAdmissionControl=ReturnType<typeof useLegacyAdmission>;
