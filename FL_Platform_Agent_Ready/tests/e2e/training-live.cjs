// Preview web against the existing API; read-only run data, no training started.
const {chromium}=require('playwright');
const fs=require('node:fs'),http=require('node:http'),assert=require('node:assert/strict');
async function main(){
 const proxy=http.createServer((req,res)=>{const up=http.request({hostname:'fl-training-web-preview',path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});up.on('error',()=>res.end());req.pipe(up)});
 await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('http://127.0.0.1:3000/#login');await page.getByLabel('Логин',{exact:true}).fill('admin');await page.getByLabel('Пароль',{exact:true}).fill(fs.readFileSync('/run/secrets/admin_password','utf8').trim());await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  const options=await page.getByLabel('Текущий проект').locator('option').evaluateAll(nodes=>nodes.map(n=>n.value));
  let found=false;
  for(const id of options){if(!id)continue;await page.getByLabel('Текущий проект').selectOption(id);try{await page.locator('.run-link').first().waitFor({timeout:5000});found=true;break}catch{}}
  assert.ok(found,'no run available for real API check');
  const panel=page.getByRole('region',{name:'Ход обучения'});await panel.waitFor();
  await page.waitForFunction(()=>{const el=document.querySelector('.training-state');return el&&!el.textContent.includes('Загружаем')});
  assert.equal(await panel.getByText('Обновление данных приостановлено',{exact:true}).count(),0);
  await panel.screenshot({path:'/evidence/training-live.png'});
  await page.getByRole('button',{name:'Выйти',exact:true}).click();await page.getByRole('button',{name:'Войти',exact:true}).waitFor();
  assert.deepEqual(errors,[]);console.log('PASS live preview: real login, project/run read, training state rendered, logout; no run or data changes');
 }finally{await browser.close();proxy.closeAllConnections();await new Promise(resolve=>proxy.close(resolve))}
}
main().catch(e=>{console.error(e);process.exitCode=1});
