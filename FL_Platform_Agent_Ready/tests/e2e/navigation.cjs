const fs=require('node:fs');
const http=require('node:http');
const assert=require('node:assert/strict');
const {chromium}=require('playwright');
async function main(){
  const proxy=http.createServer((req,res)=>{
    const up=http.request({hostname:process.env.WEB_HOST||'web',port:80,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});
    up.on('error',()=>{res.writeHead(502);res.end()});req.pipe(up);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:1440,height:900}});
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('http://127.0.0.1:3000/#login');
  await page.getByLabel('Логин',{exact:true}).fill('admin');
  await page.getByLabel('Пароль',{exact:true}).fill(fs.readFileSync('/run/secrets/admin_password','utf8').trim());
  await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('link',{name:'Агенты',exact:true}).waitFor();
  await page.mouse.click(800,70); // Real pointer input enables motion after keyboard form input.
  for(const route of ['agents','users','audit','account','experiments','agents','users','agents']){
    const immediate=await page.evaluate(target=>{
      document.querySelector(`.platform-nav a[href="#${target}"]`).click();
      return location.hash;
    },route);
    assert.equal(immediate,'#'+route,'workspace navigation is delayed behind an exit animation');
    await page.waitForFunction(target=>document.querySelector(`.platform-nav a[href="#${target}"]`)?.getAttribute('aria-current')==='page',route);
    assert.ok(await page.locator('.content').evaluate(el=>{
      const frames=el.getAnimations().flatMap(a=>a.effect.getKeyframes());
      return frames.every(frame=>(!frame.transform||frame.transform==='none')&&Number(frame.opacity??1)>=.9);
    }),'workspace content slides or fades to blank');
  }
  // Multiple choices in one event turn: only the last destination may survive.
  await page.evaluate(()=>{
    for(const route of ['users','audit','experiments','agents'])document.querySelector(`.platform-nav a[href="#${route}"]`).click();
  });
  await page.waitForTimeout(350);
  assert.ok(page.url().endsWith('#agents'));
  await page.getByRole('heading',{name:'Агенты',exact:true}).waitFor();
  assert.equal(await page.locator('.content').evaluate(el=>getComputedStyle(el).opacity),'1');
  assert.equal(await page.locator('.content').evaluate(el=>getComputedStyle(el).transform),'none');
  assert.equal(await page.locator('.content').evaluate(el=>el.getAnimations().filter(a=>a.playState==='running').length),0);
  // A delayed departure to home must be cancelled by choosing the current workspace tab.
  await page.evaluate(()=>{
    document.querySelector('.sidebar a[href="#home"]').click();
    document.querySelector('.platform-nav a[href="#agents"]').click();
  });
  await page.waitForTimeout(350);assert.ok(page.url().endsWith('#agents'),'stale home departure won');
  await page.goBack();await page.waitForTimeout(250);
  assert.equal(await page.locator('.content').evaluate(el=>getComputedStyle(el).opacity),'1');
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.getByRole('link',{name:'Мой профиль',exact:true}).click();
  await page.getByRole('heading',{name:'Мой профиль',exact:true}).waitFor();
  assert.equal(await page.locator('.content').evaluate(el=>el.getAnimations().length),0);
  await page.emulateMedia({reducedMotion:'no-preference'});
  await page.getByRole('link',{name:'Агенты',exact:true}).focus();await page.keyboard.press('Enter');
  await page.getByRole('heading',{name:'Агенты',exact:true}).waitFor();
  assert.equal(await page.locator('.content').evaluate(el=>el.getAnimations().length),0);
  await page.screenshot({path:'/evidence/workspace-navigation.png'});
  await page.getByRole('button',{name:'Выйти',exact:true}).click();
  await page.getByRole('heading',{name:'Вход в платформу',exact:true}).waitFor();
  assert.deepEqual(errors,[]);await browser.close();proxy.close();
  console.log('PASS: immediate workspace routes, rapid last-choice-wins, active-tab cancellation, no sliding/blank content, history, reduced motion, keyboard, login/logout');
}
main().catch(e=>{console.error(e);process.exit(1)});
