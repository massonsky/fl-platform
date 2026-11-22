import {useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {usePagination} from './Pagination';

import type {components} from './generated/schema';
type Provider=components['schemas']['Provider'];
type Catalog={provider:Provider;items:{id:string}[]};

export function Providers(){
 const [items,setItems]=useState<Provider[]>([]),[allowed,setAllowed]=useState<string[]>([]);
 const [loading,setLoading]=useState(true),[error,setError]=useState(''),[notice,setNotice]=useState('');
 const [editing,setEditing]=useState<Provider|null>(null),[creating,setCreating]=useState(false);
 const [name,setName]=useState(''),[base,setBase]=useState(''),[key,setKey]=useState(''),[enabled,setEnabled]=useState(true);
 const [busy,setBusy]=useState(false),[checking,setChecking]=useState(''),[formError,setFormError]=useState('');
 const [catalog,setCatalog]=useState<Catalog|null>(null),[search,setSearch]=useState('');
 const alive=useRef(true),draftID=useRef('');
 useEffect(()=>{alive.current=true;void refresh();return()=>{alive.current=false}},[]);
 async function refresh(){
  try{const data=await request<{items:Provider[];allowed_base_urls:string[]}>('/ai/providers');if(alive.current){setItems(data.items);setAllowed(data.allowed_base_urls.filter(Boolean));setError('')}}
  catch(e){if(alive.current)setError((e as Error).message)}finally{if(alive.current)setLoading(false)}
 }
 function edit(p?:Provider){setEditing(p||null);setCreating(!p);draftID.current=p?.id||crypto.randomUUID();setName(p?.name||'');setBase(p?.base_url||allowed[0]||'');setEnabled(p?.enabled??true);setKey('');setFormError('');setNotice('')}
 function close(){if(busy)return;setEditing(null);setCreating(false);setKey('');setFormError('')}
 async function save(e:FormEvent){
  e.preventDefault();setBusy(true);setFormError('');
  try{
   await request<Provider>(`/ai/providers/${draftID.current}`,'PUT',{name,base_url:base,api_key:key,enabled,revision:editing?.revision||0});
   if(alive.current){setEditing(null);setCreating(false);setKey('');setNotice('Подключение сохранено. Проверьте доступ, чтобы получить каталог моделей.');setCatalog(null);await refresh()}
  }catch(e){if(alive.current)setFormError((e as Error).message)}finally{if(alive.current)setBusy(false)}
 }
 async function check(p:Provider){
  setChecking(p.id);setError('');setNotice('');setCatalog(null);setSearch('');
  try{const data=await request<{items:{id:string}[]}>(`/ai/providers/${p.id}/models`,'POST');if(alive.current){setCatalog({provider:p,items:data.items});setNotice(`Подключение «${p.name}» проверено. Моделей: ${data.items.length}.`)}}
  catch(e){if(alive.current)setError((e as Error).message)}finally{if(alive.current)setChecking('')}
 }
 const providers=usePagination(items,'providers',8);
 const models=usePagination(catalog?.items.filter(m=>m.id.toLowerCase().includes(search.toLowerCase()))||[],`${catalog?.provider.id}:${search}`,8);
 return <>
  <div className="page-heading"><div><h1>Провайдеры</h1><p className="muted">Ваши подключения к внешним моделям.</p></div><button className="primary" onClick={()=>edit()} disabled={loading||!!error&&!items.length||!allowed.length||items.length>=20}><Icon name="plus"/>Подключить провайдера</button></div>
  {error&&<div role="alert" className="error">{error} <button onClick={()=>void refresh()} disabled={loading}>Обновить список</button></div>}
  {notice&&<p role="status" className="notice">{notice}</p>}
  <section className="panel" aria-busy={loading} aria-label="Мои подключения">
   <div className="section-heading"><h3>Мои подключения</h3><span className="muted">{items.length} / 20</span></div>
   {loading?<p role="status">Загружаем подключения…</p>:!items.length?<div className="empty"><h3>Подключите свой сервис моделей</h3><p>Добавьте адрес и API-ключ. Подключение будет доступно только вам.</p>{!allowed.length&&!error&&<p>Администратор сервера ещё не добавил разрешённые адреса провайдеров.</p>}</div>:<><div className="scroll-table"><table className="provider-table"><thead><tr><th>Подключение</th><th>Доступ</th><th>Действия</th></tr></thead><tbody>{providers.items.map(p=><tr key={p.id}><td><strong>{p.name}</strong><small className="block muted">{p.base_url}</small></td><td><span className={`badge ${p.enabled?'completed':'cancelled'}`}>{p.enabled?'Включён':'Отключён'}</span></td><td><button disabled={!!checking||!p.enabled} onClick={()=>void check(p)}>{checking===p.id?'Проверяем…':'Проверить и показать модели'}</button> <button disabled={!!checking} onClick={()=>edit(p)} aria-label={`Настроить ${p.name}`}>Настроить</button></td></tr>)}</tbody></table></div>{providers.controls}</>}
  </section>
  <p className="note">API-ключ хранится в зашифрованном виде и не отображается после сохранения. Проверка запрашивает список моделей, без отправки сообщений и запуска генерации.</p>
  {catalog&&<section className="panel" aria-label="Каталог моделей"><div className="section-heading"><h2>Модели · {catalog.provider.name}</h2><span className="muted">{catalog.items.length} доступно</span></div><label className="search-field">Найти модель<input type="search" value={search} onChange={e=>setSearch(e.target.value)} placeholder="Название модели"/></label>{models.items.length?<><div className="scroll-table"><table><thead><tr><th>Идентификатор модели</th></tr></thead><tbody>{models.items.map(m=><tr key={m.id}><td>{m.id}</td></tr>)}</tbody></table></div>{models.controls}</>:<p>{catalog.items.length?'По вашему запросу моделей не найдено.':'Провайдер вернул пустой каталог.'}</p>}<p className="muted">Каталог получен от провайдера. Каталог может быть публичным: действительность ключа и доступ к генерации проверяются отдельно.</p></section>}
  {(creating||editing)&&<Dialog label="provider-title" onClose={close}><div className="section-heading"><h2 id="provider-title">{editing?'Настройки подключения':'Подключить провайдера'}</h2><button className="icon-button" aria-label="Закрыть" onClick={close} disabled={busy}><Icon name="close"/></button></div><form onSubmit={save}>
   <fieldset disabled={busy} className="provider-fields"><label>Название<input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} autoFocus placeholder="Например, мой сервер моделей"/></label>
   <label>Адрес сервиса{editing?<input readOnly value={base}/>:<select value={base} required onChange={e=>setBase(e.target.value)}>{allowed.map(url=><option key={url} value={url}>{url}</option>)}</select>}<small>{editing?'Чтобы сменить адрес, создайте отдельное подключение.':'Список адресов настраивает администратор сервера.'}</small></label>
   <label>{editing?'Новый API-ключ (необязательно)':'API-ключ'}<input type="password" required={!editing} value={key} onChange={e=>setKey(e.target.value)} maxLength={4096} autoComplete="new-password" spellCheck={false}/><small>{editing?'Оставьте поле пустым, чтобы сохранить текущий ключ.':'Используйте личный ключ, выданный вашим провайдером.'}</small></label>
   <label className="check-label"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>Подключение включено</label></fieldset>
   {formError&&<p className="error" role="alert">{formError}</p>}<button type="submit" className="primary full" disabled={busy}>{busy?'Сохранение…':'Сохранить подключение'}</button>
  </form></Dialog>}
 </>;
}
