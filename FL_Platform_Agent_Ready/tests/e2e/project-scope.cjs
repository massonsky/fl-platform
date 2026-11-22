// Deliberately out-of-order API responses; no live project or credentials.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http');
const stamp='2026-11-14T10:00:00Z';
const run=scope=>({id:`run-${scope}`,project_id:`project-${scope}`,experiment_id:`exp-${scope}`,
  status:'cancelled',total_rounds:3,completed_rounds:0,latest_model_version:0,
  requested_at:stamp,failure_code:null,config_hash:`fixture-${scope}`});

async function fixture(browser,lateStatus=200,slowCreate=false,slowUnauthorized=false){
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
  const errors=[],calls={a:0,b:0},state={expire:false,creates:0};
  let release,seen;
  const gate=new Promise(resolve=>{release=resolve}),started=new Promise(resolve=>{seen=resolve});
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','project-scope-fixture'));
  if(slowCreate)await page.addInitScript(()=>{
    const original=window.fetch.bind(window);
    let release;
    const gate=new Promise(resolve=>{release=resolve});
    window.releaseCreatedProject=()=>release();
    window.fetch=async(input,options)=>{
      const response=await original(input,options);
      if(String(input)==='/api/v1/projects'&&options?.method==='POST'){
        const json=response.json.bind(response);
        response.json=async()=>{const value=await json();window.createdProjectDelivered=true;await gate;return value};
      }
      return response;
    };
  });
  if(slowUnauthorized)await page.addInitScript(()=>{
    const original=window.fetch.bind(window);
    let release;
    const gate=new Promise(resolve=>{release=resolve});
    window.releaseUnauthorizedBody=()=>release();
    window.fetch=async(input,options)=>{
      const response=await original(input,options);
      if(String(input).startsWith('/api/v1/projects/project-a/runs')){
        const json=response.json.bind(response);
        response.json=async()=>{const value=await json();window.unauthorizedBodyDelivered=true;await gate;return value};
      }
      return response;
    };
  });
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),path=new URL(req.url()).pathname;
    const reply=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
    const list=items=>reply({items,next_cursor:null});
    const nextSession=req.headers().authorization==='Bearer next-session-fixture';
    if(path==='/api/v1/me/legacy-lab')return reply({user_id:nextSession?'other-user':'user',status:'active',revision:1,source:'admin',updated_at:stamp,can_create_legacy_work:true});
    if(path==='/api/v1/me')return reply({id:nextSession?'other-user':'user',name:'Исследователь',role:'researcher'});
    if(path==='/api/v1/projects'&&req.method()==='POST'){
      state.creates++;return reply({id:'late-created-project',name:'Поздний проект A',slug:'late-a',owner_id:'user',created_at:stamp});
    }
    if(path==='/api/v1/projects')return list((nextSession?['b']:['a','b']).map(scope=>({id:`project-${scope}`,name:`Проект ${scope.toUpperCase()}`,slug:scope,owner_id:nextSession?'other-user':'user',created_at:stamp})));
    if(path==='/api/v1/projects/project-a/runs'){
      calls.a++;seen();await gate;
      if(state.expire&&!nextSession)return reply({code:'UNAUTHORIZED'},401);
      return lateStatus===200?list([run('a')]):reply({code:lateStatus===401?'UNAUTHORIZED':'DEPENDENCY_UNAVAILABLE'},lateStatus);
    }
    if(path==='/api/v1/projects/project-b/runs'){calls.b++;return list([run('b')])}
    if(/\/runs\/run-[ab]\/(metrics|rounds|events)$/.test(path))return list([]);
    if(path.startsWith('/api/v1/experiments/'))return reply({id:path.split('/').pop(),name:`Результат ${path.endsWith('b')?'B':'A'}`,
      config:{algorithm:'fedavg',partition:'balanced',seed:11,rounds:3}});
    if(path==='/api/v1/session'&&req.method()==='DELETE')return reply({ok:true});
    if(path==='/api/v1/session'&&req.method()==='POST')return reply({token:'next-session-fixture'});
    errors.push(`Unexpected API request ${req.method()} ${path}`);
    return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#experiments');await started;
  return {context,page,release,calls,errors,state};
}

async function releaseLate(f){
  const response=f.page.waitForResponse(result=>new URL(result.url()).pathname==='/api/v1/projects/project-a/runs');
  f.release();await response;
  // Allow the delivered response body and React update to settle before assertions.
  await f.page.waitForTimeout(250);
}

async function main(){
  const proxy=http.createServer((req,res)=>{
    const upstream=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res)});
    upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  try{
    for(const status of [200,503]){
      const f=await fixture(browser,status);
      await f.page.getByRole('combobox',{name:'Текущий проект',exact:true}).selectOption('project-b');
      await f.page.getByRole('heading',{name:'Результат B',exact:true}).waitFor();
      assert.equal(await f.page.getByRole('button',{name:/run-a/}).count(),0);
      await releaseLate(f);
      assert.equal(await f.page.getByRole('combobox',{name:'Текущий проект',exact:true}).inputValue(),'project-b');
      assert.ok(await f.page.getByRole('heading',{name:'Результат B',exact:true}).isVisible(),'late A replaced the B detail');
      assert.equal(await f.page.getByRole('button',{name:/run-a/}).count(),0,'late A populated the B run list');
      assert.equal(await f.page.getByRole('alert').count(),0,'late A failure polluted the B error state');
      assert.deepEqual(f.errors,[]);await f.context.close();
    }
    const f=await fixture(browser);
    await f.page.getByRole('button',{name:'Выйти',exact:true}).click();
    await f.page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor();
    await releaseLate(f);
    const callsAtLogout={...f.calls};
    await f.page.waitForTimeout(3200);
    assert.deepEqual(f.calls,callsAtLogout,'project polling continued after logout');
    assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),null);
    assert.equal(await f.page.locator('.workspace').count(),0,'late response restored a logged-out workspace');
    assert.deepEqual(f.errors,[]);await f.context.close();
    // The response headers were accepted in session A; its body completes in session B.
    // A successful server-side create is retained, but must not enter B's UI state.
    const created=await fixture(browser,200,true);created.release();
    await created.page.getByRole('heading',{name:'Результат A',exact:true}).waitFor();
    await created.page.getByRole('button',{name:'Создать проект',exact:true}).click();
    await created.page.getByLabel('Название',{exact:true}).fill('Поздний проект A');
    await created.page.getByLabel('Короткое имя',{exact:true}).fill('late-a');
    await created.page.getByRole('dialog').getByRole('button',{name:'Создать проект',exact:true}).click();
    await created.page.waitForFunction(()=>window.createdProjectDelivered);
    created.state.expire=true;
    await created.page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor({timeout:8000});
    await created.page.getByLabel('Логин',{exact:true}).fill('other-user');
    await created.page.getByLabel('Пароль',{exact:true}).fill('fixture-only-password');
    await created.page.getByRole('button',{name:'Войти',exact:true}).click();
    await created.page.getByRole('heading',{name:'Результат B',exact:true}).waitFor();
    await created.page.evaluate(()=>window.releaseCreatedProject());
    await created.page.waitForTimeout(250);
    assert.equal(await created.page.getByRole('combobox',{name:'Текущий проект',exact:true}).inputValue(),'project-b');
    assert.equal(await created.page.getByRole('option',{name:'Поздний проект A',exact:true}).count(),0,'old create polluted the new session');
    assert.equal(created.state.creates,1,'project creation was repeated');
    assert.equal(await created.page.getByRole('alert').count(),0);
    assert.deepEqual(created.errors,[]);await created.context.close();
    const unauthorized=await fixture(browser,401,false,true);unauthorized.release();
    await unauthorized.page.waitForFunction(()=>window.unauthorizedBodyDelivered);
    await unauthorized.page.getByRole('button',{name:'Выйти',exact:true}).click();
    await unauthorized.page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor();
    await unauthorized.page.getByLabel('Логин',{exact:true}).fill('other-user');
    await unauthorized.page.getByLabel('Пароль',{exact:true}).fill('fixture-only-password');
    await unauthorized.page.getByRole('button',{name:'Войти',exact:true}).click();
    await unauthorized.page.getByRole('heading',{name:'Результат B',exact:true}).waitFor();
    await unauthorized.page.evaluate(()=>window.releaseUnauthorizedBody());
    await unauthorized.page.waitForTimeout(250);
    assert.equal(await unauthorized.page.evaluate(()=>sessionStorage.getItem('fl-session')),'next-session-fixture','late 401 body cleared the new session');
    assert.ok(await unauthorized.page.getByRole('heading',{name:'Результат B',exact:true}).isVisible());
    assert.deepEqual(unauthorized.errors,[]);await unauthorized.context.close();
    console.log('PASS project scope: late runs ignored; logout stops reads; late create/401 bodies cannot change or clear a new session');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exitCode=1});
