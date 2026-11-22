// Read-only acceptance against the deployed API. No provider keys or paid calls.
const {chromium}=require('playwright');
const fs=require('node:fs');
const http=require('node:http');
const assert=require('node:assert/strict');

async function main(){
 const proxy=http.createServer((req,res)=>{
  const up=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});
  up.on('error',()=>{res.writeHead(502);res.end()});req.pipe(up);
 });
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto('http://127.0.0.1:3000/#login');
  await page.getByLabel('Логин',{exact:true}).fill('admin');
  await page.getByLabel('Пароль',{exact:true}).fill(fs.readFileSync('/run/secrets/admin_password','utf8').trim());
  await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('link',{name:'Провайдеры',exact:true}).click();
  await page.getByRole('heading',{name:'Провайдеры',exact:true}).waitFor();
  const result=await page.evaluate(async()=>{
   const headers={Authorization:`Bearer ${sessionStorage.getItem('fl-session')}`};
   const response=await fetch('/api/v1/ai/providers',{headers});
   const data=await response.json();
   const projects=await fetch('/api/v1/projects',{headers});
   const agents=await fetch('/api/v1/admin/agents',{headers});
   return {status:response.status,allowed:data.allowed_base_urls,count:data.items?.length,
    secretLeak:data.items?.some(p=>'api_key' in p||'encrypted_secret' in p),projects:projects.status,agents:agents.status};
  });
  assert.equal(result.status,200);assert.equal(result.secretLeak,false);
  assert.deepEqual(result.allowed,['https://api.deepseek.com','https://openrouter.ai/api/v1']);
  assert.equal(result.projects,200);assert.equal(result.agents,200);
  await page.getByRole('button',{name:'Подключить провайдера',exact:true}).click();
  assert.equal(await page.locator('dialog select option').count(),2);
  assert.equal(await page.getByLabel(/^API-ключ/).inputValue(),'');
  await page.keyboard.press('Escape');await page.locator('dialog').waitFor({state:'detached'});
  await page.screenshot({path:'/evidence/providers-live.png',fullPage:true});
  await page.getByRole('link',{name:'Эксперименты',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('button',{name:'Выйти',exact:true}).click();
  await page.getByRole('button',{name:'Войти',exact:true}).waitFor();
  assert.deepEqual(errors,[]);
  console.log('PASS live: login, providers API, approved addresses, no secret fields, form, projects, agents, route return, logout');
 }finally{await browser.close();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
