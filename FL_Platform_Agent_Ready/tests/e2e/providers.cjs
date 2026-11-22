// UI contract fixtures only; PostgreSQL/ACL coverage is in Go integration tests.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http');
async function main(){
 const proxy=http.createServer((req,res)=>{const up=http.request({hostname:process.env.WEB_HOST||'fl-ai-web-preview',port:80,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>{res.writeHead(502);res.end()});req.pipe(up)});
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>sessionStorage.setItem('fl-session','ui-contract-fixture'));
  let providers=[],badKey=false;
  await page.route('**/api/v1/**',async route=>{
   const req=route.request(),path=new URL(req.url()).pathname;
   const reply=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
   if(path.endsWith('/me'))return reply({id:'fixture-user',name:'Оператор',role:'researcher'});
   if(path==='/api/v1/me/legacy-lab'&&req.method()==='GET')return reply({user_id:'fixture-user',status:'active',revision:1,source:'admin',updated_at:'2026-11-14T10:00:00Z',can_create_legacy_work:true});
   if(path.endsWith('/projects'))return reply({items:[],next_cursor:null});
   if(path.endsWith('/ai/providers'))return reply({items:providers,allowed_base_urls:['https://models.example/v1']});
   if(req.method()==='PUT'){
    const input=req.postDataJSON(),id=path.split('/').pop();
    assert.equal(input.api_key,'fixture-provider-key');
    providers=[{id,name:input.name,base_url:input.base_url,enabled:input.enabled,revision:1,has_key:true,updated_at:new Date().toISOString()}];return reply(providers[0],201);
   }
   if(path.endsWith('/models'))return badKey?reply({code:'PROVIDER_AUTH_FAILED'},502):reply({items:Array.from({length:11},(_,i)=>({id:`model-${String(i).padStart(2,'0')}`}))});
   throw new Error(`Unexpected API request ${req.method()} ${path}`);
  });
  await page.goto('http://127.0.0.1:3000/#providers');
  await page.getByRole('heading',{name:'Провайдеры',exact:true}).waitFor();
  await page.getByRole('button',{name:'Подключить провайдера',exact:true}).click();
  await page.getByLabel('Название',{exact:true}).fill('Мой сервер моделей');
  await page.getByLabel(/^API-ключ/).fill('fixture-provider-key');
  await page.getByRole('button',{name:'Сохранить подключение'}).click();
  await page.locator('dialog').waitFor({state:'detached'});
  assert.equal(await page.locator('input[type=password]').count(),0);
  await page.getByRole('button',{name:'Проверить и показать модели'}).click();
  await page.getByRole('heading',{name:'Модели · Мой сервер моделей'}).waitFor();
  const catalog=page.getByRole('region',{name:'Каталог моделей'});
  assert.equal(await catalog.locator('tbody tr').count(),8);
  await catalog.getByRole('button',{name:'Следующая страница'}).click();
  assert.equal(await catalog.locator('tbody tr').count(),3);
  await page.getByLabel('Найти модель').fill('missing');await page.getByText('По вашему запросу моделей не найдено.').waitFor();
  await page.getByLabel('Найти модель').fill('');
  for(const width of [1440,768,390,320]){
   await page.setViewportSize({width,height:1000});
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);
   if(width<761){const box=await page.getByRole('button',{name:'Проверить и показать модели'}).boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width,`provider actions clipped ${width}`)}
   await page.screenshot({path:`/evidence/providers-${width}.png`,fullPage:true});
  }
  await page.getByRole('button',{name:'Настроить Мой сервер моделей'}).click();
  assert.equal(await page.getByLabel(/^Новый API-ключ/).inputValue(),'');
  await page.keyboard.press('Escape');await page.locator('dialog').waitFor({state:'detached'});
  badKey=true;await page.getByRole('button',{name:'Проверить и показать модели'}).click();
  await page.getByRole('alert').filter({hasText:'Провайдер отклонил'}).waitFor();
  assert.ok(await page.evaluate(()=>!!sessionStorage.getItem('fl-session')),'upstream 401 logged out user');
  await page.getByRole('link',{name:'Эксперименты',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('link',{name:'Провайдеры',exact:true}).click();
  await page.getByRole('heading',{name:'Провайдеры',exact:true}).waitFor();
  assert.deepEqual(errors,[]);console.log('PASS providers UI: CRUD fixture, catalog/search/pagination, errors, key clearing, keyboard, navigation, 4 widths');
 }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
