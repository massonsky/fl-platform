// Controlled UI contract fixtures; no real training, production data or credentials.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),http=require('node:http');
const stamp='2026-11-14T10:00:00Z',capPath='/api/v1/me/legacy-lab';
const config={algorithm:'fedavg',partition:'balanced',seed:11,rounds:3};
const run=(id,experiment,status='queued')=>({id,project_id:'project-a',experiment_id:experiment,status,
  total_rounds:3,completed_rounds:status==='completed'?3:0,latest_model_version:null,requested_at:stamp,config_hash:'fixture',failure_code:null});
async function eventually(check,label){for(let i=0;i<100;i++){if(await check())return;await new Promise(resolve=>setTimeout(resolve,100))}throw Error(`Timed out: ${label}`)}
async function settled(page){await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))}
async function fixture(browser,cap='active'){
  const context=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true}),page=await context.newPage();page.setDefaultTimeout(15000);
  const state={cap,capStatus:200,queueStatus:200,experiment:0},errors=[],writes=[],holds=[],exports=[];
  const runs=[run('queued-run','exp-queued'),run('archived-run','exp-archived','completed')];
  function pause(method,path,status){
    let release,seen;const gate=new Promise(resolve=>release=resolve),started=new Promise(resolve=>seen=resolve);
    holds.push({method,path,status,gate,seen,taken:false});return {release,started,method,path};
  }
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','legacy-work-fixture'));
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),path=new URL(req.url()).pathname,method=req.method(),next=req.headers().authorization==='Bearer next-work-fixture';
    const reply=async(body,status=200)=>{
      const snapshot=structuredClone(body),held=holds.find(item=>!item.taken&&item.method===method&&item.path===path);
      if(held){held.taken=true;held.seen();if(held.status)status=held.status;await held.gate}
      return route.fulfill({status,contentType:'application/json',body:JSON.stringify(status===401?{code:'UNAUTHORIZED'}:snapshot)});
    };
    const list=items=>reply({items,next_cursor:null});
    if(method==='POST'&&path!=='/api/v1/session')writes.push({path,body:req.postDataJSON()});
    if(path==='/api/v1/me')return reply({id:next?'other-user':'user',name:'Исследователь',role:'researcher'});
    if(path===capPath){
      if(state.capStatus!==200)return reply({code:'DEPENDENCY_UNAVAILABLE'},state.capStatus);
      const status=next?'not_admitted':state.cap;
      return reply({user_id:status==='wrong-user'?'foreign':next?'other-user':'user',status:status==='wrong-user'?'active':status,
        revision:status==='not_admitted'?0:2,...(status==='not_admitted'?{}:{source:'admin',updated_at:stamp}),can_create_legacy_work:status==='active'||status==='wrong-user'});
    }
    if(path==='/api/v1/projects'&&method==='POST')return reply({id:'new-project',name:req.postDataJSON().name,slug:req.postDataJSON().slug,owner_id:'user',created_at:stamp},201);
    if(path==='/api/v1/projects')return list(['a','b'].map(id=>({id:`project-${id}`,name:`Проект ${id.toUpperCase()}`,slug:id,owner_id:'user',created_at:stamp})));
    if(path==='/api/v1/organizations')return list([]);
    if(/^\/api\/v1\/projects\/project-[ab]\/runs$/.test(path))return list(path.includes('project-a')?runs:[]);
    if(path==='/api/v1/presets')return list([{id:'digits-balanced-11',config}]);
    if(path==='/api/v1/projects/project-a/experiments'&&method==='POST')return reply({id:`saved-exp-${++state.experiment}`,project_id:'project-a',name:req.postDataJSON().name,config:req.postDataJSON().config},201);
    if(/^\/api\/v1\/experiments\/[^/]+\/runs$/.test(path)&&method==='POST'){
      if(state.queueStatus!==200){if(state.queueStatus===403)state.cap='revoked';return reply({code:state.queueStatus===403?'LEGACY_LAB_NOT_ADMITTED':'DEPENDENCY_UNAVAILABLE'},state.queueStatus)}
      const item=run('new-queued-run',path.split('/')[4]);runs.unshift(item);return reply(item,202);
    }
    if(/^\/api\/v1\/experiments\/[^/]+$/.test(path))return reply({id:path.split('/').pop(),name:path.endsWith('archived')?'Сохранённый результат':'Текущий эксперимент',config});
    if(/^\/api\/v1\/runs\/[^/]+\/(metrics|rounds|events)$/.test(path))return list([]);
    if(path==='/api/v1/runs/queued-run/cancel'){runs[0].status='cancelled';return reply(runs[0])}
    if(path==='/api/v1/runs/archived-run/export'){exports.push(path);return route.fulfill({status:200,contentType:'application/zip',body:Buffer.from('controlled-export-fixture')})}
    if(path==='/api/v1/session')return reply(method==='POST'?{token:'next-work-fixture'}:{ok:true});
    errors.push(`Unexpected ${method} ${path}`);return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#experiments');await page.getByRole('heading',{name:'Текущий эксперимент',exact:true}).waitFor();
  return {context,page,state,errors,writes,runs,pause,exports};
}
const button=(page,name)=>page.getByRole('button',{name,exact:true});
async function allowed(f){await eventually(async()=>!await button(f.page,'Новый эксперимент').isDisabled(),'allowed capability')}
async function denied(f){await eventually(()=>button(f.page,'Новый эксперимент').isDisabled(),'denied capability');await f.page.locator('aside[aria-label="Допуск к лаборатории"]').first().getByRole('button',{name:'Проверить допуск'}).waitFor()}
async function focus(f){await f.page.evaluate(()=>window.dispatchEvent(new Event('focus')))}
async function refresh(f){const dialog=f.page.getByRole('dialog'),host=await dialog.count()?dialog:f.page;await host.locator('aside[aria-label="Допуск к лаборатории"]').first().getByRole('button',{name:'Проверить допуск'}).click()}
async function deliver(f,held){const response=f.page.waitForResponse(r=>new URL(r.url()).pathname===held.path&&r.request().method()===held.method);held.release();await (await response).finished();await settled(f.page)}
async function openExperiment(f){await allowed(f);await button(f.page,'Новый эксперимент').click();const dialog=f.page.getByRole('dialog');await dialog.getByRole('option',{name:'balanced · seed 11'}).waitFor({state:'attached'});return dialog}
async function clean(f){assert.deepEqual(f.errors,[]);await f.context.close()}
async function main(){
  const proxy=http.createServer((req,res)=>{const upstream=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res)});upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream)});
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true});
  try{
    let f=await fixture(browser,'not_admitted');await denied(f);
    for(const name of ['Новый эксперимент','Создать проект','Повторить'])assert.equal(await button(f.page,name).isDisabled(),true);
    await button(f.page,'Остановить').click();await eventually(()=>Promise.resolve(f.writes.length===1),'cancel retained');
    await button(f.page,'archived').click();await f.page.getByRole('heading',{name:'Сохранённый результат',exact:true}).waitFor();
    const download=f.page.waitForEvent('download');await button(f.page,'Экспорт').click();await download;
    assert.equal(f.exports.length,1);assert.deepEqual(f.writes.map(item=>item.path),['/api/v1/runs/queued-run/cancel']);
    for(const width of [1440,768,390,320]){await f.page.setViewportSize({width,height:1000});assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);await f.page.screenshot({path:`/evidence/legacy-work-denied-${width}.png`,fullPage:true})}
    await clean(f);console.log('PASS denied new work preserves controlled cancel, archive, export and responsive views');

    f=await fixture(browser);await allowed(f);let held=f.pause('GET',capPath);await focus(f);await held.started;
    assert.equal(await button(f.page,'Новый эксперимент').isDisabled(),true);
    f.state.cap='revoked';await focus(f);await denied(f);await deliver(f,held);await denied(f);
    f.state.cap='active';f.state.capStatus=503;await refresh(f);await f.page.getByRole('status').filter({hasText:'Не удалось проверить допуск'}).waitFor();
    assert.equal(await button(f.page,'Повторить').isDisabled(),true);f.state.capStatus=200;await refresh(f);await allowed(f);
    f.state.cap='wrong-user';await focus(f);await denied(f);assert.equal(f.writes.length,0);await clean(f);
    console.log('PASS pending, dependency refusal, wrong identity and older allowed response cannot permit work');

    f=await fixture(browser);await allowed(f);await button(f.page,'Создать проект').click();let dialog=f.page.getByRole('dialog');
    f.state.cap='revoked';await button(dialog,'Создать проект').click();await denied(f);
    assert.equal(f.writes.length,0);await f.page.emulateMedia({reducedMotion:'reduce'});
    assert.equal(await dialog.evaluate(node=>getComputedStyle(node).animationName),'none');
    for(let i=0;i<8;i++)await f.page.keyboard.press('Tab');assert.ok(await dialog.evaluate(node=>node.contains(document.activeElement)),'focus escaped dialog');
    await f.page.setViewportSize({width:390,height:900});await f.page.screenshot({path:'/evidence/legacy-work-revoked-dialog-390.png',fullPage:true});
    assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await f.page.keyboard.press('Escape');await clean(f);
    console.log('PASS revoke while project dialog is open rejects preflight without POST');

    for(const leave of ['close','project','logout']){
      f=await fixture(browser);dialog=await openExperiment(f);held=f.pause('POST','/api/v1/projects/project-a/experiments');
      await button(dialog,'Создать и запустить').click();await held.started;await f.page.keyboard.press('Escape');
      if(leave==='project')await f.page.getByLabel('Текущий проект').selectOption('project-b');
      if(leave==='logout')await button(f.page,'Выйти').click();
      await deliver(f,held);assert.equal(f.writes.filter(item=>item.path.endsWith('/runs')).length,0);await clean(f);
    }
    console.log('PASS closed form and changed project/session never queue a late created experiment');

    f=await fixture(browser);dialog=await openExperiment(f);f.state.queueStatus=403;await button(dialog,'Создать и запустить').click();
    await dialog.getByRole('alert').waitFor();await denied(f);assert.equal(f.state.experiment,1);
    f.state.cap='active';f.state.queueStatus=200;await refresh(f);await allowed(f);
    await button(dialog,'Запустить сохранённый эксперимент').click();await dialog.waitFor({state:'detached'});
    assert.equal(f.state.experiment,1);assert.deepEqual(f.writes.filter(item=>item.path.endsWith('/runs')).map(item=>item.path),Array(2).fill('/api/v1/experiments/saved-exp-1/runs'));
    await clean(f);console.log('PASS definite denied queue retains saved configuration and requires an explicit retry');

    f=await fixture(browser);dialog=await openExperiment(f);f.state.queueStatus=503;await button(dialog,'Создать и запустить').click();await button(dialog,'Проверить запуск').waitFor();
    assert.equal(f.state.experiment,1);const writes=f.writes.length;f.runs.unshift(run('unrelated-run','another-experiment'));
    await button(dialog,'Проверить запуск').click();await dialog.getByRole('alert').filter({hasText:'Подтверждённого запуска этого эксперимента пока нет'}).waitFor();
    assert.equal(f.writes.length,writes);assert.equal(await dialog.count(),1);
    f.runs.unshift(run('confirmed-run','saved-exp-1'));await button(dialog,'Проверить запуск').click();await dialog.waitFor({state:'detached'});
    await eventually(()=>Promise.resolve(f.page.locator('.run-list tr.selected').getByRole('button').textContent()).then(value=>value?.includes('confirme')),'matching saved experiment selected');
    assert.equal(f.writes.length,writes);await clean(f);console.log('PASS uncertain queue uses matching experiment read-only recovery without duplicate writes');

    f=await fixture(browser);await allowed(f);f.state.queueStatus=503;await button(f.page,'Повторить').click();await button(f.page,'Проверить запуски').waitFor();
    assert.equal(await button(f.page,'Повторить').isDisabled(),true);assert.equal(await button(f.page,'Остановить').isDisabled(),false);
    const repeatWrites=f.writes.length;f.runs.unshift(run('unrelated-repeat','another-experiment'));
    await button(f.page,'Проверить запуски').click();await f.page.getByRole('alert').filter({hasText:'Нового подтверждённого запуска этого эксперимента пока нет'}).waitFor();
    assert.equal(f.writes.length,repeatWrites);f.runs.unshift(run('confirmed-repeat','exp-queued'));
    await button(f.page,'Проверить запуски').click();await button(f.page,'Открыть запуск confirme').waitFor();
    assert.ok((await f.page.locator('.run-list tr.selected').getByRole('button').textContent()).includes('queued-r'),'inspection must not attribute a parallel candidate');
    await button(f.page,'Открыть запуск confirme').click();await eventually(async()=>(await f.page.locator('.run-list tr.selected').getByRole('button').textContent())?.includes('confirme'),'explicit candidate selected');
    assert.equal(f.writes.length,repeatWrites);await clean(f);console.log('PASS uncertain repeat blocks duplicate POST while preserving cancel and read-only inspection');

    f=await fixture(browser);await allowed(f);held=f.pause('GET',capPath,401);await focus(f);await held.started;
    await button(f.page,'Выйти').click();await f.page.getByLabel('Логин',{exact:true}).fill('next');await f.page.getByLabel('Пароль',{exact:true}).fill('fixture-password-only');await button(f.page,'Войти').click();
    await denied(f);await deliver(f,held);assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),'next-work-fixture');assert.equal(f.writes.length,0);await clean(f);
    f=await fixture(browser);await allowed(f);f.state.capStatus=401;await focus(f);await f.page.getByLabel('Логин',{exact:true}).waitFor();assert.equal(f.writes.length,0);await clean(f);
    console.log('PASS late old-session 401 ignored; current-session 401 follows normal sign-in recovery');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exit(1)});
