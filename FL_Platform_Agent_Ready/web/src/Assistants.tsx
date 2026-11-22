import {useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import type {components} from './generated/schema';
import './assistants.css';
export type AssistantProfile=components['schemas']['AssistantProfile'];
type Provider=components['schemas']['Provider'];

export function Assistants(){
 const [items,setItems]=useState<AssistantProfile[]>([]),[providers,setProviders]=useState<Provider[]>([]),[error,setError]=useState(''),[loading,setLoading]=useState(true);
 const [editing,setEditing]=useState<AssistantProfile|null>(null),[creating,setCreating]=useState(false),[busy,setBusy]=useState(false),[formError,setFormError]=useState('');
 const [name,setName]=useState(''),[provider,setProvider]=useState(''),[model,setModel]=useState(''),[instruction,setInstruction]=useState(''),[enabled,setEnabled]=useState(true);
 const [models,setModels]=useState<{id:string}[]>([]),[checking,setChecking]=useState(false),[search,setSearch]=useState('');
 const alive=useRef(true),draftID=useRef('');
 async function refresh(){try{const [a,p]=await Promise.all([request<{items:AssistantProfile[]}>('/ai/assistants'),request<{items:Provider[]}>('/ai/providers')]);if(alive.current){setItems(a.items);setProviders(p.items);setError('')}}catch(e){if(alive.current)setError((e as Error).message)}finally{if(alive.current)setLoading(false)}}
 useEffect(()=>{alive.current=true;void refresh();return()=>{alive.current=false}},[]);
 function edit(p?:AssistantProfile){setEditing(p||null);setCreating(!p);draftID.current=p?.id||crypto.randomUUID();setName(p?.name||'');setProvider(p?.provider_id||providers.find(x=>x.enabled)?.id||'');setModel(p?.model||'');setInstruction(p?.instruction||'');setEnabled(p?.enabled??true);setModels([]);setFormError('')}
 function close(){if(busy)return;setEditing(null);setCreating(false);void refresh()}
 useEffect(()=>{if((!creating&&!editing)||!provider)return;let current=true;setChecking(true);setModels([]);
  request<{items:{id:string}[]}>(`/ai/providers/${provider}/models`,'POST').then(data=>{if(current){setModels(data.items);setModel(old=>old||data.items[0]?.id||'')}}).catch(e=>{if(current)setFormError(e.message)}).finally(()=>{if(current)setChecking(false)});return()=>{current=false};
 },[provider,creating,editing]);
 async function save(e:FormEvent){e.preventDefault();setBusy(true);setFormError('');try{await request(`/ai/assistants/${draftID.current}`,'PUT',{name,provider_id:provider,model,instruction,enabled,revision:editing?.revision||0});if(alive.current){setCreating(false);setEditing(null);await refresh()}}catch(e){if(alive.current)setFormError((e as Error).message)}finally{if(alive.current)setBusy(false)}}
 const visible=items.filter(p=>`${p.name} ${p.model}`.toLowerCase().includes(search.toLowerCase()));
 return <><div className="page-heading"><div><h1>Ассистенты</h1><p className="muted">Сохраните модель и инструкцию для повторяющихся задач.</p></div><button className="primary" disabled={loading||!providers.length||items.length>=50} onClick={()=>edit()}><Icon name="plus"/>Новый ассистент</button></div>
  {error&&<p role="alert" className="error">{error} <button onClick={()=>void refresh()}>Повторить</button></p>}
  {!loading&&!providers.length&&<p>Сначала добавьте личное подключение в разделе <a href="#providers">«Провайдеры»</a>.</p>}
  <section className="panel"><label>Найти ассистента<input value={search} onChange={e=>setSearch(e.target.value)} placeholder="Название или модель"/></label>
   {visible.map(p=><article className="assistant-row" key={p.id}><div><h2>{p.name}</h2><p className="muted">{p.model} · версия {p.revision} · {p.enabled?'Доступен для новых диалогов':'Отключён'}</p><details><summary>Инструкция</summary><p className="assistant-instruction">{p.instruction}</p></details></div><button onClick={()=>edit(p)} aria-label={`Настроить ${p.name}`}>Настроить</button></article>)}
   {!visible.length&&<div className="empty">{loading?'Загружаем ассистентов…':items.length?'Ничего не найдено.':'Создайте ассистента, затем выберите его при создании диалога.'}</div>}
  </section><p className="muted">Изменения действуют только для новых диалогов. Ассистенты не получают доступ к экспериментам и не выполняют действия автоматически. <a href="#chat">Перейти в чат</a></p>
  {(creating||editing)&&<Dialog label="assistant-title" onClose={close}><div className="section-heading"><h2 id="assistant-title">{editing?'Настроить ассистента':'Новый ассистент'}</h2><button className="icon-button" aria-label="Закрыть" disabled={busy} onClick={close}><Icon name="close"/></button></div><form onSubmit={save}><fieldset className="provider-fields" disabled={busy}>
   <label>Название<input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} autoFocus/></label>
   <label>Подключение<select required value={provider} onChange={e=>{setProvider(e.target.value);setModel('');setFormError('')}}>{providers.map(p=><option key={p.id} value={p.id}>{p.name}{p.enabled?'':' (отключено)'}</option>)}</select></label>
   <label>Модель<select required value={model} onChange={e=>setModel(e.target.value)} disabled={checking}>{!model&&<option value="">{checking?'Загружаем каталог…':'Выберите модель'}</option>}{model&&!models.some(m=>m.id===model)&&<option value={model}>{model} (сохранённая)</option>}{models.map(m=><option key={m.id} value={m.id}>{m.id}</option>)}</select></label>
   <label>Инструкция ассистенту<textarea className="assistant-editor" required rows={6} maxLength={16384} value={instruction} onChange={e=>setInstruction(e.target.value)} placeholder="Опишите задачу, стиль ответа и ограничения…"/><small>Будет отправляться выбранному провайдеру вместе с сообщениями. Не включайте пароли и API-ключи.</small></label>
   <label className="check-label"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>Доступен для новых диалогов</label></fieldset>
   {formError&&<p className="error" role="alert">{formError}</p>}<button className="primary full" disabled={busy||checking||!model||!instruction.trim()||new TextEncoder().encode(instruction).length>16384}>{busy?'Сохранение…':'Сохранить ассистента'}</button>
  </form></Dialog>}
 </>;
}
