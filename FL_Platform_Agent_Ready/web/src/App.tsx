import {Assistants} from './Assistants';
import {Chat} from './Chat';
import {Providers} from './Providers';
import {Organizations} from './Organizations';
import {OrganizationProjects} from './OrganizationProjects';
import {usePagination} from './Pagination';
import {useRouteMotion} from './motion';
import {Dialog} from './Dialog';
import {Landing} from './Landing';
import {Login} from './Login';
import {Brand,Icon} from './Icon';
import {useCallback,useEffect,useRef,useState,type FormEvent} from 'react';
import {CreateExperiment} from './CreateExperiment';
import {RunDetails} from './RunDetails';
import {Users,Agents,Audit} from './Administration';
import {Account} from './Account';
import {useSessionBootstrap} from './useSessionBootstrap';
import {useProjectRuns} from './useProjectRuns';
import {useLegacyAdmission} from './useLegacyAdmission';
import {LegacyLabNotice} from './LegacyLabNotice';
import {request,setToken,token,statuses,type Project} from './api';
export default function App(){
  const [me,setMe]=useState<{id:string;name:string;role:string}|null>(null),[page,setPage]=useState(location.hash.slice(1)||'home');
  const titles:Record<string,string>={projects:'Проекты',experiments:'Эксперименты',organizations:'Организации',providers:'Провайдеры',chat:'Чат',assistants:'Ассистенты',users:'Пользователи',agents:'Агенты',audit:'Журнал действий',account:'Мой профиль'};
  const [logged,setLogged]=useState(!!token),[projects,setProjects]=useState<Project[]>([]),[project,setProject]=useState('');
  const [create,setCreate]=useState(false),[newProject,setNewProject]=useState(false);
  const [name,setName]=useState('Моё исследование'),[slug,setSlug]=useState('research'),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const activePage=page==='login'?'experiments':page;
  const scopeKey=`${token}:${logged}:${activePage}:${project}:${me?.id||''}:${me?.role||''}`;
  const uiScope=useRef({key:scopeKey,generation:0});
  if(uiScope.current.key!==scopeKey){uiScope.current.key=scopeKey;uiScope.current.generation++}
  const renderGeneration=uiScope.current.generation;
  useRouteMotion(`${page}:${logged}`);
  useEffect(()=>{const reset=()=>setLogged(false);window.addEventListener('fl-unauthorized',reset);return()=>window.removeEventListener('fl-unauthorized',reset)},[]);
  useEffect(()=>{if(!logged){setMe(null);setProjects([]);setProject('');setCreate(false);setNewProject(false);setError('');setBusy(false);}},[logged]);
  useEffect(()=>{const change=()=>{const value=location.hash.slice(1);setPage(['home','login','users','agents','audit','account','projects','experiments','organizations','providers','chat','assistants'].includes(value)?value:'home')};change();window.addEventListener('hashchange',change);return()=>window.removeEventListener('hashchange',change)},[]);
  const receiveProjects=useCallback((items:Project[])=>{setProjects(items);setProject(p=>items.some(i=>i.id===p)?p:items[0]?.id||'')},[]);
  const sessionError=useSessionBootstrap(logged,setMe,receiveProjects);
  useEffect(()=>{if(logged&&activePage==='experiments'&&projects.length&&!projects.some(item=>item.id===project))setProject(projects[0].id)},[logged,activePage,projects,project]);
  const {runs,selected,setSelected,updated,error:runsError,refresh}=useProjectRuns(logged&&activePage==='experiments',project);
  const admission=useLegacyAdmission({enabled:logged&&['projects','experiments'].includes(activePage),user:me,scopeKey:`${activePage}:${project}`});
  useEffect(()=>{setCreate(false);setError('')},[project]);
  useEffect(()=>{setCreate(false);setNewProject(false);setError('');setBusy(false)},[activePage]);
  function closeProjectDialog(){uiScope.current.generation++;setNewProject(false);setError('');setBusy(false)}
  function openProjectDialog(){if(!admission.canCreate)return;uiScope.current.generation++;setError('');setBusy(false);setNewProject(true)}
  function changeProjectContext(){uiScope.current.generation++;setProject('');setCreate(false);setNewProject(false);setError('');setBusy(false)}
  async function addProject(e:FormEvent){e.preventDefault();if(busy)return;const creationSession=token,generation=uiScope.current.generation;const current=()=>token===creationSession&&uiScope.current.generation===generation;setBusy(true);try{if(!await admission.requireCurrent()||!current())return;const created=await request<Project>('/projects','POST',{name,slug});if(!current())return;setProjects(p=>[created,...p]);setProject(created.id);setNewProject(false);setError('')}catch(e){if(current()){admission.invalidate(e);setError((e as Error).message)}}finally{if(current())setBusy(false)}}
  const runPage=usePagination(runs,project,6);
  if(page==='home')return <Landing logged={logged}/>;
  if(!logged)return <Login onLogin={()=>{setLogged(true);if(page==='login')location.hash='experiments'}}/>;
  const run=runs.find(r=>r.id===selected);
  return <div className="workspace"><a className="skip-link" href="#workspace-content" onClick={e=>{e.preventDefault();document.getElementById('workspace-content')?.focus()}}>К содержанию</a><aside className="sidebar"><a href="#home" aria-label="FL Platform — главная"><Brand/></a><p className="nav-label">РАБОЧЕЕ ПРОСТРАНСТВО</p><nav className="platform-nav" aria-label="Разделы платформы">{Object.entries(titles).filter(([key])=>me?.role==='admin'||['projects','experiments','account','organizations'].includes(key)||(['providers','chat','assistants'].includes(key)&&me?.role==='researcher')).map(([key,title])=><a key={key} href={`#${key}`} className={`nav-item ${activePage===key?'active':''}`} aria-current={activePage===key?'page':undefined}><Icon name={key==='projects'?'layers':key}/>{title}</a>)}</nav><div className="sidebar-info"><span className="dot"/> Федеративное обучение<p>Управление · выполнение · результаты</p></div><div className="sidebar-bottom"><div className="avatar">И</div><div>{me?.name||'Подключение…'}<small>{me?.role==='admin'?'Администратор':'Рабочее пространство'}</small></div><button className="logout" aria-label="Выйти" onClick={async()=>{try{await request('/session','DELETE');setToken('');setLogged(false)}catch(e){setError((e as Error).message)}}}><Icon name="logout"/></button></div></aside>
    <main id="workspace-content" tabIndex={-1}><header className="topbar"><span>Платформа <span className="muted">/ {titles[activePage]}</span></span><span className="sync"><i className="dot"/> {activePage==='projects'?'Проекты и доступ':activePage==='organizations'?'Организации и участники':activePage==='assistants'?'Личные ассистенты':activePage==='chat'?'Личные диалоги':activePage==='providers'?'Личные подключения':updated?`Обновлено ${updated}`:'Подключение…'}</span></header>
      <div className="content">{sessionError&&<p role="alert" className="error">{sessionError}</p>}{activePage==='projects'?<OrganizationProjects key={[token,me?.id,me?.role].join(":")} personalProjects={projects} legacyAdmission={admission} readOnly={!me||me.role==='viewer'} onCreatePersonal={openProjectDialog} onOpenPersonal={id=>{if(projects.some(item=>item.id===id)){setProject(id);location.hash='experiments'}}} onContextChange={changeProjectContext}/>:activePage==='organizations'?<Organizations readOnly={!me||me.role==='viewer'}/>:activePage==='assistants'?(me?.role==='researcher'||me?.role==='admin'?<Assistants/>:<p className="error">Раздел доступен исследователям и администраторам.</p>):activePage==='chat'?(me?.role==='researcher'||me?.role==='admin'?<Chat/>:<p className="error">Раздел доступен исследователям и администраторам.</p>):activePage==='providers'?(me?.role==='researcher'||me?.role==='admin'?<Providers/>:<p className="error">Раздел доступен исследователям и администраторам.</p>):activePage==='account'?<Account name={me?.name||''} role={me?.role||''} onLogout={()=>setLogged(false)}/>:activePage!=='experiments'?(me?.role==='admin'?(activePage==='users'?<Users/>:activePage==='agents'?<Agents/>:<Audit/>):<p className="error">Раздел доступен администратору.</p>):<><div className="page-heading"><div><h1>Эксперименты</h1><p className="muted">От конфигурации до воспроизводимого результата.</p></div><button className="primary" disabled={!admission.canCreate} onClick={()=>project?setCreate(true):openProjectDialog()}><Icon name="plus"/>Новый эксперимент</button></div>
        <LegacyLabNotice admission={admission}/>{(error||runsError)&&<p role="alert" className="error">{error||runsError}</p>}
        <div className="project-bar"><label>Проект<select aria-label="Текущий проект" value={project} onChange={e=>setProject(e.target.value)}>{!projects.length&&<option value="">Пока нет проектов</option>}{projects.map(p=><option value={p.id} key={p.id}>{p.name}</option>)}</select></label><button className="text-button" disabled={!admission.canCreate} onClick={openProjectDialog}><Icon name="plus"/>Создать проект</button><span className="muted">{runs.filter(r=>r.status==='completed').length} завершено · {runs.filter(r=>r.status==='running').length} выполняется</span></div>
        <section className="panel run-list"><div className="section-heading"><h3>Запуски проекта</h3><span className="muted">{runs.length} всего</span></div>{runs.length?<div className="scroll-table"><table><thead><tr><th>Запуск</th><th>Состояние</th><th>Прогресс</th><th>Создан</th></tr></thead><tbody>{runPage.items.map(r=><tr key={r.id} className={r.id===selected?'selected':''}><td><button className="run-link" onClick={()=>setSelected(r.id)}>{r.id.slice(0,8)} <Icon name="arrow"/></button></td><td><span className={`badge ${r.status}`}>{statuses[r.status]||r.status}</span></td><td>{r.completed_rounds} / {r.total_rounds}</td><td>{new Date(r.requested_at).toLocaleString('ru')}</td></tr>)}</tbody></table></div>:<div className="empty"><h3>Запусков пока нет</h3><p>{admission.canCreate?'Создайте эксперимент, чтобы начать обучение.':'Здесь появятся результаты сохранённых запусков.'}</p></div>}{runs.length>0&&runPage.controls}</section>
        {run&&<RunDetails key={run.id} admission={admission} readOnly={me?.role==='viewer'} run={run} runs={runs} refresh={id=>void refresh(id)}/>}
        </>}<footer>FL Platform <span>Федеративное обучение · воспроизводимые результаты</span></footer>
      </div></main>
    {create&&<CreateExperiment key={[token,project].join(':')} admission={admission} project={project} onClose={()=>setCreate(false)} onCreated={r=>{if(uiScope.current.generation!==renderGeneration)return;setCreate(false);void refresh(r.id)}}/>}
    {newProject&&<Dialog label="project-title" onClose={closeProjectDialog}><div className="section-heading"><h2 id="project-title">Новый проект</h2><button className="icon-button" aria-label="Закрыть" onClick={closeProjectDialog}><Icon name="close"/></button></div><LegacyLabNotice admission={admission}/><form onSubmit={addProject}><label>Название<input value={name} onChange={e=>setName(e.target.value)} maxLength={120} required/></label><label><span id="project-slug-label">Короткое имя</span><input aria-labelledby="project-slug-label" aria-describedby="project-slug-hint" value={slug} onChange={e=>setSlug(e.target.value)} pattern="[a-z][a-z0-9-]{1,47}" required/><small id="project-slug-hint">Латинские строчные буквы, цифры и дефис, 2–48 символов.</small></label>{error&&<p role="alert" className="error">{error}</p>}<button className="primary full" disabled={busy||!admission.canCreate}>{busy?'Создание…':'Создать проект'}</button></form></Dialog>}
  </div>;
}
