const fs = require('node:fs');
const http = require('node:http');
const crypto = require('node:crypto');
const {chromium} = require('playwright');

async function main() {
  const adminPassword = fs.readFileSync('/run/secrets/admin_password', 'utf8').trim();
  const proxy = http.createServer((incoming, outgoing) => {
    const upstream = http.request({hostname: process.env.WEB_HOST || 'web', port: 80, path: incoming.url,
      method: incoming.method, headers: incoming.headers}, response => {
      outgoing.writeHead(response.statusCode, response.headers); response.pipe(outgoing);
    });
    upstream.on('error', () => {outgoing.writeHead(502); outgoing.end()});
    incoming.pipe(upstream);
  });
  await new Promise(resolve => proxy.listen(3000, '127.0.0.1', resolve));
  const browser = await chromium.launch({headless: true});
  const context = await browser.newContext({viewport: {width: 1440, height: 1000}});
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  async function login(name, password) {
    await page.getByLabel('Логин', {exact: true}).fill(name);
    await page.getByLabel('Пароль', {exact: true}).fill(password);
    await page.getByRole('button', {name: 'Войти'}).click();
    await page.getByRole('navigation', {name: 'Разделы платформы'}).waitFor();
  }
  await page.goto('http://127.0.0.1:3000/#login');
  await login('admin', adminPassword);
  await page.getByRole('link', {name: 'Пользователи', exact: true}).click();
  await page.getByRole('button', {name: 'Пользователь', exact: true}).click();
  const username = 'ui-' + crypto.randomBytes(5).toString('hex');
  const password = crypto.randomBytes(20).toString('hex');
  const newPassword = crypto.randomBytes(20).toString('hex');
  await page.getByLabel('Имя', {exact: true}).fill('Browser acceptance user');
  await page.getByRole('dialog').getByLabel('Логин', {exact: false}).fill(username);
  await page.getByRole('dialog').getByLabel('Пароль', {exact: false}).fill(password);
  await page.getByRole('button', {name: 'Сохранить', exact: true}).click();
  await page.getByRole('dialog').waitFor({state: 'hidden'});
  await page.getByLabel('Поиск пользователя').fill(username);
  await page.getByRole('row').filter({hasText: username}).waitFor();
  await page.screenshot({path: '/evidence/users-desktop.png', fullPage: true});
  await page.getByRole('link', {name: 'Агенты', exact: true}).click();
  await page.getByRole('heading', {name: 'Агенты', exact: true}).waitFor();
  await page.getByText('Раздел 5', {exact: false}).waitFor();
  await page.screenshot({path: '/evidence/agents-desktop.png', fullPage: true});
  await page.setViewportSize({width: 390, height: 844});
  if (!await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)) throw Error('mobile overflow');
  await page.screenshot({path: '/evidence/agents-mobile.png', fullPage: true});
  await page.getByRole('button', {name: 'Выйти', exact: true}).click();
  await login(username, password);
  if (await page.getByRole('link', {name: 'Пользователи', exact: true}).count()) throw Error('admin navigation leaked');
  await page.getByRole('link', {name: 'Мой профиль', exact: true}).click();
  await page.getByLabel('Текущий пароль', {exact: true}).fill(password);
  await page.getByLabel('Новый пароль', {exact: true}).fill(newPassword);
  await page.getByLabel('Повторите новый пароль', {exact: true}).fill(newPassword);
  await page.getByRole('button', {name: 'Сменить пароль', exact: true}).click();
  await page.getByRole('heading', {name: 'Вход в платформу'}).waitFor();
  await login(username, newPassword);
  await page.getByRole('button', {name: 'Выйти', exact: true}).click();
  await login('admin', adminPassword);
  await page.getByRole('link', {name: 'Пользователи', exact: true}).click();
  await page.getByLabel('Поиск пользователя').fill(username);
  await page.getByRole('row').filter({hasText: username}).getByRole('button', {name: 'Изменить'}).click();
  await page.getByLabel('Заблокировать доступ').check();
  await page.getByRole('button', {name: 'Сохранить', exact: true}).click();
  await page.getByRole('dialog').waitFor({state: 'hidden'});
  await page.getByRole('row').filter({hasText: username}).getByText('Отключён', {exact: true}).waitFor();
  await page.getByRole('link', {name: 'Журнал действий', exact: true}).click();
  await page.getByText('Изменён пароль', {exact: true}).first().waitFor();
  if (errors.length) throw Error(errors.join('\n'));
  console.log('PASS: browser user creation, role-scoped navigation, agent status, mobile layout, password change/relogin, disable, audit; test account disabled');
  await browser.close(); proxy.close();
}
main().catch(error => {console.error(error); process.exit(1)});
