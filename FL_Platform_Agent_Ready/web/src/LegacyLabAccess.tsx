import {useCallback,useEffect,useState,type FormEvent} from 'react';
import {ApiError} from './api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {useRequestScope} from './project-access-api';
import {changeLegacyAdmission,legacyLabError,readLegacyAdmission,validAdmissionReason,type LegacyLabAdmission} from './legacy-lab-api';

interface Props {user:{id:string;name:string;username:string};onClose:()=>void}

export function LegacyLabAccess({user,onClose}:Props){
  const begin=useRequestScope(`legacy-lab:${user.id}`);
  const [admission,setAdmission]=useState<LegacyLabAdmission|null>(null);
  const [loading,setLoading]=useState(true),[busy,setBusy]=useState(false);
  const [reason,setReason]=useState(''),[error,setError]=useState(''),[notice,setNotice]=useState('');
  const load=useCallback(async(conflict='')=>{
    const current=begin('read');setLoading(true);setAdmission(null);setReason('');setError('');setNotice('');
    try{
      const result=await readLegacyAdmission(user.id);
      if(current()){setAdmission(result);setError(conflict)}
    }catch(cause){if(current())setError(legacyLabError(cause))}
    finally{if(current())setLoading(false)}
  },[begin,user.id]);
  useEffect(()=>{void load()},[load]);

  async function save(event:FormEvent){
    event.preventDefault();if(!admission||busy||loading||!validAdmissionReason(reason))return;
    const current=begin('write');setBusy(true);setError('');setNotice('');
    try{
      const result=await changeLegacyAdmission(user.id,{status:admission.status==='active'?'revoked':'active',revision:admission.revision,reason});
      if(current()){
        setAdmission(result);setReason('');
        setNotice(result.status==='active'?'Допуск выдан.':'Новые запуски запрещены. Текущие задачи продолжат работу.');
        window.dispatchEvent(new CustomEvent('fl-legacy-lab-changed',{detail:{userID:user.id}}));
      }
    }catch(cause){if(current()){
      setAdmission(null);setReason('');
      if(cause instanceof ApiError&&cause.code==='VERSION_CONFLICT')await load(legacyLabError(cause));
      else setError(legacyLabError(cause));
    }}finally{if(current())setBusy(false)}
  }
  const granted=admission?.status==='active';
  return <Dialog label="legacy-lab-title" onClose={onClose}>
    <div className="section-heading"><h2 id="legacy-lab-title">Допуск к лаборатории</h2><button className="icon-button" aria-label="Закрыть допуск" onClick={onClose}><Icon name="close"/></button></div>
    <p><strong>{user.name}</strong><small className="block muted">{user.username}</small></p>
    <p className="muted">Разрешение создавать личные проекты и запускать обучение на общих узлах Digits.</p>
    {loading?<p role="status">Проверяем допуск…</p>:<>
      {admission&&<div className="form-grid" aria-label="Текущее состояние допуска">
        <p><small className="block muted">Допуск</small><strong>{granted?'Выдан':admission.status==='revoked'?'Отозван':'Не выдан'}</strong></p>
        <p><small className="block muted">Новые запуски</small><strong>{admission.can_create_legacy_work?'Разрешены':'Недоступны'}</strong></p>
      </div>}
      {granted&&!admission?.can_create_legacy_work&&<p className="note">Допуск выдан, но роль или состояние учётной записи пока не позволяют запускать обучение.</p>}
      {error&&<p role="alert" className="error">{error}</p>}
      {notice&&<p role="status" className="notice">{notice}</p>}
      {!admission?<button onClick={()=>void load()} disabled={busy}>Проверить допуск</button>:<form onSubmit={save}>
        <label htmlFor="legacy-lab-reason-input">Причина изменения</label>
        <input id="legacy-lab-reason-input" value={reason} onChange={event=>setReason(event.target.value)} disabled={busy} required aria-describedby="legacy-lab-reason" autoComplete="off"/>
        <p className="muted"><small id="legacy-lab-reason">От 1 до 240 символов, без переноса строки. {Array.from(reason).length}/240</small></p>
        <p className="note">{granted?'Запрет касается только новых запусков. Текущие задачи продолжат работу, а сохранённые результаты останутся доступны.':'Допуск не меняет роль, состояние учётной записи или доступ к чужим проектам.'}</p>
        <button className={granted?'danger full':'primary full'} disabled={busy||!validAdmissionReason(reason)}>{busy?'Сохранение…':granted?'Запретить новые запуски':'Выдать допуск'}</button>
      </form>}
    </>}
  </Dialog>;
}
