import {WorkerEntrypoint} from 'cloudflare:workers';
// Edge configuration is an immutable in-memory view. There is no local DB:
// bootstrap reads one snapshot, then a long-poll applies incremental ops.
let view={revision:0,routes:new Map(),releases:new Map()};
let viewLoaded=false,booting=null,started=false,lastSync=0,lastLoop=0;
const encoder=new TextEncoder();const decoder=new TextDecoder();const strictDecoder=new TextDecoder('utf-8',{fatal:true});const json=(value,status=200)=>Response.json(value,{status});
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const sha256=async raw=>[...new Uint8Array(await crypto.subtle.digest('SHA-256',typeof raw==='string'?encoder.encode(raw):raw))].map(x=>x.toString(16).padStart(2,'0')).join('');
// The bundle profile is manifest.json + one or more modules/* entries
// (+ assets/ in schema 2). Return every regular file so the manifest verifier
// can reject undeclared payloads rather than silently ignoring them.
function untar(raw){
 const files=new Map();let off=0;
 while(off+512<=raw.length){
  const header=raw.subarray(off,off+512),base=decoder.decode(header.subarray(0,100)).replace(/\0.*$/,''),prefix=decoder.decode(header.subarray(345,500)).replace(/\0.*$/,''),name=prefix?prefix+'/'+base:base;
  const sizeField=decoder.decode(header.subarray(124,136)).replace(/[\0 ].*$/,'').trim();
  const size=parseInt(sizeField,8)||0;const type=header[156];
  if(!name)break;
  const end=off+512+size,next=off+512+Math.ceil(size/512)*512;
  if(type!==48||size<0||end>raw.length||next>raw.length||files.has(name))throw new Error('invalid artifact entry');
  if(name!=='manifest.json'&&!name.startsWith('modules/')&&!name.startsWith('assets/'))throw new Error('unsupported artifact entry');
  files.set(name,raw.subarray(off+512,end));off=next;
 }
 return files;
}
const portableUSTAR=name=>{const bytes=encoder.encode(name).length;if(bytes<=100)return true;if(bytes>256)return false;for(let index=name.lastIndexOf('/');index>=0;index=name.lastIndexOf('/',index-1)){if(encoder.encode(name.slice(0,index)).length<=155&&encoder.encode(name.slice(index+1)).length<=100)return true;}return false;};
const safeModuleName=(name,maxBytes=240,prefix='modules/')=>typeof name==='string'&&name!==''&&encoder.encode(name).length<=maxBytes&&!name.startsWith('/')&&!name.startsWith('../')&&!name.includes('\\')&&!name.split('/').some(part=>part===''||part==='.'||part==='..')&&![...name].some(char=>{const code=char.codePointAt(0);return code<0x20||code===0x7f;})&&portableUSTAR(prefix+name);
const sameStrings=(a,b)=>Array.isArray(a)&&Array.isArray(b)&&a.length===b.length&&a.every((v,i)=>v===b[i]);
const exactKeys=(value,keys)=>value&&typeof value==='object'&&!Array.isArray(value)&&Object.keys(value).every(key=>keys.includes(key));
const bytesBuffer=bytes=>bytes.slice().buffer;
async function loadArtifact(archive,descriptor){
 const files=untar(await gunzip(archive));
 const manifestBytes=files.get('manifest.json');if(!manifestBytes)throw new Error('artifact manifest missing');
 let manifest;try{manifest=JSON.parse(strictDecoder.decode(manifestBytes));}catch{throw new Error('artifact manifest invalid');}
 if(!exactKeys(manifest,['schema_version','profile','compatibility_date','compatibility_flags','main_module','modules','assets'])
  ||(manifest.schema_version!==1&&manifest.schema_version!==2)||manifest.profile!=='workerd-bundle-v2'
  ||typeof manifest.compatibility_date!=='string'||!/^\d{4}-\d{2}-\d{2}$/.test(manifest.compatibility_date)||manifest.compatibility_date<'2021-11-02'||manifest.compatibility_date>'2026-09-28'
  ||(manifest.compatibility_flags!==undefined&&(!Array.isArray(manifest.compatibility_flags)||manifest.compatibility_flags.length>32||manifest.compatibility_flags.some(flag=>typeof flag!=='string'||!/^[a-z][a-z0-9_]{0,63}$/.test(flag))||new Set(manifest.compatibility_flags).size!==manifest.compatibility_flags.length))
  ||!safeModuleName(manifest.main_module)||!Array.isArray(manifest.modules)||manifest.modules.length===0||manifest.modules.length>512)throw new Error('artifact manifest unsupported');
 manifest.compatibility_flags=manifest.compatibility_flags||[];
 if(descriptor.profile!==manifest.profile||descriptor.compatibility_date!==manifest.compatibility_date
  ||!sameStrings(descriptor.compatibility_flags||[],manifest.compatibility_flags))throw new Error('artifact compatibility mismatch');
 const allowedTypes=new Set(['esm','commonjs','wasm','text','data','source-map']);
 const modules={};const declared=new Set(['manifest.json']);let mainDigest='';let total=0;
 for(const module of manifest.modules){
  if(!exactKeys(module,['name','path','type','sha256','size_bytes'])||!safeModuleName(module.name)||module.path!=='modules/'+module.name
   ||!allowedTypes.has(module.type)||typeof module.sha256!=='string'||!/^[0-9a-f]{64}$/.test(module.sha256)
   ||!Number.isInteger(module.size_bytes)||module.size_bytes<0||module.size_bytes>2*1024*1024||declared.has(module.path))throw new Error('artifact module metadata invalid');
  const bytes=files.get(module.path);if(!bytes||bytes.length!==module.size_bytes||await sha256(bytes)!==module.sha256)throw new Error('artifact module mismatch');
  declared.add(module.path);total+=bytes.length;if(total>8*1024*1024)throw new Error('artifact modules too large');
  if(module.name===manifest.main_module){if(module.type!=='esm'&&module.type!=='commonjs')throw new Error('artifact main module type invalid');mainDigest=module.sha256;}
  try{
   if(module.type==='esm')modules[module.name]={js:strictDecoder.decode(bytes)};
   else if(module.type==='commonjs')modules[module.name]={cjs:strictDecoder.decode(bytes)};
   else if(module.type==='text')modules[module.name]={text:strictDecoder.decode(bytes)};
   else if(module.type==='data')modules[module.name]={data:bytesBuffer(bytes)};
   else if(module.type==='wasm'){
    if(bytes.length<8||bytes[0]!==0||bytes[1]!==97||bytes[2]!==115||bytes[3]!==109)throw new Error('artifact wasm module invalid');
    modules[module.name]={wasm:bytesBuffer(bytes)};
   }else if(module.type==='source-map'){
    // Source maps are integrity-checked archive metadata, not Loader modules.
    JSON.parse(strictDecoder.decode(bytes));
   }
  }catch{throw new Error('artifact text module invalid');}
 }
 if(!mainDigest||mainDigest!==descriptor.module_sha256)throw new Error('artifact main module mismatch');
 if(manifest.schema_version===1&&manifest.assets!==undefined)throw new Error('schema 1 cannot declare assets');
 if(manifest.schema_version===2){
  const assets=manifest.assets;if(!exactKeys(assets,['directory','binding','run_worker_first','not_found_handling','html_handling','files'])||!Array.isArray(assets.files))throw new Error('artifact assets invalid');
  for(const file of assets.files){
   if(!exactKeys(file,['path','sha256','size_bytes','content_type'])||!safeModuleName(file.path,240,'assets/')||typeof file.sha256!=='string'||typeof file.content_type!=='string'||!Number.isInteger(file.size_bytes))throw new Error('artifact asset metadata invalid');
   const archivePath='assets/'+file.path,bytes=files.get(archivePath);if(declared.has(archivePath)||!bytes||bytes.length!==file.size_bytes||await sha256(bytes)!==file.sha256)throw new Error('artifact asset mismatch');declared.add(archivePath);
  }
 }
 if(files.size!==declared.size||[...files.keys()].some(name=>!declared.has(name)))throw new Error('artifact contains undeclared entries');
 return {manifest,modules};
}
async function platformResponse(env,path,init={},timeout=2000){
 const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),timeout);
 const headers=new Headers(init.headers);headers.set('authorization',`Bearer ${env.RUNTIME_TOKEN}`);headers.set('x-runtime-pool',env.RUNTIME_POOL||'default');if(init.body)headers.set('content-type','application/json');
 try{return await env.PLATFORM.fetch(new Request('http://platform/api/internal/workers'+path,{...init,headers,signal:controller.signal}));}
 finally{clearTimeout(timer);}
}
async function platform(env,path,init={}){
 const response=await platformResponse(env,path,init);
 if(!response.ok)throw new Error('platform '+response.status);
 return response.json();
}
async function requestJSON(url,timeout){
 const response=await fetch(url,{signal:AbortSignal.timeout(timeout)});
 if(!response.ok)throw new Error('config '+response.status);
 return response.json();
}
async function requestBytes(url,timeout){
 const response=await fetch(url,{signal:AbortSignal.timeout(timeout)});
 if(!response.ok)throw new Error('config '+response.status);
 return new Uint8Array(await response.arrayBuffer());
}
async function gunzip(raw){
 return new Uint8Array(await new Response(new Blob([raw]).stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer());
}
function applySnapshot(snapshot,pool='default'){
 if(snapshot.pool&&snapshot.pool!==pool)throw new Error('snapshot pool mismatch');
 const routes=new Map();
 for(const route of snapshot.routes||[])if(route.host)routes.set(String(route.host).toLowerCase(),route);
 const releases=new Map();
 for(const [id,release] of Object.entries(snapshot.releases||{}))releases.set(id,release);
 view={revision:Number(snapshot.config_revision||0),routes,releases};
 viewLoaded=true;lastSync=Date.now();
}
// Bootstrap prefers object storage so a new Pod starts even while the control
// plane is unavailable; the control plane is the fallback.
async function bootstrap(env){
 if(env.CONFIG_BASE)try{
  const latest=await requestJSON(env.CONFIG_BASE+'/latest.json',5000);
  if(latest&&latest.snapshot){
   const raw=await requestBytes(env.CONFIG_BASE+'/'+latest.snapshot,15000);
   if(latest.sha256&&await sha256(raw)!==latest.sha256)throw new Error('snapshot digest mismatch');
   const snapshot=JSON.parse(decoder.decode(await gunzip(raw)));
   if(Number(snapshot.config_revision)!==Number(latest.config_revision))throw new Error('snapshot revision mismatch');
   applySnapshot(snapshot,env.RUNTIME_POOL||'default');
   return view;
  }
 }catch(e){console.error('config bootstrap from storage failed',e.message);}
 applySnapshot(await platform(env,'/config/snapshot'),env.RUNTIME_POOL||'default');
 return view;
}
async function bootstrapFromControl(env){
 applySnapshot(await platform(env,'/config/snapshot'),env.RUNTIME_POOL||'default');
 return view;
}
function ensureBootstrap(env){
 if(viewLoaded)return Promise.resolve(view);
 if(!booting)booting=bootstrap(env).finally(()=>{booting=null;});
 return booting;
}
function applyBatch(batch){
 if(!viewLoaded||Number(batch.from)!==view.revision)return false;
 const routes=new Map(view.routes),releases=new Map(view.releases);
 for(const op of batch.ops||[]){
  if(op.type==='release_put'&&op.release?.id)releases.set(op.release.id,op.release);
  else if(op.type==='route_upsert'&&op.host)routes.set(String(op.host).toLowerCase(),op.route);
  else if(op.type==='route_delete'&&op.host)routes.delete(String(op.host).toLowerCase());
  else throw new Error('unknown config op '+op.type);
 }
 // Prune descriptors no route references; a later rollback re-sends
 // release_put with its route_upsert.
 const referenced=new Set();
 for(const route of routes.values())if(route.release)referenced.add(route.release);
 for(const id of releases.keys())if(!referenced.has(id))releases.delete(id);
 // Copy-on-write: in-flight requests keep the previous view.
 view={revision:Number(batch.to),routes,releases};
 lastSync=Date.now();
 return true;
}
async function sendAck(env){
 try{await platform(env,'/ack',{method:'POST',body:JSON.stringify({pod:env.POD_NAME,pod_uid:env.POD_UID,pool:env.RUNTIME_POOL||'default',revision:view.revision})});}catch{}
}
// One long-poll per Pod. 5s waits keep ack heartbeats fresh without the old
// 500ms full-snapshot polling loop.
async function follow(env){
 let backoff=1000,lastAck=0;
 while(true){
  lastLoop=Date.now();
  try{
   await ensureBootstrap(env);
   if(Date.now()-lastAck>5000){await sendAck(env);lastAck=Date.now();}
   const response=await platformResponse(env,`/config/changes?since=${view.revision}&wait=5s`,{},30000);
   if(response.status===204){await sleep(250);backoff=1000;continue;}
   if(response.status===409){viewLoaded=false;await bootstrapFromControl(env);lastAck=0;backoff=1000;continue;}
   if(!response.ok)throw new Error('config changes '+response.status);
   const batch=await response.json();
   let applied=false;
   try{applied=applyBatch(batch);}catch(e){viewLoaded=false;throw e;}
   if(!applied){viewLoaded=false;await bootstrapFromControl(env);}
   lastAck=0;backoff=1000;
  }catch(e){
   await sleep(backoff);backoff=Math.min(backoff*2,15000);
  }
 }
}
function ensureStarted(env,ctx){
 // A cancelled waitUntil callback may never run follow()'s finally; restart a
 // loop that has made no progress for two minutes.
 if(started&&Date.now()-lastLoop<120000)return;
 started=true;
 const loop=follow(env).finally(()=>{started=false;});
 if(ctx&&typeof ctx.waitUntil==='function')ctx.waitUntil(loop);
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
 const baseHeaders={etag,'x-content-type-options':'nosniff'};
 if(request.headers.get('if-none-match')===etag)return new Response(null,{status:304,headers:baseHeaders});
 return new Response(value.bytes,{status:200,headers:{...baseHeaders,'content-type':value.content_type,'cache-control':hashedAsset.test(path)?'public, max-age=31536000, immutable':'public, max-age=300'}});
}
async function boundAssetResponse(env,release,request){
 const manifest=await assetManifest(env,release);if(!manifest.config)return json({error:'assets unavailable'},404);
 const path=normalizePath(new URL(request.url).pathname);if(path===null)return json({error:'invalid path'},400);
 const files=new Map((manifest.files||[]).map(file=>['/'+file.path,file.path]));
 const hit=lookupAsset(files,path,manifest.config.html_handling||'auto-trailing-slash');
 if(hit.redirect!==undefined)return new Response(null,{status:307,headers:{location:hit.redirect}});
 if(hit.file)return serveAsset(request,hit.file,await assetBytes(env,release,files.get(hit.file)));
 if(manifest.config.not_found_handling==='single-page-application'&&files.has('/index.html'))return serveAsset(request,'/index.html',await assetBytes(env,release,files.get('/index.html')));
 return json({error:'asset not found'},404);
}
export class ReleaseAssets extends WorkerEntrypoint {
 async fetch(request){
  const releaseId=this.ctx?.props?.release_id;if(typeof releaseId!=='string'||releaseId==='')return json({error:'asset binding unavailable'},404);
  return boundAssetResponse(this.env,{id:releaseId},request);
 }
}
const bindingName=/^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
const reservedBindings=new Set(['PLATFORM','LOADER','RUNTIME_TOKEN','RUNTIME_POOL','MANAGEMENT_TOKEN','CONFIG_BASE','POD_NAME','POD_UID','ASSETS_CACHE_MAX']);
const magicBindings=new Set(['__proto__','prototype','constructor']);
async function worker(env,ctx,d,artifact) {
 return env.LOADER.get(`${d.organization_id}/${d.service_id}/${d.environment}/${d.id}/workerd-20260928/workerd-bundle-v2`,async()=>{
  // The platform hands out a short-lived URL; artifact bytes go straight from
  // object storage to this Pod, never through the control plane. Candidate
  // validation may supply the URL with the management request instead.
  const a=artifact||await platform(env,'/artifacts/'+encodeURIComponent(d.artifact_id));
  const response=await fetch(a.download_url,{signal:AbortSignal.timeout(15000)});
  if(!response.ok)throw new Error('artifact download failed ('+response.status+')');
  const archive=new Uint8Array(await response.arrayBuffer());
  if(archive.length!==a.size||await sha256(archive)!==a.archive_sha256)throw new Error('artifact archive mismatch');
  const loaded=await loadArtifact(archive,d),manifest=loaded.manifest;
  let secrets={};
  if(d.secret_digest){
   const payload=await platform(env,'/releases/'+encodeURIComponent(d.id)+'/secrets');
   if(payload.release_id!==d.id||payload.secret_digest!==d.secret_digest||!payload.values||typeof payload.values!=='object')throw new Error('secret binding mismatch');
   secrets=payload.values;
  }
  // Object spread defines own data properties even for magic names such as
  // `__proto__`; the assets binding below uses defineProperty for the same reason.
  // A normal prototype is required because WorkerLoader structured-clones env.
  const childEnv={...(d.variables||{}),...secrets,KRUN_ENVIRONMENT:d.environment,KRUN_SERVICE_ID:d.service_id,KRUN_RELEASE_ID:d.id};
  const assetBinding=manifest.assets?.binding;
  if(assetBinding){
   if(!bindingName.test(assetBinding)||assetBinding.startsWith('KRUN_')||reservedBindings.has(assetBinding)||magicBindings.has(assetBinding)||Object.hasOwn(childEnv,assetBinding))throw new Error('asset binding name conflicts with environment');
   if(!ctx?.exports?.ReleaseAssets)throw new Error('asset binding factory unavailable');
   Object.defineProperty(childEnv,assetBinding,{value:ctx.exports.ReleaseAssets({props:{release_id:d.id}}),enumerable:true,writable:false,configurable:false});
  }
  return {compatibilityDate:manifest.compatibility_date,compatibilityFlags:manifest.compatibility_flags,mainModule:manifest.main_module,modules:loaded.modules,env:childEnv,globalOutbound:null};
 });
}
async function validate(env,ctx,d,artifact) {const stub=await worker(env,ctx,d,artifact);await stub.getEntrypoint('KrunValidation').ping();}
export class Private extends WorkerEntrypoint {
 async fetch(request){
  try{
   ensureStarted(this.env,this.ctx);
   await ensureBootstrap(this.env);
   const url=new URL(request.url);
   const route=view.routes.get(url.hostname.toLowerCase());if(!route)return json({error:'unknown route'},404);
   if(!route.enabled)return json({error:'environment disabled'},410);
   const d=view.releases.get(route.release);if(!d)return json({error:'environment not published'},503);
   const headers=new Headers(request.headers);for(const name of ['x-krun-release','x-krun-scope','x-forwarded-host','x-internal'])headers.delete(name);
   const forward=async()=>{const stub=await worker(this.env,this.ctx,d);return await stub.getEntrypoint().fetch(new Request(request,{headers}));};
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
  // The readiness probe starts the follow loop, so an idle Pod still converges
  // and acks new config revisions even when it receives no business traffic.
  ensureStarted(this.env,this.ctx);
  const path=new URL(request.url).pathname;
  if(path!=='/ready'&&request.headers.get('authorization')!==`Bearer ${this.env.MANAGEMENT_TOKEN}`)return json({error:'denied'},403);
  try {
   if(path==='/validate'){
    const input=await request.json();
    if(!input.release||!input.artifact)return json({error:'release and artifact required'},400);
    // The controller supplies the immutable descriptor and a short-lived
    // artifact URL; cold-load and run the KrunValidation RPC on this member.
    await validate(this.env,this.ctx,input.release,input.artifact);
    return json({pod_uid:this.env.POD_UID,release_id:input.release.id,archive_sha256:input.release.archive_sha256});
   }
   if(path!=='/ready')return json({error:'not found'},404);
   // Readiness means the config view is loaded; releases are lazy-loaded on
   // first request so Pod start does not scale with the number of services.
   await ensureBootstrap(this.env);
   return json({status:'ready',pod_uid:this.env.POD_UID,pool:this.env.RUNTIME_POOL||'default',revision:view.revision,stale_ms:Date.now()-lastSync});
  }catch(e){const dependency=String(e.message).startsWith('platform');console.error('runtime management failed',e.message);return json({code:dependency?'DEPENDENCY_UNAVAILABLE':'CANDIDATE_INVALID',error:'runtime validation failed'},503);}
 }
}
export default {fetch(){return json({error:'entrypoint required'},404);}};
