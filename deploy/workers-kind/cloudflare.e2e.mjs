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
await check('deleted Script names receive a fresh internal service identity',async()=>{const before=(await api(workerPath)).result.tag;await api(scriptPath,'DELETE');await api(workerPath,'GET',undefined,false,404);code('recreated');await wrangler(['deploy']);const after=(await api(workerPath)).result.tag;assert.notEqual(before,after);const secrets=(await api(scriptPath+'/secrets')).result;assert.equal(secrets.length,0)});
await check('native service deletion cannot resurrect a previous Script identity',async()=>{const id=(await api(workerPath)).result.tag;await api('/api/v1/orgs/'+org.id+'/services/'+id,'DELETE',undefined,false,204);const response=await api(cf+'/workers/workers','POST',{name:script},false,409);assert.equal(response.success,false);await api('/api/v1/orgs/'+org.id+'/services/'+id,'GET',undefined,false,404)});
console.log('CF WRANGLER E2E PASSED '+results.length+' cases');
