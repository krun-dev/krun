import {readFile,mkdir,writeFile,readdir,stat} from 'node:fs/promises';
import {mkdirSync,realpathSync} from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {gzipSync,gunzipSync} from 'node:zlib';
import {execFile} from 'node:child_process';
import {builtinModules} from 'node:module';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';
import {runWranglerMode} from './wrangler-project.mjs';
const execute=promisify(execFile);
const builderRoot=path.dirname(fileURLToPath(import.meta.url));
const pinnedNpm=path.join(builderRoot,'node_modules','.bin','npm');
function trustedCAEnvironment(){
 const value=process.env.NODE_EXTRA_CA_CERTS;
 if(!value)return {};
 if(!path.isAbsolute(value)||value.includes('\0'))throw new Error('NODE_EXTRA_CA_CERTS must be an absolute path');
 return {NODE_EXTRA_CA_CERTS:value};
}
function commandFailure(label,error){
 const output=[error?.stderr,error?.stdout].map(value=>Buffer.isBuffer(value)?value.toString('utf8'):String(value||'')).join('\n').trim();
 return new Error(label+': '+(output||error?.message||'command failed').slice(-4096));
}
const bareNodeBuiltins=new Set(builtinModules.filter(name=>!name.startsWith('node:')));
const nodeSpecifiers=new Set([...builtinModules.filter(name=>name.startsWith('node:')),...[...bareNodeBuiltins].map(name=>'node:'+name)]);
const hash=raw=>createHash('sha256').update(raw).digest('hex');
const contentType=name=>({
 '.html':'text/html; charset=utf-8','.htm':'text/html; charset=utf-8',
 '.js':'application/javascript; charset=utf-8','.mjs':'application/javascript; charset=utf-8',
 '.css':'text/css; charset=utf-8','.json':'application/json','.map':'application/json',
 '.txt':'text/plain; charset=utf-8','.csv':'text/csv; charset=utf-8','.md':'text/markdown; charset=utf-8',
 '.svg':'image/svg+xml','.png':'image/png','.jpg':'image/jpeg','.jpeg':'image/jpeg',
 '.gif':'image/gif','.webp':'image/webp','.avif':'image/avif','.ico':'image/x-icon',
 '.woff':'font/woff','.woff2':'font/woff2','.ttf':'font/ttf','.otf':'font/otf',
 '.webmanifest':'application/manifest+json','.xml':'application/xml','.pdf':'application/pdf',
 '.wasm':'application/wasm','.glb':'model/gltf-binary','.gltf':'model/gltf+json',
 '.mp3':'audio/mpeg','.mp4':'video/mp4','.webm':'video/webm','.wav':'audio/wav','.ogg':'audio/ogg'
})[path.extname(name).toLowerCase()]||'application/octet-stream';
const safeName=(name,maxBytes)=>name!==''&&Buffer.byteLength(name,'utf8')<=maxBytes&&!name.startsWith('/')&&path.posix.normalize(name)===name&&!name.startsWith('../')&&!name.includes('\\');
const sourceExtensions=new Set(['.js','.mjs','.cjs','.jsx','.ts','.tsx','.json']);
const loopback=host=>host==='127.0.0.1'||host==='localhost'||host==='[::1]';
const validDate=value=>{const date=new Date(value+'T00:00:00Z');return !Number.isNaN(date.valueOf())&&date.toISOString().slice(0,10)===value;};
function registryURL(){
 const registry=new URL(process.env.KRUN_NPM_REGISTRY||'https://registry.npmjs.org/');
 if(registry.username||registry.password||!['https:','http:'].includes(registry.protocol)||registry.protocol==='http:'&&!loopback(registry.hostname))throw new Error('NPM registry must be HTTPS without embedded credentials');
 if(!registry.pathname.endsWith('/'))registry.pathname+='/';
 registry.search='';registry.hash='';
 return registry;
}
function dependencyPlan(files,registry){
 if(!Object.hasOwn(files,'package.json'))return null;
 let pkg;
 try{pkg=JSON.parse(files['package.json']);}catch{throw new Error('package.json is not valid JSON');}
 if(!pkg||typeof pkg!=='object'||Array.isArray(pkg))throw new Error('package.json must be an object');
 const dependencies={...(pkg.dependencies||{}),...(pkg.optionalDependencies||{})};
 const names=Object.keys(dependencies);
 if(names.length===0)return null;
 if(names.length>64||names.some(name=>!/^(@[a-z0-9._~-]+\/)?[a-z0-9._~-]+$/i.test(name)))throw new Error('package.json has too many or invalid dependencies');
 if(!Object.hasOwn(files,'package-lock.json'))throw new Error('npm dependencies require package-lock.json');
 let lock;
 try{lock=JSON.parse(files['package-lock.json']);}catch{throw new Error('package-lock.json is not valid JSON');}
 if(![2,3].includes(lock?.lockfileVersion)||!lock.packages||typeof lock.packages!=='object')throw new Error('npm dependencies require a package-lock v2 or v3');
 let packages=0;
 for(const [name,item] of Object.entries(lock.packages)){
  if(name==='')continue;
  packages++;if(packages>1024)throw new Error('npm dependency graph exceeds 1024 packages');
  if(!item||typeof item!=='object'||item.link===true||typeof item.resolved!=='string'||!/^sha512-/.test(item.integrity||''))throw new Error('package-lock contains an unsupported dependency: '+name);
  let resolved;
  try{resolved=new URL(item.resolved);}catch{throw new Error('package-lock contains an invalid resolved URL: '+name);}
  if(resolved.protocol!==registry.protocol||resolved.host!==registry.host||!resolved.pathname.startsWith(registry.pathname))throw new Error('package-lock dependency is outside the configured registry: '+name);
 }
 return {packages};
}
async function dependencyFootprint(root){
 const pending=[root];let files=0,total=0;
 while(pending.length){
  const current=pending.pop();
  for(const entry of await readdir(current,{withFileTypes:true})){
   const target=path.join(current,entry.name);
   if(entry.isSymbolicLink())continue;
   if(entry.isDirectory())pending.push(target);
   else if(entry.isFile()){
    files++;total+=(await stat(target)).size;
    if(files>20000||total>128*1024*1024)throw new Error('Installed dependencies exceed build limits');
   }
  }
 }
 return {files,total};
}
async function installDependencies(root,workDir,plan,registry){
 if(!plan)return;
 // The official @cloudflare/worker-bundler only runs inside workerd. This
 // builder is a short-lived Node process, so npm performs the deterministic
 // lockfile install and esbuild consumes the resulting node_modules tree.
 const cache=path.join(workDir,'npm-cache'),home=path.join(workDir,'npm-home');
 const userConfig=path.join(home,'user.npmrc'),globalConfig=path.join(home,'global.npmrc');
 await mkdir(cache,{recursive:true});await mkdir(home,{recursive:true});
 await writeFile(userConfig,'',{mode:0o600});await writeFile(globalConfig,'',{mode:0o600});
 // Do not let an untrusted build inherit the control-plane user's ~/.npmrc,
 // auth tokens, proxy settings, NODE_OPTIONS, or arbitrary npm executable.
 const npmEnv={PATH:[path.join(builderRoot,'node_modules','.bin'),path.dirname(process.execPath),'/usr/local/bin','/usr/bin','/bin'].join(':'),HOME:home,npm_config_cache:cache,npm_config_userconfig:userConfig,npm_config_globalconfig:globalConfig,npm_config_update_notifier:'false',...trustedCAEnvironment()};
 let result;
 try{result=await execute(pinnedNpm,['ci','--ignore-scripts','--omit=dev','--no-audit','--no-fund','--userconfig='+userConfig,'--globalconfig='+globalConfig,'--registry='+registry.href],{cwd:root,timeout:60000,maxBuffer:1024*1024,env:npmEnv});}
 catch(error){throw commandFailure('npm ci failed',error);}
 const {stdout,stderr}=result;
 if(stdout.trim())console.log(stdout.trim());if(stderr.trim())console.warn(stderr.trim());
 const footprint=await dependencyFootprint(path.join(root,'node_modules'));
 console.log('Package manager: npm 11.21.1');console.log('Dependencies:',plan.packages+' packages,',footprint.files+' files,',footprint.total+' bytes');
}
async function responseBuffer(response,limit,label){
 const declared=response.headers.get('content-length');
 if(declared!==null){
  if(!/^\d+$/.test(declared)){try{await response.body?.cancel();}catch{}throw new Error(label+' has an invalid Content-Length');}
  if(BigInt(declared)>BigInt(limit)){try{await response.body?.cancel();}catch{}throw new Error(label+' is too large');}
 }
 if(!response.body)return Buffer.alloc(0);
 const reader=response.body.getReader(),chunks=[];let total=0;
 while(true){
  const {done,value}=await reader.read();if(done)break;
  total+=value.byteLength;
  if(total>limit){try{await reader.cancel();}catch{}throw new Error(label+' is too large');}
  chunks.push(Buffer.from(value.buffer,value.byteOffset,value.byteLength));
 }
 return Buffer.concat(chunks,total);
}
function tarFile(name,bytes){
 if(Buffer.byteLength(name,'utf8')>100)throw new Error('Archive path exceeds portable ustar limit: '+name);
 const header=Buffer.alloc(512);header.write(name,0,100,'utf8');
 const octal=(offset,length,n)=>header.write(n.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((n,b)=>n+b,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');
 return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}
// Source bundles have exactly two top sections: src/ (Worker code) and
// assets/ (files relative to assets.directory).
function untarSource(raw){
 const files={};const assets={};const seen=new Set();let off=0,entries=0,total=0,terminated=false;
 while(off+512<=raw.length){
  const header=raw.subarray(off,off+512);const name=header.subarray(0,100).toString('utf8').replace(/\0.*$/,'');
  if(!name){terminated=true;break;}
  const sizeText=header.subarray(124,136).toString('ascii').replace(/\0.*$/,'').trim();
  const checksumText=header.subarray(148,156).toString('ascii').replace(/\0.*$/,'').trim();
  if(!/^[0-7]+$/.test(sizeText)||!/^[0-7]+$/.test(checksumText))throw new Error('Source bundle has an invalid tar header');
  const size=parseInt(sizeText,8);const expected=parseInt(checksumText,8);
  let actual=0;for(let i=0;i<512;i++)actual+=i>=148&&i<156?32:header[i];
  if(actual!==expected)throw new Error('Source bundle tar checksum mismatch');
  const type=header[156];
  const end=off+512+size,next=off+512+Math.ceil(size/512)*512;
  if(type!==48||end>raw.length||next>raw.length||seen.has(name)||++entries>1012)throw new Error('Source bundle may only contain unique regular files');
  if(header.subarray(345,500).some(byte=>byte!==0))throw new Error('Source bundle uses unsupported extended tar paths');
  const data=raw.subarray(off+512,end);total+=data.length;if(total>22*1024*1024)throw new Error('Source bundle expanded payload is too large');
  seen.add(name);
  if(name.startsWith('src/')&&safeName(name.slice(4),96))files[name.slice(4)]=data;
  else if(name.startsWith('assets/')&&safeName(name.slice(7),93))assets[name.slice(7)]=data;
  else throw new Error('Unexpected source bundle entry: '+name);
  off=next;
 }
 if(!terminated||raw.subarray(off).some(byte=>byte!==0))throw new Error('Source bundle has no valid tar terminator');
 return {files,assets};
}
const dispatchInputPath=process.env.KRUN_BUILD_INPUT||'/input/input.json';
let dispatchInput;
try{dispatchInput=JSON.parse(await readFile(dispatchInputPath,'utf8'));}catch(error){console.error('Build failed:',error.message);process.exit(1);}
if(dispatchInput.build_mode==='wrangler'){
 try{await runWranglerMode(dispatchInput,dispatchInputPath,process.env.KRUN_BUILD_WORK||'/work',process.env.KRUN_BUILD_PHASE||'all');}
 catch(error){console.error('Build failed:',error.message);process.exitCode=1;}
}else try {
 const inputPath=process.env.KRUN_BUILD_INPUT||'/input/input.json';
 const rawWorkDir=process.env.KRUN_BUILD_WORK||'/work';
 mkdirSync(rawWorkDir,{recursive:true});
 // macOS /var is a symlink to /private/var; esbuild resolves real paths, so the
 // boundary check must compare against the real work dir.
 const workDir=realpathSync(rawWorkDir);
 const sourceRoot=path.join(workDir,'src');
 const entryStub=path.join(sourceRoot,'__krun_entry__.mjs');
 const input=JSON.parse(await readFile(inputPath,'utf8'));
 const compatibilityDate=input.compatibility_date||'2026-09-28';
 if(!/^\d{4}-\d{2}-\d{2}$/.test(compatibilityDate)||compatibilityDate<'2021-11-02'||compatibilityDate>'2026-09-28'||!validDate(compatibilityDate))throw new Error('Valid supported compatibility_date required');
 if(input.compatibility_flags!=null&&(!Array.isArray(input.compatibility_flags)||input.compatibility_flags.length>32||input.compatibility_flags.some(flag=>typeof flag!=='string'||!/^[a-z][a-z0-9_]{0,63}$/.test(flag))))throw new Error('Invalid compatibility_flags');
 if(input.compatibility_flags&&new Set(input.compatibility_flags).size!==input.compatibility_flags.length)throw new Error('Duplicate compatibility_flags');
 if(!input.source_url)throw new Error('Source bundle URL missing');
 const response=await fetch(input.source_url,{signal:AbortSignal.timeout(15000)});
 if(!response.ok)throw new Error('Source bundle download failed ('+response.status+')');
 const compressed=await responseBuffer(response,32*1024*1024,'Source bundle download');
 const source=untarSource(gunzipSync(compressed,{maxOutputLength:24*1024*1024}));
 const entries=Object.entries(source.files);
 if(entries.length===0||entries.length>512)throw new Error('Invalid source file count');
 if(!Object.hasOwn(source.files,input.entrypoint))throw new Error('Entrypoint missing');
 const root=sourceRoot;await mkdir(root,{recursive:true});
 let total=0;
 for(const [name,data] of entries){
  if(path.basename(name).startsWith('__krun_')||!safeName(name,96)||!sourceExtensions.has(path.extname(name)))throw new Error('Invalid source path: '+name);
  total+=data.length;if(total>2*1024*1024)throw new Error('Source too large');
  const target=path.join(root,name);await mkdir(path.dirname(target),{recursive:true});await writeFile(target,data);
 }
 const registry=registryURL();
 const dependency=dependencyPlan(Object.fromEntries(entries.map(([name,data])=>[name,data.toString('utf8')])),registry);
 await installDependencies(root,workDir,dependency,registry);
 const assetEntries=Object.entries(source.assets);
 const hasAssets=input.assets!=null;
 if(hasAssets){
  if(assetEntries.length===0)throw new Error('Assets directory is empty');
  if(assetEntries.length>500)throw new Error('Too many asset files');
  let assetTotal=0;
  for(const [name,data] of assetEntries){
   if(!safeName(name,93)||!contentType(name))throw new Error('Unsupported asset type: '+name);
   if(data.length>5*1024*1024)throw new Error('Asset exceeds 5 MiB: '+name);
   assetTotal+=data.length;if(assetTotal>20*1024*1024)throw new Error('Assets exceed 20 MiB');
  }
 }
 const wrapper=`import worker from ${JSON.stringify('./'+input.entrypoint)};\nimport {WorkerEntrypoint} from 'cloudflare:workers';\nexport default worker;\nexport class KrunValidation extends WorkerEntrypoint {ping(){if(!worker || (typeof worker.fetch!=='function'&&typeof worker?.prototype?.fetch!=='function'))throw new Error('Default export must provide fetch');return {environment:this.env.KRUN_ENVIRONMENT,release_id:this.env.KRUN_RELEASE_ID};}}\n`;
 await writeFile(entryStub,wrapper);
 console.log('Profile: workerd-bundle-v2');console.log('Compiling',input.entrypoint);
 const nodeCompat=input.compatibility_flags?.includes('nodejs_compat')===true;
 const result=await build({absWorkingDir:root,entryPoints:['__krun_entry__.mjs'],bundle:true,write:false,format:'esm',platform:'neutral',target:'es2022',mainFields:['workerd','browser','module','main'],conditions:['workerd','worker','browser','import','default'],external:['cloudflare:*'],sourcemap:input.upload_source_maps?'inline':false,logLevel:'warning',plugins:[{name:'source-boundary',setup(build){build.onResolve({filter:/.*/},args=>{if(args.path.startsWith('cloudflare:'))return {path:args.path,external:true};if(bareNodeBuiltins.has(args.path)||nodeSpecifiers.has(args.path))return nodeCompat?{path:args.path,external:true}:{errors:[{text:'Node.js builtin imports require the nodejs_compat compatibility flag'}]};if(args.path.startsWith('node:'))return {errors:[{text:'Unsupported Node.js builtin: '+args.path}]};if(args.kind==='entry-point')return;if(!args.path.startsWith('./')&&!args.path.startsWith('../'))return;const resolved=path.resolve(args.resolveDir,args.path);if(resolved!==root&&!resolved.startsWith(root+path.sep))return {errors:[{text:'Import escapes source directory'}]};});build.onLoad({filter:/.*/},args=>{if(args.namespace!=='file')return;let resolved;try{resolved=realpathSync(args.path);}catch{return {errors:[{text:'Source module cannot be resolved'}]};}if(resolved!==root&&!resolved.startsWith(root+path.sep))return {errors:[{text:'Source access outside project is forbidden'}]};});}}]});
 const module=Buffer.from(result.outputFiles[0].contents);if(module.length>2*1024*1024)throw new Error('Compiled module too large');
 const manifest={schema_version:hasAssets?2:1,profile:'workerd-bundle-v2',compatibility_date:compatibilityDate,compatibility_flags:input.compatibility_flags||[],main_module:'index.js',modules:[{name:'index.js',path:'modules/index.js',type:'esm',sha256:hash(module),size_bytes:module.length}]};
 if(hasAssets)manifest.assets={directory:input.assets.directory,binding:input.assets.binding||'',run_worker_first:input.assets.run_worker_first||[],not_found_handling:input.assets.not_found_handling||'',html_handling:input.assets.html_handling||'',files:assetEntries.map(([name,data])=>({path:name,sha256:hash(data),size_bytes:data.length,content_type:contentType(name)}))};
 const parts=[tarFile('manifest.json',Buffer.from(JSON.stringify(manifest))),tarFile('modules/index.js',module)];
 if(hasAssets)for(const [name,data] of assetEntries)parts.push(tarFile('assets/'+name,data));
 const archive=gzipSync(Buffer.concat([...parts,Buffer.alloc(1024)]),{level:6});
 const upload=await fetch(input.upload_url,{method:'PUT',body:archive,signal:AbortSignal.timeout(15000)});if(!upload.ok)throw new Error('Artifact upload failed ('+upload.status+')');
 console.log('Compiled bytes:',module.length);if(hasAssets)console.log('Assets:',assetEntries.length,'files');
 console.log('Archive SHA-256:',hash(archive));console.log('Uploaded; awaiting platform finalize');
}catch(error){console.error('Build failed:',error.message);process.exitCode=1;}
