// UI fixtures represent API states, never published experiment results.
const {chromium}=require('playwright');
const http=require('node:http'),assert=require('node:assert/strict');
async function main(){
 const proxy=http.createServer((req,res)=>{const up=http.request({hostname:'fl-training-web-preview',path:req.url,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>res.end());req.pipe(up)});
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1100}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','fixture'));
  let stage='collecting',status='running',accepted=2,broken=false;
  const run=()=>({id:'run-a',experiment_id:'exp-a',project_id:'project-a',status,total_rounds:8,completed_rounds:2,latest_model_version:2,requested_at:'2026-11-14T10:00:00Z',failure_code:null,config_hash:'fixture'});
  await page.route('**/api/v1/**',route=>{
   const path=new URL(route.request().url()).pathname;
   const reply=(body,code=200)=>route.fulfill({status:code,contentType:'application/json',body:JSON.stringify(body)});
   const list=items=>reply({items,next_cursor:null});
   if(path.endsWith('/me'))return reply({id:'user',name:'Исследователь',role:'researcher'});
   if(path.endsWith('/projects'))return list([{id:'project-a',name:'Проверка визуализации'}]);
   if(path.endsWith('/runs'))return list([run()]);
   if(path.endsWith('/rounds'))return broken?reply({code:'NETWORK_ERROR'},503):list([{id:'round-3',run_id:'run-a',round_no:3,status:stage,selected_count:5,accepted_count:accepted,model_version:2}]);
   if(path.endsWith('/metrics'))return list([.7,.76].map((value,i)=>({id:String(i+1),run_id:'run-a',round_no:i+1,split:'validation_global',name:'accuracy',value,client_id:null,measured_at:'2026-11-14T10:00:00Z'})));
   if(path.endsWith('/events'))return list([{sequence:'1',run_id:'run-a',type:'round.started',created_at:'2026-11-14T10:00:00Z'}]);
   if(path.endsWith('/exp-a'))return reply({id:'exp-a',name:'Обучение общей модели',config:{algorithm:'fedavg',partition:'balanced',seed:11}});
   return reply({code:'NOT_FOUND'},404);
  });
  await page.goto('http://127.0.0.1:3000/#experiments');const panel=page.getByRole('region',{name:'Ход обучения'});
  await panel.locator('[aria-current=step]').filter({hasText:'Локальное обучение'}).waitFor();
  assert.equal(await panel.getByRole('progressbar').getAttribute('aria-valuenow'),'2');
  await panel.getByText('+6.00 п. п. относительно раунда 1',{exact:true}).waitFor();
  assert.ok((await page.locator('.chart svg').textContent()).includes('8'),'chart must use configured round budget');
  for(const width of [1440,1024,768,390,320]){
   await page.setViewportSize({width,height:1100});await panel.scrollIntoViewIfNeeded();
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);
   await panel.screenshot({path:`/evidence/training-${width}.png`});
  }
  await page.emulateMedia({reducedMotion:'reduce'});
  assert.equal(await panel.locator('.is-current .phase-marker').evaluate(el=>getComputedStyle(el,'::after').animationName),'none');
  await page.emulateMedia({reducedMotion:'no-preference'});
  stage='aggregating';accepted=5;await panel.locator('[aria-current=step]').filter({hasText:'Объединение'}).waitFor();
  assert.equal(await panel.getByRole('progressbar').getAttribute('aria-valuenow'),'5');
  stage='evaluating';await panel.locator('[aria-current=step]').filter({hasText:'Оценка'}).waitFor();
  broken=true;await panel.getByText('Обновление данных приостановлено',{exact:true}).waitFor();assert.equal(await panel.locator('.is-current').count(),0);
  broken=false;await panel.locator('.is-current').waitFor();
  status='cancelled';await panel.getByText(/Запуск остановлен/).waitFor();assert.equal(await panel.locator('.is-current').count(),0);
  status='completed';stage='completed';await panel.getByText(/Обучение завершено/).waitFor();assert.equal(await panel.locator('.is-done').count(),4);
  await page.getByRole('link',{name:'Мой профиль',exact:true}).click();await page.getByRole('link',{name:'Эксперименты',exact:true}).click();await panel.waitFor();
  assert.deepEqual(errors,[]);console.log('PASS training: phases, accepted updates, accuracy delta, configured axis, stale/recovery, terminal states, reduced motion, navigation, five widths');
 }finally{await browser.close();proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
