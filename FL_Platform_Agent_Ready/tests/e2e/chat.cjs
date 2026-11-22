// Isolated browser contract fixture; no provider calls or production writes.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http');
async function main(){
 let conversations=[],items=[],stream=null,mode='complete',requests=0;
 const provider={id:'fixture-provider',name:'Тестовый провайдер',base_url:'https://models.example/v1',enabled:true};
 const proxy=http.createServer(async(req,res)=>{
  if(!req.url.startsWith('/api/v1/')){const up=http.request({hostname:process.env.WEB_HOST||'fl-chat-web-preview',path:req.url,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>res.end());req.pipe(up);return}
  const chunks=[];for await(const chunk of req)chunks.push(chunk);
  const raw=Buffer.concat(chunks).toString(),input=raw?JSON.parse(raw):{},path=req.url.split('?')[0];
  const reply=(body,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(body))};
  if(path.endsWith('/me'))return reply({id:'fixture-user',name:'Оператор',role:'researcher'});
  if(path.endsWith('/projects'))return reply({items:[],next_cursor:null});
  if(path.endsWith('/ai/assistants'))return reply({items:[]});
  if(path.endsWith('/ai/providers'))return reply({items:[provider]});
  if(path.endsWith('/models'))return reply({items:[{id:'fixture-text-model'}]});
  if(path.endsWith('/conversations')){
   if(req.method==='GET')return reply({items:conversations});
   assert.equal(input.consent,true);conversations=[{id:'fixture-chat',provider_id:provider.id,provider_name:provider.name,base_url:provider.base_url,model:input.model,title:'Новый диалог',created_at:new Date().toISOString()}];return reply(conversations[0],201);
  }
  if(path.endsWith('/messages')){
   requests++;const g={request_id:input.request_id,conversation_id:'fixture-chat',input:input.content,content:'',status:'streaming',error_code:'',finish_reason:'',usage:null,created_at:new Date().toISOString(),deadline:new Date(Date.now()+120000).toISOString()};items.push(g);
   res.writeHead(200,{'Content-Type':'text/event-stream'});
   const event=(name,data)=>res.write(`event: ${name}\ndata: ${JSON.stringify(data)}\n\n`);
   event('start',g);g.content='Часть ответа';event('delta',{text:g.content});stream={res,g,event};
   if(mode==='complete')setTimeout(()=>{g.content+=' — готово';event('delta',{text:' — готово'});g.status='completed';g.finish_reason='stop';event('done',g);res.end();stream=null},600);
   if(mode==='disconnect')setTimeout(()=>{g.status='interrupted';g.error_code='PROVIDER_STREAM_INTERRUPTED';res.end();stream=null},200);
   return;
  }
  if(path.endsWith('/cancel')){if(stream){stream.g.status='cancelled';stream.event('done',stream.g);stream.res.end();stream=null}return reply({cancelled:true})}
  if(req.method==='DELETE'){conversations=[];items=[];return reply({deleted:true})}
  return reply({conversation:conversations[0],items});
 });
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','ui-contract-fixture'));
  await page.goto('http://127.0.0.1:3000/#chat');
  await page.getByRole('button',{name:'Новый диалог',exact:true}).click();
  const create=page.getByRole('button',{name:'Создать диалог',exact:true});
  assert.equal(await create.isDisabled(),true);
  await page.getByRole('checkbox').check();await create.click();
  await page.getByLabel('Сообщение',{exact:true}).fill('Первый вопрос');
  await page.getByRole('button',{name:'Отправить',exact:true}).click();
  await page.getByText('Часть ответа',{exact:true}).waitFor();
  assert.equal(await page.getByRole('button',{name:'Остановить ответ'}).count(),1);
  await page.getByText('Часть ответа — готово',{exact:true}).waitFor();
  await page.getByRole('button',{name:'Отправить',exact:true}).waitFor();
  await page.reload();await page.getByText('Первый вопрос',{exact:true}).waitFor();
  mode='hold';await page.getByLabel('Сообщение',{exact:true}).fill('Останови');await page.keyboard.press('Control+Enter');
  await page.getByRole('button',{name:'Остановить ответ'}).click();await page.getByText('Остановлено',{exact:true}).waitFor();
  mode='disconnect';await page.getByLabel('Сообщение',{exact:true}).fill('Обрыв');await page.getByRole('button',{name:'Отправить',exact:true}).click();
  await page.getByRole('alert').filter({hasText:'Соединение оборвалось'}).waitFor();
  assert.equal(requests,3,'client retried a paid request');
  for(const width of [1440,768,390,320]){await page.setViewportSize({width,height:1000});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);await page.screenshot({path:`/evidence/chat-${width}.png`,fullPage:true})}
  await page.getByRole('link',{name:'Эксперименты',exact:true}).click();await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('link',{name:'Чат',exact:true}).click();await page.getByText('Первый вопрос',{exact:true}).waitFor();
  await page.getByRole('button',{name:'Удалить диалог',exact:true}).click();await page.getByRole('button',{name:'Удалить историю',exact:true}).click();
  await page.getByText('Создайте первый диалог.',{exact:true}).waitFor();
  assert.deepEqual(errors,[]);console.log('PASS chat UI: consent, incremental SSE, persistence fixture, cancel, disconnect without retry, navigation, delete, 4 widths');
 }finally{await browser.close();proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
