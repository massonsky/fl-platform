// COM-01 UI fixtures; no live account, membership mutation or credentials.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http');
const orgID='11111111-1111-4111-8111-111111111111';
const ownerID='22222222-2222-4222-8222-222222222222';
const memberID='33333333-3333-4333-8333-333333333333';
const stamp='2026-11-14T10:00:00Z';

async function fixture(browser,options={}){
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const page=await context.newPage(),errors=[],writes=[];
  const state={off:false,conflict:false,lastOwner:false,...options};
  const organizations=[{id:orgID,name:'Исследовательская группа',kind:'shared',state:options.suspended?'suspended':'active',revision:1,created_at:stamp,role:options.orgRole||'owner',membership_status:'active',can_manage_members:!options.viewer&&!options.suspended}];
  const members={
    [orgID]:[
      {user_id:ownerID,display_name:'Александр',role:'owner',status:'active',revision:1,updated_at:stamp},
      {user_id:memberID,display_name:'Мария',role:'researcher',status:'active',revision:1,updated_at:stamp},
    ],
  };
  page.on('pageerror',error=>errors.push(error.message));
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','organizations-fixture'));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),path=new URL(req.url()).pathname;
    const reply=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
    if(path==='/api/v1/me')return reply({id:ownerID,name:'Александр',role:options.viewer?'viewer':'researcher'});
    if(path==='/api/v1/me/legacy-lab'&&req.method()==='GET')return reply({user_id:ownerID,status:'active',revision:1,source:'admin',updated_at:stamp,can_create_legacy_work:!options.viewer});
    if(path==='/api/v1/projects')return reply({items:[],next_cursor:null});
    if(path==='/api/v1/organizations'&&req.method()==='GET')return state.off?reply({code:'ORGS_NOT_CONFIGURED'},503):reply({items:organizations});
    const match=path.match(/^\/api\/v1\/organizations\/([^/]+)(?:\/members(?:\/([^/]+))?)?$/);
    if(match){
      const [,id,user]=match;
      if(req.method()==='GET')return reply({items:members[id]||[]});
      if(req.method()==='PUT'){
        const input=req.postDataJSON();assert.deepEqual(Object.keys(input),['name']);
        assert.match(id,/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
        const organization={...organizations[0],id,name:input.name};
        organizations.push(organization);members[id]=[members[orgID][0]];writes.push({method:'PUT',input});return reply(organization);
      }
      if(req.method()==='PATCH'){
        const input=req.postDataJSON();writes.push({method:'PATCH',input,user});
        assert.deepEqual(Object.keys(input).sort(),['revision','role','status']);
        if(state.lastOwner)return reply({code:'LAST_OWNER'},409);
        const member=members[id].find(item=>item.user_id===user);
        if(state.conflict){state.conflict=false;member.revision++;member.role='admin';return reply({code:'VERSION_CONFLICT'},409)}
        assert.equal(input.revision,member.revision,'UI overwrote a newer membership without rereading');
        Object.assign(member,input,{revision:member.revision+1});return reply(member);
      }
    }
    errors.push(`Unexpected API request ${req.method()} ${path}`);
    return reply({code:'UNEXPECTED_FIXTURE_REQUEST'},500);
  });
  await page.goto('http://127.0.0.1:3000/#organizations');
  await page.getByRole('heading',{name:'Организации',exact:true}).waitFor();
  return {context,page,state,writes,errors};
}

async function checkMobileOrganizations(page,width){
  const region=page.getByRole('region',{name:'Мои организации',exact:true});
  const bounds=await region.boundingBox();
  assert.ok(bounds,'organization list is missing');
  const rows=region.locator('tbody tr');
  assert.equal(await rows.count(),2);
  for(const row of await rows.all()){
    const fields=[row.getByRole('button'),row.getByRole('cell').nth(1).locator('span'),row.getByRole('cell').nth(2).locator('span')];
    for(const field of fields){
      const content=(await field.textContent()).trim(),box=await field.boundingBox();
      assert.ok(content&&await field.isVisible(),`missing organization field at ${width}`);
      assert.ok(box&&box.width>0&&box.x>=bounds.x&&box.x+box.width<=bounds.x+bounds.width,
        `organization field '${content}' requires horizontal scrolling at ${width}`);
      assert.ok(await field.evaluate(element=>element.scrollWidth<=element.clientWidth),
        `organization field '${content}' is internally clipped at ${width}`);
    }
  }
  assert.ok(await region.locator('.scroll-table').evaluate(element=>element.scrollWidth<=element.clientWidth),
    `organization list has horizontal overflow at ${width}`);
  const team=region.getByRole('button',{name:'Команда моделей',exact:true});
  const research=region.getByRole('button',{name:'Исследовательская группа',exact:true});
  await team.focus();await page.keyboard.press('Enter');
  await page.getByRole('region',{name:'Участники: Команда моделей',exact:true}).waitFor();
  assert.equal(await team.getAttribute('aria-pressed'),'true');
  assert.equal(await research.getAttribute('aria-pressed'),'false');
  assert.ok(await team.evaluate(element=>element.closest('tr').classList.contains('selected')));
  await research.focus();await page.keyboard.press('Space');
  await page.getByRole('button',{name:'Изменить доступ: Мария',exact:true}).waitFor();
  assert.equal(await research.getAttribute('aria-pressed'),'true');
  assert.equal(await team.getAttribute('aria-pressed'),'false');
  assert.ok(await research.evaluate(element=>element===document.activeElement&&getComputedStyle(element).outlineStyle!=='none'),
    `keyboard selection lost visible focus at ${width}`);
}

async function main(){
  const proxy=http.createServer((req,res)=>{
    const upstream=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res)});
    upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  try{
    let f=await fixture(browser,{off:true});
    await f.page.getByRole('heading',{name:'Раздел готовится к запуску'}).waitFor();
    assert.equal(await f.page.getByRole('button',{name:'Создать организацию',exact:true}).count(),0);
    assert.equal(await f.page.getByRole('cell',{name:'Исследовательская группа'}).count(),0);
    assert.deepEqual(f.errors,[]);await f.context.close();

    f=await fixture(browser);
    await f.page.getByRole('button',{name:'Изменить доступ: Мария'}).waitFor();
    await f.page.mouse.click(800,70);
    await f.page.getByRole('link',{name:'Эксперименты',exact:true}).click();
    await f.page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
    const immediate=await f.page.evaluate(()=>{document.querySelector('.platform-nav a[href="#organizations"]').click();return location.hash});
    assert.equal(immediate,'#organizations','organization route waited for marketing exit animation');
    await f.page.getByRole('heading',{name:'Организации',exact:true}).waitFor();
    await f.page.getByRole('button',{name:'Создать организацию',exact:true}).click();
    await f.page.getByLabel('Название организации',{exact:true}).fill('Команда моделей');
    await f.page.getByRole('dialog').getByRole('button',{name:'Создать организацию',exact:true}).click();
    await f.page.getByRole('dialog').waitFor({state:'detached'});
    await f.page.getByRole('region',{name:'Участники: Команда моделей'}).waitFor();
    assert.equal(f.writes[0].input.name,'Команда моделей');
    await f.page.getByRole('button',{name:'Исследовательская группа',exact:true}).click();
    await f.page.getByRole('button',{name:'Изменить доступ: Мария'}).click();
    f.state.conflict=true;
    await f.page.getByLabel('Роль в организации').selectOption('viewer');
    await f.page.getByRole('button',{name:'Сохранить доступ'}).click();
    await f.page.getByRole('dialog').getByRole('alert').filter({hasText:'уже изменились'}).waitFor();
    const beforeRefresh=f.writes.length;
    await f.page.getByRole('button',{name:'Обновить данные участника'}).click();
    await f.page.waitForFunction(()=>document.querySelector('dialog select')?.value==='admin');
    assert.equal(f.writes.length,beforeRefresh,'CAS recovery submitted a write automatically');
    await f.page.getByLabel('Роль в организации').selectOption('viewer');
    const labelEvidence=await f.page.getByRole('dialog').locator('select').evaluateAll(fields=>fields.map(field=>({
      labelText:Array.from(field.labels||[]).map(label=>label.textContent),
      labelledBy:field.getAttribute('aria-labelledby'),
      caption:document.getElementById(field.getAttribute('aria-labelledby')||'')?.textContent,
    })));
    assert.equal(await f.page.getByRole('dialog').getByLabel('Доступ',{exact:true}).count(),1,
      `Access field must have the exact visible caption: ${JSON.stringify(labelEvidence)}`);
    await f.page.getByLabel('Доступ',{exact:true}).selectOption('suspended');
    await f.page.getByRole('button',{name:'Сохранить доступ'}).click();
    await f.page.getByRole('dialog').waitFor({state:'detached'});
    assert.deepEqual(f.writes.at(-1).input,{role:'viewer',status:'suspended',revision:2});
    await f.page.getByRole('row').filter({hasText:'Мария'}).getByText('Приостановлен',{exact:true}).waitFor();
    f.state.lastOwner=true;
    await f.page.getByRole('button',{name:'Изменить доступ: Александр'}).click();
    await f.page.getByLabel('Роль в организации').selectOption('viewer');
    await f.page.getByRole('button',{name:'Сохранить доступ'}).click();
    await f.page.getByRole('alert').filter({hasText:'последнего владельца'}).waitFor();
    await f.page.keyboard.press('Escape');await f.page.getByRole('dialog').waitFor({state:'detached'});
    for(const width of [1440,768,390,320]){
      await f.page.setViewportSize({width,height:1000});
      assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`organization overflow ${width}`);
      if(width<761){
        const writesBeforeSelection=f.writes.length;
        await checkMobileOrganizations(f.page,width);
        assert.equal(f.writes.length,writesBeforeSelection,'keyboard organization selection changed server data');
        const action=await f.page.getByRole('button',{name:'Изменить доступ: Мария'}).boundingBox();
        assert.ok(action&&action.width>0&&action.x>=0&&action.x+action.width<=width,`member action requires horizontal scrolling at ${width}`);
      }
      await f.page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo(0,0)});
      await f.page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
      await f.page.screenshot({path:`/evidence/organizations-${width}.png`,fullPage:true});
    }
    await f.page.emulateMedia({reducedMotion:'reduce'});
    await f.page.getByRole('link',{name:'Эксперименты',exact:true}).click();
    await f.page.getByRole('link',{name:'Организации',exact:true}).focus();await f.page.keyboard.press('Enter');
    await f.page.getByRole('heading',{name:'Организации',exact:true}).waitFor();
    assert.equal(await f.page.locator('.content').evaluate(element=>element.getAnimations().length),0);
    assert.deepEqual(f.errors,[]);await f.context.close();

    for(const options of [{viewer:true},{suspended:true},{orgRole:'admin'}]){
      f=await fixture(browser,options);
      await f.page.getByRole('row').filter({hasText:'Александр'}).waitFor();
      assert.equal(await f.page.getByRole('button',{name:'Изменить доступ: Александр'}).count(),0);
      if(options.orgRole==='admin'){
        await f.page.getByRole('button',{name:'Изменить доступ: Мария'}).click();
        assert.equal(await f.page.getByLabel('Роль в организации').locator('option[value=owner]').count(),0);
      }else assert.equal(await f.page.getByRole('button',{name:'Изменить доступ: Мария'}).count(),0);
      if(options.viewer)assert.equal(await f.page.getByRole('button',{name:'Создать организацию',exact:true}).count(),0);
      assert.equal(f.writes.length,0);assert.deepEqual(f.errors,[]);await f.context.close();
    }
    console.log('PASS organizations: feature off, list/create, member CAS/reload, last owner, viewer/admin/suspended rights, keyboard/reduced motion, complete mobile fields and 4 widths');
  }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(error=>{console.error(error);process.exitCode=1});
