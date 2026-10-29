import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {existsSync,mkdtempSync,mkdirSync,readFileSync,realpathSync,rmSync,writeFileSync} from 'node:fs';
import {createServer,request as httpRequest} from 'node:http';
import net from 'node:net';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {gunzipSync,gzipSync} from 'node:zlib';

const here=path.dirname(fileURLToPath(import.meta.url));
const runtimeRoot=path.resolve(here,'..');
const repositoryRoot=path.resolve(runtimeRoot,'..','..');
const builderRoot=path.join(repositoryRoot,'workers','builder');
const wrangler=path.join(builderRoot,'node_modules','.bin','wrangler');
const workerd=path.join(runtimeRoot,'node_modules','.bin','workerd');

if(!existsSync(wrangler)){
 if(process.env.CI)throw new Error('Wrangler artifact smoke requires npm ci --prefix workers/builder before runtime tests');
 console.log('WRANGLER ARTIFACT WORKERD SMOKE SKIPPED (builder dependencies are not installed)');
 process.exit(0);
}

const sha256=value=>createHash('sha256').update(value).digest('hex');
const scratch=realpathSync(mkdtempSync(path.join(tmpdir(),'krun-wrangler-workerd-')));
const work=path.join(scratch,'work');
mkdirSync(work,{recursive:true});

function tarFile(name,bytes){
 bytes=Buffer.from(bytes);let base=name,prefix='';
 if(Buffer.byteLength(base,'utf8')>100){const parts=name.split('/');while(parts.length>1&&Buffer.byteLength(parts.join('/'),'utf8')>100)prefix+=(prefix?'/':'')+parts.shift();base=parts.join('/');}
 const header=Buffer.alloc(512);header.write(base,0,100,'utf8');if(prefix)header.write(prefix,345,155,'utf8');
 const octal=(offset,length,value)=>header.write(value.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((value,byte)=>value+byte,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');
 return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}

function sourceArchive(files){
 return gzipSync(Buffer.concat([...Object.entries(files).map(([name,bytes])=>tarFile('src/'+name,bytes)),Buffer.alloc(1024)]));
}

function archiveEntries(compressed){
 const raw=gunzipSync(compressed),entries=new Map();
 for(let offset=0;offset+512<=raw.length;){
  const header=raw.subarray(offset,offset+512),base=header.subarray(0,100).toString('utf8').replace(/\0.*$/,''),prefix=header.subarray(345,500).toString('utf8').replace(/\0.*$/,'');
  if(!base)break;
  const name=prefix?prefix+'/'+base:base,size=Number.parseInt(header.subarray(124,136).toString('ascii').replace(/[\0 ].*$/,'').trim(),8)||0;
  entries.set(name,Buffer.from(raw.subarray(offset+512,offset+512+size)));offset+=512+Math.ceil(size/512)*512;
 }
 return entries;
}

async function freePort(){
 return new Promise((resolve,reject)=>{const server=net.createServer();server.once('error',reject);server.listen(0,'127.0.0.1',()=>{const port=server.address().port;server.close(error=>error?reject(error):resolve(port));});});
}

async function runBuilder(inputPath){
 return new Promise(resolve=>{
  const child=spawn(process.execPath,[path.join(builderRoot,'build-worker.mjs')],{cwd:builderRoot,env:{...process.env,KRUN_BUILD_INPUT:inputPath,KRUN_BUILD_WORK:work,KRUN_BUILD_PHASE:'all',KRUN_WRANGLER_BIN:wrangler,KRUN_NPM_REGISTRY:'https://registry.npmjs.org/'},stdio:['ignore','pipe','pipe']});
  let output='';child.stdout.on('data',chunk=>{output+=chunk;});child.stderr.on('data',chunk=>{output+=chunk;});child.once('close',code=>resolve({code,output}));
 });
}

const project=sourceArchive({
 'wrangler.jsonc':Buffer.from(JSON.stringify({name:'wrangler-runtime-smoke',main:'src/index.mjs',compatibility_date:'2026-09-28',rules:[{type:'Text',globs:['**/*.txt'],fallthrough:true},{type:'Data',globs:['**/*.bin'],fallthrough:true},{type:'CompiledWasm',globs:['**/*.wasm'],fallthrough:true}]})),
 'src/index.mjs':Buffer.from(`import {WorkerEntrypoint} from 'cloudflare:workers';
import message from './message.txt';
import payload from './payload.bin';
import wasm from './empty.wasm';
const values=()=>({message,data:[...new Uint8Array(payload)],wasm:wasm instanceof WebAssembly.Module});
export class KrunValidation extends WorkerEntrypoint {async ping(){const value=values();if(value.message!=='wrangler-to-workerd'||value.data.join(',')!=='4,5,6'||!value.wasm)throw new Error('typed modules missing');return true;}}
export class Named extends WorkerEntrypoint {async fetch(){return Response.json({named:true,...values()});}}
export default {async fetch(){return Response.json({source:'real-wrangler-artifact',...values()});}};`),
 'src/message.txt':Buffer.from('wrangler-to-workerd'),
 'src/payload.bin':Buffer.from([4,5,6]),
 'src/empty.wasm':Buffer.from([0,97,115,109,1,0,0,0]),
});

let artifact=null,release=null,descriptor=null,artifactDownloads=0;
const server=createServer((request,response)=>{
 const chunks=[];request.on('data',chunk=>chunks.push(chunk));request.on('end',()=>{
  if(request.url==='/source'){response.writeHead(200,{'content-type':'application/gzip'});response.end(project);return;}
  if(request.url==='/upload'&&request.method==='PUT'){artifact=Buffer.concat(chunks);response.writeHead(200);response.end();return;}
  if(request.url==='/artifact'){artifactDownloads++;response.writeHead(200,{'content-type':'application/gzip','content-length':artifact.length});response.end(artifact);return;}
  if(request.url==='/api/internal/workers/config/snapshot'){response.setHeader('content-type','application/json');response.end(JSON.stringify({pool:'default',config_revision:1,routes:[{host:'wrangler.test',path:'/',enabled:true,release:release.id}],releases:{[release.id]:release}}));return;}
  if(request.url.startsWith('/api/internal/workers/config/changes')){response.writeHead(204);response.end();return;}
  if(request.url==='/api/internal/workers/ack'){response.setHeader('content-type','application/json');response.end('{"ok":true}');return;}
  if(request.url==='/api/internal/workers/artifacts/'+release.artifact_id){response.setHeader('content-type','application/json');response.end(JSON.stringify(descriptor));return;}
  if(request.url==='/api/internal/workers/releases/'+release.id+'/assets'){response.setHeader('content-type','application/json');response.end(JSON.stringify({release_id:release.id,config:null,files:[]}));return;}
  response.writeHead(404);response.end('not found');
 });
});
await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
const platformPort=server.address().port;

let child,exited=false,output='';
try{
 const inputPath=path.join(scratch,'input.json');
 writeFileSync(inputPath,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${platformPort}/source`,upload_url:`http://127.0.0.1:${platformPort}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const built=await runBuilder(inputPath);assert.equal(built.code,0,built.output);assert.ok(artifact?.length,built.output);
 const entries=archiveEntries(artifact),manifest=JSON.parse(entries.get('manifest.json').toString('utf8')),main=manifest.modules.find(module=>module.name===manifest.main_module);
 assert.ok(main,'Wrangler artifact has no main module');
 assert.ok(manifest.modules.some(module=>module.type==='text'),'Wrangler artifact lost Text module');
 assert.ok(manifest.modules.some(module=>module.type==='data'),'Wrangler artifact lost Data module');
 assert.ok(manifest.modules.some(module=>module.type==='wasm'),'Wrangler artifact lost Wasm module');
 release={id:'rel_wrangler_runtime',service_id:'svc',organization_id:'org',environment:'production',artifact_id:'art_wrangler_runtime',profile:manifest.profile,compatibility_date:manifest.compatibility_date,compatibility_flags:manifest.compatibility_flags,module_sha256:main.sha256,archive_sha256:sha256(artifact),variables:{},pool:'default'};
 descriptor={artifact_id:release.artifact_id,archive_sha256:release.archive_sha256,size:artifact.length,download_url:`http://127.0.0.1:${platformPort}/artifact`};

 const privatePort=await freePort(),managementPort=await freePort();
 const config=`using W = import "/workerd/workerd.capnp";
const config :W.Config = (
 services = [
  (name="entry",worker=(modules=[(name="entry.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot,'entry.js'))})],compatibilityDate="2026-09-28",compatibilityFlags=["experimental","enable_ctx_exports"],bindings=[
   (name="LOADER",workerLoader=(id="krun-wrangler-artifact-smoke")),
   (name="PLATFORM",service="platform"),(name="POD_NAME",text="pod"),(name="POD_UID",text="uid"),(name="RUNTIME_TOKEN",text="runtime-token"),(name="MANAGEMENT_TOKEN",text="management-token"),(name="RUNTIME_POOL",text="default"),(name="ASSETS_CACHE_MAX",text="1048576")
  ],globalOutbound="storage")),
  (name="platform",external=(address="127.0.0.1:${platformPort}",http=())),
  (name="storage",network=(allow=["127.0.0.1/32"]))
 ],
 sockets=[(name="private",address="127.0.0.1:${privatePort}",http=(),service=(name="entry",entrypoint="Private")),(name="management",address="127.0.0.1:${managementPort}",http=(),service=(name="entry",entrypoint="Management"))]
);`;
 const configPath=path.join(scratch,'runtime.capnp');writeFileSync(configPath,config);
 child=spawn(workerd,['serve',configPath,'-I','/','--experimental'],{cwd:scratch,stdio:['ignore','pipe','pipe']});child.stdout.on('data',chunk=>{output+=chunk;});child.stderr.on('data',chunk=>{output+=chunk;});child.once('exit',()=>{exited=true;});

 let validation;
 for(let attempt=0;attempt<100;attempt++){
  if(exited)throw new Error('workerd exited before readiness\n'+output);
  try{validation=await fetch(`http://127.0.0.1:${managementPort}/validate`,{method:'POST',headers:{authorization:'Bearer management-token','content-type':'application/json'},body:JSON.stringify({release,artifact:descriptor})});break;}catch{await new Promise(resolve=>setTimeout(resolve,50));}
 }
 assert.ok(validation,'workerd did not become ready\n'+output);const validationText=await validation.text();assert.equal(validation.status,200,validationText+'\n'+output);
 const response=await new Promise((resolve,reject)=>{const request=httpRequest({hostname:'127.0.0.1',port:privatePort,path:'/run',headers:{host:'wrangler.test'}},incoming=>{const chunks=[];incoming.on('data',chunk=>chunks.push(chunk));incoming.on('end',()=>resolve({status:incoming.statusCode,headers:incoming.headers,body:Buffer.concat(chunks).toString('utf8')}));});request.on('error',reject);request.end();});
 assert.equal(response.status,200,response.body+'\n'+output);
 assert.deepEqual(JSON.parse(response.body),{source:'real-wrangler-artifact',message:'wrangler-to-workerd',data:[4,5,6],wasm:true});
 assert.equal(response.headers['x-krun-release'],release.id);assert.ok(artifactDownloads>=1,'runtime never downloaded the Wrangler artifact');
 console.log('REAL WRANGLER ARTIFACT TO WORKERD SMOKE PASSED');
}finally{
 if(child&&!exited){child.kill('SIGTERM');await new Promise(resolve=>{const timeout=setTimeout(resolve,2000);child.once('exit',()=>{clearTimeout(timeout);resolve();});});}
 await new Promise(resolve=>server.close(resolve));rmSync(scratch,{recursive:true,force:true});
}
