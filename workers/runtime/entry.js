import {WorkerEntrypoint} from 'cloudflare:workers';
let cached=null,refresh=null,nextRefresh=0;
const encoder=new TextEncoder();const decoder=new TextDecoder();const json=(value,status=200)=>Response.json(value,{status});
const sha256=async raw=>[...new Uint8Array(await crypto.subtle.digest('SHA-256',typeof raw==='string'?encoder.encode(raw):raw))].map(x=>x.toString(16).padStart(2,'0')).join('');
// The bundle profile is exactly manifest.json + modules/index.js (esbuild-esm-v1).
function untar(raw){
 const files={};let off=0;
 while(off+512<=raw.length){
  const header=raw.subarray(off,off+512);const name=decoder.decode(header.subarray(0,100)).replace(/\0.*$/,'');
  const sizeField=decoder.decode(header.subarray(124,136)).replace(/[\0 ].*$/,'').trim();
  const size=parseInt(sizeField,8)||0;const type=header[156];
  if(!name)break;
  if(type===48&&['manifest.json','modules/index.js'].includes(name)&&!files[name])files[name]=raw.subarray(off+512,off+512+size);
  off+=512+Math.ceil(size/512)*512;
 }
 return files;
}
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
  // The platform hands out a short-lived URL; artifact bytes go straight from
  // object storage to this Pod, never through the control plane.
  const a=await platform(env,'/artifacts/'+encodeURIComponent(d.artifact_id));
  const response=await fetch(a.download_url,{signal:AbortSignal.timeout(15000)});
  if(!response.ok)throw new Error('artifact download failed ('+response.status+')');
  const archive=new Uint8Array(await response.arrayBuffer());
  if(archive.length!==a.size||await sha256(archive)!==a.archive_sha256)throw new Error('artifact archive mismatch');
  const files=untar(new Uint8Array(await new Response(new Blob([archive]).stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer()));
  const manifest=JSON.parse(decoder.decode(files['manifest.json']));
  const code=decoder.decode(files['modules/index.js']);
  const module=manifest.modules?.[0];
  if(manifest.schema_version!==1||manifest.profile!=='esbuild-esm-v1'||manifest.main_module!=='index.js'
   ||module?.name!=='index.js'||module?.path!=='modules/index.js'||module?.type!=='esm'
   ||module.size_bytes!==encoder.encode(code).length||module.sha256!==await sha256(code)
   ||module.sha256!==d.module_sha256)throw new Error('artifact manifest mismatch');
  return {compatibilityDate:'2026-09-28',mainModule:'index.js',modules:{'index.js':{js:code}},env:{...d.variables,KRUN_ENVIRONMENT:d.environment,KRUN_SERVICE_ID:d.service_id,KRUN_RELEASE_ID:d.id},globalOutbound:null};
 });
}
async function validate(env,d) {const stub=await worker(env,d);await stub.getEntrypoint('KrunValidation').ping();}
export class Private extends WorkerEntrypoint {
 async fetch(request){try{const s=await snapshot(this.env);const url=new URL(request.url);const route=s.routes.find(r=>r.host===url.hostname.toLowerCase());if(!route)return json({error:'unknown route'},404);if(!route.enabled)return json({error:'environment disabled'},410);const d=s.releases[route.release];if(!d)return json({error:'environment not published'},503);const headers=new Headers(request.headers);for(const name of ['x-krun-release','x-krun-scope','x-forwarded-host','x-internal'])headers.delete(name);const stub=await worker(this.env,d);const response=await stub.getEntrypoint().fetch(new Request(request,{headers}));const out=new Response(response.body,response);out.headers.set('x-krun-release',d.id);out.headers.set('x-krun-pod',this.env.POD_UID);return out;}catch(e){console.error('runtime request failed',e.message);return json({error:'runtime unavailable'},503);}}
}
export class Management extends WorkerEntrypoint {
 async fetch(request){
  const path=new URL(request.url).pathname;
  if(path!=='/ready'&&request.headers.get('authorization')!==`Bearer ${this.env.MANAGEMENT_TOKEN}`)return json({error:'denied'},403);
  try {
   if(path==='/validate'||path==='/adopt'){
    const input=await request.json();const d=await platform(this.env,'/releases/'+encodeURIComponent(input.release_id));
    // /validate cold-loads and runs the KrunValidation RPC on this member;
    // /adopt only confirms the snapshot now — remaining members lazy-load the
    // code on their first request, so adoption must not load it here.
    if(path==='/validate')await validate(this.env,d);
    let revision=0;if(path==='/adopt'){const s=await snapshot(this.env,true);if(s.revision<input.minimum_revision)throw new Error('platform snapshot not current');const published=s.routes.some(r=>r.enabled&&r.release===d.id);if(!published)throw new Error('platform release not published');revision=s.revision;}
    return json({pod_uid:this.env.POD_UID,release_id:d.id,archive_sha256:d.archive_sha256,revision});
   }
   if(path!=='/ready')return json({error:'not found'},404);
   const s=await snapshot(this.env);for(const r of s.routes){if(r.enabled&&r.release){const d=s.releases[r.release];if(!d)throw new Error('platform descriptor missing');await validate(this.env,d);}}
   return json({status:'ready',pod_uid:this.env.POD_UID,revision:s.revision});
  }catch(e){const dependency=String(e.message).startsWith('platform');console.error('runtime management failed',e.message);return json({code:dependency?'DEPENDENCY_UNAVAILABLE':'CANDIDATE_INVALID',error:'runtime validation failed'},503);}
 }
}
export default {fetch(){return json({error:'entrypoint required'},404);}};
