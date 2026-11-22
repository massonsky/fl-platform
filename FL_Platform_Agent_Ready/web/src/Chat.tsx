import type {AssistantProfile} from './Assistants';
import {useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {ChatThread} from './ChatThread';
import type {Conversation} from './chat-api';
import type {components} from './generated/schema';
import './chat.css';
type Provider=components['schemas']['Provider'];

export function Chat(){
 const [conversations,setConversations]=useState<Conversation[]>([]),[selected,setSelected]=useState('');
 const [providers,setProviders]=useState<Provider[]>([]),[creating,setCreating]=useState(false),[loading,setLoading]=useState(true),[busy,setBusy]=useState(false);
 const [provider,setProvider]=useState(''),[models,setModels]=useState<{id:string}[]>([]),[model,setModel]=useState(''),[consent,setConsent]=useState(false);
 const [modelLoading,setModelLoading]=useState(false),[error,setError]=useState(''),[formError,setFormError]=useState('');
 const [assistants,setAssistants]=useState<AssistantProfile[]>([]),[assistant,setAssistant]=useState('');
 const chosen=assistants.find(a=>a.id===assistant);
 const alive=useRef(true);
 async function refresh(){try{const data=await request<{items:Conversation[]}>('/ai/conversations');if(alive.current){setConversations(data.items);setSelected(id=>data.items.some(c=>c.id===id)?id:data.items[0]?.id||'');setError('')}}catch(e){if(alive.current)setError((e as Error).message)}finally{if(alive.current)setLoading(false)}}
 useEffect(()=>{alive.current=true;void refresh();request<{items:AssistantProfile[]}>('/ai/assistants').then(data=>{if(alive.current)setAssistants(data.items.filter(a=>a.enabled))}).catch(e=>{if(alive.current)setError(e.message)});request<{items:Provider[]}>('/ai/providers').then(data=>{if(alive.current)setProviders(data.items.filter(p=>p.enabled))}).catch(e=>{if(alive.current)setError(e.message)});return()=>{alive.current=false}},[]);
 useEffect(()=>{
  if(!creating||!provider||assistant)return;let current=true;setModels([]);setModel('');setConsent(false);setModelLoading(true);setFormError('');
  request<{items:{id:string}[]}>(`/ai/providers/${provider}/models`,'POST').then(data=>{if(current){setModels(data.items);setModel(data.items[0]?.id||'')}}).catch(e=>{if(current)setFormError(e.message)}).finally(()=>{if(current)setModelLoading(false)});
  return()=>{current=false};
 },[provider,creating,assistant]);
 function open(){setAssistant('');setCreating(true);setProvider(providers[0]?.id||'');setConsent(false);setFormError('')}
 async function create(e:FormEvent){e.preventDefault();setBusy(true);try{const c=await request<Conversation>('/ai/conversations','POST',{provider_id:provider,model,consent,...(chosen?{assistant_id:chosen.id,assistant_revision:chosen.revision}:{})});if(alive.current){setConversations(old=>[c,...old]);setSelected(c.id);setCreating(false)}}catch(e){if(alive.current)setFormError((e as Error).message)}finally{if(alive.current)setBusy(false)}}
 const current=conversations.find(c=>c.id===selected),connection=providers.find(p=>p.id===provider);
 return <><div className="page-heading"><div><h1>Чат</h1><p className="muted">Ваши модели. Ваши диалоги.</p></div><button className="primary" onClick={open} disabled={loading||!providers.length||conversations.length>=100}><Icon name="plus"/>Новый диалог</button></div>
  {error&&<p role="alert" className="error">{error} <button onClick={()=>void refresh()}>Обновить</button></p>}
  {!loading&&!providers.length&&<p className="note">Чтобы начать, добавьте и включите подключение в разделе <a href="#providers">«Провайдеры»</a>.</p>}
  <div className="chat-layout"><aside className="chat-history" aria-label="Диалоги"><h2>Диалоги <span className="muted">{conversations.length}</span></h2><label className="chat-mobile-select">Текущий диалог<select value={selected} onChange={e=>setSelected(e.target.value)}>{!conversations.length&&<option value="">Пока нет диалогов</option>}{conversations.map(c=><option key={c.id} value={c.id}>{c.title}</option>)}</select></label><div className="chat-history-list">{conversations.map(c=><button key={c.id} aria-current={selected===c.id?'true':undefined} onClick={()=>setSelected(c.id)}><strong>{c.title}</strong><small>{c.model}</small></button>)}</div>{!conversations.length&&<p className="muted">{loading?'Загружаем…':'Создайте первый диалог.'}</p>}</aside>
   {current?<ChatThread key={current.id} conversation={current} onChanged={()=>void refresh()} onDeleted={()=>{setSelected('');void refresh()}}/>:<section className="chat-empty"><Icon name="chat"/><h2>Рабочее пространство для разговора</h2><p>Выберите подключение и модель, чтобы начать.<br/>История каждого диалога доступна только вам.</p></section>}
  </div>
  {creating&&<Dialog label="new-chat-title" onClose={()=>{if(!busy)setCreating(false)}}><h2 id="new-chat-title">Новый диалог</h2><form onSubmit={create}><fieldset disabled={busy} className="provider-fields"><label>Ассистент<select aria-label="Ассистент" value={assistant} onChange={e=>{const a=assistants.find(a=>a.id===e.target.value);setAssistant(e.target.value);setConsent(false);setFormError('');if(a){setProvider(a.provider_id);setModel(a.model);setModelLoading(false);setModels([{id:a.model}])}}}><option value="">Без ассистента</option>{assistants.map(a=><option key={a.id} value={a.id}>{a.name} · v{a.revision}</option>)}</select></label>{chosen&&<details><summary>Инструкция для этого диалога</summary><p className="assistant-instruction">{chosen.instruction}</p></details>}<label>Подключение<select aria-label="Подключение" disabled={!!chosen} value={provider} onChange={e=>setProvider(e.target.value)} required>{providers.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label>Модель<select aria-label="Модель" value={model} onChange={e=>{setModel(e.target.value);setConsent(false)}} required disabled={!!chosen||modelLoading||!models.length}>{!models.length&&<option value="">{modelLoading?'Загружаем каталог…':'Нет доступных моделей'}</option>}{models.map(m=><option key={m.id} value={m.id}>{m.id}</option>)}</select></label><p className="muted">Инструкция ассистента, сообщения и история этого диалога будут отправляться в {connection?.base_url}. Провайдер может взимать плату по тарифу вашего ключа.</p><label className="check-label"><input type="checkbox" checked={consent} onChange={e=>setConsent(e.target.checked)} required/>Разрешаю передачу инструкции, сообщений и истории выбранному провайдеру.</label><p className="muted">История хранится до удаления. Ручные резервные копии сохраняются до удаления оператором. Данные экспериментов автоматически не передаются.</p></fieldset>{formError&&<p role="alert" className="error">{formError}</p>}<button className="primary full" disabled={busy||modelLoading||!model||!consent}>{busy?'Создание…':'Создать диалог'}</button></form></Dialog>}
 </>;
}
