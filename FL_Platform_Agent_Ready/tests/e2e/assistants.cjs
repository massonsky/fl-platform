// UI contract fixture only. No external model calls or real keys.
const {chromium}=require('playwright');
const http=require('node:http'),assert=require('node:assert/strict');
async function main(){
 const proxy=http.createServer((req,res)=>{const up=http.request({hostname:'fl-assistants-web-preview',path:req.url,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>res.end());req.pipe(up)});
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));await page.addInitScript(()=>sessionStorage.setItem('fl-session','fixture'));
  let profiles=[],conversations=[],conflict=false;
  const provider={id:'provider-fixture',name:'Личный провайдер',enabled:true,base_url:'https://models.example/v1'};
  await page.route('**/api/v1/**',route=>{
   const req=route.request(),path=new URL(req.url()).pathname;
   const reply=(data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
   if(path.endsWith('/me'))return reply({id:'user',name:'Исследователь',role:'researcher'});
   if(path.endsWith('/projects'))return reply({items:[],next_cursor:null});
   if(path.endsWith('/providers'))return reply({items:[provider]});
   if(path.endsWith('/models'))return reply({items:[{id:'fixture-model'}]});
   if(path.endsWith('/assistants'))return reply({items:profiles});
   if(req.method()==='PUT'){
    if(conflict)return reply({code:'VERSION_CONFLICT'},409);
    const input=req.postDataJSON();assert.equal(input.revision,profiles[0]?.revision||0);
    profiles=[{...input,id:path.split('/').pop(),revision:input.revision+1,updated_at:new Date().toISOString()}];return reply(profiles[0],input.revision?200:201);
   }
   if(path.endsWith('/conversations')){
    if(req.method()==='GET')return reply({items:conversations});
    const input=req.postDataJSON(),p=profiles[0];assert.equal(input.assistant_id,p.id);assert.equal(input.assistant_revision,p.revision);assert.equal(input.consent,true);assert.equal(input.model,p.model);
    conversations=[{id:'conversation',title:'Диалог с ассистентом',provider_id:provider.id,provider_name:provider.name,base_url:provider.base_url,model:p.model,assistant_id:p.id,assistant_revision:p.revision,assistant_name:p.name,system_instruction:p.instruction,created_at:new Date().toISOString()}];return reply(conversations[0],201);
   }
   return reply({conversation:conversations[0],items:[]});
  });
  await page.goto('http://127.0.0.1:3000/#assistants');await page.getByRole('button',{name:'Новый ассистент',exact:true}).click();
  await page.getByLabel('Название',{exact:true}).fill('Редактор текста');await page.getByRole('textbox',{name:/^Инструкция ассистенту/}).fill('Проверяй ясность текста. Объясняй исправления.');
  await page.getByRole('button',{name:'Сохранить ассистента'}).click();await page.locator('dialog').waitFor({state:'detached'});
  await page.getByText('Инструкция',{exact:true}).click();await page.getByText('Проверяй ясность текста. Объясняй исправления.',{exact:true}).waitFor();
  for(const width of [1440,768,390,320]){await page.setViewportSize({width,height:1000});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);await page.screenshot({path:`/evidence/assistants-${width}.png`,fullPage:true})}
  await page.getByRole('link',{name:'Чат',exact:true}).click();await page.getByRole('button',{name:'Новый диалог',exact:true}).click();
  await page.getByLabel('Ассистент',{exact:true}).selectOption(profiles[0].id);assert.equal(await page.getByLabel('Модель',{exact:true}).isDisabled(),true);
  assert.equal(await page.getByRole('button',{name:'Создать диалог',exact:true}).isDisabled(),true);await page.getByRole('checkbox').check();await page.getByRole('button',{name:'Создать диалог',exact:true}).click();
  await page.locator('.assistant-snapshot summary').click();await page.locator('.assistant-snapshot').getByText('Проверяй ясность текста. Объясняй исправления.',{exact:true}).waitFor();
  await page.getByRole('link',{name:'Ассистенты',exact:true}).click();await page.getByRole('button',{name:'Настроить Редактор текста'}).click();
  await page.getByRole('textbox',{name:/^Инструкция ассистенту/}).fill('Новая версия инструкции');conflict=true;await page.getByRole('button',{name:'Сохранить ассистента'}).click();await page.getByRole('alert').filter({hasText:'Настройки уже изменились'}).waitFor();
  conflict=false;await page.getByRole('checkbox').uncheck();await page.getByRole('button',{name:'Сохранить ассистента'}).click();await page.locator('dialog').waitFor({state:'detached'});
  await page.getByRole('link',{name:'Чат',exact:true}).click();await page.locator('.assistant-snapshot summary').click();await page.locator('.assistant-snapshot').getByText('Проверяй ясность текста. Объясняй исправления.',{exact:true}).waitFor();
  await page.getByRole('button',{name:'Новый диалог',exact:true}).click();assert.equal(await page.getByLabel('Ассистент',{exact:true}).locator('option').count(),1);await page.keyboard.press('Escape');
  assert.deepEqual(errors,[]);console.log('PASS assistants UI: create/catalog, consent and profile selection, immutable snapshot fixture, edit/conflict/disable, keyboard, four widths');
 }finally{await browser.close();proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
