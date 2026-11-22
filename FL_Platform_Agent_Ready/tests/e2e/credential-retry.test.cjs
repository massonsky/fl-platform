// Exercise the actual provisioning helper with fake HTTP/time; no network or secrets.
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),crypto=require('node:crypto');
const {api}=require('./organization-projects-real-support.cjs');

function harness(t,steps,{oversleep=0}={}){
  const previous={};
  for(const [name,value] of Object.entries({FL_ORGANIZATION_UI_ISOLATED:'1',QA_API_HOST:'api',WEB_HOST:'web'})){
    previous[name]=process.env[name];process.env[name]=value;
  }
  t.after(()=>{for(const [name,value] of Object.entries(previous))if(value===undefined)delete process.env[name];else process.env[name]=value});
  const originalRead=fs.readFileSync;
  t.mock.method(fs,'readFileSync',(path,...args)=>path==='/run/qa/fixture.json'?JSON.stringify({kind:'disposable-organization-ui'}):originalRead(path,...args));
  let clock=0,keys=0;const calls=[],sleeps=[];
  t.mock.method(performance,'now',()=>clock);
  t.mock.method(crypto,'randomUUID',()=>`11111111-1111-4111-8111-${String(++keys).padStart(12,'0')}`);
  t.mock.method(globalThis,'setTimeout',(callback,delay)=>{sleeps.push(delay);clock+=delay+oversleep;queueMicrotask(callback);return 1});
  t.mock.method(globalThis,'fetch',async(url,request)=>{
    calls.push({url,method:request.method,headers:{...request.headers},body:request.body});
    const step=steps[calls.length-1];assert.ok(step,'unexpected extra request');clock+=step.elapsed||0;
    step.effect?.();if(step.error)throw step.error;
    return {status:step.status,headers:{get:name=>name==='Retry-After'?step.header:null},json:async()=>structuredClone(step.body||{code:'RATE_LIMITED'})};
  });
  const body={username:'qa-fixture',name:'Original',role:'researcher',password:'fixture-only-password'};
  return {calls,sleeps,body,keys:()=>keys,call:(expected=201,retry=true,path='/admin/users')=>api('fixture-bearer','POST',path,body,expected,retry)};
}

test('429 respects exact header delays and preserves one key, body and actor',async t=>{
  const steps=[{status:429,header:'2'},{status:429,header:'1'},{status:201,body:{id:'created'}}],f=harness(t,steps);
  steps[0].effect=()=>{f.body.name='Changed after first send'};
  assert.deepEqual(await f.call(),{id:'created'});assert.deepEqual(f.sleeps,[2000,1000]);assert.equal(f.calls.length,3);assert.equal(f.keys(),1);
  for(const call of f.calls){
    assert.equal(call.url,'http://api:8080/api/v1/admin/users');assert.equal(call.method,'POST');
    assert.deepEqual(call.headers,f.calls[0].headers);assert.equal(call.headers.Authorization,'Bearer fixture-bearer');
    assert.equal(call.body,f.calls[0].body);assert.equal(JSON.parse(call.body).name,'Original');
  }
});

test('six attempts is a hard cap with no seventh request or extra sleep',async t=>{
  const f=harness(t,Array.from({length:6},()=>({status:429,header:'1'})));
  await assert.rejects(f.call(),{name:'AssertionError'});assert.equal(f.calls.length,6);assert.equal(f.sleeps.length,5);assert.equal(f.keys(),1);
});

test('delay cannot consume the remaining deadline',async t=>{
  const f=harness(t,[{status:429,header:'20',elapsed:2000},{status:429,header:'9'}]);
  await assert.rejects(f.call(),{name:'AssertionError'});assert.equal(f.calls.length,2);assert.deepEqual(f.sleeps,[20000]);
});

test('overslept deadline stops before another HTTP request',async t=>{
  const f=harness(t,[{status:429,header:'1'}],{oversleep:30000});
  await assert.rejects(f.call(),/deadline exhausted/);assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[1000]);
});

test('missing or non-canonical Retry-After fails without waiting',async t=>{
  for(const header of [undefined,null,'','0','31','-1','+1','01','1.0','1 ',' 1','١','Wed, 21 Oct 2026 07:28:00 GMT']){
    await t.test(String(header),async child=>{const f=harness(child,[{status:429,header}]);await assert.rejects(f.call(),{name:'AssertionError'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[])});
  }
});

test('valid thirty-second Retry-After does not fit a thirty-second total budget',async t=>{
  const f=harness(t,[{status:429,header:'30'}]);await assert.rejects(f.call(),{name:'AssertionError'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[]);
});

test('503, credential denials and non-rate429 are never retried',async t=>{
  for(const [status,code] of [[503,'DEPENDENCY_UNAVAILABLE'],[401,'UNAUTHORIZED'],[403,'FORBIDDEN'],[409,'VERSION_CONFLICT'],[429,'OTHER_ERROR']]){
    await t.test(`${status}/${code}`,async child=>{
      const f=harness(child,[{status,header:'1',body:{code}}]);await assert.rejects(f.call(),{name:'AssertionError'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[]);
    });
  }
});

test('network failure is propagated once',async t=>{
  const failure=new Error('controlled network failure'),f=harness(t,[{error:failure}]);
  await assert.rejects(f.call(),error=>error===failure);assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[]);
});

test('explicit expected429 returns its envelope once even with opt-in',async t=>{
  const f=harness(t,[{status:429,header:'1',body:{code:'RATE_LIMITED'}}]);
  assert.deepEqual(await f.call(429,true),{code:'RATE_LIMITED'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[]);
});

test('default-off and unrelated routes cannot gain retries',async t=>{
  for(const [name,retry,path] of [['default-off',false,'/admin/users'],['noncredential',true,'/projects']]){
    await t.test(name,async child=>{const f=harness(child,[{status:429,header:'1'}]);await assert.rejects(f.call(201,retry,path),{name:'AssertionError'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[])});
  }
});

test('ordinary expected denials retain their existing one-shot meaning',async t=>{
  const f=harness(t,[{status:403,body:{code:'FORBIDDEN'}}]);assert.deepEqual(await f.call(403,true),{code:'FORBIDDEN'});assert.equal(f.calls.length,1);assert.deepEqual(f.sleeps,[]);
});
