import {WorkerEntrypoint} from 'cloudflare:workers';
let cached=null,refresh=null,nextRefresh=0;
const encoder=new TextEncoder();const json=(value,status=200)=>Response.json(value,{status});
async function platform(env,path,init={}) {
 const controller=new AbortController();let timer;
 const headers=new Headers(init.headers);headers.set('authorization',`Bearer ${env.RUNTIME_TOKEN}`);headers.set('x-runtime-pool','default');if(init.body)headers.set('content-type','application/json');
 try{return await Promise.race([(async()=>{const response=await env.PLATFORM.fetch(new Request('http://platform/api/internal/workers'+path,{...init,headers,signal:controller.signal}));if(!response.ok)throw new Error('platform '+response.status);return response.json();})(),new Promise((_,reject)=>{timer=setTimeout(()=>{controller.abort();reject(new Error('platform deadline'));},2000);})]);}finally{clearTimeout(timer);}
}
async function snapshot(env,force=false) {
 if(!force&&cached&&Date.now()<nextRefresh&&Date.now()<cached.validUntil)return cached;
 if(!refresh)refresh=(async()=>{try{const data=await platform(env,'/snapshot');if(data.pool!=='default'||data.validUntil<=Date.now())throw new Error('platform invalid snapshot');cached=data;nextRefresh=Date.now()+500;await platform(env,'/ack',{method:'POST',body:JSON.stringify({pod:env.POD_NAME,pod_uid:env.POD_UID,pool:'default',revision:data.revision})});return data;}catch(e){if(cached&&Date.now()<cached.validUntil)return cached;throw e;}})().finally(()=>{refresh=null;});return refresh;
}
async function worker(env,d) {
 return env.LOADER.get(`${d.organization_id}/${d.service_id}/${d.environment}/${d.id}/workerd-20260928/profile-v1`,async()=>{
  const a=await platform(env,'/artifacts/'+encodeURIComponent(d.artifact_id));const source=a.modules?.['index.js']?.js;if(typeof source!=='string')throw new Error('platform invalid code');const digest=[...new Uint8Array(await crypto.subtle.digest('SHA-256',encoder.encode(source)))].map(x=>x.toString(16).padStart(2,'0')).join('');
  if(digest!==d.module_sha256||digest!==a.digest||a.archive_digest!==d.archive_sha256||encoder.encode(source).length!==a.size)throw new Error('platform artifact integrity mismatch');
  return {compatibilityDate:a.compatibilityDate,mainModule:a.mainModule,modules:a.modules,env:{...d.variables,KRUN_ENVIRONMENT:d.environment,KRUN_SERVICE_ID:d.service_id,KRUN_DEPLOYMENT_ID:d.id},globalOutbound:null};
 });
}
async function validate(env,d) {const stub=await worker(env,d);await stub.getEntrypoint('KrunValidation').ping();}
export class Private extends WorkerEntrypoint {
 async fetch(request){try{const s=await snapshot(this.env);const url=new URL(request.url);const route=s.routes.find(r=>r.host===url.hostname.toLowerCase());if(!route)return json({error:'unknown route'},404);if(!route.enabled)return json({error:'environment disabled'},410);const d=s.deployments[route.deployment];if(!d)return json({error:'environment not published'},503);const headers=new Headers(request.headers);for(const name of ['x-krun-deployment','x-krun-scope','x-forwarded-host','x-internal'])headers.delete(name);const stub=await worker(this.env,d);const response=await stub.getEntrypoint().fetch(new Request(request,{headers}));const out=new Response(response.body,response);out.headers.set('x-krun-deployment',d.id);out.headers.set('x-krun-pod',this.env.POD_UID);return out;}catch(e){console.error('runtime request failed',e.message);return json({error:'runtime unavailable'},503);}}
}
export class Management extends WorkerEntrypoint {
 async fetch(request){
  const path=new URL(request.url).pathname;
  if(path!=='/ready'&&request.headers.get('authorization')!==`Bearer ${this.env.MANAGEMENT_TOKEN}`)return json({error:'denied'},403);
  try {
   if(path==='/validate'||path==='/adopt'){
    const input=await request.json();const d=await platform(this.env,'/deployments/'+encodeURIComponent(input.deployment_id));await validate(this.env,d);
    let revision=0;if(path==='/adopt'){const s=await snapshot(this.env,true);if(s.revision<input.minimum_revision)throw new Error('platform snapshot not current');const published=s.routes.some(r=>r.enabled&&r.deployment===d.id);if(!published)throw new Error('platform deployment not released');revision=s.revision;}
    return json({pod_uid:this.env.POD_UID,deployment_id:d.id,archive_sha256:d.archive_sha256,revision});
   }
   if(path!=='/ready')return json({error:'not found'},404);
   const s=await snapshot(this.env);for(const r of s.routes){if(r.enabled&&r.deployment){const d=s.deployments[r.deployment];if(!d)throw new Error('platform descriptor missing');await validate(this.env,d);}}
   return json({status:'ready',pod_uid:this.env.POD_UID,revision:s.revision});
  }catch(e){const dependency=String(e.message).startsWith('platform');console.error('runtime management failed',e.message);return json({code:dependency?'DEPENDENCY_UNAVAILABLE':'CANDIDATE_INVALID',error:'runtime validation failed'},503);}
 }
}
export default {fetch(){return json({error:'entrypoint required'},404);}};
