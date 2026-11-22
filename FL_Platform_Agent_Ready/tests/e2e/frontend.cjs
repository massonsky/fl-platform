const fs = require('node:fs');
const http = require('node:http');
const assert = require('node:assert/strict');
const {chromium} = require('playwright');

async function main() {
  const proxy = http.createServer((req, res) => {
    const upstream = http.request({hostname: process.env.WEB_HOST || 'web', port:80,
      path:req.url, method:req.method, headers:req.headers}, response => {
      res.writeHead(response.statusCode, response.headers); response.pipe(res);
    });
    upstream.on('error',()=>{res.writeHead(502);res.end()});req.pipe(upstream);
  });
  await new Promise(resolve=>proxy.listen(3000,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('http://127.0.0.1:3000/',{waitUntil:'networkidle'});
  await page.getByRole('heading',{name:'Федеративное обучение.'}).waitFor();
  assert.equal(await page.locator('.city-label').count(),5);
  assert.equal(await page.locator('.scene-control').count(),0);
  await page.getByRole('tab').first().focus();await page.keyboard.press('ArrowRight');
  assert.equal(await page.getByRole('tab').nth(1).getAttribute('aria-selected'),'true');
  await page.emulateMedia({reducedMotion:'reduce'});
  assert.equal(await page.locator('.network-motion').evaluate(el=>getComputedStyle(el).display),'none');
  for(const width of [1440,1024,768,390,320]){
    await page.setViewportSize({width,height:width<500?844:1000});
    await page.evaluate(()=>scrollTo(0,0));
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`landing overflow ${width}`);
    await page.screenshot({path:`/evidence/landing-${width}.png`,fullPage:true});
  }
  await page.getByRole('link',{name:'Войти в платформу',exact:true}).first().click();
  await page.getByLabel('Логин',{exact:true}).fill('invalid-user');
  await page.getByLabel('Пароль',{exact:true}).fill('invalid-password');
  await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('alert').waitFor();
  await page.screenshot({path:'/evidence/login-error-mobile.png',fullPage:true});
  await page.getByLabel('Логин',{exact:true}).fill('admin');
  await page.getByLabel('Пароль',{exact:true}).fill(fs.readFileSync('/run/secrets/admin_password','utf8').trim());
  await page.getByRole('button',{name:'Войти',exact:true}).click();
  await page.getByRole('heading',{name:'Эксперименты',exact:true}).waitFor();
  await page.getByLabel('Текущий проект').selectOption(JSON.parse(fs.readFileSync('/evidence/runs.json','utf8')).project_id);
  await page.locator('.chart svg').waitFor();
  await page.waitForTimeout(500);
  for(const width of [1440,1024,768,390,320]){
    await page.setViewportSize({width,height:width<500?844:1000});
    await page.waitForTimeout(100);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`workspace overflow ${width}`);
    await page.screenshot({path:`/evidence/workspace-${width}.png`,fullPage:true});
  }
  const roundsPanel=page.locator('.detail-grid .panel').first();
  assert.ok(await roundsPanel.locator('tbody tr').count()<=6,'round list exceeds page size');
  await roundsPanel.getByRole('button',{name:'Следующая страница'}).click();
  assert.equal(await roundsPanel.locator('tbody tr').first().locator('td').first().innerText(),'7');
  await roundsPanel.getByRole('button',{name:'Предыдущая страница'}).click();
  assert.equal(await roundsPanel.locator('tbody tr').first().locator('td').first().innerText(),'1');
  const trigger=page.getByRole('button',{name:'Создать проект',exact:true});
  await trigger.click();
  const dialog=page.getByRole('dialog');await dialog.waitFor();
  for(let i=0;i<8;i++)await page.keyboard.press('Tab');
  assert.ok(await dialog.evaluate(el=>el.contains(document.activeElement)),'dialog focus escaped');
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  assert.ok(await trigger.evaluate(el=>el===document.activeElement),'focus not restored');
  await page.setViewportSize({width:1440,height:900});
  for(const route of ['agents','users','audit','account']){
    await page.goto('http://127.0.0.1:3000/#'+route);await page.waitForTimeout(600);
    await page.screenshot({path:`/evidence/${route}-desktop.png`,fullPage:true});
    if(route==='users'||route==='audit'){
      assert.ok(await page.locator('.scroll-table').evaluate(el=>el.clientHeight>=el.scrollHeight),'table rows clipped');
      const next=page.getByRole('button',{name:'Следующая страница'});
      if(await next.count()){
        const first=await page.locator('tbody tr').first().innerText();await next.click();
        assert.notEqual(await page.locator('tbody tr').first().innerText(),first,'pagination did not change rows');
        await page.getByRole('button',{name:'Предыдущая страница'}).click();
        assert.equal(await page.locator('tbody tr').first().innerText(),first,'pagination did not restore rows');
      }
      if(route==='users'){
        await page.getByPlaceholder('Имя или логин').fill('no-match-for-design-test');
        await page.getByText('Пользователи не найдены.',{exact:true}).waitFor();
        await page.getByPlaceholder('Имя или логин').fill('');
      }
    }
  }
  await page.setViewportSize({width:320,height:844});
  for(const route of ['agents','users','audit','account']){
    await page.goto('http://127.0.0.1:3000/#'+route);await page.waitForTimeout(600);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${route} overflow at 320`);
    await page.screenshot({path:`/evidence/${route}-320.png`,fullPage:true});
  }
  await page.getByRole('button',{name:'Выйти',exact:true}).click();
  await page.getByRole('heading',{name:'Вход в платформу'}).waitFor();
  await page.setViewportSize({width:1440,height:1000});
  await page.screenshot({path:'/evidence/login-desktop.png',fullPage:true});
  assert.deepEqual(errors,[]);
  await browser.close();proxy.close();
  console.log('PASS: landing/routes, city labels, keyboard tabs, reduced motion, invalid login, real login/logout, modal focus/Escape/restore, 5 widths, all workspace routes at 320, pagination forward/back, empty search, unclipped tables, no browser errors');
}
main().catch(e=>{console.error(e.message);process.exit(1)});
