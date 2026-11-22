// Real isolated HTTP/UI only. No response mocks, synthetic tokens, run seeds or ML workers.
const fs=require('node:fs'),assert=require('node:assert/strict'),{chromium}=require('playwright');
const {fixture,api,eventually,proxy,redact}=require('./organization-projects-real-support.cjs');
async function main(){
  const data=fixture(),server=await proxy(),browser=await chromium.launch({headless:true}),contexts=[],errors=[],writes=[];
  async function login(actor){
    const context=await browser.newContext({viewport:{width:1440,height:1000}});contexts.push(context);const page=await context.newPage();page.setDefaultTimeout(15000);
    const capabilityRequests=[],capabilityResponses=[];
    await page.addInitScript(()=>{
      window.__legacyFocusTrace=[];
      const capture=event=>{window.__legacyFocusTrace.push({event:event.type,visibility:document.visibilityState,focused:document.hasFocus()});window.__legacyFocusTrace=window.__legacyFocusTrace.slice(-16)};
      for(const name of ['focus','blur'])window.addEventListener(name,capture);
      document.addEventListener('visibilitychange',capture);
    });
    page.on('request',request=>{if(request.method()==='GET'&&new URL(request.url()).pathname==='/api/v1/me/legacy-lab')capabilityRequests.push(Date.now())});
    page.on('response',async response=>{
      if(response.request().method()!=='GET'||new URL(response.url()).pathname!=='/api/v1/me/legacy-lab')return;
      try{const value=await response.json();capabilityResponses.push({http:response.status(),status:value.status,revision:value.revision,allowed:value.can_create_legacy_work,userMatches:value.user_id===actor.id})}catch{capabilityResponses.push({http:response.status(),json:false})}
    });
    page.on('pageerror',error=>errors.push(error.message));page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/v1/projects')writes.push(page)});
    await page.goto('http://127.0.0.1:3000/#login');await page.getByLabel('Логин',{exact:true}).fill(actor.username);await page.getByLabel('Пароль',{exact:true}).fill(actor.password);
    await page.getByRole('button',{name:'Войти',exact:true}).click();await page.getByRole('navigation',{name:'Разделы платформы'}).waitFor();
    const token=await page.evaluate(()=>sessionStorage.getItem('fl-session'));assert.ok(token);return {page,token,capabilityRequests,capabilityResponses};
  }
  async function change(admin,status){
    await admin.page.bringToFront();await admin.page.getByRole('link',{name:'Пользователи',exact:true}).click();
    await admin.page.getByRole('button',{name:`Допуск к лаборатории: ${data.actors.member.name}`,exact:true}).click();const dialog=admin.page.getByRole('dialog');
    await dialog.getByLabel('Причина изменения',{exact:true}).fill(status==='active'?'Проверка допуска в изолированной приёмке':'Отзыв новых вычислений в изолированной приёмке');
    await dialog.getByRole('button',{name:status==='active'?'Выдать допуск':'Запретить новые запуски',exact:true}).click();
    await dialog.getByText(status==='active'?'Выдан':'Отозван',{exact:true}).waitFor();await admin.page.keyboard.press('Escape');
  }
  try{
    const member=await login(data.actors.member),admin=await login(data.admin),page=member.page;
    let own=await api(member.token,'GET','/me/legacy-lab');assert.equal(own.status,'not_admitted');assert.equal(own.can_create_legacy_work,false);
    await page.bringToFront();await page.locator('aside[aria-label="Допуск к лаборатории"]').getByRole('button',{name:'Проверить допуск'}).waitFor();
    assert.equal(await page.getByRole('button',{name:'Новый эксперимент',exact:true}).isDisabled(),true);
    assert.equal(await page.getByRole('button',{name:'Создать проект',exact:true}).isDisabled(),true);assert.equal(writes.length,0);
    console.log('PASS real new researcher starts without legacy work capability');

    await change(admin,'active');own=await api(member.token,'GET','/me/legacy-lab');assert.equal(own.revision,1);assert.equal(own.can_create_legacy_work,true);
    const beforeFocus=member.capabilityRequests.length;await page.bringToFront();
    // Headless contexts can all report focused/visible without firing focus.
    // Use the real user-facing recovery action; controlled fixtures test focus.
    await page.locator('aside[aria-label="Допуск к лаборатории"]').getByRole('button',{name:'Проверить допуск',exact:true}).click();
    try{await eventually(async()=>!await page.getByRole('button',{name:'Создать проект',exact:true}).isDisabled(),'grant refreshed by explicit user action')}
    catch(error){
      const focus=await page.evaluate(()=>({visibility:document.visibilityState,focused:document.hasFocus(),events:window.__legacyFocusTrace}));
      console.log('DIAGNOSTIC grant-refresh '+JSON.stringify({directRead:{revision:own.revision,allowed:own.can_create_legacy_work},beforeRefresh:beforeFocus,afterRefresh:member.capabilityRequests.length,responses:member.capabilityResponses.slice(-12),focus}));throw error;
    }
    await page.getByRole('button',{name:'Создать проект',exact:true}).click();let dialog=page.getByRole('dialog');
    await dialog.getByLabel('Название',{exact:true}).fill('Проверенное личное исследование');await dialog.getByLabel('Короткое имя',{exact:true}).fill('admitted-personal');
    await dialog.getByRole('button',{name:'Создать проект',exact:true}).click();await dialog.waitFor({state:'detached'});
    const projects=(await api(member.token,'GET','/projects')).items;assert.equal(projects.length,1);const project=projects[0];assert.equal(project.slug,'admitted-personal');assert.equal(writes.length,1);
    await eventually(async()=>await page.getByLabel('Текущий проект').inputValue()===project.id,'new personal project selected');
    assert.ok(member.capabilityRequests.length>beforeFocus,'explicit refresh did not read effective capability');
    console.log('PASS real admin editor grant and explicit capability refresh permit one personal project metadata creation');

    await eventually(async()=>!await page.getByRole('button',{name:'Создать проект',exact:true}).isDisabled(),'capability after creation');
    await page.getByRole('button',{name:'Создать проект',exact:true}).click();dialog=page.getByRole('dialog');
    await dialog.getByLabel('Название',{exact:true}).fill('Не должно быть создано');await dialog.getByLabel('Короткое имя',{exact:true}).fill('revoked-personal');
    await change(admin,'revoked');await page.bringToFront();
    const beforeSubmit=member.capabilityRequests.length;
    // Do not trust the still-allowed snapshot: submit must make a new GET and
    // stop before POST when another administrator has revoked admission.
    assert.equal(await dialog.getByRole('button',{name:'Создать проект',exact:true}).isDisabled(),false,'headless fixture must exercise stale allowed preflight');
    const preflight=page.waitForResponse(response=>response.request().method()==='GET'&&new URL(response.url()).pathname==='/api/v1/me/legacy-lab');
    await dialog.getByRole('button',{name:'Создать проект',exact:true}).click();
    const checked=await (await preflight).json();assert.equal(checked.status,'revoked');assert.equal(checked.can_create_legacy_work,false);
    await dialog.locator('aside[aria-label="Допуск к лаборатории"]').getByRole('button',{name:'Проверить допуск',exact:true}).waitFor();
    assert.equal(await dialog.getByRole('button',{name:'Создать проект',exact:true}).isDisabled(),true);assert.ok(member.capabilityRequests.length>beforeSubmit);
    assert.equal(writes.length,1);own=await api(member.token,'GET','/me/legacy-lab');assert.equal(own.status,'revoked');assert.equal(own.revision,2);assert.equal(own.can_create_legacy_work,false);
    const blocked=await api(member.token,'POST','/projects',{name:'Direct denied check',slug:'direct-denied'},403);assert.equal(blocked.code,'LEGACY_LAB_NOT_ADMITTED');
    await page.screenshot({path:'/evidence/legacy-lab-real-revoked-dialog.png',fullPage:true});await page.keyboard.press('Escape');
    await page.getByRole('link',{name:'Проекты',exact:true}).click();await page.getByRole('button',{name:`Открыть эксперименты: ${project.name}`,exact:true}).waitFor();
    assert.equal(await page.getByRole('button',{name:'Создать проект',exact:true}).count(),0);
    assert.deepEqual((await api(member.token,'GET','/projects')).items.map(item=>item.id),[project.id]);
    await page.getByRole('button',{name:`Открыть эксперименты: ${project.name}`,exact:true}).click();await page.getByRole('heading',{name:'Запусков пока нет',exact:true}).waitFor();
    assert.deepEqual((await api(member.token,'GET',`/projects/${project.id}/runs`)).items,[]);assert.equal(writes.length,1);
    for(const width of [1440,390]){await page.setViewportSize({width,height:1000});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);await page.screenshot({path:`/evidence/legacy-lab-real-preserved-${width}.png`,fullPage:true})}
    assert.deepEqual(errors,[]);console.log('PASS real revoke is enforced by submit preflight without POST; personal project metadata and empty archive view preserved');
    fs.writeFileSync('/evidence/legacy-lab-real-results.json',JSON.stringify({checks:4,new_user_default_deny:true,admin_grant_revoke:true,explicit_capability_refresh:true,revoked_submit_preflight:true,created_projects:1,metadata_preserved:true,response_mocking:false,run_or_metric_seeds:0,ml_worker:false,queue_protocol:'Separate D1B SQL and controlled UI evidence'},null,2));
  }finally{for(const context of contexts)await context.close();await browser.close();await new Promise(resolve=>server.close(resolve))}
}
main().catch(error=>{console.error(redact(error));process.exit(1)});
