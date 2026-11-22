// Browser contract fixtures only: no live account, database, or credentials.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http');
const fixtureToken='session-recovery-fixture';
const project={id:'recovery-project',name:'Восстановленный проект',slug:'recovery'};
const temporaryMessage='Сервис временно недоступен. Повторите попытку позже.';

async function fixture(browser,initial){
  const context=await browser.newContext({viewport:{width:1440,height:900}});
  const page=await context.newPage(),errors=[],counts={me:0,projects:0};
  const state={me:200,projects:200,...initial};
  page.on('pageerror',error=>errors.push(error.message));
  await page.addInitScript(token=>sessionStorage.setItem('fl-session',token),fixtureToken);
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),path=new URL(req.url()).pathname;
    const reply=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
    assert.equal(req.headers().authorization,`Bearer ${fixtureToken}`,'saved session was not sent');
    if(path==='/api/v1/me/legacy-lab')return reply({user_id:'fixture-user',status:'active',revision:1,source:'admin',updated_at:'2026-11-14T10:00:00Z',can_create_legacy_work:true});
    const resource=path==='/api/v1/me'?'me':path==='/api/v1/projects'?'projects':null;
    if(resource){
      counts[resource]++;
      const status=state[resource];
      if(status===0)return route.abort('connectionfailed');
      if(status!==200)return reply({code:status===503?'DEPENDENCY_UNAVAILABLE':status===401?'UNAUTHORIZED':'FORBIDDEN',retryable:status===503},status);
      return reply(resource==='me'?{id:'fixture-user',name:'Оператор восстановления',role:'admin'}:{items:[project],next_cursor:null});
    }
    if(path===`/api/v1/projects/${project.id}/runs`)return reply({items:[],next_cursor:null});
    if(path==='/api/v1/session'&&req.method()==='DELETE')return reply({ok:true});
    errors.push(`Unexpected API request ${req.method()} ${path}`);
    return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#experiments');
  return {context,page,state,counts,errors};
}

async function assertWorkspace(page){
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>sessionStorage.getItem('fl-session')),fixtureToken);
  assert.equal(await page.getByRole('heading',{name:'Вход в платформу',exact:true}).count(),0);
}

async function main(){
  const proxy=http.createServer((req,res)=>{
    const upstream=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res)});
    upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  try{
    // Role and project reads recover independently without reload or a new login.
    let f=await fixture(browser,{me:503,projects:503});
    await f.page.getByRole('alert').filter({hasText:temporaryMessage}).waitFor();
    await assertWorkspace(f.page);
    f.state.me=200;
    await f.page.getByRole('link',{name:'Пользователи',exact:true}).waitFor({timeout:12000});
    await f.page.getByRole('alert').filter({hasText:temporaryMessage}).waitFor();
    f.state.projects=200;
    await f.page.waitForFunction(id=>document.querySelector('select[aria-label="Текущий проект"]')?.value===id,project.id,{timeout:12000});
    await f.page.getByRole('alert').waitFor({state:'detached',timeout:12000});
    await assertWorkspace(f.page);
    assert.ok(f.counts.me>=2&&f.counts.projects>=2,'initial reads were not retried');
    assert.deepEqual(f.errors,[]);await f.context.close();

    // A transport failure has the same recovery path and retains the session.
    f=await fixture(browser,{me:0,projects:200});
    await f.page.getByRole('alert').filter({hasText:'Не удалось связаться с сервером'}).waitFor();
    await assertWorkspace(f.page);f.state.me=200;
    await f.page.getByRole('link',{name:'Пользователи',exact:true}).waitFor({timeout:12000});
    await f.page.getByRole('alert').waitFor({state:'detached',timeout:12000});
    await assertWorkspace(f.page);
    assert.deepEqual(f.errors,[]);await f.context.close();

    // Authentication rejection clears the token and must not enter a retry loop.
    f=await fixture(browser,{me:401,projects:200});
    await f.page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor();
    assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),null);
    const rejectedCalls=f.counts.me;
    await f.page.waitForTimeout(1500);
    assert.equal(f.counts.me,rejectedCalls,'401 was retried');
    assert.deepEqual(f.errors,[]);await f.context.close();

    // Forbidden is not transient; it preserves the token without retrying.
    f=await fixture(browser,{me:403,projects:200});
    await f.page.getByRole('alert').filter({hasText:'Недостаточно прав'}).waitFor();
    const forbiddenCalls=f.counts.me;
    await f.page.waitForTimeout(1500);
    assert.equal(f.counts.me,forbiddenCalls,'403 was retried');
    await assertWorkspace(f.page);
    assert.deepEqual(f.errors,[]);await f.context.close();

    // Leaving the session cancels scheduled retries and late bootstrap updates.
    f=await fixture(browser,{me:503,projects:503});
    await f.page.getByRole('alert').filter({hasText:temporaryMessage}).waitFor();
    await f.page.getByRole('button',{name:'Выйти',exact:true}).click();
    await f.page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor();
    const callsAtLogout={...f.counts};
    f.state.me=200;f.state.projects=200;
    await f.page.waitForTimeout(2500);
    assert.deepEqual(f.counts,callsAtLogout,'bootstrap retried after logout');
    assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),null);
    assert.deepEqual(f.errors,[]);await f.context.close();
    console.log('PASS session recovery: initial 503/network preserves token/workspace; role and projects recover without reload; 401 clears session; 403 and logout stop retries');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exitCode=1});
