import {WorkerEntrypoint} from 'cloudflare:workers';
let cached=null,refresh=null,nextRefresh=0;
const encoder=new TextEncoder();const decoder=new TextDecoder();const json=(value,status=200)=>Response.json(value,{status});
const sha256=async raw=>[...new Uint8Array(await crypto.subtle.digest('SHA-256',typeof raw==='string'?encoder.encode(raw):raw))].map(x=>x.toString(16).padStart(2,'0')).join('');
// The bundle profile is manifest.json + modules/index.js (+ assets/ in schema 2).
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

// Asset manifests are immutable per release; skip re-fetching them per request.
const manifests=new Map();
async function assetManifest(env,release){
 if(manifests.has(release.id))return manifests.get(release.id);
 const m=await platform(env,'/releases/'+encodeURIComponent(release.id)+'/assets');
 if(manifests.size>512)manifests.clear();
 manifests.set(release.id,m);
 return m;
}

// Per-file lazy loading from the shared bucket, with a bounded in-memory LRU.
// workerd has no filesystem, so this is the local cache.
const assetCache=new Map();let assetCacheBytes=0;
function cacheGet(key){const hit=assetCache.get(key);if(hit){assetCache.delete(key);assetCache.set(key,hit);}return hit;}
function cachePut(env,key,value){
 const previous=assetCache.get(key);if(previous)assetCacheBytes-=previous.bytes.length;
 assetCache.set(key,value);assetCacheBytes+=value.bytes.length;
 const max=Number(env.ASSETS_CACHE_MAX||0)||64*1024*1024;
 while(assetCacheBytes>max&&assetCache.size>0){
  const first=assetCache.keys().next().value;const old=assetCache.get(first);assetCacheBytes-=old.bytes.length;assetCache.delete(first);
 }
}
async function assetBytes(env,release,path){
 const key=release.id+'\0'+path;const hit=cacheGet(key);if(hit)return hit;
 const meta=await platform(env,'/releases/'+encodeURIComponent(release.id)+'/assets/'+path.split('/').map(encodeURIComponent).join('/'));
 const response=await fetch(meta.download_url,{signal:AbortSignal.timeout(15000)});
 if(!response.ok)throw new Error('asset download failed ('+response.status+')');
 const bytes=new Uint8Array(await response.arrayBuffer());
 if(bytes.length!==meta.size_bytes||await sha256(bytes)!==meta.sha256)throw new Error('asset mismatch');
 const value={bytes,content_type:meta.content_type,digest:meta.sha256};
 cachePut(env,key,value);
 return value;
}
function normalizePath(input){
 try{input=decodeURIComponent(input);}catch{return null;}
 const out=[];for(const part of input.split('/')){if(part===''||part==='.')continue;if(part==='..')out.pop();else out.push(part);}
 return '/'+out.join('/');
}
// Negated patterns take precedence over positive ones (Cloudflare semantics).
function patternHits(path,pattern){
 const base=pattern.replace(/\*$/,'');
 return pattern.endsWith('*')?path.startsWith(base):path===base;
}
function workerFirst(path,patterns){
 for(const pattern of patterns||[]){if(pattern[0]==='!'&&patternHits(path,pattern.slice(1)))return 'static';}
 for(const pattern of patterns||[]){if(pattern[0]!=='!'&&patternHits(path,pattern))return 'worker';}
 return null;
}
function lookupAsset(files,path,htmlHandling){
 if(htmlHandling!=='none'){
  if(path==='/')return files.has('/index.html')?{file:'/index.html'}:{};
  if(path.endsWith('/index.html'))return {redirect:path.slice(0,-10)};
  if(path.endsWith('.html'))return {redirect:path.slice(0,-5)||'/'};
  if(path.endsWith('/'))return files.has(path+'index.html')?{file:path+'index.html'}:{};
 }
 if(files.has(path))return {file:path};
 if(htmlHandling!=='none'&&files.has(path+'.html'))return {file:path+'.html'};
 return {};
}
const hashedAsset=/[-.][0-9a-f]{8,}\.[a-z0-9]+$/i;
function serveAsset(request,path,value){
 const etag='"'+value.digest+'"';
 if(request.headers.get('if-none-match')===etag)return new Response(null,{status:304,headers:{etag}});
 return new Response(value.bytes,{status:200,headers:{'content-type':value.content_type,etag,'cache-control':hashedAsset.test(path)?'public, max-age=31536000, immutable':'public, max-age=300'}});
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
  if((manifest.schema_version!==1&&manifest.schema_version!==2)||manifest.profile!=='esbuild-esm-v1'||manifest.main_module!=='index.js'
   ||module?.name!=='index.js'||module?.path!=='modules/index.js'||module?.type!=='esm'
   ||module.size_bytes!==encoder.encode(code).length||module.sha256!==await sha256(code)
   ||module.sha256!==d.module_sha256)throw new Error('artifact manifest mismatch');
  return {compatibilityDate:'2026-09-28',mainModule:'index.js',modules:{'index.js':{js:code}},env:{...d.variables,KRUN_ENVIRONMENT:d.environment,KRUN_SERVICE_ID:d.service_id,KRUN_RELEASE_ID:d.id},globalOutbound:null};
 });
}
async function validate(env,d) {const stub=await worker(env,d);await stub.getEntrypoint('KrunValidation').ping();}
export class Private extends WorkerEntrypoint {
 async fetch(request){
  try{
   const s=await snapshot(this.env);const url=new URL(request.url);
   const route=s.routes.find(r=>r.host===url.hostname.toLowerCase());if(!route)return json({error:'unknown route'},404);
   if(!route.enabled)return json({error:'environment disabled'},410);
   const d=s.releases[route.release];if(!d)return json({error:'environment not published'},503);
   const headers=new Headers(request.headers);for(const name of ['x-krun-release','x-krun-scope','x-forwarded-host','x-internal'])headers.delete(name);
   const forward=async()=>{const stub=await worker(this.env,d);return await stub.getEntrypoint().fetch(new Request(request,{headers}));};
   let response;
   const manifest=await assetManifest(this.env,d);
   if(!manifest.config){response=await forward();}
   else{
    const path=normalizePath(url.pathname);
    if(path===null)response=json({error:'invalid path'},400);
    else{
     const decision=workerFirst(path,manifest.config.run_worker_first);
     // Manifests store asset paths relative to assets.directory; the lookup table
     // and the internal asset API both key off that relative form.
     const files=new Map((manifest.files||[]).map(f=>['/'+f.path,f.path]));
     const hit=decision==='worker'?{worker:true}:lookupAsset(files,path,manifest.config.html_handling||'auto-trailing-slash');
     if(hit.redirect!==undefined)response=new Response(null,{status:307,headers:{location:hit.redirect}});
     else if(hit.worker)response=await forward();
     else if(hit.file)response=serveAsset(request,hit.file,await assetBytes(this.env,d,files.get(hit.file)));
     else if(request.headers.get('sec-fetch-mode')==='navigate'&&manifest.config.not_found_handling==='single-page-application'&&files.has('/index.html')){response=serveAsset(request,'/index.html',await assetBytes(this.env,d,files.get('/index.html')));}
     else response=await forward();
    }
   }
   const out=new Response(response.body,response);
   out.headers.set('x-krun-release',d.id);out.headers.set('x-krun-pod',this.env.POD_UID);
   return out;
  }catch(e){console.error('runtime request failed',e.message);return json({error:'runtime unavailable'},503);}
 }
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
