import http from 'node:http';
import {execFileSync,spawn} from 'node:child_process';
import {mkdirSync,writeFileSync,openSync,closeSync,chmodSync,readFileSync} from 'node:fs';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {gzipSync} from 'node:zlib';
const kubeconfig=process.env.KUBECONFIG;assert(kubeconfig,'KUBECONFIG must identify the intended kind cluster');
const kubectl=(...args)=>execFileSync('kubectl',['--kubeconfig',kubeconfig,...args],{encoding:'utf8',timeout:180000});
const clusterName=process.env.KIND_CLUSTER_NAME;assert(clusterName,'KIND_CLUSTER_NAME must identify the intended kind cluster');
const expectedContext='kind-'+clusterName;assert.equal(kubectl('config','current-context').trim(),expectedContext,'Verification requires the intended kind context');
const podTransport=process.env.KRUN_E2E_TRANSPORT!=='port-forward';
const origin='http://127.0.0.1:4380';const cookies=new Map();let csrf='',key='';const evidence=[];const transportRetries=[];
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
function request(route,method='GET',input,options={}){return new Promise((resolve,reject)=>{
 const data=input===undefined?undefined:options.form?input:JSON.stringify(input);
 const headers={accept:'application/json',...(data?{'content-type':options.form?'application/x-www-form-urlencoded':'application/json','content-length':Buffer.byteLength(data)}:{}),...options.headers};
 if(options.session){headers.cookie=[...cookies].map(([k,v])=>k+'='+v).join('; ');headers.origin=origin;if(csrf)headers['x-csrf-token']=csrf;}else if(options.token!==null&&key){headers.authorization='Bearer '+(options.token||key);}
 if(podTransport){
  try {
   const script=`const http=require('http');let input='';process.stdin.on('data',x=>input+=x);process.stdin.on('end',()=>{const v=JSON.parse(input);const r=http.request('http://backend:9000'+v.route,{method:v.method,headers:v.headers,agent:false,signal:AbortSignal.timeout(12000)},res=>{let raw='';res.setEncoding('utf8');res.on('data',x=>raw+=x);res.on('end',()=>{let body;try{body=JSON.parse(raw)}catch{body=raw};console.log(JSON.stringify({status:res.statusCode,body,headers:res.headers}));});});r.on('error',e=>{console.error(e.message);process.exitCode=1});r.end(v.data);});`;
   const raw=execFileSync('kubectl',['--kubeconfig',kubeconfig,'exec','-i','-n','workers-mvp','deploy/tester','--','node','-e',script],{input:JSON.stringify({route,method,headers,data}),encoding:'utf8',timeout:20000});
   const response=JSON.parse(raw);for(const c of response.headers['set-cookie']||[]){const [name,value]=c.split(';')[0].split('=');cookies.set(name,value)}resolve(response);
  }catch(e){reject(new Error(method+' '+route+': tester request failed',{cause:e}));}
  return;
 }
 const r=http.request(origin+route,{method,headers,agent:false,signal:AbortSignal.timeout(12000)},res=>{for(const c of res.headers['set-cookie']||[]){const [name,value]=c.split(';')[0].split('=');cookies.set(name,value)}let raw='';res.setEncoding('utf8');res.on('data',x=>raw+=x);res.on('end',()=>{let body;try{body=JSON.parse(raw)}catch{body=raw}resolve({status:res.statusCode,body,headers:res.headers});});});r.on('error',e=>reject(new Error(method+' '+route+': '+e.message,{cause:e})));r.end(data);
});}
async function api(route,method='GET',body,expected=200,options={}){
 let r;for(let attempt=0;;attempt++){try{r=await request(route,method,body,options);if(method==='GET'&&expected===200&&r.status===503&&r.body==='')throw new Error('proxy upstream temporarily unavailable');break;}catch(error){if(method!=='GET'||attempt>=3)throw error;transportRetries.push({route,attempt:attempt+1});writeFileSync('evidence/transport-retries.json',JSON.stringify(transportRetries,null,2));await sleep(1000);}}
 assert.equal(r.status,expected,method+' '+route+' '+JSON.stringify(r.body));return r.body;
}
async function check(name,run){const detail=await run();const result={name,state:'pass',detail};evidence.push(result);writeFileSync('evidence/e2e.json',JSON.stringify(evidence,null,2));console.log(JSON.stringify(result));}
async function poll(route,states,timeout=150000){const until=Date.now()+timeout;let last;while(Date.now()<until){last=await api(route);if(states.includes(last.state))return last;if(last.state==='failed')throw new Error('Unexpected failed operation: '+JSON.stringify(last));await sleep(500);}throw new Error('Timeout '+JSON.stringify(last));}
let forward;
process.on('exit',()=>forward?.kill());
async function startForward(){
 if(podTransport){for(let i=0;i<60;i++){try{await api('/api/healthz');return;}catch{await sleep(500);}}throw new Error('Backend did not become healthy');}
 forward?.kill();
 const log=openSync('evidence/port-forward.log','a');
 forward=spawn('kubectl',['--kubeconfig',kubeconfig,'-n','workers-mvp','port-forward','deployment/backend','4380:9000','--address=127.0.0.1'],{stdio:['ignore',log,log],env:{...process.env,KUBECTL_PORT_FORWARD_WEBSOCKETS:'false'}});closeSync(log);forward.unref();
 for(let i=0;i<60;i++){try{await api('/api/healthz');return;}catch{await sleep(500);}}
 throw new Error('Backend port-forward did not become healthy');
}
mkdirSync('evidence',{recursive:true});
writeFileSync('evidence/transport-retries.json','[]');
await startForward();
const auth=await api('/api/auth/csrf','POST',undefined,200,{session:true});csrf=auth.csrf_token;
const login=await api('/api/auth/local/login','POST',new URLSearchParams({return_to:'/'}).toString(),200,{session:true,form:true});
const callback=new URL(login.redirect_url,origin);await api(callback.pathname+callback.search,'GET',undefined,303,{session:true});
let me=await api('/api/auth/me','GET',undefined,200,{session:true});if(!me.system_admin)kubectl('exec','-n','workers-mvp','deploy/backend','-c','backend','--','/app/krun-server','bootstrap','--config','/config/control.yaml','--user',me.user.user_id);
csrf=(await api('/api/auth/csrf','POST',undefined,200,{session:true})).csrf_token;
const credential=await api('/api/v1/system/api-keys','POST',{name:'Workers kind verification',expires_in:86400,policy:{schema_version:1,statements:[{effect:'allow',actions:['*'],resource:{kind:'*',all_in_system:true}}]}},201,{session:true});key=credential.key;
mkdirSync('generated',{recursive:true});writeFileSync('generated/api-key.json',JSON.stringify({key,expires_at:credential.expires_at}),{mode:0o600});chmodSync('generated/api-key.json',0o600);
const run=randomUUID().slice(0,8);
const org=await api('/api/v1/orgs','POST',{name:'Workers '+run},201,{session:true});const service=await api('/api/v1/orgs/'+org.id+'/services','POST',{name:'orders-'+run},201);const base='/api/v1/orgs/'+org.id+'/services/'+service.id+'/workers';
const initialPods=JSON.parse(kubectl('get','pods','-n','workers-mvp','-l','role=krun-runtime','-o','json')).items.filter(p=>!p.metadata.deletionTimestamp).map(p=>({name:p.metadata.name,uid:p.metadata.uid,restarts:p.status.containerStatuses[0].restartCount}));
// Private and public scopes are separate Gateway instances. Requests are aimed
// at a specific instance so cross-scope Host forgery can be asserted.
const gateway=JSON.parse(kubectl('get','service','workers-mvp','-n','kgateway-system','-o','json')).spec.clusterIP;
const publicGateway=JSON.parse(kubectl('get','service','workers-public','-n','kgateway-system','-o','json')).spec.clusterIP;
async function invokeAt(address,host,pathname='/',headers={}){
 const script=`const http=require('http');http.get('http://${address}:8080'+process.argv[1],{headers:{...JSON.parse(process.argv[3]||'{}'),host:process.argv[2]}},r=>{let s='';r.on('data',x=>s+=x);r.on('end',()=>{let body;try{body=JSON.parse(s)}catch{body=s};console.log(JSON.stringify({status:r.statusCode,body,headers:r.headers}));});}).on('error',e=>{console.error(e.message);process.exitCode=1;});`;
 return JSON.parse(kubectl('exec','-n','workers-mvp','deploy/tester','--','node','-e',script,pathname,host,JSON.stringify(headers)));
}
async function invoke(host,pathname='/',headers={}){return invokeAt(gateway,host,pathname,headers)}
function environmentHost(environment,domain='internal'){
 const direct=environment.display_hosts&&environment.display_hosts[domain];
 if(direct)return direct;
 const index=(environment.domains||[]).indexOf(domain);
 return index>=0?(environment.hosts||[])[index]:undefined;
}
function testerPut(url,base64){
 const script=`const http=require('http');const u=new URL(process.argv[1]);const d=Buffer.from(process.argv[2],'base64');const r=http.request({hostname:u.hostname,port:u.port,path:u.pathname+u.search,method:'PUT',headers:{'content-length':d.length}},res=>{res.resume();res.on('end',()=>console.log(JSON.stringify({status:res.statusCode})));});r.on('error',e=>{console.error(e.message);process.exitCode=1;});r.end(d);`;
 return JSON.parse(kubectl('exec','-n','workers-mvp','deploy/tester','--','node','-e',script,url,base64));
}
function testerGet(url){
 const script=`const http=require('http');const u=new URL(process.argv[1]);http.get({hostname:u.hostname,port:u.port,path:u.pathname+u.search},res=>{let s='';res.on('data',x=>s+=x);res.on('end',()=>console.log(JSON.stringify({status:res.statusCode,body:s.slice(0,200)})));}).on('error',e=>{console.error(e.message);process.exitCode=1;});`;
 return JSON.parse(kubectl('exec','-n','workers-mvp','deploy/tester','--','node','-e',script,url));
}
function sourceBundle(code,assets){
 const tarFile=(name,bytes)=>{const h=Buffer.alloc(512);h.write(name,0,100,'utf8');const o=(off,len,n)=>h.write(n.toString(8).padStart(len-1,'0')+'\0',off,len,'ascii');o(100,8,0o644);o(108,8,0);o(116,8,0);o(124,12,bytes.length);o(136,12,0);h.fill(32,148,156);h[156]=48;h.write('ustar\0',257,6,'ascii');h.write('00',263,2,'ascii');const sum=h.reduce((n,b)=>n+b,0);h.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');return Buffer.concat([h,bytes,Buffer.alloc((512-bytes.length%512)%512)]);};
 const parts=[];for(const [name,text] of Object.entries(code))parts.push(tarFile('src/'+name,Buffer.from(text)));for(const [name,text] of Object.entries(assets||{}))parts.push(tarFile('assets/'+name,Buffer.from(text)));parts.push(Buffer.alloc(1024));
 return gzipSync(Buffer.concat(parts),{level:6});
}
const fullstackFile=name=>readFileSync(new URL('../../examples/workers-fullstack/'+name,import.meta.url));
const fullstackCode={
 'index.js':fullstackFile('src/index.js'),
 'http.js':fullstackFile('src/http.js'),
};
const fullstackAssets={
 'index.html':fullstackFile('frontend/public/index.html'),
 'about.html':fullstackFile('frontend/public/about.html'),
 'assets/app-a1b2c3d4.js':fullstackFile('frontend/public/assets/app-a1b2c3d4.js'),
 'assets/styles-a1b2c3d4.css':fullstackFile('frontend/public/assets/styles-a1b2c3d4.css'),
 'assets/krun-a1b2c3d4.svg':fullstackFile('frontend/public/assets/krun-a1b2c3d4.svg'),
};
const source=version=>({entrypoint:'src/index.ts',files:{'src/lib.ts':`export const version = ${JSON.stringify(version)};`,'src/index.ts':`import {version} from './lib'; import {connect} from 'cloudflare:sockets'; export default {async fetch(request: Request, env: Record<string,string>) {if(new URL(request.url).pathname==='/probe'){let blocked=false;try{await fetch('http://backend:9001/api/internal/workers/config/snapshot')}catch{blocked=true};let tcpBlocked=false;try{const socket=connect({hostname:'postgres',port:5432});await socket.opened;await socket.close()}catch{tcpBlocked=true};return Response.json({blocked,tcpBlocked,bindings:Object.keys(env)})};return Response.json({version,environment:env.KRUN_ENVIRONMENT,greeting:env.GREETING||'default',url:request.url})}}`}});
async function build(input,expected='succeeded',options={}){const b=await api(base+'/builds','POST',{environment:'production',...input,idempotency_key:randomUUID()},202,options);return poll(base+'/builds/'+b.id,[expected],150000);}
async function publish(buildId,environment,revision){const op=await api(base+'/environments/'+environment+'/releases','POST',{build_id:buildId,expected_revision:revision,idempotency_key:randomUUID()},202);return poll(base+'/operations/'+op.id,['available'],150000);}
let b1,b2,prodFirst,hostTest,hostProd;
await check('authenticated real Kubernetes build finalizes S3 archive',async()=>{b1=await build(source('v1-'+run));const a=await api(base+'/artifacts/'+b1.artifact_id);assert.equal(a.state,'finalized');assert.equal(a.storage_location_id,'primary');assert.equal(a.bucket,'krun-artifacts');assert(a.object_key.endsWith('/bundle.tar.gz'));assert(a.archive_size_bytes>0);const logs=await api(base+'/builds/'+b1.id+'/logs');assert(logs.log.includes('Archive SHA-256'));assert(!logs.log.includes('X-Amz-'));return {build_id:b1.id,artifact:a,job:b1.job};});
await check('preview release waits for actual runtime and Gateway adoption',async()=>{await api(base+'/environments','POST',{name:'fix-auth'},201);const op=await publish(b1.id,'fix-auth',0);assert(op.steps.every(s=>s.state==='done'));const envs=(await api(base+'/environments')).items;hostTest=environmentHost(envs.find(e=>e.name==='fix-auth'));hostProd=environmentHost(envs.find(e=>e.name==='production'));assert(hostTest&&hostProd);const r=await invoke(hostTest,'/orders?id=1');assert.equal(r.status,200);assert.equal(r.body.version,'v1-'+run);assert.equal(r.body.environment,'fix-auth');assert(r.body.url.includes('/orders?id=1'));assert.equal(envs.find(e=>e.name==='production').release_revision,0);return {operation:op.id,response:r.body};});
await check('same Artifact publishes with production configuration',async()=>{await api(base+'/environments/production','PATCH',{expected_revision:1,variables:{GREETING:'production-value'}});prodFirst=await publish(b1.id,'production',0);const r=await invoke(hostProd);assert.equal(r.body.greeting,'production-value');assert.equal(r.body.environment,'production');return {release:prodFirst.release_id,response:r.body};});
await check('v2 build and publish changes production without changing test',async()=>{b2=await build(source('v2-'+run));await publish(b2.id,'production',1);assert.equal((await invoke(hostProd)).body.version,'v2-'+run);assert.equal((await invoke(hostTest)).body.version,'v1-'+run);return {build_id:b2.id};});
await check('stale Release revision rejected',async()=>{const r=await request(base+'/environments/production/releases','POST',{build_id:b1.id,expected_revision:1,idempotency_key:randomUUID()});assert.equal(r.status,409);return r.body.error.code;});
await check('historical release rollback restores code and immutable configuration',async()=>{await api(base+'/environments/production','PATCH',{expected_revision:2,variables:{GREETING:'future-config'}});const op=await api(base+'/environments/production/rollbacks','POST',{release_id:prodFirst.release_id,expected_revision:2,idempotency_key:randomUUID()},202);await poll(base+'/operations/'+op.id,['available']);const r=await invoke(hostProd);assert.equal(r.body.version,'v1-'+run);assert.equal(r.body.greeting,'production-value');assert.equal((await api(base+'/environments/production/history')).items.length,3);return r.body;});
await check('syntax error fails build and retains production',async()=>{const b=await build({entrypoint:'index.ts',files:{'index.ts':'export default {fetch('}},'failed');assert(!b.artifact_id);assert((await api(base+'/builds/'+b.id+'/logs')).log.includes('Build failed'));assert.equal((await invoke(hostProd)).body.version,'v1-'+run);return {build_id:b.id,error:b.error};});
await check('compiler cannot read files outside project',async()=>{const b=await build({entrypoint:'index.ts',files:{'index.ts':`import input from '/input/input.json';export default {fetch(){return Response.json(input)}}`}},'failed');assert((await api(base+'/builds/'+b.id+'/logs')).log.includes('Only relative'));return {build_id:b.id};});
await check('candidate module initialization failure leaves current Release',async()=>{const bad=await build({entrypoint:'index.ts',files:{'index.ts':`throw new Error('candidate-init-failed'); export default {fetch(){return new Response('bad')}}`}});const op=await api(base+'/environments/production/releases','POST',{build_id:bad.id,expected_revision:3,idempotency_key:randomUUID()},202);const result=await poll(base+'/operations/'+op.id,['failed']);assert(result.error.includes('Candidate'));assert.equal((await invoke(hostProd)).body.version,'v1-'+run);assert.equal((await api(base+'/environments')).items.find(e=>e.name==='production').release_revision,3);return {operation:op.id,error:result.error};});
await check('API Key is scoped to environment and cannot publish production',async()=>{const limited=await api('/api/v1/orgs/'+org.id+'/api-keys','POST',{name:'test only',expires_in:3600,policy:{schema_version:1,statements:[{effect:'allow',actions:['service.publish','service.read','service.version.read'],resource:{kind:'service',organization_id:org.id,ids:[service.id]},conditions:{environments:['fix-auth']}}]}},201,{session:true});const r=await request(base+'/environments/production/releases','POST',{build_id:b1.id,expected_revision:3,idempotency_key:randomUUID()},{token:limited.key});assert.equal(r.status,403);const visible=await api(base+'/environments','GET',undefined,200,{token:limited.key});assert.deepEqual(visible.items.map(e=>e.name),['fix-auth']);assert.equal((await api(base+'/builds','GET',undefined,200,{token:limited.key})).items.length,0);return {denied:r.status,visible:['fix-auth']};});
await check('preview lifecycle: create, publish with own variables, then delete',async()=>{await api(base+'/environments','POST',{name:'demo-preview'},201);await api(base+'/environments/demo-preview','PATCH',{expected_revision:1,variables:{GREETING:'preview-value'}});await publish(b1.id,'demo-preview',0);const envs=(await api(base+'/environments')).items;const stage=envs.find(e=>e.name==='demo-preview');assert.equal((await invoke(environmentHost(stage))).body.greeting,'preview-value');await api(base+'/environments/demo-preview','DELETE',undefined,204);assert.equal((await api(base+'/environments')).items.some(e=>e.name==='demo-preview'),false);const recreated=await api(base+'/environments','POST',{name:'demo-preview'},201);await api(base+'/environments/demo-preview','DELETE',undefined,204);return {deleted:'demo-preview',recreated:recreated.name,baseline:envs.find(e=>e.name==='production').name};});
await check('business network and platform credentials are absent',async()=>{const r=await invoke(hostTest,'/probe');assert.equal(r.status,200);assert.equal(r.body.blocked,true);assert.equal(r.body.tcpBlocked,true);assert(!r.body.bindings.some(n=>['PLATFORM','LOADER','RUNTIME_TOKEN','MANAGEMENT_TOKEN'].includes(n)));return r.body;});
await check('build idempotency and content addressed Artifact deduplication',async()=>{const data={environment:'fix-auth',...source('v1-'+run),idempotency_key:randomUUID()};const a=await api(base+'/builds','POST',data,202);const b=await api(base+'/builds','POST',data);assert.equal(a.id,b.id);const done=await poll(base+'/builds/'+a.id,['succeeded']);assert.equal(done.artifact_id,b1.artifact_id);const conflict=await request(base+'/builds','POST',{...data,...source('different')});assert.equal(conflict.status,409);return {same_build:a.id,artifact:done.artifact_id};});
await check('invalid paths and cross-service Artifact access rejected',async()=>{const r=await request(base+'/builds','POST',{environment:'fix-auth',entrypoint:'../bad.ts',files:{'../bad.ts':'x'},idempotency_key:randomUUID()});assert.equal(r.status,422);const other=await api('/api/v1/orgs/'+org.id+'/services','POST',{name:'unrelated'},201);const x=await request('/api/v1/orgs/'+org.id+'/services/'+other.id+'/workers/artifacts/'+b1.artifact_id);assert.equal(x.status,404);return {path:r.status,other_service:x.status};});
await check('publishing never restarted the runtime Pods',async()=>{const now=JSON.parse(kubectl('get','pods','-n','workers-mvp','-l','role=krun-runtime','-o','json')).items.filter(p=>!p.metadata.deletionTimestamp);assert.deepEqual(now.map(p=>p.metadata.uid).sort(),initialPods.map(p=>p.uid).sort());assert(now.every(p=>p.status.containerStatuses[0].restartCount===initialPods.find(x=>x.uid===p.metadata.uid).restarts));return initialPods;});
writeFileSync('generated/test-service.json',JSON.stringify({org:org.id,service:service.id,base,hostTest,hostProd,b1:b1.id,prodFirst:prodFirst.release_id},null,2));
await check('control restart recovers durable state and object-backed code',async()=>{kubectl('rollout','restart','deployment/backend','-n','workers-mvp');kubectl('rollout','status','deployment/backend','-n','workers-mvp','--timeout=120s');await startForward();const history=await api(base+'/environments/production/history');assert.equal(history.items.length,3);assert.equal((await invoke(hostProd)).body.version,'v1-'+run);return {history:history.items.length};});
let recoveryBuild;
await check('control interruption during build resumes the durable Job',async()=>{
 const queued=await api(base+'/builds','POST',{environment:'fix-auth',...source('v3-'+run),idempotency_key:randomUUID()},202);
 await poll(base+'/builds/'+queued.id,['building','succeeded']);
 kubectl('rollout','restart','deployment/backend','-n','workers-mvp');kubectl('rollout','status','deployment/backend','-n','workers-mvp','--timeout=120s');await startForward();
 recoveryBuild=await poll(base+'/builds/'+queued.id,['succeeded']);assert.equal((await invoke(hostProd)).body.version,'v1-'+run);
 return {build:recoveryBuild.id,job:recoveryBuild.job};
});
await check('control interruption during publish resumes the Operation',async()=>{
 const queued=await api(base+'/environments/production/releases','POST',{build_id:recoveryBuild.id,expected_revision:3,idempotency_key:randomUUID()},202);
 kubectl('rollout','restart','deployment/backend','-n','workers-mvp');kubectl('rollout','status','deployment/backend','-n','workers-mvp','--timeout=120s');await startForward();
 const done=await poll(base+'/operations/'+queued.id,['available']);assert.equal((await invoke(hostProd)).body.version,'v3-'+run);
 const rollback=await api(base+'/environments/production/rollbacks','POST',{release_id:prodFirst.release_id,expected_revision:4,idempotency_key:randomUUID()},202);await poll(base+'/operations/'+rollback.id,['available']);
 assert.equal((await invoke(hostProd)).body.version,'v1-'+run);assert.equal((await api(base+'/environments/production/history')).items.length,5);
 return {operation:done.id,state:done.state,rollback:rollback.id};
});
await check('rollback recovers a committed release with stalled runtime adoption',async()=>{
 const node=clusterName+'-control-plane';
 const docker=(...args)=>execFileSync('docker',args,{encoding:'utf8',timeout:20000});
 const pod=JSON.parse(kubectl('get','pods','-n','workers-mvp','-l','app=backend','-o','json')).items.find(p=>!p.metadata.deletionTimestamp);
 const container=pod.status.containerStatuses.find(c=>c.name==='backend').containerID.replace('containerd://','');
 const pid=JSON.parse(docker('exec',node,'crictl','inspect',container)).info.pid;
 assert(Number.isInteger(pid)&&pid>1);
 const rule=verb=>docker('exec',node,'nsenter','-t',String(pid),'-n','iptables',verb,'INPUT','-p','tcp','--dport','9001','-j','REJECT');
 const prodEnv=()=>api(base+'/environments').then(e=>e.items.find(x=>x.name==='production'));
 const beforeRelease=await prodEnv();
 let queued,rollback;
 // Block the runtime's config feed and ack path before publishing, so the
 // Operation commits but cannot converge while the rule is active.
 rule('-I');
 try {
  queued=await api(base+'/environments/production/releases','POST',{build_id:b2.id,expected_revision:beforeRelease.release_revision,idempotency_key:randomUUID()},202);
  let committed;for(let i=0;i<1500;i++){committed=await api(base+'/operations/'+queued.id);if(['committed','available','failed'].includes(committed.state))break;await sleep(100);}
  assert.equal(committed.state,'committed');
  await sleep(1500);assert.equal((await api(base+'/operations/'+queued.id)).state,'committed');
  rollback=await api(base+'/environments/production/rollbacks','POST',{release_id:prodFirst.release_id,expected_revision:beforeRelease.release_revision+1,idempotency_key:randomUUID()},202);
	  // Enqueue itself does not supersede the stalled commit, but the controller
	  // may validate and commit the rollback before this next HTTP round trip.
	  // Both observations are valid; failure/availability here would not be.
	  assert(['committed','superseded'].includes((await api(base+'/operations/'+queued.id)).state));
 }finally{rule('-D');}
 await poll(base+'/operations/'+rollback.id,['available']);
 const previous=await api(base+'/operations/'+queued.id);assert(['superseded','available'].includes(previous.state));assert.equal((await invoke(hostProd)).body.version,'v1-'+run);
 return {previous:queued.id,previous_state:previous.state,recovery:rollback.id};
});
await check('replacement runtime Pod cold-loads the current object-backed Release',async()=>{const old=initialPods[0];kubectl('delete','pod',old.name,'-n','workers-mvp');kubectl('rollout','status','deployment/runtime-default','-n','workers-mvp','--timeout=120s');const pods=JSON.parse(kubectl('get','pods','-n','workers-mvp','-l','role=krun-runtime','-o','json')).items.filter(p=>!p.metadata.deletionTimestamp);const replacement=pods.find(p=>!initialPods.some(x=>x.uid===p.metadata.uid));assert(replacement);let r;for(let i=0;i<16;i++){r=await invoke(hostProd);if(r.headers['x-krun-pod']===replacement.metadata.uid)break;}assert.equal(r.headers['x-krun-pod'],replacement.metadata.uid);assert.equal(r.body.version,'v1-'+run);return {replaced:old.uid,replacement:replacement.metadata.uid,response:r.body};});
let assetBuild,assetRelease,assetArtifact;
await check('full-stack example source and static assets build as one Artifact',async()=>{
 const upload=await api(base+'/source-uploads','POST',{},201);
 assert(upload.upload_url.includes('/uploads/'));
 const bundle=sourceBundle(fullstackCode,fullstackAssets);
 const put=testerPut(upload.upload_url,bundle.toString('base64'));
 assert.equal(put.status,200);
 assetBuild=await build({source_type:'upload',source_upload:upload.source_upload,entrypoint:'index.js',assets:{directory:'frontend/public',run_worker_first:['/api/*'],not_found_handling:'single-page-application',html_handling:'auto-trailing-slash'}});
 assert(assetBuild.artifact_id);
 const artifact=await api(base+'/artifacts/'+assetBuild.artifact_id);assert(artifact.archive_size_bytes>0);
 const source=await api(base+'/builds/'+assetBuild.id+'/source');assert(source.download_url.includes('source.tar.gz'));
 return {build:assetBuild.id,artifact:artifact.id,size:artifact.archive_size_bytes,code_files:Object.keys(fullstackCode).length,asset_files:Object.keys(fullstackAssets).length};
});
await check('full-stack page, API, SPA, HTML, and cache behavior work through runtime',async()=>{
 const envs=(await api(base+'/environments')).items;const prod=envs.find(e=>e.name==='production');
 assetRelease=await publish(assetBuild.id,'production',prod.release_revision);
 assetArtifact=await api(base+'/artifacts/'+assetBuild.artifact_id);
 const root=await invoke(hostProd,'/');
 assert.equal(root.status,200);assert(String(root.body).includes('data-app="krun-workers-fullstack"'));assert((root.headers['content-type']||'').startsWith('text/html'));
 const about=await invoke(hostProd,'/about');
 assert.equal(about.status,200);assert(String(about.body).includes('STATIC HTML'));
 const redirect=await invoke(hostProd,'/about.html');
 assert.equal(redirect.status,307);assert.equal(redirect.headers.location,'/about');
 const script=await invoke(hostProd,'/assets/app-a1b2c3d4.js');
 assert.equal(script.status,200);assert(String(script.body).includes('/api/health'));assert((script.headers['cache-control']||'').includes('immutable'));
 const styles=await invoke(hostProd,'/assets/styles-a1b2c3d4.css');
 assert.equal(styles.status,200);assert((styles.headers['content-type']||'').startsWith('text/css'));assert((styles.headers['cache-control']||'').includes('immutable'));
 const health=await invoke(hostProd,'/api/health');
 assert.equal(health.status,200);assert.equal(health.body.ok,true);assert.equal(health.body.service,'krun-workers-fullstack');assert.equal(health.body.environment,'production');assert.equal(health.body.release_id,assetRelease.release_id);
 const echoResult=await api(base+'/environments/production/invoke','POST',{path:'/api/echo',method:'POST',body:JSON.stringify({message:'browser to Worker round trip'})});
 assert.equal(echoResult.status,200);const echo=JSON.parse(echoResult.body);assert.equal(echo.echo,'browser to Worker round trip');assert.equal(echo.environment,'production');
 const missing=await invoke(hostProd,'/api/missing');assert.equal(missing.status,404);assert.equal(missing.body.error,'not_found');
 const navigate=await invoke(hostProd,'/dashboard',{'sec-fetch-mode':'navigate'});
 assert.equal(navigate.status,200);assert(String(navigate.body).includes('data-app="krun-workers-fullstack"'));
 const fallthrough=await invoke(hostProd,'/dashboard');
 assert.equal(fallthrough.status,404);assert.equal(fallthrough.body.error,'not_found');
 const etag=root.headers.etag;assert(etag);
 const cached=await invoke(hostProd,'/',{'if-none-match':etag});
 assert.equal(cached.status,304);
 return {release:assetRelease.release_id,etag,root:root.status,api:health.status,echo:echoResult.status,spa:navigate.status,redirect:redirect.status,missing:missing.status};
});
await check('Secret put and delete create configuration-only Releases and deploy immediately',async()=>{
 const before=(await api(base+'/environments')).items.find(e=>e.name==='production');
 const secretValue='kind-secret-'+randomUUID();
 const queued=await api(base+'/environments/production/secrets/VERIFICATION_SECRET','PUT',{value:secretValue,expected_revision:before.release_revision,idempotency_key:randomUUID()},202);
 const deployed=await poll(base+'/operations/'+queued.id,['available']);
 const secretRelease=await api(base+'/releases/'+deployed.release_id);
 assert.equal(secretRelease.artifact_id,assetBuild.artifact_id);
 const withSecret=await invoke(hostProd,'/api/health');
 assert.equal(withSecret.status,200);assert.equal(withSecret.body.secret_configured,true);assert.equal(withSecret.body.release_id,deployed.release_id);
 const listed=await api(base+'/environments/production/secrets');
 assert.deepEqual(listed.items.map(item=>item.name),['VERIFICATION_SECRET']);
 assert(!JSON.stringify(listed).includes(secretValue));
 const current=(await api(base+'/environments')).items.find(e=>e.name==='production');
 const removing=await api(base+'/environments/production/secrets/VERIFICATION_SECRET','DELETE',{expected_revision:current.release_revision,idempotency_key:randomUUID()},202);
 const removed=await poll(base+'/operations/'+removing.id,['available']);
 const withoutSecret=await invoke(hostProd,'/api/health');
 assert.equal(withoutSecret.body.secret_configured,false);assert.equal(withoutSecret.body.release_id,removed.release_id);
 const deleteRelease=await api(base+'/releases/'+removed.release_id);assert.equal(deleteRelease.artifact_id,assetBuild.artifact_id);
 return {put_release:deployed.release_id,delete_release:removed.release_id,artifact:assetBuild.artifact_id,value_exposed:false};
});
await check('static domain serves pub/ directly and v1/ stays private',async()=>{
 const path='/'+org.id+'/'+service.id+'/'+assetArtifact.archive_sha256+'/index.html';
 let static200;for(let i=0;i<20;i++){static200=await invoke('static.workers.test',path);if(static200.status===200)break;await sleep(500);}
 assert.equal(static200.status,200);assert(String(static200.body).includes('<!doctype html>'));
 assert.equal(static200.headers['access-control-allow-origin'],'*');
 const pub=testerGet('http://minio:9000/krun-artifacts/pub/'+org.id+'/'+service.id+'/'+assetArtifact.archive_sha256+'/index.html');
 assert.equal(pub.status,200);
 const privateObject=testerGet('http://minio:9000/krun-artifacts/v1/'+org.id+'/sha256/'+assetArtifact.archive_sha256+'/bundle.tar.gz');
 assert.equal(privateObject.status,403);
 return {static:static200.status,pub:pub.status,v1:privateObject.status};
});
// The security invariant: each Gateway instance only knows the hostnames of
// the domains bound to it. A forged Host aimed at the other instance must not
// resolve to any route.
await check('gateway instances are isolated per domain binding',async()=>{
 const privateHost=hostProd;
 // Domains are selected per service; enable the public one and wait for its route.
 await api(base+'/domains','PUT',{domains:['internal','external'],expected_revision:0});
 let publicHost;
 for(let i=0;i<20;i++){
  const envs=(await api(base+'/environments','GET')).items;
  const prod=envs.find(e=>e.name==='production');
  publicHost=environmentHost(prod,'external');
  if(publicHost){const probe=await invokeAt(publicGateway,publicHost);if(probe.status!==404)break;}
  await sleep(500);
 }
 assert(publicHost,'second domain host missing after binding');
 const viaPublicPrivateHost=await invokeAt(publicGateway,privateHost);
 assert.equal(viaPublicPrivateHost.status,404);
 const viaPrivatePublicHost=await invokeAt(gateway,publicHost);
 assert.equal(viaPrivatePublicHost.status,404);
 const viaPublicOwn=await invokeAt(publicGateway,publicHost);
 assert.equal(viaPublicOwn.status,200);
 const viaPrivateOwn=await invokeAt(gateway,privateHost);
 assert.equal(viaPrivateOwn.status,200);
 return {publicHost,privateHost,forgedPublic:viaPublicPrivateHost.status,forgedPrivate:viaPrivatePublicHost.status,public:viaPublicOwn.status,private:viaPrivateOwn.status};
});
console.log('PASS '+evidence.length+' kind backend end-to-end checks');
