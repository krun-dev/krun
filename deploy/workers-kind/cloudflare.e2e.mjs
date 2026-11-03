// Run against the isolated kind backend through its same-origin console entry.
// All generated projects, CLI logs and credentials stay under ignored .data/.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdirSync,writeFileSync} from 'node:fs';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
const origin=process.env.KRUN_UI_ORIGIN;
assert(origin,'KRUN_UI_ORIGIN must identify the isolated same-origin E2E console');
const directory=path.resolve(process.env.KRUN_CF_E2E_DIR||'.data/cf-api-e2e');mkdirSync(directory,{recursive:true});
const cookies=new Map();let csrf='',token='';const results=[];
async function api(route,method='GET',body,session=false,status=200){
 const headers={accept:'application/json'};
 if(body!==undefined)headers['content-type']=typeof body==='string'?'application/x-www-form-urlencoded':'application/json';
 if(session){headers.cookie=[...cookies].map(([k,v])=>k+'='+v).join('; ');headers.origin=origin;if(csrf)headers['x-csrf-token']=csrf}else if(token)headers.authorization='Bearer '+token;
 const response=await fetch(origin+route,{method,headers,body:body===undefined?undefined:typeof body==='string'?body:JSON.stringify(body),redirect:'manual',signal:AbortSignal.timeout(180000)});
 for(const c of response.headers.getSetCookie()){const [pair]=c.split(';');const at=pair.indexOf('=');cookies.set(pair.slice(0,at),pair.slice(at+1))}
 const raw=await response.text();let out;try{out=JSON.parse(raw)}catch{out=raw}
 assert.equal(response.status,status,method+' '+route+' '+JSON.stringify(out));return out;
}
async function check(name,fn){await fn();results.push({name,state:'pass'});writeFileSync(path.join(directory,'cloudflare-results.json'),JSON.stringify(results,null,2));console.log('PASS '+name)}
const auth=await api('/api/auth/csrf','POST',undefined,true);csrf=auth.csrf_token;
const login=await api('/api/auth/local/login','POST',new URLSearchParams({return_to:'/'}).toString(),true);
const callback=new URL(login.redirect_url,origin);await api(callback.pathname+callback.search,'GET',undefined,true,303);
csrf=(await api('/api/auth/csrf','POST',undefined,true)).csrf_token;
const credential=await api('/api/v1/system/api-keys','POST',{name:'CF protocol verification',expires_in:3600,policy:{schema_version:1,statements:[{effect:'allow',actions:['*'],resource:{kind:'*',all_in_system:true}}]}},true,201);token=credential.key;
const org=await api('/api/v1/orgs','POST',{name:'CF protocol '+randomUUID().slice(0,8)},true,201);
const script='cf-wire-'+randomUUID().slice(0,8);const cf='/api/v1/cloudflare/client/v4/accounts/'+org.id;const scriptPath=cf+'/workers/scripts/'+script;const workerPath=cf+'/workers/workers/'+script;
const config=path.join(directory,'wrangler.json');const source=path.join(directory,'index.js');
writeFileSync(config,JSON.stringify({name:script,main:'index.js',compatibility_date:'2026-09-28',workers_dev:true,preview_urls:true,vars:{label:'production'},previews:{vars:{label:'preview'}}}));
function code(marker){writeFileSync(source,`export default {fetch(request,env){return Response.json({marker:${JSON.stringify(marker)},label:env.label,secret:env.apiToken??null,base:env.baseToken??null})}}`)}
let commandCount=0;
async function wrangler(args,input=''){
 const env={...process.env,CI:'true',CLOUDFLARE_API_BASE_URL:origin+'/api/v1/cloudflare/client/v4',CLOUDFLARE_API_TOKEN:token,CLOUDFLARE_ACCOUNT_ID:org.id,WRANGLER_SEND_METRICS:'false',WRANGLER_LOG_PATH:path.join(directory,'wrangler-private.log')};
 const child=spawn(process.execPath,[path.resolve('workers/builder/node_modules/wrangler/bin/wrangler.js'),...args,'--config',config],{env,stdio:['pipe','pipe','pipe']});let log='';child.stdout.on('data',b=>log+=b);child.stderr.on('data',b=>log+=b);child.stdin.end(input);
 const timer=setTimeout(()=>child.kill('SIGTERM'),180000);const status=await new Promise(resolve=>child.on('exit',resolve));clearTimeout(timer);
 log=log.split(token).join('[redacted]');writeFileSync(path.join(directory,'command-'+(++commandCount)+'.log'),log,{mode:0o600});assert.equal(status,0,args.join(' ')+' failed: '+log.slice(-6000));return log;
}
let native='';
async function invoke(environment,expected){
 let last;for(let i=0;i<60;i++){
  last=await api(native+'/environments/'+environment+'/invoke','POST',{path:'/',method:'GET'});
  if(last.status===200){let out;try{out=JSON.parse(last.body)}catch{};if(out&&Object.entries(expected).every(([k,v])=>out[k]===v))return out}
  await new Promise(r=>setTimeout(r,500));
 }assert.fail('runtime did not converge: '+JSON.stringify(last));
}
let firstVersion,secondVersion;
await check('official Wrangler deploy uploads modules and publishes production',async()=>{
 code('first');const output=await wrangler(['deploy']);const details=(await api(workerPath)).result;native='/api/v1/orgs/'+org.id+'/services/'+details.tag+'/workers';await invoke('production',{marker:'first',label:'production'});firstVersion=(await api(scriptPath+'/versions')).result.items[0].id;assert.match(firstVersion,/^[0-9a-f-]{36}$/);assert(output.includes('Current Version ID: '+firstVersion));
});
await check('official versions upload leaves production on its previous version',async()=>{
 code('second');await wrangler(['versions','upload']);const versions=(await api(scriptPath+'/versions')).result.items;secondVersion=versions[0].id;assert.notEqual(firstVersion,secondVersion);await invoke('production',{marker:'first'});
});
await check('ordinary Secret mutation rejects an unpublished latest version',async()=>{
 const response=await api(scriptPath+'/secrets','PUT',{name:'apiToken',type:'secret_text',text:'fixture-secret'},false,409);assert.equal(response.errors[0].code,10215);
});
await check('official versions deploy selects one immutable version at 100 percent',async()=>{await wrangler(['versions','deploy',secondVersion+'@100%','--yes']);await invoke('production',{marker:'second'})});
await check('official Secret put and list publish a masked secret snapshot',async()=>{
 await wrangler(['secret','put','apiToken'],'fixture-secret\n');const list=await wrangler(['secret','list']);assert(list.includes('apiToken'));assert(!list.includes('fixture-secret'));await invoke('production',{marker:'second',secret:'fixture-secret'});
});
await check('official Previews Base secret is isolated from production',async()=>{
 await wrangler(['preview','base-config','secret','put','baseToken'],'fixture-base\n');const list=await wrangler(['preview','base-config','secret','list']);assert(list.includes('baseToken'));assert(!list.includes('fixture-base'));await invoke('production',{base:null});
});
await check('official Wrangler Preview copies Base bindings and deploys independently',async()=>{code('preview');await wrangler(['preview','--name','branch-one']);await invoke('branch-one',{marker:'preview',label:'preview',base:'fixture-base',secret:null});await invoke('production',{marker:'second',secret:'fixture-secret',base:null})});
await check('official Preview Secret creates a new Preview deployment',async()=>{await wrangler(['preview','secret','put','apiToken','--name','branch-one'],'fixture-preview\n');await invoke('branch-one',{marker:'preview',secret:'fixture-preview'});await invoke('production',{secret:'fixture-secret'})});
await check('official Preview delete removes only its target',async()=>{await wrangler(['preview','delete','--name','branch-one','--skip-confirmation']);await api(workerPath+'/previews/branch-one','GET',undefined,false,404);await invoke('production',{marker:'second'})});
await check('traffic splitting is explicitly rejected',async()=>{const r=await api(scriptPath+'/deployments','POST',{strategy:'percentage',versions:[{version_id:firstVersion,percentage:50},{version_id:secondVersion,percentage:50}]},false,422);assert.equal(r.success,false)});
await check('first Secret on a nonexistent Worker follows Wrangler placeholder deployment',async()=>{const other='cf-secret-'+randomUUID().slice(0,8);await wrangler(['secret','put','initialToken','--name',other],'fixture-initial\n');const list=await api(cf+'/workers/scripts/'+other+'/secrets');assert(list.result.some(b=>b.name==='initialToken'));await api(cf+'/workers/scripts/'+other,'DELETE')});
// Bindings must survive the complete official upload/deploy/rollback path.
let bindingsVersion, bindingsConfig;
async function invokePath(url, environment='production') {
 return api(native+'/environments/'+environment+'/invoke','POST',{path:url,method:'GET'});
}
await check('asset capabilities reject corrupt files, incomplete tokens and cross-Worker reuse',async()=>{
 const started=(await api(scriptPath+'/assets-upload-session','POST',{manifest:{'/fixture.txt':{hash:'0'.repeat(32),size:4}}})).result;
 const form=new FormData();form.append('0'.repeat(32),new Blob([Buffer.from('bad!').toString('base64')]),'file');
 const response=await fetch(origin+cf+'/workers/assets/upload?base64=true',{method:'POST',headers:{authorization:'Bearer '+started.jwt},body:form});assert.equal(response.status,422);
 function deployment(jwt){const form=new FormData();form.append('metadata',JSON.stringify({main_module:'index.js',compatibility_date:'2026-09-28',assets:{jwt}}));form.append('index.js',new Blob(['export default {fetch(){return new Response("ok")}}'],{type:'application/javascript+module'}),'index.js');return form}
 const incomplete=await fetch(origin+scriptPath,{method:'PUT',headers:{authorization:'Bearer '+token},body:deployment(started.jwt)});assert.equal(incomplete.status,401);
 const completed=(await api(scriptPath+'/assets-upload-session','POST',{manifest:{}})).result;
 const other='cf-cap-'+randomUUID().slice(0,8);await api(cf+'/workers/workers','POST',{name:other});
 const crossed=await fetch(origin+cf+'/workers/scripts/'+other,{method:'PUT',headers:{authorization:'Bearer '+token},body:deployment(completed.jwt)});assert.equal(crossed.status,403);
 await api(cf+'/workers/scripts/'+other,'DELETE');
});
await check('official Wrangler uploads JSON, version metadata and assets without deploying',async()=>{
 const assets=path.join(directory,'public');mkdirSync(assets,{recursive:true});
 writeFileSync(path.join(assets,'index.html'),'<h1>assets-one</h1>');
 writeFileSync(path.join(assets,'readme.txt'),'binding-file-one');
 writeFileSync(path.join(assets,'empty.txt'),'');
 bindingsConfig={name:script,main:'index.js',compatibility_date:'2026-09-28',workers_dev:true,preview_urls:true,
  vars:{CONFIG:{nested:{enabled:true,nullable:null},items:[1,'two',null]},COUNT:42,FLAG:false,EMPTY:null},
  version_metadata:{binding:'META'},assets:{directory:'public',binding:'ASSETS',run_worker_first:['/api/*','/bound']},
  previews:{vars:{CONFIG:{preview:true}},version_metadata:{binding:'META'}}};
 writeFileSync(config,JSON.stringify(bindingsConfig));
 writeFileSync(source,`export default {async fetch(request,env){const url=new URL(request.url);if(url.pathname==='/bound')return env.ASSETS.fetch(new Request('https://assets.invalid/readme.txt'));return Response.json({config:env.CONFIG,count:env.COUNT,flag:env.FLAG,empty:env.EMPTY,meta:env.META})}}`);
 await wrangler(['versions','upload','--tag','assets-one','--message','JSON and asset fixture']);
 const version=(await api(scriptPath+'/versions')).result.items[0];bindingsVersion=version.id;
 assert.equal(version.annotations['workers/tag'],'assets-one');
 assert.equal(version.annotations['workers/message'],'JSON and asset fixture');
 assert(version.resources.bindings.some(b=>b.name==='CONFIG'&&b.type==='json'));
 assert(version.resources.bindings.some(b=>b.name==='META'&&b.type==='version_metadata'));
 assert(version.resources.bindings.some(b=>b.name==='ASSETS'&&b.type==='assets'));
 await invoke('production',{marker:'second'});
});
await check('deployed JSON values and version metadata retain their actual types',async()=>{
 const output=await wrangler(['versions','deploy',bindingsVersion+'@100%','--yes']);assert(output.includes('assets-one'));
 let value;for(let i=0;i<60;i++){const r=await invokePath('/api/config');try{value=JSON.parse(r.body)}catch{};if(value?.meta?.id===bindingsVersion)break;await new Promise(r=>setTimeout(r,500))}
 assert.deepEqual(value.config,{nested:{enabled:true,nullable:null},items:[1,'two',null]});assert.equal(value.count,42);assert.equal(value.flag,false);assert.equal(value.empty,null);
 assert.equal(value.meta.tag,'assets-one');assert.equal(value.meta.id,bindingsVersion);assert(Number.isFinite(Date.parse(value.meta.timestamp)));
 const settings=(await api(scriptPath+'/settings')).result;assert.deepEqual(settings.bindings.find(b=>b.name==='CONFIG').json,value.config);
});
await check('official asset uploads serve static files and the ASSETS fetch binding',async()=>{
 const root=await invokePath('/');assert.equal(root.status,200);assert(root.body.includes('assets-one'));
 const bound=await invokePath('/bound');assert.equal(bound.status,200);assert.equal(bound.body,'binding-file-one');
 const empty=await invokePath('/empty.txt');assert.equal(empty.status,200);assert.equal(empty.body,'');
});
await check('changing assets creates another immutable version and rollback restores old files',async()=>{
 writeFileSync(path.join(directory,'public','index.html'),'<h1>assets-two</h1>');writeFileSync(path.join(directory,'public','readme.txt'),'binding-file-two');
 bindingsConfig.vars.CONFIG={changed:true};writeFileSync(config,JSON.stringify(bindingsConfig));
 await wrangler(['versions','upload','--tag','assets-two']);
 assert((await invokePath('/')).body.includes('assets-one'));
 const next=(await api(scriptPath+'/versions')).result.items[0].id;
 await wrangler(['versions','deploy',next+'@100%','--yes']);
 await assertEventually(async()=>assert((await invokePath('/')).body.includes('assets-two')));
 const output=await wrangler(['versions','deploy',bindingsVersion+'@100%','--yes']);assert(output.includes('assets-one'));
 await assertEventually(async()=>assert((await invokePath('/')).body.includes('assets-one')));
 assert.equal((await invokePath('/bound')).body,'binding-file-one');
 const value=JSON.parse((await invokePath('/api/config')).body);assert.equal(value.meta.tag,'assets-one');assert.equal(value.config.nested.enabled,true);
});
async function assertEventually(fn){let error;for(let i=0;i<60;i++){try{await fn();return}catch(e){error=e;await new Promise(r=>setTimeout(r,500))}}throw error}
await check('keep_assets reuses the deployed immutable files during a code-only upload',async()=>{
 const form=new FormData();form.append('metadata',JSON.stringify({main_module:'index.js',compatibility_date:'2026-09-28',keep_assets:true,bindings:[{name:'ASSETS',type:'assets'},{name:'META',type:'version_metadata'}],annotations:{'workers/tag':'kept-assets'}}));
 form.append('index.js',new Blob(['export default {fetch(request,env){if(new URL(request.url).pathname==="/bound")return env.ASSETS.fetch(new Request("https://assets.invalid/readme.txt"));return Response.json({meta:env.META})}}'],{type:'application/javascript+module'}),'index.js');
 const response=await fetch(origin+scriptPath,{method:'PUT',headers:{authorization:'Bearer '+token},body:form});const body=await response.json();assert.equal(response.status,200,JSON.stringify(body));
 await assertEventually(async()=>assert((await invokePath('/')).body.includes('assets-one')));assert.equal((await invokePath('/bound')).body,'binding-file-one');
 await assertEventually(async()=>assert.equal(JSON.parse((await invokePath('/api/metadata')).body).meta.tag,'kept-assets'));
});
await check('official Preview assets and JSON bindings remain isolated from production',async()=>{
 await wrangler(['preview','--name','assets-preview','--tag','preview-assets']);
 await assertEventually(async()=>assert((await invokePath('/','assets-preview')).body.includes('assets-two')));
 assert((await invokePath('/')).body.includes('assets-one'));
 const value=JSON.parse((await invokePath('/api/config','assets-preview')).body);assert.deepEqual(value.config,{preview:true});assert(value.meta&&value.meta.id!==bindingsVersion);assert.equal(value.meta.tag,'preview-assets');const details=(await api(workerPath+'/previews/assets-preview/deployments/latest')).result;assert.equal(details.annotations['workers/tag'],'preview-assets');assert.equal((await invokePath('/bound','assets-preview')).body,'binding-file-two');
 await wrangler(['preview','delete','--name','assets-preview','--skip-confirmation']);
});
await check('official Wrangler deploy supports an assets-only Worker',async()=>{
 const other='cf-assets-'+randomUUID().slice(0,8);writeFileSync(config,JSON.stringify({name:other,compatibility_date:'2026-09-28',assets:{directory:'public'}}));
 await wrangler(['deploy']);const details=(await api(cf+'/workers/workers/'+other)).result;
 const base='/api/v1/orgs/'+org.id+'/services/'+details.tag+'/workers';
 await assertEventually(async()=>{const r=await api(base+'/environments/production/invoke','POST',{path:'/',method:'GET'});assert.equal(r.status,200);assert(r.body.includes('assets-two'))});
 await api(cf+'/workers/scripts/'+other,'DELETE');
 writeFileSync(config,JSON.stringify({name:script,main:'index.js',compatibility_date:'2026-09-28',workers_dev:true,preview_urls:true}));code('second');
});
await check('deleted Script names receive a fresh internal service identity',async()=>{const before=(await api(workerPath)).result.tag;await api(scriptPath,'DELETE');await api(workerPath,'GET',undefined,false,404);code('recreated');await wrangler(['deploy']);const after=(await api(workerPath)).result.tag;assert.notEqual(before,after);const secrets=(await api(scriptPath+'/secrets')).result;assert.equal(secrets.length,0)});
await check('native service deletion cannot resurrect a previous Script identity',async()=>{const id=(await api(workerPath)).result.tag;await api('/api/v1/orgs/'+org.id+'/services/'+id,'DELETE',undefined,false,204);const response=await api(cf+'/workers/workers','POST',{name:script},false,409);assert.equal(response.success,false);await api('/api/v1/orgs/'+org.id+'/services/'+id,'GET',undefined,false,404)});
console.log('CF WRANGLER E2E PASSED '+results.length+' cases');
