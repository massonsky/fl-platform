// D3A browser contract fixtures only; no live identities, secrets or database.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),http=require('node:http');
const a='11111111-1111-4111-8111-111111111111',b='22222222-2222-4222-8222-222222222222';
const stamp='2026-11-14T10:00:00Z',path=id=>`/api/v1/admin/users/${id}/legacy-lab`;
const absent=id=>({user_id:id,status:'not_admitted',revision:0,can_create_legacy_work:false});

async function fixture(browser){
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
  const state={readStatus:200,writeStatus:200,conflict:false},errors=[],writes=[],reads=[],holds=[];
  const values=new Map([[a,absent(a)],[b,{...absent(b),status:'active',revision:4,source:'admin',updated_at:stamp}]]);
  const users=[{id:a,name:'Мария Исследователь',username:'maria',role:'researcher',disabled:false},
    {id:b,name:'Иван Наблюдатель',username:'ivan',role:'viewer',disabled:true}];
  function pause(method,id,status){
    let release,seen;const gate=new Promise(resolve=>release=resolve),started=new Promise(resolve=>seen=resolve);
    holds.push({method,id,status,gate,seen,taken:false});return {release,started};
  }
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','lab-admin-fixture'));
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),url=new URL(req.url()),method=req.method();
    const reply=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
    if(url.pathname==='/api/v1/me')return reply({id:'admin-fixture',name:'Администратор',role:'admin'});
    if(url.pathname==='/api/v1/me/legacy-lab')return reply({user_id:'admin-fixture',status:'active',revision:1,source:'admin',updated_at:stamp,can_create_legacy_work:true});
    if(url.pathname==='/api/v1/projects')return reply({items:[],next_cursor:null});
    if(url.pathname==='/api/v1/admin/users')return reply({items:users,next_cursor:null});
    if(url.pathname==='/api/v1/session')return reply(method==='POST'?{token:'next-admin-fixture'}:{ok:true});
    const id=[a,b].find(id=>path(id)===url.pathname);
    if(id){
      let status=method==='GET'?state.readStatus:state.writeStatus;
      if(method==='GET')reads.push(id);
      else if(method==='PUT'){
        const input=req.postDataJSON();writes.push({id,input});
        assert.equal(url.search,'');assert.deepEqual(Object.keys(input).sort(),['reason','revision','status']);
        const old=values.get(id);
        if(state.conflict){
          state.conflict=false;values.set(id,{...old,status:'active',revision:old.revision+1,source:'admin',updated_at:stamp,can_create_legacy_work:id===a});status=409;
        }else if(status===200){
          assert.equal(input.revision,old.revision,'stale revision was silently retried');
          values.set(id,{...old,status:input.status,revision:old.revision+1,source:'admin',updated_at:stamp,can_create_legacy_work:input.status==='active'&&id===a});
        }
      }else throw Error('unexpected admission method');
      let body=structuredClone(values.get(id));
      const held=holds.find(item=>!item.taken&&item.method===method&&item.id===id);
      if(held){held.taken=true;held.seen();if(held.status)status=held.status;await held.gate}
      if(status!==200)body={code:status===409?'VERSION_CONFLICT':status===403?'FORBIDDEN':status===401?'UNAUTHORIZED':status===404?'NOT_FOUND':'DEPENDENCY_UNAVAILABLE'};
      return reply(body,status);
    }
    errors.push(`Unexpected API request ${method} ${url.pathname}`);return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#users');
  await page.getByRole('button',{name:'Допуск к лаборатории: Мария Исследователь',exact:true}).waitFor();
  return {context,page,state,errors,writes,reads,pause};
}
async function open(f,name='Мария Исследователь'){
  await f.page.getByRole('button',{name:`Допуск к лаборатории: ${name}`,exact:true}).click();
  return f.page.getByRole('dialog');
}
async function settled(page){await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))}
async function deliver(page,paused,method){
  const received=page.waitForResponse(response=>response.url().endsWith(path(a))&&response.request().method()===method);
  paused.release();await (await received).finished();await settled(page);
}
async function clean(f){assert.deepEqual(f.errors,[]);await f.context.close()}

async function main(){
  const proxy=http.createServer((req,res)=>{
    const upstream=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res)});
    upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  try{
    let f=await fixture(browser),dialog=await open(f);
    await dialog.getByText('Не выдан',{exact:true}).waitFor();
    const reason=dialog.getByLabel('Причина изменения',{exact:true});
    assert.equal(await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).isDisabled(),true);
    await reason.fill('я'.repeat(241));assert.equal(await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).isDisabled(),true);
    await reason.fill('Проверено администратором');await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).click();
    await dialog.getByText('Разрешены',{exact:true}).waitFor();
    assert.deepEqual(f.writes[0],{id:a,input:{status:'active',revision:0,reason:'Проверено администратором'}});
    await reason.fill('Отозвать новые вычисления');await dialog.getByRole('button',{name:'Запретить новые запуски',exact:true}).click();
    await dialog.getByText('Отозван',{exact:true}).waitFor();assert.equal(f.writes[1].input.revision,1);
    await f.page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    assert.equal(await f.page.getByRole('button',{name:'Допуск к лаборатории: Мария Исследователь',exact:true}).evaluate(node=>node===document.activeElement),true);
    dialog=await open(f,'Иван Наблюдатель');await dialog.getByText('Выдан',{exact:true}).waitFor();
    await dialog.getByText('Допуск выдан, но роль или состояние учётной записи пока не позволяют запускать обучение.').waitFor();
    assert.equal(await dialog.getByText('Недоступны',{exact:true}).count(),1);
    for(const width of [1440,768,390,320]){
      await f.page.setViewportSize({width,height:900});
      assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);
      for(const control of await dialog.locator('input,button').all()){
        const box=await control.boundingBox();assert.ok(box&&box.width>0&&box.x>=0&&box.x+box.width<=width,`clipped control ${width}`);
      }
      await f.page.screenshot({path:`/evidence/legacy-lab-admin-${width}.png`,fullPage:true});
    }
    await f.page.emulateMedia({reducedMotion:'reduce'});
    assert.equal(await dialog.evaluate(node=>getComputedStyle(node).animationName),'none');
    for(let i=0;i<7;i++)await f.page.keyboard.press('Tab');
    assert.ok(await dialog.evaluate(node=>node.contains(document.activeElement)),'focus escaped dialog');
    await clean(f);

    f=await fixture(browser);dialog=await open(f);await dialog.getByText('Не выдан',{exact:true}).waitFor();
    f.state.conflict=true;await dialog.getByLabel('Причина изменения',{exact:true}).fill('Исходное намерение');
    await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).click();
    await dialog.getByRole('alert').filter({hasText:'Допуск уже изменён'}).waitFor();
    await dialog.getByText('Выдан',{exact:true}).waitFor();
    assert.equal(f.writes.length,1);assert.ok(f.reads.length>=2);assert.equal(await dialog.getByLabel('Причина изменения',{exact:true}).inputValue(),'');
    assert.equal(await dialog.getByRole('button',{name:'Запретить новые запуски',exact:true}).isDisabled(),true);
    await dialog.getByLabel('Причина изменения',{exact:true}).fill('Новое подтверждённое намерение');
    await dialog.getByRole('button',{name:'Запретить новые запуски',exact:true}).click();await dialog.getByText('Отозван',{exact:true}).waitFor();
    assert.equal(f.writes.length,2);assert.equal(f.writes[1].input.revision,1);await clean(f);

    f=await fixture(browser);f.state.readStatus=503;dialog=await open(f);
    await dialog.getByRole('alert').waitFor();assert.equal(await dialog.locator('form').count(),0);
    f.state.readStatus=200;await dialog.getByRole('button',{name:'Проверить допуск',exact:true}).click();await dialog.getByText('Не выдан',{exact:true}).waitFor();
    f.state.writeStatus=503;await dialog.getByLabel('Причина изменения',{exact:true}).fill('Проверка отказа');
    await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).click();await dialog.getByRole('alert').waitFor();
    assert.equal(await dialog.locator('form').count(),0);assert.equal(f.writes.length,1);
    f.state.readStatus=403;await dialog.getByRole('button',{name:'Проверить допуск',exact:true}).click();await dialog.getByRole('alert').filter({hasText:'Право управления'}).waitFor();
    assert.equal(await dialog.locator('form').count(),0);await clean(f);

    for(const method of ['GET','PUT'])for(const status of [200,503]){
      f=await fixture(browser);let paused;
      if(method==='GET')paused=f.pause('GET',a,status);
      dialog=await open(f);
      if(method==='PUT'){
        await dialog.getByText('Не выдан',{exact:true}).waitFor();paused=f.pause('PUT',a,status);
        await dialog.getByLabel('Причина изменения',{exact:true}).fill('Поздний ответ');await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).click();
      }
      await paused.started;await f.page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
      dialog=await open(f,'Иван Наблюдатель');await dialog.getByText('Выдан',{exact:true}).waitFor();
      await dialog.getByLabel('Причина изменения',{exact:true}).fill('Черновик другого пользователя');await deliver(f.page,paused,method);
      assert.equal(await dialog.getByLabel('Причина изменения',{exact:true}).inputValue(),'Черновик другого пользователя');
      assert.equal(await dialog.getByRole('alert').count(),0);assert.equal(await dialog.getByText('Иван Наблюдатель',{exact:true}).count(),1);
      await clean(f);
    }
    f=await fixture(browser);dialog=await open(f);await dialog.getByText('Не выдан',{exact:true}).waitFor();
    const paused=f.pause('PUT',a,401);await dialog.getByLabel('Причина изменения',{exact:true}).fill('Старая сессия');
    await dialog.getByRole('button',{name:'Выдать допуск',exact:true}).click();await paused.started;
    await f.page.keyboard.press('Escape');await f.page.getByRole('button',{name:'Выйти',exact:true}).click();
    await f.page.getByLabel('Логин',{exact:true}).fill('next-admin');await f.page.getByLabel('Пароль',{exact:true}).fill('fixture-password-only');
    await f.page.getByRole('button',{name:'Войти',exact:true}).click();
    dialog=await open(f,'Иван Наблюдатель');await dialog.getByText('Выдан',{exact:true}).waitFor();await deliver(f.page,paused,'PUT');
    assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),'next-admin-fixture');
    assert.equal(await dialog.getByRole('alert').count(),0);await clean(f);
    console.log('PASS: explicit admission grant/revoke/CAS, metadata caps, dependency refusal, late target/session responses, keyboard/reduced-motion and responsive dialog');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exit(1)});
