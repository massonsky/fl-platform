// Read-only deployed check; no provider keys, paid calls or chat records created.
const {chromium}=require('playwright');
const fs=require('node:fs'),http=require('node:http'),assert=require('node:assert/strict');
async function main(){
 const proxy=http.createServer((req,res)=>{const up=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>{res.writeHead(502);res.end()});req.pipe(up)});
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('http://127.0.0.1:3000/#login');
  await page.getByLabel('Логин',{exact:true}).fill('admin');
  await page.getByLabel('Пароль',{exact:true}).fill(fs.readFileSync('/run/secrets/admin_password','utf8').trim());
  await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('link',{name:'Ассистенты',exact:true}).click();await page.getByRole('heading',{name:'Ассистенты',exact:true}).waitFor();
  const statuses=await page.evaluate(async()=>{const headers={Authorization:`Bearer ${sessionStorage.getItem('fl-session')}`};return Promise.all(['/ai/assistants','/ai/conversations','/ai/providers','/projects','/admin/agents'].map(async path=>(await fetch(`/api/v1${path}`,{headers})).status))});
  assert.deepEqual(statuses,[200,200,200,200,200]);
  await page.screenshot({path:'/evidence/assistants-live.png',fullPage:true});
  await page.getByRole('link',{name:'Эксперименты',exact:true}).click();await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByRole('button',{name:'Выйти',exact:true}).click();await page.getByRole('button',{name:'Войти',exact:true}).waitFor();
  assert.deepEqual(errors,[]);console.log('PASS live assistants: login, assistant route and API, providers, projects, agents, logout; no paid calls');
 }finally{await browser.close();proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
