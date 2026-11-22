// COM-02C3D: real HTTP only. Credentials exist solely in the runner's temporary directory.
const fs=require('node:fs');
const http=require('node:http');
const crypto=require('node:crypto');
const assert=require('node:assert/strict');
const fixturePath='/run/qa/fixture.json';
function fixture(){
  assert.equal(process.env.FL_ORGANIZATION_UI_ISOLATED,'1','isolated runner required');
  assert.equal(process.env.QA_API_HOST,'api');assert.equal(process.env.WEB_HOST,'web');
  const value=JSON.parse(fs.readFileSync(fixturePath,'utf8'));
  assert.equal(value.kind,'disposable-organization-ui');return value;
}
async function api(token,method,path,input,status=200,retryCredential429=false){
  fixture();assert.ok(path.startsWith('/')&&!path.startsWith('//'));
  // Materialize one logical request before any retry; never regenerate its key.
  const request={method,
    headers:{...(token?{Authorization:`Bearer ${token}`} : {}),
      ...(input===undefined?{}:{'Content-Type':'application/json'}),
      ...(method==='POST'?{'Idempotency-Key':crypto.randomUUID()}: {})},
    body:input===undefined?undefined:JSON.stringify(input)};
  const eligible=retryCredential429&&method==='POST'&&path==='/admin/users'&&status===201&&typeof input?.password==='string';
  const deadline=performance.now()+30000;
  for(let attempt=1;attempt<=6;attempt++){
    const remaining=Math.floor(deadline-performance.now());assert.ok(remaining>0,'Credential fixture deadline exhausted');
    const response=await fetch(`http://api:8080/api/v1${path}`,{...request,signal:AbortSignal.timeout(Math.min(15000,remaining))});
    const data=await response.json();
    if(response.status===status)return data; // Expected429 and negative cases remain one-shot.
    const header=response.headers.get('Retry-After');
    const wait=typeof header==='string'&&/^(?:[1-9]|[12][0-9]|30)$/.test(header)?Number(header)*1000:0;
    if(eligible&&response.status===429&&data.code==='RATE_LIMITED'&&wait>0&&attempt<6&&wait<deadline-performance.now()){
      await new Promise(resolve=>setTimeout(resolve,wait));continue;
    }
    assert.equal(response.status,status,`${method} ${path}: HTTP ${response.status}, code ${data.code||'-'}`);
  }
}
async function eventually(check,label,timeout=45000){
  const deadline=Date.now()+timeout;
  while(Date.now()<deadline){if(await check())return;await new Promise(resolve=>setTimeout(resolve,250))}
  throw Error(`Timed out: ${label}`);
}
async function storage(){
  fixture();const host='minio:9000',bucket='organization-ui-check';
  await eventually(async()=>{try{return (await fetch(`http://${host}/minio/health/ready`,{signal:AbortSignal.timeout(2000)})).ok}catch{return false}},'disposable MinIO');
  const user=fs.readFileSync('/run/secrets/minio_user','utf8').trim();
  const password=fs.readFileSync('/run/secrets/minio_password','utf8').trim();
  const date=new Date().toISOString().replace(/[:-]|\.\d{3}/g,''),day=date.slice(0,8);
  const hash=value=>crypto.createHash('sha256').update(value).digest('hex');
  const digest=hash(''),path=`/${bucket}`,scope=`${day}/us-east-1/s3/aws4_request`;
  const signed='host;x-amz-content-sha256;x-amz-date';
  const canonical=['PUT',path,'',`host:${host}\nx-amz-content-sha256:${digest}\nx-amz-date:${date}\n`,signed,digest].join('\n');
  let key=Buffer.from(`AWS4${password}`);
  for(const part of [day,'us-east-1','s3','aws4_request'])key=crypto.createHmac('sha256',key).update(part).digest();
  const signature=crypto.createHmac('sha256',key).update(`AWS4-HMAC-SHA256\n${date}\n${scope}\n${hash(canonical)}`).digest('hex');
  const response=await fetch(`http://${host}${path}`,{method:'PUT',headers:{
    'x-amz-content-sha256':digest,'x-amz-date':date,
    Authorization:`AWS4-HMAC-SHA256 Credential=${user}/${scope}, SignedHeaders=${signed}, Signature=${signature}`},signal:AbortSignal.timeout(15000)});
  assert.equal(response.status,200,'temporary bucket creation');console.log('PASS temporary signed S3 bucket creation');
}
async function setup(){
  const data=fixture();
  await eventually(async()=>{try{return (await fetch('http://api:8080/readyz',{signal:AbortSignal.timeout(2500)})).ok}catch{return false}},'API PostgreSQL/S3 readiness');
  const root=(await api('','POST','/session',{username:'admin',password:data.admin.password})).token;
  data.admin.id=(await api(root,'GET','/me')).id;
  for(const actor of Object.values(data.actors)){
    const user=await api(root,'POST','/admin/users',{username:actor.username,name:actor.name,role:actor.role,password:actor.password},201,true);
    actor.id=user.id;
  }
  const manager=data.actors.manager;
  const token=(await api('','POST','/session',{username:manager.username,password:manager.password})).token;
  for(const org of Object.values(data.organizations))await api(token,'PUT',`/organizations/${org.id}`,{name:org.name});
  const personal={slug:'qa-personal',name:'Личный контроль'};
  const denied=await api(token,'POST','/projects',personal,403);
  assert.equal(denied.code,'LEGACY_LAB_NOT_ADMITTED','new manager must not inherit lab access');
  const admission=await api(root,'GET',`/admin/users/${manager.id}/legacy-lab`);
  assert.equal(admission.status,'not_admitted');assert.equal(admission.revision,0);
  await api(root,'PUT',`/admin/users/${manager.id}/legacy-lab`,{
    status:'active',revision:admission.revision,reason:'Disposable organization UI personal-project fixture'});
  data.personal=await api(token,'POST','/projects',personal,201);
  await api(token,'DELETE','/session');await api(root,'DELETE','/session');
  fs.writeFileSync(fixturePath,JSON.stringify(data),{mode:0o600});
  console.log('PASS real login, users, organizations and private project setup');
}
async function proxy(){
  fixture();
  const server=http.createServer((incoming,outgoing)=>{
    const upstream=http.request({hostname:'web',port:80,path:incoming.url,method:incoming.method,headers:incoming.headers},response=>{
      outgoing.writeHead(response.statusCode,response.headers);response.pipe(outgoing);
    });
    upstream.on('error',()=>{outgoing.writeHead(502);outgoing.end()});incoming.pipe(upstream);
  });
  await new Promise(resolve=>server.listen(3000,'127.0.0.1',resolve));return server;
}
function redact(error){
  let value=String(error);
  try{const data=fixture();for(const actor of [data.admin,...Object.values(data.actors)])value=value.replaceAll(actor.password,'[redacted]')}catch{}
  return value;
}
module.exports={fixture,api,eventually,proxy,redact};
if(require.main===module){
  const mode=process.argv[2];
  (mode==='storage'?storage:mode==='setup'?setup:async()=>{throw Error('Expected storage or setup')})()
    .catch(error=>{console.error(redact(error));process.exitCode=1});
}
