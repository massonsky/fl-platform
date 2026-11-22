// COM-02C3C: real generated DTO shapes, intercepted API only; no live credentials.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),http=require('node:http');
const ids={a:'11111111-1111-4111-8111-111111111111',b:'22222222-2222-4222-8222-222222222222',pa:'33333333-3333-4333-8333-333333333333',pb:'44444444-4444-4444-8444-444444444444',archive:'55555555-5555-4555-8555-555555555555',owner:'66666666-6666-4666-8666-666666666666',maria:'77777777-7777-4777-8777-777777777777',ivan:'88888888-8888-4888-8888-888888888888'};
const stamp='2026-11-14T10:00:00Z',base=org=>`/api/v1/organizations/${ids[org]}/projects`;
const cursors={projects:'opaque projects A / +?=',access:'opaque roster / +?=',candidates:'opaque candidates / +?='};
async function fixture(browser,options={}){
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
  const errors=[],calls=[],writes=[],holds=[],state={off:false,grantConflict:false,visibilityConflict:false,createFail:false,denied:false,accessRevoked:false,demoted:false,...options};
  const manager=!options.viewer&&!options.suspended&&!options.researcher;
  const caps=(write=manager,manage=manager,read=true)=>({can_read:read,can_export:read,can_create_experiments:write,can_start_runs:false,can_cancel_runs:write,can_manage_access:manage});
  const make=(id,org,name='Модель качества')=>({id,organization_id:org,owner_id:ids.owner,created_by:ids.owner,slug:id===ids.archive?'archive':'quality',name,access_mode:'organization',visibility:'private',acl_revision:1,created_at:stamp,capabilities:caps(),access_source:options.researcher?'explicit_researcher':'organization_admin',run_blocked_reason:'ORG_AGENTS_NOT_READY'});
  const projects=new Map([[ids.pa,make(ids.pa,ids.a)],[ids.pb,make(ids.pb,ids.b)],[ids.archive,make(ids.archive,ids.a,'Архив модели')]]);
  const grants=new Map([ids.pa,ids.pb].map(project=>[`${project}:${ids.maria}`,{user_id:ids.maria,role:'viewer',status:'active',revision:1,updated_at:stamp}]));
  function member(id,project){
    const owner=id===ids.owner,viewer=id===ids.ivan||owner&&options.viewer,grant=grants.get(`${project.id}:${id}`)||null,denied=grant?.status==='revoked';
    const readable=owner||!denied&&(!!grant||project.visibility==='organization'),write=readable&&!viewer&&!options.suspended&&(owner||grant?.role==='researcher');
    return {user_id:id,display_name:owner?'Александр':id===ids.ivan?'Иван':'Мария',global_role:viewer?'viewer':'researcher',organization_role:owner?'owner':'researcher',membership_status:'active',account_active:true,grant,capabilities:caps(write,owner&&!options.viewer&&!options.suspended,readable),access_source:owner?'organization_admin':denied?'explicit_denied':grant?.role==='researcher'?'explicit_researcher':grant?'explicit_viewer':readable?'organization_read':'none',can_grant:manager&&!state.accessRevoked&&!owner,can_revoke:manager&&!state.accessRevoked&&!owner,...(owner?{grant_blocked_reason:'INHERITED_ACCESS'}:{}),...(viewer?{write_cap_reason:'GLOBAL_VIEWER'}:{})};
  }
  function pause(method,path,status,transform){let release,seen;const gate=new Promise(resolve=>release=resolve),started=new Promise(resolve=>seen=resolve);holds.push({method,path,status,transform,gate,seen,taken:false});return {release,started}}
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','organization-projects-fixture'));
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),url=new URL(req.url()),path=url.pathname,method=req.method();calls.push({path,method,cursor:url.searchParams.get('cursor')});
    const reply=async(body,status=200)=>{
      const held=holds.find(item=>!item.taken&&item.method===method&&(typeof item.path==='string'?item.path===path:item.path.test(path)));
      let payload=JSON.stringify(body);
      if(held){held.taken=true;held.seen();if(held.transform)payload=JSON.stringify(held.transform(JSON.parse(payload)));if(held.status){status=held.status;payload=JSON.stringify({code:status===503?'DEPENDENCY_UNAVAILABLE':'VERSION_CONFLICT'})}await held.gate}
      return route.fulfill({status,contentType:'application/json',body:payload});
    };
    if(path==='/api/v1/me')return reply({id:ids.owner,name:'Александр',role:options.viewer?'viewer':'researcher'});
    if(path==='/api/v1/me/legacy-lab')return reply({user_id:ids.owner,status:'active',revision:1,source:'admin',updated_at:stamp,can_create_legacy_work:!options.viewer});
    if(path==='/api/v1/projects'&&method==='GET')return reply({items:[{id:'personal',name:'Личное исследование',slug:'personal',owner_id:ids.owner,created_at:stamp}],next_cursor:null});
    if(path==='/api/v1/projects/personal/runs')return reply({items:[],next_cursor:null});
    if(path==='/api/v1/session')return reply({ok:true});
    if(path==='/api/v1/organizations')return reply({items:['a','b'].map(key=>({id:ids[key],name:`Лаборатория ${key.toUpperCase()}`,kind:'shared',state:options.suspended?'suspended':'active',role:options.researcher?'researcher':'owner',membership_status:'active',revision:1,created_at:stamp,can_manage_members:manager}))});
    const match=path.match(/^\/api\/v1\/organizations\/([^/]+)\/projects(?:\/([^/]+))?(?:\/(access|visibility|members))?(?:\/([^/]+))?$/);
    if(match){
      const [,org,id,action,target]=match,cursor=url.searchParams.get('cursor');
      if(state.off)return reply({code:'ORG_PROJECTS_NOT_CONFIGURED'},503);
      if(!id){
        if(cursor&&cursor!==cursors.projects)return reply({code:'INVALID_CURSOR'},400);
        return reply({items:[projects.get(org===ids.b?ids.pb:cursor?ids.archive:ids.pa)],context:{organization_id:org,organization_state:options.suspended?'suspended':'active',capabilities:{can_create_projects:manager}},next_cursor:org===ids.a&&!cursor?cursors.projects:null});
      }
      if(method==='PUT'&&!action){
        const input=req.postDataJSON();writes.push({path,method,input});assert.deepEqual(Object.keys(input).sort(),['name','slug']);assert.match(id,/^[0-9a-f-]{36}$/);
        if(!projects.has(id))projects.set(id,{...make(id,org,input.name),slug:input.slug});
        if(state.createFail){state.createFail=false;return reply({code:'DEPENDENCY_UNAVAILABLE'},503)}
        return reply(projects.get(id));
      }
      if(state.denied||!projects.has(id)||projects.get(id).organization_id!==org)return reply({code:'NOT_FOUND'},404);
      const project=projects.get(id);
      if(state.accessRevoked)project.capabilities={...project.capabilities,can_manage_access:false};
      if(state.demoted){project.access_source='explicit_viewer';project.capabilities=caps(false,false,true)}
      if(method==='GET'&&!action)return reply(project);
      if(method==='GET'&&action==='access'){
        if(options.researcher||state.demoted)return reply({code:'FORBIDDEN'},403);
        if(target&&state.accessRevoked)return reply({code:'FORBIDDEN'},403);
        const routeName=target?'candidates':'access';
        if(cursor&&cursor!==cursors[routeName])return reply({code:'INVALID_CURSOR'},400);
        return reply({project,items:(cursor?[ids.ivan]:target?[ids.maria]:[ids.owner,ids.maria]).map(user=>member(user,project)),next_cursor:cursor?null:cursors[routeName]});
      }
      const input=req.postDataJSON();writes.push({path,method,input});assert.ok(manager,'read-only fixture received a mutation');
      if(action==='visibility'){
        assert.equal(method,'PATCH');assert.deepEqual(Object.keys(input).sort(),['acl_revision','visibility']);
        if(state.visibilityConflict){state.visibilityConflict=false;project.acl_revision++;return reply({code:'VERSION_CONFLICT'},409)}
        assert.equal(input.acl_revision,project.acl_revision);project.visibility=input.visibility;project.acl_revision++;return reply(project);
      }
      if(action==='members'){
        assert.equal(method,'PUT');assert.deepEqual(Object.keys(input).sort(),['revision','role','status']);
        const grantKey=`${id}:${target}`,grant=grants.get(grantKey);
        if(state.grantConflict){state.grantConflict=false;grant.revision++;return reply({code:'VERSION_CONFLICT'},409)}
        assert.equal(input.revision,grant?.revision||0);grants.set(grantKey,{user_id:target,...input,revision:input.revision+1,updated_at:stamp});project.acl_revision++;return reply(grants.get(grantKey));
      }
    }
    errors.push(`Unexpected or legacy shared request ${method} ${path}`);return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#projects');
  const organizationOption=page.getByRole('option',{name:options.suspended?'Лаборатория A · приостановлена':'Лаборатория A',exact:true});
  await organizationOption.waitFor({state:'attached'});assert.equal(await organizationOption.getAttribute('value'),ids.a);
  return {page,context,state,calls,writes,errors,pause};
}
const select=async(f,org='a')=>{await f.page.getByLabel('Рабочая область',{exact:true}).selectOption(ids[org]);await f.page.getByRole('region',{name:`Проекты: Лаборатория ${org.toUpperCase()}`,exact:true}).getByRole('button',{name:'Модель качества quality',exact:true}).waitFor()};
async function openProject(f){await f.page.getByRole('button',{name:'Модель качества quality',exact:true}).click();await f.page.getByRole('region',{name:'Доступ к проекту: Модель качества',exact:true}).getByRole('heading',{name:'Кому виден проект',exact:true}).waitFor();await f.page.waitForFunction(()=>document.querySelector('.project-access')?.getAttribute('aria-busy')==='false')}
async function createForm(f,name='Новый проект'){await f.page.getByRole('button',{name:'Создать проект',exact:true}).click();await f.page.getByLabel('Название проекта',{exact:true}).fill(name);await f.page.getByLabel('Короткое имя',{exact:true}).fill('new-project')}
const settled=page=>page.waitForTimeout(250);
async function main(){
  const proxy=http.createServer((req,res)=>{const up=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>{res.writeHead(502);res.end()});req.pipe(up)});
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true});
  try{
    let f=await fixture(browser,{off:true});await f.page.getByLabel('Рабочая область',{exact:true}).selectOption(ids.a);await f.page.getByRole('heading',{name:'Раздел готовится к запуску'}).waitFor();assert.equal(await f.page.getByRole('button',{name:'Создать проект',exact:true}).count(),0);
    await f.page.getByLabel('Рабочая область',{exact:true}).selectOption('');await f.page.getByRole('button',{name:'Открыть эксперименты: Личное исследование'}).click();await f.page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();assert.deepEqual(f.errors,[]);await f.context.close();
    f=await fixture(browser);await select(f);
    const personalRuns=f.page.waitForResponse(response=>new URL(response.url()).pathname==='/api/v1/projects/personal/runs');
    await f.page.getByRole('link',{name:'Эксперименты',exact:true}).click();await personalRuns;
    assert.equal(await f.page.getByRole('combobox',{name:'Текущий проект',exact:true}).inputValue(),'personal','entering experiments failed to restore a real personal project');
    await f.page.getByRole('link',{name:'Проекты',exact:true}).click();await select(f);
    const listing=f.page.getByRole('region',{name:'Проекты: Лаборатория A',exact:true});await listing.getByRole('button',{name:'Далее',exact:true}).click();await listing.getByRole('button',{name:'Архив модели archive',exact:true}).waitFor();assert.ok(f.calls.some(c=>c.path===base('a')&&c.cursor===cursors.projects));await listing.getByRole('button',{name:'Назад',exact:true}).click();await openProject(f);
    assert.equal(await f.page.getByRole('button',{name:'Настроить доступ: Александр'}).count(),0);
    await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).click();await f.page.getByLabel('Роль в проекте',{exact:true}).selectOption('researcher');f.state.grantConflict=true;await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await f.page.getByRole('dialog').getByRole('alert').waitFor();const conflictWrites=f.writes.length;await settled(f.page);assert.equal(f.writes.length,conflictWrites);
    await f.page.getByRole('button',{name:'Закрыть и обновить'}).click();await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).click();await f.page.getByLabel('Роль в проекте',{exact:true}).selectOption('researcher');await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await f.page.getByRole('dialog').waitFor({state:'detached'});assert.equal(f.writes.at(-1).input.revision,2);
    await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).click();await f.page.getByLabel('Доступ',{exact:true}).selectOption('revoked');await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await f.page.getByRole('dialog').waitFor({state:'detached'});assert.equal(f.writes.at(-1).input.status,'revoked');
    await f.page.getByLabel('Область просмотра',{exact:true}).selectOption('organization');f.state.visibilityConflict=true;await f.page.getByRole('button',{name:'Сохранить видимость'}).click();await f.page.getByRole('alert').waitFor();const visibilityWrites=f.writes.length;await settled(f.page);assert.equal(f.writes.length,visibilityWrites);await f.page.getByRole('button',{name:'Обновить доступ'}).click();await f.page.getByLabel('Область просмотра',{exact:true}).selectOption('organization');await f.page.getByRole('button',{name:'Сохранить видимость'}).click();await f.page.getByRole('status').filter({hasText:'Видимость проекта сохранена'}).waitFor();
    await listing.getByText('Просмотр организации',{exact:true}).waitFor();
    await f.page.getByRole('button',{name:'Выдать доступ',exact:true}).click();await f.page.getByRole('dialog').getByRole('button',{name:'Далее',exact:true}).click();await f.page.getByRole('button',{name:'Выбрать участника: Иван'}).click();await f.page.getByRole('dialog').getByText(/Учётная запись допускает только просмотр/).waitFor();await f.page.getByLabel('Роль в проекте',{exact:true}).selectOption('researcher');await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await f.page.getByRole('dialog').waitFor({state:'detached'});assert.equal(f.writes.at(-1).input.revision,0);assert.ok(f.calls.some(c=>c.path.endsWith('/access/candidates')&&c.cursor===cursors.candidates));
    const access=f.page.getByRole('region',{name:'Доступ к проекту: Модель качества',exact:true});await access.getByRole('button',{name:'Далее',exact:true}).click();await access.getByText('Иван',{exact:true}).waitFor();assert.ok(f.calls.some(c=>c.path.endsWith('/access')&&c.cursor===cursors.access));await access.getByRole('button',{name:'Назад',exact:true}).click();
    for(const width of [1440,768,390,320]){await f.page.setViewportSize({width,height:1000});await f.page.waitForFunction(()=>document.querySelector('.project-access')?.getAttribute('aria-busy')==='false');assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`project page overflow ${width}`);await f.page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo(0,0)});await f.page.screenshot({path:`/evidence/organization-projects-${width}.png`,fullPage:true})}
    await f.page.setViewportSize({width:1440,height:1000});await createForm(f);f.state.createFail=true;await f.page.getByRole('dialog').getByRole('button',{name:'Создать проект',exact:true}).click();await f.page.getByRole('dialog').getByRole('alert').waitFor();const first=f.writes.at(-1),beforeRetry=f.writes.length;await settled(f.page);assert.equal(f.writes.length,beforeRetry);await f.page.getByRole('dialog').getByRole('button',{name:'Создать проект',exact:true}).click();await f.page.getByRole('dialog').waitFor({state:'detached'});assert.equal(f.writes.at(-1).path,first.path,'create retry changed UUID');await f.page.getByRole('heading',{name:'Новый проект',exact:true}).waitFor();
    f.state.denied=true;await f.page.getByRole('button',{name:'Обновить доступ'}).click();await f.page.getByRole('alert').filter({hasText:'Проект больше недоступен'}).waitFor();assert.equal(await f.page.locator('.project-access').count(),0);assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),'organization-projects-fixture');assert.equal(await f.page.getByRole('button',{name:/Запустить|Экспорт|Повторить/}).count(),0);assert.deepEqual(f.errors,[]);await f.context.close();
    for(const option of [{viewer:true},{suspended:true},{researcher:true}]){f=await fixture(browser,option);await select(f);assert.equal(await f.page.getByRole('button',{name:'Создать проект',exact:true}).count(),0);await openProject(f);assert.equal(await f.page.getByRole('button',{name:'Выдать доступ',exact:true}).count(),0);assert.equal(await f.page.getByLabel('Область просмотра',{exact:true}).count(),0);assert.equal(f.writes.length,0);assert.equal(f.calls.some(c=>c.path.endsWith('/access')),!option.researcher);assert.deepEqual(f.errors,[]);await f.context.close()}
    for(const status of [200,503]){
      f=await fixture(browser);const late=f.pause('GET',base('a'),status===503?503:undefined,body=>({...body,items:body.items.map(item=>({...item,name:'ПРОШЛЫЙ A'}))}));await f.page.getByLabel('Рабочая область',{exact:true}).selectOption(ids.a);await late.started;await select(f,'b');await select(f,'a');late.release();await settled(f.page);assert.equal(await f.page.getByRole('button',{name:/ПРОШЛЫЙ A/}).count(),0);assert.ok(await f.page.getByRole('button',{name:'Модель качества quality',exact:true}).isVisible());assert.equal(await f.page.getByRole('alert').count(),0);assert.deepEqual(f.errors,[]);await f.context.close();
    }
    for(const status of [200,409]){
      f=await fixture(browser);await select(f);const late=f.pause('PUT',new RegExp(`${base('a')}/[^/]+$`),status===409?409:undefined);await createForm(f);await f.page.getByRole('dialog').getByRole('button',{name:'Создать проект',exact:true}).click();await late.started;await f.page.evaluate(()=>location.hash='account');await f.page.getByRole('heading',{name:'Мой профиль',exact:true}).waitFor();await f.page.getByRole('link',{name:'Проекты',exact:true}).click();await select(f,'b');await createForm(f,'Черновик B');late.release();await settled(f.page);assert.equal(await f.page.getByLabel('Название проекта',{exact:true}).inputValue(),'Черновик B');assert.equal(await f.page.getByRole('dialog').getByRole('alert').count(),0);assert.ok(await f.page.getByRole('dialog').getByRole('button',{name:'Создать проект',exact:true}).isEnabled());assert.equal(f.writes.length,1);assert.deepEqual(f.errors,[]);await f.context.close();
    }
    f=await fixture(browser);await select(f);await openProject(f);
    f.state.accessRevoked=true;
    await f.page.getByRole('button',{name:'Выдать доступ',exact:true}).click();
    await f.page.getByRole('alert').filter({hasText:'Права изменились'}).waitFor();
    assert.equal(await f.page.getByRole('dialog').count(),0,'403 kept the candidate picker open');
    assert.equal(await f.page.getByRole('button',{name:'Выдать доступ',exact:true}).count(),0);
    assert.equal(await f.page.getByLabel('Область просмотра',{exact:true}).count(),0);
    await f.page.getByRole('button',{name:'Обновить доступ'}).click();
    await f.page.getByText('Доступен просмотр прав. Изменения сейчас запрещены.',{exact:true}).waitFor();
    assert.equal(await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).count(),0);
    assert.equal(f.writes.length,0);assert.equal(await f.page.evaluate(()=>sessionStorage.getItem('fl-session')),'organization-projects-fixture');
    assert.deepEqual(f.errors,[]);await f.context.close();
    f=await fixture(browser);await select(f);await openProject(f);
    const rosterReads=f.calls.filter(call=>call.path.endsWith('/access')).length;f.state.demoted=true;
    await f.page.getByRole('button',{name:'Обновить доступ'}).click();
    await f.page.getByText(/Ваш доступ: Просмотр\./).waitFor();
    assert.equal(await f.page.getByRole('heading',{name:'Доступ участников',exact:true}).count(),0,'cached org role retained the roster after demotion');
    assert.equal(await f.page.getByRole('alert').count(),0);
    assert.equal(f.calls.filter(call=>call.path.endsWith('/access')).length,rosterReads,'fresh non-admin detail still requested forbidden roster');
    await f.page.getByRole('button',{name:'Обновить доступ'}).click();
    await f.page.waitForFunction(()=>document.querySelector('.project-access')?.getAttribute('aria-busy')==='false');
    assert.equal(f.calls.filter(call=>call.path.endsWith('/access')).length,rosterReads);
    assert.equal(f.writes.length,0);assert.deepEqual(f.errors,[]);await f.context.close();
    f=await fixture(browser);await select(f);await openProject(f);
    const lateGrant=f.pause('PUT',`${base('a')}/${ids.pa}/members/${ids.maria}`);f.state.grantConflict=true;
    await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).click();
    await f.page.getByLabel('Роль в проекте',{exact:true}).selectOption('researcher');
    await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await lateGrant.started;
    await f.page.evaluate(()=>location.hash='account');await f.page.getByRole('heading',{name:'Мой профиль',exact:true}).waitFor();
    await f.page.getByRole('link',{name:'Проекты',exact:true}).click();await select(f,'b');await openProject(f);
    await f.page.getByRole('button',{name:'Настроить доступ: Мария'}).click();
    await f.page.getByLabel('Роль в проекте',{exact:true}).selectOption('researcher');lateGrant.release();await settled(f.page);
    assert.equal(await f.page.getByLabel('Роль в проекте',{exact:true}).inputValue(),'researcher');
    assert.equal(await f.page.getByRole('alert').count(),0,'late grant conflict entered the new organization');
    assert.ok(await f.page.getByRole('button',{name:'Сохранить права',exact:true}).isEnabled());
    await f.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await f.page.getByRole('dialog').waitFor({state:'detached'});
    assert.equal(f.writes.at(-1).input.revision,1,'organization A grant changed the organization B revision');
    assert.ok(f.writes.at(-1).path.startsWith(base('b')));assert.deepEqual(f.errors,[]);await f.context.close();
    f=await fixture(browser);await select(f);await openProject(f);const late=f.pause('PATCH',`${base('a')}/${ids.pa}/visibility`,409);await f.page.getByLabel('Область просмотра',{exact:true}).selectOption('organization');await f.page.getByRole('button',{name:'Сохранить видимость'}).click();await late.started;await select(f,'b');late.release();await settled(f.page);assert.equal(await f.page.getByRole('alert').count(),0);assert.equal(await f.page.locator('.project-access').count(),0);assert.deepEqual(f.errors,[]);
    await f.page.emulateMedia({reducedMotion:'reduce'});await f.page.getByRole('link',{name:'Эксперименты',exact:true}).click();await f.page.getByRole('link',{name:'Проекты',exact:true}).focus();await f.page.keyboard.press('Enter');await f.page.getByRole('heading',{name:'Проекты',exact:true}).waitFor();assert.equal(await f.page.locator('.content').evaluate(element=>element.getAnimations().length),0);await f.context.close();
    console.log('PASS organization projects: scoped pages/cursors, UUID retry, grants/revoke/CAS, caps, feature-off, late reads/writes, private handoff, keyboard/reduced motion and four widths');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exitCode=1});
