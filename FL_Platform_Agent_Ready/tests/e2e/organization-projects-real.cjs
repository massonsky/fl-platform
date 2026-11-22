// COM-02C3D: no response mocking, synthetic tokens, live credentials or queue calls.
const fs=require('node:fs');
const crypto=require('node:crypto');
const assert=require('node:assert/strict');
const {chromium}=require('playwright');
const {fixture,api,eventually,proxy,redact}=require('./organization-projects-real-support.cjs');

async function main(){
  const data=fixture(),server=await proxy(),browser=await chromium.launch({headless:true});
  const contexts=[],errors=[],requests=[],checks=[];
  const base=key=>`/organizations/${data.organizations[key].id}/projects`;
  const pass=name=>{checks.push(name);console.log('PASS '+name)};
  async function login(actor){
    const context=await browser.newContext({viewport:{width:1440,height:1000}});contexts.push(context);
    const page=await context.newPage();page.setDefaultTimeout(15000);
    page.on('pageerror',error=>errors.push(error.message));
    page.on('request',request=>{const url=new URL(request.url());if(url.pathname.startsWith('/api/'))requests.push({method:request.method(),path:url.pathname,page})});
    await page.goto('http://127.0.0.1:3000/#login');
    await page.getByLabel('Логин',{exact:true}).fill(actor.username);
    await page.getByLabel('Пароль',{exact:true}).fill(actor.password);
    await page.getByRole('button',{name:'Войти',exact:true}).click();
    await page.getByRole('navigation',{name:'Разделы платформы'}).waitFor();
    const token=await page.evaluate(()=>sessionStorage.getItem('fl-session'));assert.ok(token);
    return {page,token};
  }
  async function select(actor,key){
    await actor.page.getByRole('link',{name:'Проекты',exact:true}).click();
    const selector=actor.page.getByLabel('Рабочая область',{exact:true});
    await selector.getByRole('option',{name:data.organizations[key].name,exact:true}).waitFor({state:'attached'});
    await selector.selectOption(data.organizations[key].id);
    const region=actor.page.getByRole('region',{name:`Проекты: ${data.organizations[key].name}`,exact:true});
    await region.waitFor();await eventually(async()=>await region.getAttribute('aria-busy')==='false','project list');
    // Another browser may have changed grants while this context stayed selected.
    // Re-selecting the same native option is not a user refresh action.
    const refreshed=actor.page.waitForResponse(response=>response.request().method()==='GET'&&new URL(response.url()).pathname===`/api/v1${base(key)}`&&response.status()===200);
    await region.getByRole('button',{name:'Обновить проекты',exact:true}).click();await refreshed;
    await eventually(async()=>await region.getAttribute('aria-busy')==='false','explicit project list refresh');
    return region;
  }
  async function loaded(actor){
    await actor.page.locator('.project-access').waitFor();
    await eventually(async()=>await actor.page.locator('.project-access').getAttribute('aria-busy')==='false','project detail');
  }
  async function open(actor){
    await actor.page.getByRole('button',{name:'Совместная модель quality',exact:true}).click();await loaded(actor);
  }
  async function refresh(actor){await actor.page.getByRole('button',{name:'Обновить доступ',exact:true}).click();await loaded(actor)}
  async function create(actor,key){
    await select(actor,key);await actor.page.getByRole('button',{name:'Создать проект',exact:true}).click();
    const dialog=actor.page.getByRole('dialog');
    await dialog.getByLabel('Название проекта',{exact:true}).fill('Совместная модель');
    await dialog.getByLabel('Короткое имя',{exact:true}).fill('quality');
    await dialog.getByRole('button',{name:'Создать проект',exact:true}).click();await dialog.waitFor({state:'detached'});await loaded(actor);
    const page=await api(actor.token,'GET',base(key));assert.equal(page.items.length,1);
    assert.equal(page.items[0].visibility,'private');return page.items[0];
  }
  async function edit(actor,name,role,status='active',candidate=false){
    if(candidate){await actor.page.getByRole('button',{name:'Выдать доступ',exact:true}).click();await actor.page.getByRole('button',{name:`Выбрать участника: ${name}`,exact:true}).click()}
    else await actor.page.getByRole('button',{name:`Настроить доступ: ${name}`,exact:true}).click();
    await actor.page.getByLabel('Роль в проекте',{exact:true}).selectOption(role);
    await actor.page.getByLabel('Доступ',{exact:true}).selectOption(status);
  }
  async function save(actor){await actor.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await actor.page.getByRole('dialog').waitFor({state:'detached'});await loaded(actor)}
  try{
    const manager=await login(data.actors.manager),member=await login(data.actors.member),capped=await login(data.actors.capped);
    const pa=await create(manager,'a'),pb=await create(manager,'b'),path=base('a')+'/'+pa.id;
    assert.notEqual(pa.id,pb.id);assert.equal(pa.slug,pb.slug);assert.equal(pa.name,pb.name);
    await select(manager,'a');await open(manager);
    const legacy=(await api(manager.token,'GET','/projects')).items;
    assert.deepEqual(legacy.map(item=>item.id),[data.personal.id]);
    await api(manager.token,'GET',`/projects/${pa.id}/runs`,undefined,404);
    const before=requests.length;
    await manager.page.getByRole('link',{name:'Эксперименты',exact:true}).click();
    await eventually(async()=>requests.slice(before).some(r=>r.page===manager.page&&r.path===`/api/v1/projects/${data.personal.id}/runs`),'personal runs reload');
    assert.equal(await manager.page.getByLabel('Текущий проект').inputValue(),data.personal.id);
    await select(manager,'a');await open(manager);pass('real creation, A/B context and legacy private isolation');

    for(const actor of [member,capped]){
      await select(actor,'a');await actor.page.getByRole('heading',{name:'Нет доступных проектов',exact:true}).waitFor();
      assert.equal(await actor.page.getByRole('button',{name:'Создать проект',exact:true}).count(),0);
      await api(actor.token,'GET',path,undefined,404);
    }
    let outsiderToken;
    for(const credentials of [data.actors.outsider,data.admin]){
      const actor=await login(credentials);if(credentials===data.actors.outsider)outsiderToken=actor.token;
      await actor.page.getByRole('link',{name:'Проекты',exact:true}).click();
      const organizations=(await api(actor.token,'GET','/organizations')).items;
      assert.equal(organizations.some(org=>org.id===data.organizations.a.id),false);
      assert.equal(await actor.page.getByLabel('Рабочая область',{exact:true}).locator(`option[value="${data.organizations.a.id}"]`).count(),0);
      await api(actor.token,'GET',path,undefined,404);
      await api(actor.token,'PATCH',path+'/visibility',{visibility:'organization',acl_revision:pa.acl_revision},404);
      await actor.page.close();
    }
    await api(manager.token,'GET',base('b')+'/'+pa.id,undefined,404);
    const denied=await api(outsiderToken,'PUT',path+'/members/'+data.actors.outsider.id,{role:'researcher',status:'active',revision:0},404);
    assert.equal(denied.code,'NOT_FOUND');assert.equal((await api(manager.token,'GET',path)).acl_revision,pa.acl_revision);
    pass('real foreign org denial, platform-admin no bypass and denied writes leave revision unchanged');

    await edit(manager,data.actors.member.name,'researcher','active',true);await save(manager);
    let detail=await api(member.token,'GET',path);
    assert.equal(detail.access_source,'explicit_researcher');assert.equal(detail.capabilities.can_create_experiments,true);
    assert.equal(detail.capabilities.can_start_runs,false);assert.equal(detail.run_blocked_reason,'ORG_AGENTS_NOT_READY');
    await select(member,'a');await open(member);
    assert.equal(await member.page.getByRole('button',{name:'Выдать доступ',exact:true}).count(),0);
    await api(member.token,'GET',path+'/access',undefined,403);
    await api(member.token,'PATCH',path+'/visibility',{visibility:'organization',acl_revision:detail.acl_revision},403);
    pass('grant produces actual researcher capability while shared queue remains unavailable');

    // Another real request changes the grant after the dialog captured its revision.
    await edit(manager,data.actors.member.name,'viewer');
    let row=(await api(manager.token,'GET',path+'/access')).items.find(item=>item.user_id===data.actors.member.id);
    await api(manager.token,'PUT',path+'/members/'+row.user_id,{role:'viewer',status:'active',revision:row.grant.revision});
    const writesBefore=requests.filter(r=>r.page===manager.page&&r.method==='PUT'&&r.path.endsWith('/members/'+row.user_id)).length;
    const conflict=manager.page.waitForResponse(response=>response.request().method()==='PUT'&&response.url().endsWith('/members/'+row.user_id)&&response.status()===409);
    await manager.page.getByRole('button',{name:'Сохранить права',exact:true}).click();await conflict;
    await manager.page.getByRole('dialog').getByRole('alert').waitFor();
    assert.equal(requests.filter(r=>r.page===manager.page&&r.method==='PUT'&&r.path.endsWith('/members/'+row.user_id)).length,writesBefore+1);
    assert.equal((await api(member.token,'GET',path)).capabilities.can_create_experiments,false);
    await manager.page.getByRole('button',{name:'Закрыть и обновить',exact:true}).click();await loaded(manager);
    await edit(manager,data.actors.member.name,'researcher');await save(manager);
    pass('real stale grant CAS returns 409 and requires explicit refresh');

    await manager.page.getByLabel('Область просмотра',{exact:true}).selectOption('organization');
    await manager.page.getByRole('button',{name:'Сохранить видимость',exact:true}).click();await loaded(manager);
    await manager.page.getByRole('region',{name:`Проекты: ${data.organizations.a.name}`,exact:true}).getByText('Просмотр организации',{exact:true}).waitFor();
    assert.equal((await api(manager.token,'GET',path)).visibility,'organization');
    await select(capped,'a');await open(capped);
    assert.equal((await api(capped.token,'GET',path)).access_source,'organization_read');
    await edit(manager,data.actors.capped.name,'researcher','active',true);await save(manager);
    detail=await api(capped.token,'GET',path);assert.equal(detail.capabilities.can_create_experiments,false);
    await refresh(capped);assert.equal(await capped.page.getByRole('button',{name:'Выдать доступ',exact:true}).count(),0);
    await api(capped.token,'PUT',base('a')+'/'+crypto.randomUUID(),{slug:'forbidden',name:'Forbidden'},403);
    await capped.page.close();
    pass('organization visibility and global viewer cap reflect persisted API state');

    const administrator=await login(data.actors.orgadmin);await select(administrator,'a');await open(administrator);
    await administrator.page.getByRole('heading',{name:'Доступ участников',exact:true}).waitFor();
    const membership=(await api(manager.token,'GET',`/organizations/${data.organizations.a.id}/members`)).items.find(item=>item.user_id===data.actors.orgadmin.id);
    await api(manager.token,'PATCH',`/organizations/${data.organizations.a.id}/members/${membership.user_id}`,{role:'researcher',status:'active',revision:membership.revision});
    const rosterBefore=requests.filter(r=>r.page===administrator.page&&r.path.endsWith('/access')).length;
    await refresh(administrator);await administrator.page.getByText(/Ваш доступ: Просмотр\./).waitFor();
    assert.equal(await administrator.page.getByRole('heading',{name:'Доступ участников',exact:true}).count(),0);
    assert.equal(requests.filter(r=>r.page===administrator.page&&r.path.endsWith('/access')).length,rosterBefore);
    assert.equal(await administrator.page.getByRole('alert').count(),0);
    await api(administrator.token,'GET',path+'/access',undefined,403);
    await administrator.page.close();
    pass('fresh API capabilities recover after external organization-role demotion');

    await edit(manager,data.actors.member.name,'researcher','revoked');await save(manager);
    await api(member.token,'GET',path,undefined,404);
    await member.page.getByRole('button',{name:'Обновить доступ',exact:true}).click();
    await member.page.getByRole('alert').filter({hasText:'Проект больше недоступен'}).waitFor();
    assert.equal(await member.page.locator('.project-access').count(),0);
    assert.ok(await member.page.evaluate(()=>sessionStorage.getItem('fl-session'))===member.token,'revoke unexpectedly cleared the session');
    assert.equal((await api(member.token,'GET',base('a'))).items.some(item=>item.id===pa.id),false);
    const revoked=(await api(manager.token,'GET',path+'/access')).items.find(item=>item.user_id===data.actors.member.id);
    assert.equal(revoked.grant.status,'revoked');assert.equal(revoked.capabilities.can_read,false);
    pass('explicit revoke overrides organization visibility without clearing session');

    const second=crypto.randomUUID();await api(manager.token,'PUT',base('a')+'/'+second,{slug:'pagination',name:'Контроль страницы'});
    const first=await api(manager.token,'GET',base('a')+'?limit=1');assert.ok(first.next_cursor);
    const next=await api(manager.token,'GET',base('a')+'?limit=1&cursor='+encodeURIComponent(first.next_cursor));
    assert.equal(next.items.length,1);assert.notEqual(next.items[0].id,first.items[0].id);
    await api(manager.token,'GET',base('b')+'?limit=1&cursor='+encodeURIComponent(first.next_cursor),undefined,400);
    await api(member.token,'GET',base('a')+'?limit=1&cursor='+encodeURIComponent(first.next_cursor),undefined,400);
    pass('real signed pagination is bound to organization and actor');

    await select(manager,'b');await select(manager,'a');await open(manager);
    for(const width of [1440,768,390,320]){
      await manager.page.setViewportSize({width,height:1000});await manager.page.evaluate(()=>scrollTo(0,0));
      assert.equal(await manager.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`overflow at ${width}`);
      if(width===1440||width===390)await manager.page.screenshot({path:`/evidence/organization-projects-real-${width}.png`,fullPage:true});
    }
    await manager.page.emulateMedia({reducedMotion:'reduce'});
    await manager.page.getByRole('link',{name:'Эксперименты',exact:true}).click();
    await manager.page.getByRole('link',{name:'Проекты',exact:true}).focus();await manager.page.keyboard.press('Enter');
    await manager.page.getByRole('heading',{name:'Проекты',exact:true}).waitFor();
    assert.equal(await manager.page.locator('.content').evaluate(element=>element.getAnimations().length),0);
    assert.equal(requests.some(r=>r.method!=='GET'&&/\/(runs|experiments)(\/|$)/.test(r.path)),false,'unexpected training mutation');
    assert.deepEqual(errors,[]);pass('four widths, keyboard/reduced motion and no shared training mutations');
    await manager.page.getByRole('button',{name:'Выйти',exact:true}).click();await manager.page.getByRole('button',{name:'Войти',exact:true}).waitFor();
    await api(manager.token,'GET',path,undefined,401);pass('real logout revokes the accepted bearer session');
    fs.writeFileSync('/evidence/acceptance.json',JSON.stringify({kind:'real-api-browser',checks,route_fulfillment:false,login_count:8,viewports:[1440,768,390,320]},null,2));
  }finally{for(const context of contexts)await context.close();await browser.close();await new Promise(resolve=>server.close(resolve))}
}
main().catch(error=>{console.error(redact(error));process.exitCode=1});
