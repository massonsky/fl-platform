import {ApiError,errorMessage,setToken,token} from './api';
import type {components} from './generated/schema';
export type Conversation=components['schemas']['ChatConversation'];
export type Generation=components['schemas']['ChatGeneration'];
export type ChatEvent={event:'start'|'done';data:Generation}|{event:'delta';data:{text:string}};

export async function sendMessage(conversation:string,id:string,content:string,signal:AbortSignal,receive:(event:ChatEvent)=>void){
 const session=token;
 const response=await fetch(`/api/v1/ai/conversations/${conversation}/messages`,{method:'POST',signal,headers:{Authorization:`Bearer ${session}`,'Content-Type':'application/json'},body:JSON.stringify({request_id:id,content})});
 function checkSession(){if(token!==session)throw new Error('Сессия изменилась.')}
 checkSession();
 if(!response.ok){
  const data=await response.json().catch(()=>({code:'NETWORK_ERROR'}));
  if(response.status===401){setToken('');window.dispatchEvent(new Event('fl-unauthorized'))}
  throw new ApiError(response.status,errorMessage(data.code));
 }
 if(response.headers.get('Content-Type')?.includes('application/json')){const data:Generation=await response.json();checkSession();receive({event:'done',data});return}
 if(!response.body)throw new Error('Поток ответа недоступен.');
 const reader=response.body.getReader(),decoder=new TextDecoder();let buffer='',done=false;
 try{
  while(true){
   const part=await reader.read();checkSession();buffer+=decoder.decode(part.value,{stream:!part.done});
   if(buffer.length>8*1024*1024)throw new Error('Ответ превышает допустимый размер.');
   let boundary:number;
   while((boundary=buffer.indexOf('\n\n'))!==-1){
    const frame=buffer.slice(0,boundary);buffer=buffer.slice(boundary+2);
    const event=frame.split('\n').find(l=>l.startsWith('event: '))?.slice(7);
    const data=frame.split('\n').filter(l=>l.startsWith('data: ')).map(l=>l.slice(6)).join('\n');
    if(!data||!['start','delta','done'].includes(event||''))continue;
    receive({event,data:JSON.parse(data)} as ChatEvent);if(event==='done')done=true;
   }
   if(part.done)break;
  }
 }finally{await reader.cancel().catch(()=>{});reader.releaseLock()}
 if(!done)throw new Error('Соединение оборвалось. История перечитана; запрос не повторён.');
}
