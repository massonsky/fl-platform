import {usePagination} from './Pagination';
import {Icon} from './Icon';
import {Dialog} from './Dialog';
import {LegacyLabAccess} from './LegacyLabAccess';
import {useCallback,useEffect,useState,type FormEvent} from 'react';
import {list,request} from './api';
import type {components} from './generated/schema';

type User=components['schemas']['AdminUser'];
type Agent=components['schemas']['Agent'];
type Event=components['schemas']['AdminEvent'];
const roles={admin:'Администратор',researcher:'Исследователь',viewer:'Наблюдатель'};
const actions:Record<string,string>={'ai.provider.create':'Добавлено подключение моделей','ai.provider.update':'Изменено подключение моделей','user.create':'Создан пользователь','user.update':'Изменена учётная запись','agent.update':'Изменён доступ агента','password.change':'Изменён пароль'};

export function Users(){
 const [users,setUsers]=useState<User[]>([]),[selected,setSelected]=useState<User|null>(null);
 const [labUser,setLabUser]=useState<User|null>(null);
 const [creating,setCreating]=useState(false),[name,setName]=useState(''),[username,setUsername]=useState('');
 const [role,setRole]=useState<User['role']>('researcher'),[disabled,setDisabled]=useState(false),[password,setPassword]=useState('');
 const [error,setError]=useState(''),[busy,setBusy]=useState(false),[search,setSearch]=useState(''),[notice,setNotice]=useState('');
 const refresh=useCallback(()=>list<User>('/admin/users').then(setUsers).catch(e=>setError(e.message)),[]);
 useEffect(()=>{void refresh()},[refresh]);
 function edit(user?:User){setSelected(user||null);setCreating(!user);setName(user?.name||'');setUsername(user?.username||'');setRole(user?.role||'researcher');setDisabled(user?.disabled||false);setPassword('');setError('');setNotice('')}
 async function save(e:FormEvent){e.preventDefault();setBusy(true);setError('');try{
  await request(selected?`/admin/users/${selected.id}`:'/admin/users','POST',selected?{role,disabled,...(password?{password}:{})}:{username,name,role,password});
  setCreating(false);setSelected(null);setPassword('');setNotice('Учётная запись сохранена. При изменении доступов прежние сессии отозваны.');await refresh();
 }catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 const visible=users.filter(u=>(u.name+' '+u.username).toLowerCase().includes(search.toLowerCase()));
 const pagination=usePagination(visible,search);
 return <><div className="page-heading"><div><h1>Пользователи</h1><p className="muted">Учётные записи, роли и доступ к платформе.</p></div><button className="primary" onClick={()=>edit()}><Icon name="plus"/>Пользователь</button></div>
 {error&&<p className="error" role="alert">{error}</p>}{notice&&<p className="notice" role="status">{notice}</p>}
 <section className="panel"><label className="search-field">Поиск пользователя<input type="search" value={search} onChange={e=>setSearch(e.target.value)} placeholder="Имя или логин"/></label><div className="scroll-table"><table><thead><tr><th>Пользователь</th><th>Роль</th><th>Доступ</th><th>Действие</th></tr></thead><tbody>{pagination.items.map(u=><tr key={u.id}><td><strong>{u.name}</strong><small className="block muted">{u.username}</small></td><td>{roles[u.role]}</td><td><span className={`badge ${u.disabled?'failed':'completed'}`}>{u.disabled?'Отключён':'Активен'}</span></td><td><div className="actions"><button onClick={()=>edit(u)}>Изменить</button><button onClick={()=>setLabUser(u)} aria-label={`Допуск к лаборатории: ${u.name}`}>Допуск</button></div></td></tr>)}</tbody></table></div>{!visible.length&&<p className="muted">Пользователи не найдены.</p>}{pagination.controls}</section>
 {labUser&&<LegacyLabAccess key={labUser.id} user={labUser} onClose={()=>setLabUser(null)}/>}
 {(creating||selected)&&<Dialog label="user-title" onClose={()=>{setCreating(false);setSelected(null);setPassword('')}}><div className="section-heading"><h2 id="user-title">{selected?'Учётная запись':'Новый пользователь'}</h2><button className="icon-button" aria-label="Закрыть" onClick={()=>{setCreating(false);setSelected(null);setPassword('')}}><Icon name="close"/></button></div><form onSubmit={save}>
 {!selected&&<><label>Имя<input value={name} onChange={e=>setName(e.target.value)} required maxLength={120}/></label><label>Логин<input value={username} onChange={e=>setUsername(e.target.value)} required pattern="[a-z][a-z0-9._-]{2,63}" autoComplete="off"/><small>3–64 символа: латинские строчные буквы, цифры, точка, дефис.</small></label></>}
 <label>Роль<select value={role} onChange={e=>setRole(e.target.value as User['role'])}>{Object.entries(roles).map(([key,value])=><option key={key} value={key}>{value}</option>)}</select></label>
 <label>{selected?'Новый пароль (необязательно)':'Пароль'}<input type="password" value={password} onChange={e=>setPassword(e.target.value)} required={!selected} minLength={12} maxLength={128} autoComplete="new-password"/><small>Не менее 12 символов. Передайте пользователю отдельно.</small></label>
 {selected&&<label className="check-label"><input type="checkbox" checked={disabled} onChange={e=>setDisabled(e.target.checked)}/> Заблокировать доступ</label>}
 <p className="note">Администратор управляет доступом. Эксперименты других владельцев остаются закрыты. Последнего администратора отключить нельзя.</p>{error&&<p className="error" role="alert">{error}</p>}<button className="primary full" disabled={busy}>{busy?'Сохранение…':'Сохранить'}</button></form></Dialog>}</>;
}

export function Agents(){
 const [agents,setAgents]=useState<Agent[]>([]),[error,setError]=useState(''),[busy,setBusy]=useState('');
 const refresh=useCallback(()=>list<Agent>('/admin/agents').then(setAgents).catch(e=>setError(e.message)),[]);
 useEffect(()=>{void refresh();const timer=setInterval(refresh,5000);return()=>clearInterval(timer)},[refresh]);
 async function toggle(agent:Agent){setBusy(agent.id);setError('');try{await request(`/admin/agents/${agent.id}`,'POST',{disabled:agent.status!=='disabled'});await refresh()}catch(e){setError((e as Error).message)}finally{setBusy('')}}
 const pagination=usePagination(agents);
 return <><div className="page-heading"><div><h1>Агенты</h1><p className="muted">Подключение и доступ клиентов федеративного обучения.</p></div><span className="badge completed">{agents.filter(a=>a.status==='online').length} в сети</span></div>{error&&<p className="error" role="alert">{error}</p>}
 <section className="panel"><div className="scroll-table"><table><thead><tr><th>Агент</th><th>Состояние</th><th>Последний сигнал</th><th>Задания</th><th>Доступ</th></tr></thead><tbody>{pagination.items.map(a=><tr key={a.id}><td><strong>{a.name}</strong><small className="block muted">Раздел {a.shard+1} · версия {a.version}</small></td><td><span className={`badge ${a.status==='online'?'completed':a.status==='disabled'?'failed':'cancelled'}`}>{a.status==='online'?'В сети':a.status==='disabled'?'Отключён':'Нет связи'}</span></td><td>{a.last_seen_at?new Date(a.last_seen_at).toLocaleString('ru'):'Ещё не подключался'}</td><td>{a.active_tasks}</td><td><button disabled={busy===a.id} onClick={()=>void toggle(a)}>{a.status==='disabled'?'Включить':'Отключить'}</button></td></tr>)}</tbody></table></div>{!agents.length&&<p className="empty">Нет зарегистрированных агентов.</p>}{pagination.controls}</section>
 <section className="panel diagnostics"><h3>Подключение и диагностика</h3><p>Каждый агент работает со своим разделом данных и индивидуальным ключом. Для текущего профиля Digits используются пять разделов. В strict-режиме для запуска нужны все пять агентов.</p><p className="muted">«Нет связи» означает отсутствие сигнала более 30 секунд. Отключение запрещает новые запросы агента; незавершённый раунд следует отменить или дождаться установленного таймаута.</p><p>Порядок установки и переноса узла приведён в руководстве администратора. Ключи не отображаются в этом списке.</p></section></>;
}

export function Audit(){
 const [events,setEvents]=useState<Event[]>([]),[error,setError]=useState('');
 const refresh=useCallback(()=>list<Event>('/admin/events').then(setEvents).catch(e=>setError(e.message)),[]);
 useEffect(()=>{void refresh()},[refresh]);
 const pagination=usePagination(events);
 return <><div className="page-heading"><div><h1>Журнал действий</h1><p className="muted">Изменения учётных записей и доступа агентов.</p></div><button onClick={()=>void refresh()}>Обновить</button></div>{error&&<p className="error" role="alert">{error}</p>}<section className="panel"><div className="scroll-table"><table><thead><tr><th>Время</th><th>Кто</th><th>Действие</th><th>Объект</th></tr></thead><tbody>{pagination.items.map(e=><tr key={e.id}><td>{new Date(e.created_at).toLocaleString('ru')}</td><td>{e.actor}</td><td>{actions[e.action]||e.action}</td><td><code>{e.target_id.slice(0,8)}</code></td></tr>)}</tbody></table></div>{!events.length&&<p className="empty">Изменения ещё не зарегистрированы.</p>}{pagination.controls}</section></>;
}
