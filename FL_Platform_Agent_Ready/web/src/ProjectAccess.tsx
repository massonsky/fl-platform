import {useCallback,useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './api';
import type {Organization} from './organizations-api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {accessLabel,accessSources,errorCode,pagePath,projectError,projectPath,useRequestScope,writeLimits,
  type AccessMember,type AccessPage,type GrantInput,type OrganizationProject,type VisibilityInput} from './project-access-api';

interface Props {organization:Organization;projectID:string;readOnly:boolean;onUnavailable:()=>void;onProjectChanged:(project:OrganizationProject)=>void}
export function ProjectAccess({organization,projectID,readOnly,onUnavailable,onProjectChanged}:Props){
  const base=projectPath(organization.id,projectID),begin=useRequestScope(`${organization.id}:${projectID}`);
  const lost=useRef(onUnavailable);lost.current=onUnavailable;
  const changed=useRef(onProjectChanged);changed.current=onProjectChanged;
  const [project,setProject]=useState<OrganizationProject|null>(null),[roster,setRoster]=useState<AccessPage|null>(null);
  const [loading,setLoading]=useState(true),[error,setError]=useState(''),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false);
  const [visibility,setVisibility]=useState<VisibilityInput['visibility']>('private');
  const [cursors,setCursors]=useState<(string|null)[]>([null]),[page,setPage]=useState(0);
  const [picking,setPicking]=useState(false),[candidates,setCandidates]=useState<AccessPage|null>(null),[candidateLoading,setCandidateLoading]=useState(false),[candidateError,setCandidateError]=useState('');
  const [editing,setEditing]=useState<AccessMember|null>(null),[role,setRole]=useState<GrantInput['role']>('viewer'),[status,setStatus]=useState<GrantInput['status']>('active'),[formError,setFormError]=useState('');
  const canReadRoster=project?.access_source==='organization_admin',cursor=cursors[page];
  const load=useCallback(async(next:string|null=null)=>{
    const current=begin('read');setLoading(true);
    try{
      const detail=await request<OrganizationProject>(base);
      if(!current())return;
      setProject(detail);changed.current(detail);setVisibility(detail.visibility);
      if(detail.access_source==='organization_admin'){
        const result=await request<AccessPage>(pagePath(`${base}/access`,next));
        if(!current())return;
        setRoster(result);setProject(result.project);changed.current(result.project);setVisibility(result.project.visibility);
      }else setRoster(null);
      if(current())setError('');
    }catch(cause){if(current()){
      setError(projectError(cause));
      if(errorCode(cause)==='NOT_FOUND')lost.current();
      if(errorCode(cause)==='FORBIDDEN')setRoster(null);
    }}finally{if(current())setLoading(false)}
  },[base,begin]);
  useEffect(()=>{void load(cursor)},[load,cursor]);
  const canManage=!!project?.capabilities.can_manage_access&&!readOnly&&!loading&&!error;
  function refresh(){setCursors([null]);setPage(0);setEditing(null);setPicking(false);setFormError('');setNotice('');void load(null)}
  async function saveVisibility(event:FormEvent){
    event.preventDefault();if(!project||!canManage)return;
    const current=begin('write');setBusy(true);setNotice('');
    const input:VisibilityInput={visibility,acl_revision:project.acl_revision};
    try{
      const result=await request<OrganizationProject>(`${base}/visibility`,'PATCH',input);
      if(current()){setProject(result);changed.current(result);setNotice('Видимость проекта сохранена.');await load(cursor)}
    }catch(cause){if(current()){setError(projectError(cause));if(errorCode(cause)==='NOT_FOUND')lost.current()}}
    finally{if(current())setBusy(false)}
  }
  async function loadCandidates(next:string|null=null){
    const current=begin('candidates');setCandidateLoading(true);setCandidateError('');
    try{
      const result=await request<AccessPage>(pagePath(`${base}/access/candidates`,next));
      if(current()){setCandidates(result);setProject(result.project);changed.current(result.project)}
    }catch(cause){if(current()){
      setCandidateError(projectError(cause));
      if(errorCode(cause)==='NOT_FOUND')lost.current();
      if(errorCode(cause)==='FORBIDDEN'){setPicking(false);setCandidates(null);setError(projectError(cause))}
    }}
    finally{if(current())setCandidateLoading(false)}
  }
  function edit(member:AccessMember){setPicking(false);begin('candidates');setEditing(member);setRole(member.grant?.role||'viewer');setStatus(member.grant?.status||'active');setFormError('');setNotice('')}
  async function saveGrant(event:FormEvent){
    event.preventDefault();if(!editing||!canManage||(status==='active'?!editing.can_grant:!editing.can_revoke))return;
    const current=begin('write');setBusy(true);setFormError('');
    const input:GrantInput={role,status,revision:editing.grant?.revision||0};
    try{
      await request(`${base}/members/${encodeURIComponent(editing.user_id)}`,'PUT',input);
      if(current()){setEditing(null);setNotice(status==='revoked'?'Доступ участника отозван.':'Права участника сохранены.');await load(cursor)}
    }catch(cause){if(current()){
      setFormError(projectError(cause));
      if(errorCode(cause)==='NOT_FOUND')lost.current();
      if(errorCode(cause)==='FORBIDDEN'){setEditing(null);setError(projectError(cause))}
    }}finally{if(current())setBusy(false)}
  }
  if(!project)return <section className="panel" aria-busy={loading}>{error?<p role="alert" className="error">{error} <button onClick={refresh}>Обновить проект</button></p>:<p role="status">Загружаем проект…</p>}</section>;
  return <section className="panel project-access" aria-label={`Доступ к проекту: ${project.name}`} aria-busy={loading}>
    <div className="section-heading organization-heading"><div><h2>{project.name}</h2><p className="muted">{project.slug} · {accessSources[project.access_source]}</p></div><button onClick={refresh} disabled={loading||busy}>Обновить доступ</button></div>
    {error&&<p role="alert" className="error">{error}</p>}{notice&&<p role="status" className="notice">{notice}</p>}
    <div className="project-visibility"><div><h3>Кому виден проект</h3><p className="muted">{project.visibility==='organization'?'Активные участники организации могут просматривать проект. Индивидуальный отзыв доступа сохраняет силу.':'Доступ есть у автора и назначенных участников, пока действуют их права в организации. Администрация имеет доступ по своей роли.'}</p></div>
      {canManage?<form onSubmit={saveVisibility}><label><span id="project-visibility-label">Область просмотра</span><select aria-labelledby="project-visibility-label" value={visibility} disabled={busy} onChange={event=>setVisibility(event.target.value as VisibilityInput['visibility'])}><option value="private">Ограниченный доступ</option><option value="organization">Просмотр организации</option></select></label><button disabled={busy||visibility===project.visibility}>Сохранить видимость</button></form>:<strong>{project.visibility==='organization'?'Просмотр организации':'Ограниченный доступ'}</strong>}
    </div>
    <p className="note">Обучение организации будет доступно после подключения её вычислительных узлов. Личные эксперименты продолжают работать отдельно.</p>
    {canReadRoster?<>
      <div className="section-heading organization-heading"><h3>Доступ участников</h3>{canManage&&<button disabled={busy} onClick={()=>{setPicking(true);setCandidates(null);void loadCandidates()}}><Icon name="plus"/>Выдать доступ</button>}</div>
      {!project.capabilities.can_manage_access&&<p className="muted">Доступен просмотр прав. Изменения сейчас запрещены.</p>}
      {roster?.items.length?<ul className="access-members">{roster.items.map(member=><li key={member.user_id}><div><strong>{member.display_name}</strong><small>{accessSources[member.access_source]}</small>{member.write_cap_reason&&<small>{writeLimits[member.write_cap_reason]}</small>}</div><div className="access-member-right"><span>{accessLabel(member)}</span>{canManage&&(member.can_grant||member.can_revoke)?<button disabled={busy} aria-label={`Настроить доступ: ${member.display_name}`} onClick={()=>edit(member)}>Настроить</button>:<small className="muted">{member.grant_blocked_reason==='INHERITED_ACCESS'?'По роли в организации':'Только просмотр'}</small>}</div></li>)}</ul>:!loading&&!error&&<p className="empty">Участники не найдены.</p>}
      {roster&&<div className="table-pagination"><span>Страница {page+1}</span><div><button disabled={loading||busy||page===0} onClick={()=>setPage(page-1)}>Назад</button><button disabled={loading||busy||!roster.next_cursor} onClick={()=>{if(roster.next_cursor){setCursors(old=>[...old.slice(0,page+1),roster.next_cursor]);setPage(page+1)}}}>Далее</button></div></div>}
    </>:<p className="muted">Ваш доступ: {accessLabel(project)}. Настройка прав участников доступна администрации организации.</p>}
    {picking&&canManage&&<Dialog label="project-candidates-title" onClose={()=>{begin('candidates');setPicking(false)}}><div className="section-heading"><h2 id="project-candidates-title">Выдать доступ</h2><button className="icon-button" aria-label="Закрыть" onClick={()=>{begin('candidates');setPicking(false)}}><Icon name="close"/></button></div><p className="muted">Выберите действующего участника организации.</p>{candidateError&&<p role="alert" className="error">{candidateError} <button onClick={()=>void loadCandidates()}>Обновить кандидатов</button></p>}{candidateLoading?<p role="status">Загружаем участников…</p>:candidates?.items.length?<ul className="project-candidates">{candidates.items.map(member=><li key={member.user_id}><div><strong>{member.display_name}</strong>{member.write_cap_reason&&<small>{writeLimits[member.write_cap_reason]}</small>}</div>{member.can_grant&&<button aria-label={`Выбрать участника: ${member.display_name}`} onClick={()=>edit(member)}>Выбрать</button>}</li>)}</ul>:!candidateError&&<p className="empty">Нет доступных участников. Приглашения здесь не отправляются.</p>}<div className="actions"><button disabled={candidateLoading} onClick={()=>void loadCandidates()}>В начало</button><button disabled={candidateLoading||!candidates?.next_cursor} onClick={()=>void loadCandidates(candidates?.next_cursor)}>Далее</button></div></Dialog>}
    {editing&&canManage&&<Dialog label="project-member-title" onClose={()=>{if(!busy)setEditing(null)}}><div className="section-heading"><h2 id="project-member-title">Права участника</h2><button className="icon-button" aria-label="Закрыть" disabled={busy} onClick={()=>setEditing(null)}><Icon name="close"/></button></div><p>{editing.display_name}</p><form onSubmit={saveGrant}><fieldset className="provider-fields" disabled={busy}><label><span id="project-member-role">Роль в проекте</span><select aria-labelledby="project-member-role" value={role} onChange={event=>setRole(event.target.value as GrantInput['role'])}><option value="viewer">Наблюдатель</option><option value="researcher">Исследователь</option></select></label><label><span id="project-member-status">Доступ</span><select aria-labelledby="project-member-status" value={status} onChange={event=>setStatus(event.target.value as GrantInput['status'])}><option value="active" disabled={!editing.can_grant}>Предоставлен</option><option value="revoked" disabled={!editing.can_revoke}>Отозван</option></select></label>{editing.write_cap_reason&&<p className="note">{writeLimits[editing.write_cap_reason]}</p>}<p className="muted">Отзыв запрещает доступ и при общем просмотре организации. Назначенная роль не расширяет ограничения учётной записи.</p></fieldset>{formError&&<p role="alert" className="error">{formError} <button type="button" disabled={busy} onClick={refresh}>Закрыть и обновить</button></p>}<button className="primary full" disabled={busy||(status==='active'?!editing.can_grant:!editing.can_revoke)}>{busy?'Сохранение…':'Сохранить права'}</button></form></Dialog>}
  </section>;
}
