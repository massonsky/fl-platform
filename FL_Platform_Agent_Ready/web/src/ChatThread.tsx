import {useEffect,useRef,useState,type FormEvent} from 'react';
import {request,errorMessage} from './api';
import {sendMessage,type Conversation,type Generation} from './chat-api';
import {Dialog} from './Dialog';

const labels:Record<string,string>={streaming:'Формируется ответ…',completed:'Готово',cancelled:'Остановлено',interrupted:'Ответ прерван',failed:'Не удалось получить ответ'};
export function ChatThread({conversation,onChanged,onDeleted}:{conversation:Conversation;onChanged:()=>void;onDeleted:()=>void}){
 const [items,setItems]=useState<Generation[]>([]),[draft,setDraft]=useState(''),[error,setError]=useState('');
 const [loading,setLoading]=useState(true),[sending,setSending]=useState(false),[deleting,setDeleting]=useState(false),[confirm,setConfirm]=useState(false);
 const alive=useRef(true),controller=useRef<AbortController|null>(null),list=useRef<HTMLDivElement>(null),nearEnd=useRef(true);
 const selected=conversation.id;
 const active=sending||items.some(g=>g.status==='streaming');
 async function refresh(){try{const data=await request<{items:Generation[]}>(`/ai/conversations/${selected}`);if(alive.current&&!controller.current)setItems(data.items)}catch(e){if(alive.current)setError((e as Error).message)}finally{if(alive.current)setLoading(false)}}
 useEffect(()=>{alive.current=true;void refresh();const timer=setInterval(()=>{if(!controller.current)void refresh()},2000);return()=>{alive.current=false;controller.current?.abort();clearInterval(timer)}},[selected]);
 useEffect(()=>{if(nearEnd.current&&list.current)list.current.scrollTop=list.current.scrollHeight},[items]);
 async function send(e:FormEvent){
  e.preventDefault();if(active||!draft.trim())return;
  const text=draft,id=crypto.randomUUID(),abort=new AbortController();controller.current=abort;
  setSending(true);setError('');nearEnd.current=true;let started=false;
  try{await sendMessage(selected,id,text,abort.signal,event=>{
   if(!alive.current)return;
   if(event.event==='start'){started=true;setDraft('');setItems(old=>[...old.filter(g=>g.request_id!==event.data.request_id),event.data])}
   else if(event.event==='delta')setItems(old=>old.map(g=>g.request_id===id?{...g,content:g.content+event.data.text}:g));
   else{started=true;setDraft('');setItems(old=>[...old.filter(g=>g.request_id!==event.data.request_id),event.data]);onChanged()}
  })}catch(e){if(alive.current&&!abort.signal.aborted)setError((e as Error).message)}
  finally{controller.current=null;if(alive.current){setSending(false);if(!started)setDraft(text);await refresh();onChanged()}}
 }
 async function cancel(){try{await request(`/ai/conversations/${selected}/cancel`,'POST');controller.current?.abort();await refresh()}catch(e){setError((e as Error).message)}}
 async function remove(){setDeleting(true);try{await request(`/ai/conversations/${selected}`,'DELETE');controller.current?.abort();onDeleted()}catch(e){setError((e as Error).message);setConfirm(false)}finally{if(alive.current)setDeleting(false)}}
 return <section className="chat-thread" aria-label="Переписка">
  <header className="chat-header"><div><h2>{conversation.title}</h2><p className="muted">{conversation.provider_name} · {conversation.model}</p></div><button onClick={()=>setConfirm(true)} disabled={deleting}>Удалить диалог</button></header>
  {conversation.assistant_name&&<details className="assistant-snapshot"><summary>{conversation.assistant_name} · версия {conversation.assistant_revision}</summary><p>{conversation.system_instruction}</p><small>Снимок инструкции при создании диалога.</small></details>}
  <div className="chat-messages" ref={list} tabIndex={0} aria-label="Сообщения" onScroll={()=>{const el=list.current;if(el)nearEnd.current=el.scrollHeight-el.scrollTop-el.clientHeight<80}}>
   {loading?<p role="status">Загружаем историю…</p>:!items.length?<div className="chat-empty"><h3>О чём поговорим?</h3><p>Напишите сообщение. Ответит выбранная вами модель.</p></div>:items.map(g=><div key={g.request_id} className="chat-turn"><article className="chat-message chat-user"><h3>Вы</h3><p>{g.input}</p></article><article className="chat-message"><h3>{conversation.model}</h3><p>{g.content||labels[g.status]}</p><small className={`chat-status ${g.status}`}>{labels[g.status]}{g.finish_reason==='length'?' · Достигнут лимит ответа':''}</small>{g.error_code&&<p className="error">{errorMessage(g.error_code)}</p>}{g.usage?.total_tokens!==undefined&&<small className="muted">Токенов: {g.usage.total_tokens}</small>}</article></div>)}
  </div>
  <form className="chat-composer" onSubmit={send}>
   {error&&<p className="error" role="alert">{error}</p>}
   <label htmlFor="chat-message">Сообщение<textarea id="chat-message" value={draft} onChange={e=>setDraft(e.target.value)} rows={3} maxLength={32768} disabled={loading||active} placeholder="Напишите сообщение…" onKeyDown={e=>{if((e.ctrlKey||e.metaKey)&&e.key==='Enter'&&!e.nativeEvent.isComposing){e.preventDefault();e.currentTarget.form?.requestSubmit()}}}/></label>
   <div className="chat-compose-actions"><small className="muted">{active?'Формируется ответ. Его можно остановить.':'Ctrl / ⌘ + Enter — отправить. Ответы модели могут содержать ошибки.'}</small>{active?<button type="button" onClick={()=>void cancel()}>Остановить ответ</button>:<button className="primary" disabled={loading||!draft.trim()||new TextEncoder().encode(draft).length>32768}>Отправить</button>}</div>
  </form>
  {confirm&&<Dialog label="delete-chat-title" onClose={()=>{if(!deleting)setConfirm(false)}}><h2 id="delete-chat-title">Удалить этот диалог?</h2><p>История будет удалена из платформы, текущий ответ остановится. Копии у провайдера и в резервных копиях могут сохраниться.</p><div className="chat-compose-actions"><button disabled={deleting} onClick={()=>setConfirm(false)}>Отмена</button><button className="primary" disabled={deleting} onClick={()=>void remove()}>{deleting?'Удаление…':'Удалить историю'}</button></div></Dialog>}
 </section>;
}
