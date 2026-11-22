import {useCallback,useEffect,useRef,useState,type FormEvent} from 'react';
import {request,type Project} from './api';
import type {Organization} from './organizations-api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {ProjectAccess} from './ProjectAccess';
import {LegacyLabNotice} from './LegacyLabNotice';
import type {LegacyAdmissionControl} from './useLegacyAdmission';
import {accessLabel,errorCode,pagePath,projectError,projectPath,useRequestScope,type ProjectPage,type ProjectCreateInput,type OrganizationProject} from './project-access-api';
import './organization-projects.css';

interface Props {personalProjects:Project[];legacyAdmission:LegacyAdmissionControl;readOnly:boolean;onCreatePersonal:()=>void;onOpenPersonal:(id:string)=>void;onContextChange:()=>void}
export function OrganizationProjects({personalProjects,legacyAdmission,readOnly,onCreatePersonal,onOpenPersonal,onContextChange}:Props){
  const [organizations,setOrganizations]=useState<Organization[]>([]),[orgError,setOrgError]=useState('');
  const [context,setContext]=useState(''),[selected,setSelected]=useState('');
  const [data,setData]=useState<ProjectPage|null>(null),[loading,setLoading]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('');
  const [cursors,setCursors]=useState<(string|null)[]>([null]),[page,setPage]=useState(0),[available,setAvailable]=useState(true);
  const [draft,setDraft]=useState<{id:string;name:string;slug:string}|null>(null),[busy,setBusy]=useState(false),[formError,setFormError]=useState('');
  const begin=useRequestScope(context),organization=organizations.find(item=>item.id===context),cursor=cursors[page];
  const detailScope=`${context}:${selected}`,contextEpoch=useRef({key:detailScope,version:0});
  if(contextEpoch.current.key!==detailScope){contextEpoch.current.key=detailScope;contextEpoch.current.version++}
  const renderedEpoch=contextEpoch.current.version;
  useEffect(()=>{
    const current=begin('organizations');
    request<{items:Organization[]}>('/organizations').then(result=>{if(current()){setOrganizations(result.items.filter(item=>item.kind==='shared'));setOrgError('')}})
      .catch(cause=>{if(current())setOrgError(projectError(cause))});
  },[begin]);
  const load=useCallback(async(next:string|null=null)=>{
    if(!context)return;
    const current=begin('projects');setLoading(true);
    try{
      const result=await request<ProjectPage>(pagePath(projectPath(context),next));
      if(current()){setData(result);setError('');setAvailable(true)}
    }catch(cause){if(current()){
      const code=errorCode(cause);setError(projectError(cause));
      if(['NOT_FOUND','FORBIDDEN','ORG_PROJECTS_NOT_CONFIGURED'].includes(code)){setData(null);setSelected('');setDraft(null)}
      if(code==='ORG_PROJECTS_NOT_CONFIGURED')setAvailable(false);
    }}finally{if(current())setLoading(false)}
  },[context,begin]);
  useEffect(()=>{void load(cursor)},[load,cursor]);
  function changeContext(value:string){
    setContext(value);setSelected('');setData(null);setCursors([null]);setPage(0);setDraft(null);
    setError('');setFormError('');setNotice('');setBusy(false);setAvailable(true);onContextChange();
  }
  function refresh(){setCursors([null]);setPage(0);setSelected('');setDraft(null);setNotice('');void load(null)}
  function move(next:number){setSelected('');setError('');setPage(next)}
  function create(){
    if(!context){if(legacyAdmission.canCreate)onCreatePersonal();return}
    setDraft({id:crypto.randomUUID(),name:'',slug:''});setFormError('');setNotice('');
  }
  async function save(event:FormEvent){
    event.preventDefault();if(!draft||!data?.context.capabilities.can_create_projects||readOnly)return;
    const current=begin('create');setBusy(true);setFormError('');
    const input:ProjectCreateInput={name:draft.name.trim(),slug:draft.slug};
    try{
      const result=await request<OrganizationProject>(projectPath(context,draft.id),'PUT',input);
      if(current()){setDraft(null);setNotice(`Проект «${result.name}» создан.`);setSelected(result.id);await load(cursor)}
    }catch(cause){if(current()){
      setFormError(projectError(cause));
      if(['NOT_FOUND','FORBIDDEN'].includes(errorCode(cause))){setDraft(null);setSelected('');setData(null);setError(projectError(cause))}
    }}finally{if(current())setBusy(false)}
  }
  function unavailable(id:string){setSelected('');setData(old=>old?{...old,items:old.items.filter(item=>item.id!==id)}:old);setError('Проект больше недоступен. Обновите список.')}
  const canCreate=!context?legacyAdmission.canCreate:!readOnly&&available&&!loading&&data?.context.capabilities.can_create_projects;
  return <>
    <div className="page-heading"><div><h1>Проекты</h1><p className="muted">Личная работа и доступ внутри организаций.</p></div>{canCreate&&<button className="primary" onClick={create}><Icon name="plus"/>Создать проект</button>}</div>
    <div className="project-context"><label><span id="project-context-label">Рабочая область</span><select aria-labelledby="project-context-label" value={context} onChange={event=>changeContext(event.target.value)}><option value="">Личные проекты</option>{organizations.map(item=><option key={item.id} value={item.id}>{item.name}{item.state==='suspended'?' · приостановлена':''}</option>)}</select></label><p className="muted">{context?'Права и проекты выбранной организации.':'Ваши личные проекты доступны только вам. Личные чаты и подключения остаются отдельными.'}</p></div>
    {orgError&&<p role="status" className="note">{orgError}</p>}
    {notice&&<p role="status" className="notice">{notice}</p>}
    {!context&&<LegacyLabNotice admission={legacyAdmission}/>}
    {!context?<section className="panel" aria-label="Личные проекты"><h2>Личные проекты</h2>{personalProjects.length?<ul className="project-rows">{personalProjects.map(item=><li key={item.id}><div><strong>{item.name}</strong><small className="block muted">{item.slug} · Личный доступ</small></div><button onClick={()=>onOpenPersonal(item.id)} aria-label={`Открыть эксперименты: ${item.name}`}>Открыть эксперименты<Icon name="arrow"/></button></li>)}</ul>:<div className="empty"><h3>Пока нет личных проектов</h3><p>{legacyAdmission.canCreate?'Создайте проект, чтобы начать работу с экспериментами.':'Здесь остаются ваши ранее созданные проекты. Допуск нужен только для новой работы.'}</p></div>}</section>:<>
      {!available?<section className="panel empty"><h2>Раздел готовится к запуску</h2><p>Проекты организаций пока не включены на сервере. Вы можете продолжить работу в личных проектах.</p><button onClick={refresh} disabled={loading}>Проверить доступность</button></section>:<>
        {error&&<p className="error" role="alert">{error} <button onClick={refresh} disabled={loading}>Обновить список</button></p>}
        <section className="panel" aria-label={`Проекты: ${organization?.name||'Организация'}`} aria-busy={loading}>
          <div className="section-heading organization-heading"><h2>{organization?.name||'Проекты организации'}</h2><button onClick={refresh} disabled={loading}>Обновить проекты</button></div>
          {data?.context.organization_state==='suspended'&&<p className="note">Организация приостановлена. Доступен только просмотр.</p>}
          {loading&&!data?<p role="status">Загружаем проекты…</p>:data?.items.length?<ul className="project-rows">{data.items.map(item=><li key={item.id} className={selected===item.id?'selected':''}><button className="project-name" aria-pressed={selected===item.id} onClick={()=>{setSelected(item.id);setNotice('')}}><strong>{item.name}</strong><small>{item.slug}</small></button><div className="project-row-meta"><span>{item.visibility==='organization'?'Просмотр организации':'Ограниченный доступ'}</span><small className="muted">{accessLabel(item)}</small></div></li>)}</ul>:!error&&<div className="empty"><h3>Нет доступных проектов</h3><p>{canCreate?'Создайте проект. Совместный просмотр можно включить отдельно.':'Проекты появятся, когда вам предоставят доступ или откроют просмотр организации.'}</p></div>}
          {data&&<div className="table-pagination"><span>Страница {page+1} · {data.items.length} проектов</span><div><button disabled={loading||page===0} onClick={()=>move(page-1)}>Назад</button><button disabled={loading||!data.next_cursor} onClick={()=>{if(data.next_cursor){setCursors(old=>[...old.slice(0,page+1),data.next_cursor]);move(page+1)}}}>Далее</button></div></div>}
        </section>
        {selected&&organization&&<ProjectAccess key={`${context}:${selected}`} organization={organization} projectID={selected} readOnly={readOnly}
          onUnavailable={()=>{if(contextEpoch.current.version===renderedEpoch)unavailable(selected)}}
          onProjectChanged={updated=>{if(contextEpoch.current.version!==renderedEpoch||updated.organization_id!==context)return;setData(old=>old&&old.context.organization_id===context?{...old,items:old.items.map(item=>item.id===updated.id?updated:item)}:old)}}/>}
      </>}
    </>}
    {draft&&<Dialog label="organization-project-create" onClose={()=>{if(!busy)setDraft(null)}}><div className="section-heading"><h2 id="organization-project-create">Создать проект</h2><button className="icon-button" aria-label="Закрыть" disabled={busy} onClick={()=>setDraft(null)}><Icon name="close"/></button></div><p className="muted">Организация: {organization?.name}</p><form onSubmit={save}><fieldset className="provider-fields" disabled={busy}><label>Название проекта<input autoFocus required maxLength={120} value={draft.name} onChange={event=>setDraft({...draft,name:event.target.value})}/></label><label><span id="organization-project-slug-label">Короткое имя</span><input aria-labelledby="organization-project-slug-label" aria-describedby="organization-project-slug-help" required pattern="[a-z][a-z0-9-]{1,47}" value={draft.slug} onChange={event=>setDraft({...draft,slug:event.target.value})}/><small id="organization-project-slug-help">Латинские строчные буквы, цифры и дефис, 2–48 символов.</small></label></fieldset><p className="note">Проект будет доступен вам и администрации организации. Доступ участников и общий просмотр настраиваются отдельно.</p>{formError&&<p role="alert" className="error">{formError}</p>}<button className="primary full" disabled={busy||!draft.name.trim()}>{busy?'Создание…':'Создать проект'}</button></form></Dialog>}
  </>;
}
