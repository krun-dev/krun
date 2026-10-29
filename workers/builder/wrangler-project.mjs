import {createHash} from 'node:crypto';
import {execFile} from 'node:child_process';
import {lstat,mkdir,readFile,readdir,realpath,rm,symlink,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {promisify} from 'node:util';
import {gunzipSync,gzipSync} from 'node:zlib';
import globToRegExp from 'glob-to-regexp';
import ignore from 'ignore';
import {parse as parseJSONC,printParseErrorCode} from 'jsonc-parser';
import {parse as parseTOML} from 'smol-toml';

const execute=promisify(execFile);
const WRANGLER_VERSION='4.155.0';
const BUILDER_ROOT=path.dirname(fileURLToPath(import.meta.url));
const WRANGLER_BIN=path.join(BUILDER_ROOT,'node_modules','.bin','wrangler');
const TOOL_VERSIONS={npm:'11.21.1',pnpm:'12.16.1',yarnClassic:'1.22.22',yarnBerry:'4.18.1',bun:'1.4.4'};
const rawCommandTimeout=Number.parseInt(process.env.KRUN_BUILD_TIMEOUT_MS||'540000',10);
const COMMAND_TIMEOUT_MS=Number.isSafeInteger(rawCommandTimeout)&&rawCommandTimeout>=30000&&rawCommandTimeout<=1770000?rawCommandTimeout:540000;
const SOURCE_COMPRESSED_MAX=32*1024*1024;
const SOURCE_EXPANDED_MAX=24*1024*1024;
const SOURCE_ARCHIVE_EXPANDED_MAX=32*1024*1024;
const SOURCE_FILE_MAX=8*1024*1024;
const SOURCE_FILES_MAX=4096;
const MODULE_FILE_MAX=2*1024*1024;
const MODULES_TOTAL_MAX=8*1024*1024;
const MODULES_MAX=512;
const ASSET_FILE_MAX=5*1024*1024;
const ASSETS_TOTAL_MAX=20*1024*1024;
const ASSETS_MAX=500;
const DEFAULT_RULES=[
 {type:'Text',globs:['**/*.txt','**/*.html','**/*.sql']},
 {type:'Data',globs:['**/*.bin']},
 {type:'CompiledWasm',globs:['**/*.wasm','**/*.wasm?module']}
];
const MODULE_TYPES={ESModule:'esm',CommonJS:'commonjs',CompiledWasm:'wasm',Text:'text',Data:'data'};
const hash=raw=>createHash('sha256').update(raw).digest('hex');
const within=(root,target)=>target===root||target.startsWith(root+path.sep);
const byteLength=value=>Buffer.byteLength(value,'utf8');
const safeRelative=(name,maxBytes=240)=>typeof name==='string'&&name!==''&&byteLength(name)<=maxBytes&&!name.startsWith('/')&&!name.startsWith('../')&&!name.includes('\\')&&path.posix.normalize(name)===name&&!name.split('/').some(part=>part===''||part==='.'||part==='..')&&![...name].some(char=>{const code=char.codePointAt(0);return code<0x20||code===0x7f;});
const validDate=value=>{const date=new Date(value+'T00:00:00Z');return typeof value==='string'&&/^\d{4}-\d{2}-\d{2}$/.test(value)&&!Number.isNaN(date.valueOf())&&date.toISOString().slice(0,10)===value&&value>='2021-11-02'&&value<='2026-09-28';};

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

async function responseBuffer(response,limit,label){
 const declared=response.headers.get('content-length');
 if(declared!==null&&(!/^\d+$/.test(declared)||BigInt(declared)>BigInt(limit))){try{await response.body?.cancel();}catch{}throw new Error(label+' is too large');}
 if(!response.body)return Buffer.alloc(0);
 const reader=response.body.getReader(),chunks=[];let total=0;
 while(true){
  const {done,value}=await reader.read();if(done)break;
  total+=value.byteLength;if(total>limit){try{await reader.cancel();}catch{}throw new Error(label+' is too large');}
  chunks.push(Buffer.from(value.buffer,value.byteOffset,value.byteLength));
 }
 return Buffer.concat(chunks,total);
}

function parseOctal(header,start,end,label){
 const text=header.subarray(start,end).toString('ascii').replace(/\0.*$/,'').trim();
 if(!/^[0-7]+$/.test(text))throw new Error('Source bundle has an invalid '+label);
 return Number.parseInt(text,8);
}

export function unpackWranglerSource(raw){
 let expanded;
 try{expanded=gunzipSync(raw,{maxOutputLength:SOURCE_ARCHIVE_EXPANDED_MAX});}catch{throw new Error('Source bundle is not valid gzip or exceeds archive limits');}
 const files=new Map(),seen=new Set();let offset=0,total=0,terminated=false;
 while(offset+512<=expanded.length){
  const header=expanded.subarray(offset,offset+512),base=header.subarray(0,100).toString('utf8').replace(/\0.*$/,''),prefix=header.subarray(345,500).toString('utf8').replace(/\0.*$/,'');
  if(!base){terminated=true;break;}
  const archiveName=prefix?prefix+'/'+base:base,size=parseOctal(header,124,136,'size'),expected=parseOctal(header,148,156,'checksum');
  let actual=0;for(let index=0;index<512;index++)actual+=index>=148&&index<156?32:header[index];
  const end=offset+512+size,next=offset+512+Math.ceil(size/512)*512;
  if(actual!==expected||header[156]!==48||end>expanded.length||next>expanded.length)throw new Error('Source bundle may only contain portable regular files');
  if(!archiveName.startsWith('src/'))throw new Error('Wrangler source bundle entries must be below src/');
  const name=archiveName.slice(4);
  if(!safeRelative(name,240)||seen.has(name)||files.size>=SOURCE_FILES_MAX||size>SOURCE_FILE_MAX)throw new Error('Wrangler source bundle contains an unsafe, duplicate, or oversized file');
  const data=Buffer.from(expanded.subarray(offset+512,end));total+=data.length;if(total>SOURCE_EXPANDED_MAX)throw new Error('Wrangler source bundle exceeds 24 MiB');
  files.set(name,data);seen.add(name);offset=next;
 }
 if(!terminated||expanded.subarray(offset).some(byte=>byte!==0)||files.size===0)throw new Error('Source bundle has no valid tar terminator or project files');
 return files;
}

async function writeProject(files,root){
 await rm(root,{recursive:true,force:true});await mkdir(root,{recursive:true});
 for(const [name,data] of files){const target=path.join(root,...name.split('/'));await mkdir(path.dirname(target),{recursive:true});await writeFile(target,data,{mode:0o600});}
}

function configObject(raw,name){
 try{
  if(name.endsWith('.toml'))return parseTOML(raw);
  const errors=[];const value=parseJSONC(raw,errors,{allowTrailingComma:true,disallowComments:false});
  if(errors.length)throw new Error(printParseErrorCode(errors[0].error));
  return value;
 }catch(error){throw new Error('Invalid Wrangler configuration '+name+': '+error.message);}
}

async function readRegularInside(root,target,label){
 const absolute=path.resolve(target);if(!within(root,absolute))throw new Error(label+' escapes the project');
 const info=await lstat(absolute);if(!info.isFile()||info.isSymbolicLink())throw new Error(label+' must be a regular file');
 const canonical=await realpath(absolute);if(!within(root,canonical))throw new Error(label+' escapes the project');
 return readFile(canonical);
}

export async function resolveWranglerConfig(projectRoot,requested){
 if(!safeRelative(requested,240)||!['wrangler.jsonc','wrangler.json','wrangler.toml'].includes(path.posix.basename(requested)))throw new Error('config_path must identify a Wrangler JSONC, JSON, or TOML file');
 let relative=requested,absolute=path.resolve(projectRoot,...requested.split('/'));
 await readRegularInside(projectRoot,absolute,'Wrangler config');
 const redirect=path.join(projectRoot,'.wrangler','deploy','config.json');
 try{
  const redirectRaw=await readRegularInside(projectRoot,redirect,'Generated Wrangler config redirect');
  const parsed=JSON.parse(redirectRaw.toString('utf8'));
  const redirectKeys=parsed&&typeof parsed==='object'&&!Array.isArray(parsed)?Object.keys(parsed):[];
  if(!parsed||typeof parsed.configPath!=='string'||redirectKeys.some(key=>!['configPath','auxiliaryWorkers'].includes(key))||parsed.auxiliaryWorkers!==undefined&&(!Array.isArray(parsed.auxiliaryWorkers)||parsed.auxiliaryWorkers.length!==0))throw new Error('Generated Wrangler config redirect is invalid or declares unsupported auxiliary Workers');
  absolute=path.resolve(path.dirname(redirect),parsed.configPath);
  if(!within(projectRoot,absolute))throw new Error('Generated Wrangler config escapes the project');
  relative=path.relative(projectRoot,absolute).split(path.sep).join('/');
  if(!safeRelative(relative,240)||!['.json','.jsonc','.toml'].includes(path.extname(relative)))throw new Error('Generated Wrangler config path is invalid');
  await readRegularInside(projectRoot,absolute,'Generated Wrangler config');
 }catch(error){if(error?.code!=='ENOENT')throw error;}
 const raw=await readRegularInside(projectRoot,absolute,'Wrangler config'),config=configObject(raw.toString('utf8'),relative);
 if(!config||typeof config!=='object'||Array.isArray(config))throw new Error('Wrangler config must be an object');
 if(typeof config.main==='string'&&config.main.endsWith('.py')||Array.isArray(config.compatibility_flags)&&config.compatibility_flags.includes('python_workers'))throw new Error('Python Workers are not supported');
 return {absolute,relative,config};
}

function normalizeRules(userRules){
 if(userRules==null)userRules=[];
 if(!Array.isArray(userRules)||userRules.length>64)throw new Error('Wrangler rules must be an array of at most 64 entries');
 const all=[];
 for(const raw of [...userRules,...DEFAULT_RULES]){
  if(!raw||typeof raw!=='object'||!MODULE_TYPES[raw.type]||!Array.isArray(raw.globs)||raw.globs.length===0||raw.globs.length>64||raw.globs.some(glob=>typeof glob!=='string'||glob===''||glob.length>256))throw new Error('Unsupported Wrangler module rule');
  all.push({type:raw.type,globs:[...raw.globs],fallthrough:raw.fallthrough===true});
 }
 const completed=new Set(),effective=[];
 for(const rule of all){if(completed.has(rule.type))continue;effective.push(rule);if(!rule.fallthrough)completed.add(rule.type);}
 return effective;
}

const unhashedModuleName=name=>name.split('/').map(part=>part.replace(/^[0-9a-f]{40}-/,'')).join('/');

export function classifyWranglerModule(name,userRules=[]){
 if(name.endsWith('.map'))return 'source-map';
 for(const candidate of [name,unhashedModuleName(name)])for(const rule of normalizeRules(userRules))for(const glob of rule.globs){
  let matches=false;try{matches=globToRegExp(glob,{globstar:true}).test(candidate);}catch{throw new Error('Invalid Wrangler module rule glob: '+glob);}
  if(matches)return MODULE_TYPES[rule.type];
 }
 return '';
}

function normalizeCompatibility(input,config){
 const date=input.compatibility_date||config.compatibility_date,flags=input.compatibility_flags??config.compatibility_flags??[];
 if(!validDate(date))throw new Error('Valid supported compatibility_date required');
 if(!Array.isArray(flags)||flags.length>32||flags.some(flag=>typeof flag!=='string'||!/^[a-z][a-z0-9_]{0,63}$/.test(flag))||new Set(flags).size!==flags.length)throw new Error('Invalid compatibility_flags');
 return {date,flags:[...flags]};
}

function registryURL(){
 const registry=new URL(process.env.KRUN_NPM_REGISTRY||'https://registry.npmjs.org/');
 if(registry.username||registry.password||registry.protocol!=='https:')throw new Error('NPM registry must be HTTPS without embedded credentials');
 if(!registry.pathname.endsWith('/'))registry.pathname+='/';registry.search='';registry.hash='';return registry;
}

async function projectPackage(projectRoot){
 try{const value=JSON.parse((await readRegularInside(projectRoot,path.join(projectRoot,'package.json'),'package.json')).toString('utf8'));if(!value||typeof value!=='object'||Array.isArray(value))throw new Error('must be an object');return value;}
 catch(error){if(error?.code==='ENOENT')return null;throw new Error('package.json is not valid JSON: '+error.message);}
}

async function regularExists(projectRoot,name){
 try{await readRegularInside(projectRoot,path.join(projectRoot,name),name);return true;}catch(error){if(error?.code==='ENOENT')return false;throw error;}
}

export async function detectManager(projectRoot,pkg){
 const candidates=[];
 if(await regularExists(projectRoot,'package-lock.json'))candidates.push({name:'npm',lock:'package-lock.json',version:TOOL_VERSIONS.npm});
 if(await regularExists(projectRoot,'pnpm-lock.yaml'))candidates.push({name:'pnpm',lock:'pnpm-lock.yaml',version:TOOL_VERSIONS.pnpm});
 if(await regularExists(projectRoot,'yarn.lock')){
  const lock=await readFile(path.join(projectRoot,'yarn.lock'),'utf8'),berry=/^__metadata:\s*$/m.test(lock);
  candidates.push({name:'yarn',flavor:berry?'berry':'classic',lock:'yarn.lock',version:berry?TOOL_VERSIONS.yarnBerry:TOOL_VERSIONS.yarnClassic});
 }
 const bunText=await regularExists(projectRoot,'bun.lock'),bunBinary=await regularExists(projectRoot,'bun.lockb');
 if(bunText&&bunBinary)throw new Error('Only one Bun lockfile is allowed');
 if(bunText||bunBinary)candidates.push({name:'bun',lock:bunText?'bun.lock':'bun.lockb',version:TOOL_VERSIONS.bun});
 if(candidates.length>1)throw new Error('Exactly one package-manager lockfile is allowed');
 const requested=typeof pkg?.packageManager==='string'?pkg.packageManager.split('@')[0]:'';
 if(requested&&candidates.length&&requested!==candidates[0].name)throw new Error('packageManager does not match '+candidates[0].lock);
 if(requested&&!['npm','pnpm','yarn','bun'].includes(requested))throw new Error('Unsupported packageManager: '+requested);
 return candidates[0]||null;
}

export function managerInvocation(manager,action){
 if(manager.name==='npm')return {file:path.join(BUILDER_ROOT,'node_modules','.bin','npm'),args:action==='install'?['ci','--no-audit','--no-fund']:['run','build']};
 if(manager.name==='pnpm')return {file:path.join(BUILDER_ROOT,'node_modules','.bin','pnpm'),args:action==='install'?['install','--frozen-lockfile','--reporter=append-only']:['run','build']};
 if(manager.name==='yarn'){
  const cli=manager.flavor==='berry'?path.join(BUILDER_ROOT,'node_modules','@yarnpkg','cli-dist','bin','yarn.js'):path.join(BUILDER_ROOT,'node_modules','yarn','bin','yarn.js');
  return {file:process.execPath,args:[cli,...action==='install'?(manager.flavor==='berry'?['install','--immutable']:['install','--frozen-lockfile','--non-interactive']):['run','build']]};
 }
 if(manager.name==='bun')return {file:path.join(BUILDER_ROOT,'node_modules','.bin','bun'),args:action==='install'?['install','--frozen-lockfile']:['run','build']};
 throw new Error('Unsupported package manager');
}

async function installProject(projectRoot,workRoot,pkg){
 if(!pkg)return null;
 const hasDependencies=['dependencies','devDependencies','optionalDependencies'].some(key=>pkg[key]&&Object.keys(pkg[key]).length>0);
 const manager=await detectManager(projectRoot,pkg),needsInstall=hasDependencies||typeof pkg.scripts?.build==='string';
 if(!manager&&!needsInstall)return null;
 if(!manager)throw new Error('Cloudflare Worker projects with dependencies or build scripts require exactly one lockfile');
 if(manager.name==='npm'){
  const lock=JSON.parse((await readFile(path.join(projectRoot,manager.lock))).toString('utf8'));if(![2,3].includes(lock?.lockfileVersion))throw new Error('Cloudflare Worker projects require package-lock.json v2 or v3');
 }
 const {home,cache,userConfig,globalConfig}=await prepareBuildEnvironment(workRoot),registry=registryURL(),env={...sanitizedBuildEnv(workRoot),HOME:home,npm_config_cache:cache};
 const invocation=managerInvocation(manager,'install');if(manager.name==='npm')invocation.args.push('--userconfig='+userConfig,'--globalconfig='+globalConfig,'--registry='+registry.href);if(manager.name==='yarn'&&manager.flavor==='classic')invocation.args.push('--registry',registry.href);if(manager.name==='bun')invocation.args.push('--registry',registry.href);
 const {stdout,stderr}=await execute(invocation.file,invocation.args,{cwd:projectRoot,timeout:COMMAND_TIMEOUT_MS,maxBuffer:4*1024*1024,env});if(stdout.trim())console.log(stdout.trim());if(stderr.trim())console.warn(stderr.trim());
 console.log('Package manager:',manager.name,manager.version);return manager;
}

async function prepareBuildEnvironment(workRoot){
 const home=path.join(workRoot,'package-home'),cache=path.join(workRoot,'package-cache'),wranglerHome=path.join(workRoot,'wrangler-home');await mkdir(home,{recursive:true});await mkdir(cache,{recursive:true});await mkdir(wranglerHome,{recursive:true});await mkdir(path.join(workRoot,'tmp'),{recursive:true});
 const userConfig=path.join(home,'user.npmrc'),globalConfig=path.join(home,'global.npmrc');await writeFile(userConfig,'',{mode:0o600});await writeFile(globalConfig,'',{mode:0o600});return {home,cache,userConfig,globalConfig};
}

function sanitizedBuildEnv(workRoot){
 const home=path.join(workRoot,'wrangler-home'),packageHome=path.join(workRoot,'package-home'),registry=registryURL();
 const ca=process.env.NODE_EXTRA_CA_CERTS;
 if(ca&&(!path.isAbsolute(ca)||ca.includes('\0')))throw new Error('NODE_EXTRA_CA_CERTS must be an absolute path');
 return {PATH:[path.join(BUILDER_ROOT,'node_modules','.bin'),path.dirname(process.execPath),'/usr/local/bin','/usr/bin','/bin'].join(':'),HOME:home,XDG_CONFIG_HOME:path.join(home,'config'),TMPDIR:path.join(workRoot,'tmp'),CI:'1',WRANGLER_SEND_METRICS:'false',WRANGLER_LOG_PATH:path.join(home,'wrangler.log'),npm_config_cache:path.join(workRoot,'package-cache'),npm_config_userconfig:path.join(packageHome,'user.npmrc'),npm_config_globalconfig:path.join(packageHome,'global.npmrc'),npm_config_update_notifier:'false',npm_config_registry:registry.href,YARN_NPM_REGISTRY_SERVER:registry.href.replace(/\/$/,''),BUN_CONFIG_REGISTRY:registry.href,YARN_IGNORE_PATH:'1',NO_COLOR:'1',FORCE_COLOR:'0',NO_PROXY:'*',no_proxy:'*',...(ca?{NODE_EXTRA_CA_CERTS:ca}:{})};
}

function rejectCloudflareDeploy(command,label){
 if(typeof command!=='string')return;
 const deploy=/\bwrangler(?:@[a-zA-Z0-9._-]+)?\s+(?:deploy|versions\s+(?:upload|deploy))\b/i.test(command);
 if(deploy&&!/(?:^|\s)--dry-run(?:\s|$)/.test(command))throw new Error(label+' must not deploy to Cloudflare; use a build-only command or Wrangler --dry-run');
}

async function runPackageBuild(projectRoot,workRoot,pkg,config,manager){
 if(config.build&&typeof config.build.command==='string'&&config.build.command.trim()!=='')return;
 if(!pkg?.scripts||typeof pkg.scripts.build!=='string'||pkg.scripts.build.trim()==='')return;
 if(!manager)throw new Error('A package lockfile is required to run the project build script');const invocation=managerInvocation(manager,'build'),{stdout,stderr}=await execute(invocation.file,invocation.args,{cwd:projectRoot,timeout:COMMAND_TIMEOUT_MS,maxBuffer:4*1024*1024,env:sanitizedBuildEnv(workRoot)});
 if(stdout.trim())console.log(stdout.trim());if(stderr.trim())console.warn(stderr.trim());
}

async function runWranglerCLI(projectRoot,workRoot,configPath,input){
 const output=path.join(workRoot,'wrangler-output'),metafile=path.join(workRoot,'wrangler-metafile.json');
 await rm(output,{recursive:true,force:true});await rm(metafile,{force:true});await mkdir(output,{recursive:true});await mkdir(path.join(workRoot,'tmp'),{recursive:true});await mkdir(path.join(workRoot,'wrangler-home'),{recursive:true});
 const args=['deploy','--dry-run','--no-autoconfig','--containers-rollout=none','--outdir',output,'--metafile',metafile,'--config',configPath];
 if(input.compatibility_date)args.push('--compatibility-date',input.compatibility_date);for(const flag of input.compatibility_flags||[])args.push('--compatibility-flags',flag);
 const binary=process.env.KRUN_WRANGLER_BIN||WRANGLER_BIN,{stdout,stderr}=await execute(binary,args,{cwd:projectRoot,timeout:COMMAND_TIMEOUT_MS,maxBuffer:4*1024*1024,env:sanitizedBuildEnv(workRoot)});
 if(stdout.trim())console.log(stdout.trim());if(stderr.trim())console.warn(stderr.trim());return {output,metafile};
}

function tarFile(name,bytes){
 let base=name,prefix='';
 if(Buffer.byteLength(base,'utf8')>100){
  const parts=name.split('/');
  while(parts.length>1&&Buffer.byteLength(parts.join('/'),'utf8')>100)prefix+=(prefix?'/':'')+parts.shift();
  base=parts.join('/');
 }
 if(Buffer.byteLength(base,'utf8')>100||Buffer.byteLength(prefix,'utf8')>155)throw new Error('Archive path exceeds portable ustar limit: '+name);
 const header=Buffer.alloc(512);header.write(base,0,100,'utf8');if(prefix)header.write(prefix,345,155,'utf8');const octal=(offset,length,n)=>header.write(n.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((n,b)=>n+b,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}

async function walkRegular(root,{maxFiles,maxTotal,maxFile,label,maxName}){
 const rootInfo=await lstat(root);if(!rootInfo.isDirectory()||rootInfo.isSymbolicLink()||await realpath(root)!==path.resolve(root))throw new Error(label+' root must be a real directory');
 const pending=[''],files=[];let total=0;
 while(pending.length){const relative=pending.pop(),directory=path.join(root,...relative.split('/').filter(Boolean)),entries=await readdir(directory,{withFileTypes:true});entries.sort((a,b)=>a.name.localeCompare(b.name));
  for(const entry of entries){const name=relative?relative+'/'+entry.name:entry.name,target=path.join(directory,entry.name);if(!safeRelative(name,maxName))throw new Error(label+' path is unsafe or too long: '+name);const info=await lstat(target);if(info.isSymbolicLink())throw new Error(label+' tree contains a symbolic link: '+name);if(info.isDirectory()){pending.push(name);continue;}if(!info.isFile())throw new Error(label+' tree contains a non-regular file: '+name);if(info.size>maxFile||files.length>=maxFiles)throw new Error(label+' tree exceeds file limits');total+=info.size;if(total>maxTotal)throw new Error(label+' tree exceeds total size limit');files.push({name,path:target,size:info.size});}}
 files.sort((a,b)=>a.name.localeCompare(b.name));return files;
}

async function mainOutputName(projectRoot,outputRoot,metafile,config){
 try{const meta=JSON.parse((await readRegularInside(path.dirname(metafile),metafile,'Wrangler metafile')).toString('utf8')),names=[];
  for(const [output,details] of Object.entries(meta.outputs||{}))if(details&&typeof details.entryPoint==='string'){
   const normalized=output.split(path.sep).join('/'),marker='/'+path.basename(outputRoot)+'/',marked=normalized.lastIndexOf(marker);let relative='';
   if(marked>=0)relative=normalized.slice(marked+marker.length);
   else{const absolute=path.resolve(projectRoot,output);if(within(outputRoot,absolute))relative=path.relative(outputRoot,absolute).split(path.sep).join('/');}
   if(!safeRelative(relative,240))throw new Error('Wrangler metafile entry escapes output directory');await readRegularInside(outputRoot,path.join(outputRoot,...relative.split('/')),'Wrangler main output');names.push(relative);
  }
  if(names.length===1&&safeRelative(names[0],240))return names[0];
 }catch(error){if(!config.no_bundle)throw new Error('Wrangler metafile is invalid: '+error.message);}
 if(typeof config.main!=='string'||!safeRelative(config.main,240))throw new Error('Unable to determine Wrangler main module');const original=path.posix.basename(config.main),base=config.no_bundle?original:original.replace(/\.(?:[cm]?[jt]sx?)$/i,'.js');if(!safeRelative(base,240))throw new Error('Wrangler main module name is invalid');return base;
}

function wrapperFor(userName){
 const specifier='./'+path.posix.basename(userName);return Buffer.from(`import worker from ${JSON.stringify(specifier)};\nexport * from ${JSON.stringify(specifier)};\nimport {WorkerEntrypoint} from 'cloudflare:workers';\nexport default worker;\nexport class KrunValidation extends WorkerEntrypoint {ping(){if(!worker || (typeof worker.fetch!=='function'&&typeof worker?.prototype?.fetch!=='function'))throw new Error('Default export must provide fetch');return {environment:this.env.KRUN_ENVIRONMENT,release_id:this.env.KRUN_RELEASE_ID};}}\n`);
}

const assetsOnlyWorker=()=>Buffer.from(`import {WorkerEntrypoint} from 'cloudflare:workers';\nconst worker={fetch(){return new Response('Not Found',{status:404});}};\nexport default worker;\nexport class KrunValidation extends WorkerEntrypoint {ping(){return {environment:this.env.KRUN_ENVIRONMENT,release_id:this.env.KRUN_RELEASE_ID};}}\n`);

function normalizeAssets(config,input,projectRoot,configPath){
 const value=input.assets??config.assets;if(value==null)return null;
 if(!value||typeof value!=='object'||Array.isArray(value)||typeof value.directory!=='string'||value.directory===''||path.isAbsolute(value.directory)||value.directory.includes('\\'))throw new Error('Wrangler assets.directory must be a safe relative directory');
 const assetRoot=path.resolve(input.assets?projectRoot:path.dirname(configPath),value.directory);if(!within(projectRoot,assetRoot)||assetRoot===projectRoot)throw new Error('Assets directory escapes project');
 const directory=path.relative(projectRoot,assetRoot).split(path.sep).join('/');if(!safeRelative(directory,240))throw new Error('Wrangler assets.directory must be a safe relative directory');
 let run=value.run_worker_first??[];if(run===true)run=['/*'];else if(run===false)run=[];if(!Array.isArray(run)||run.length>16||run.some(item=>typeof item!=='string'||item.length>128))throw new Error('Invalid assets.run_worker_first');
 return {directory,binding:typeof value.binding==='string'?value.binding:'',run_worker_first:run,not_found_handling:typeof value.not_found_handling==='string'?value.not_found_handling:'',html_handling:typeof value.html_handling==='string'?value.html_handling:''};
}

async function collectAssets(projectRoot,assets){
 if(!assets)return [];
 const root=path.resolve(projectRoot,assets.directory);if(!within(projectRoot,root))throw new Error('Assets directory escapes project');
 const files=await walkRegular(root,{maxFiles:ASSETS_MAX,maxTotal:ASSETS_TOTAL_MAX,maxFile:ASSET_FILE_MAX,label:'asset',maxName:240}),matcher=ignore();
 const ignoreFile=files.find(file=>file.name==='.assetsignore');
 if(ignoreFile){
  if(ignoreFile.size>64*1024)throw new Error('.assetsignore exceeds 64 KiB');
  let rules;try{rules=new TextDecoder('utf-8',{fatal:true}).decode(await readFile(ignoreFile.path));}catch{throw new Error('.assetsignore must be valid UTF-8');}
  try{matcher.add(rules);}catch(error){throw new Error('Invalid .assetsignore: '+error.message);}
 }
 const included=files.filter(file=>file.name!=='.assetsignore'&&!matcher.ignores(file.name));if(included.length===0)throw new Error('Assets directory is empty');
 return Promise.all(included.map(async file=>{const type=contentType(file.name);if(!type)throw new Error('Unsupported asset type: '+file.name);const data=await readFile(file.path);return {...file,data,content_type:type};}));
}

export async function packWranglerProject(input,workRoot){
 if(!input.upload_url)throw new Error('Artifact upload URL missing');
 const canonicalWorkRoot=await realpath(workRoot),projectRoot=path.join(canonicalWorkRoot,'src'),outputRoot=path.join(canonicalWorkRoot,'wrangler-output'),metafile=path.join(canonicalWorkRoot,'wrangler-metafile.json'),{absolute:configPath,relative:effectiveConfigPath,config}=await resolveWranglerConfig(projectRoot,input.config_path);
 let buildResult;try{buildResult=JSON.parse((await readRegularInside(canonicalWorkRoot,path.join(canonicalWorkRoot,'build-result.json'),'Wrangler build result')).toString('utf8'));}catch(error){throw new Error('Wrangler build result is missing or invalid: '+error.message);}
 if(!buildResult||buildResult.schema_version!==1||buildResult.wrangler_version!==WRANGLER_VERSION||buildResult.config_path!==effectiveConfigPath||buildResult.output!=='wrangler-output'||buildResult.metafile!=='wrangler-metafile.json')throw new Error('Wrangler build result does not match the trusted pack profile');
 const assets=normalizeAssets(config,input,projectRoot,configPath),assetsOnly=typeof config.main!=='string'||config.main==='',compatibility=normalizeCompatibility(input,config),mainName=assetsOnly?'index.js':await mainOutputName(projectRoot,outputRoot,metafile,config),outputFiles=await walkRegular(outputRoot,{maxFiles:MODULES_MAX,maxTotal:MODULES_TOTAL_MAX,maxFile:MODULE_FILE_MAX,label:'module',maxName:240}),uploadMaps=config.upload_source_maps===true,rawModules=[];
 for(const file of outputFiles){if(file.name==='README.md'||assetsOnly||file.name.endsWith('.map')&&!uploadMaps)continue;const data=await readFile(file.path);let type=file.name===mainName?(String(config.main||'').endsWith('.cjs')?'commonjs':'esm'):classifyWranglerModule(file.name,config.rules);if(!type)throw new Error('Unable to classify Wrangler output module: '+file.name);if(type==='source-map'){try{JSON.parse(data.toString('utf8'));}catch{throw new Error('Wrangler emitted an invalid source map: '+file.name);}}rawModules.push({name:file.name,type,data});}
 let modules,wrapperName=mainName;
 if(assetsOnly){if(!assets)throw new Error('Wrangler project has neither a main module nor assets');modules=[{name:wrapperName,type:'esm',data:assetsOnlyWorker()},...rawModules];}
 else{const main=rawModules.find(module=>module.name===mainName);if(!main)throw new Error('Wrangler main module is missing');if(main.type!=='esm')throw new Error('Legacy Service Worker/CommonJS entrypoints are not supported');const directory=path.posix.dirname(mainName),userBase='__krun_user_'+path.posix.basename(mainName),userName=directory==='.'?userBase:directory+'/'+userBase;if(!safeRelative(userName,240)||rawModules.some(module=>module.name===userName))throw new Error('Unable to reserve the platform wrapper module name');main.name=userName;modules=[{name:wrapperName,type:'esm',data:wrapperFor(userName)},...rawModules];}
 if(modules.length>MODULES_MAX||modules.reduce((sum,module)=>sum+module.data.length,0)>MODULES_TOTAL_MAX)throw new Error('Wrangler output exceeds module limits');modules.sort((a,b)=>a.name===wrapperName?-1:b.name===wrapperName?1:a.name.localeCompare(b.name));
 const assetFiles=await collectAssets(projectRoot,assets),manifest={schema_version:assets?2:1,profile:'workerd-bundle-v2',compatibility_date:compatibility.date,compatibility_flags:compatibility.flags,main_module:wrapperName,modules:modules.map(module=>({name:module.name,path:'modules/'+module.name,type:module.type,sha256:hash(module.data),size_bytes:module.data.length}))};
 if(assets)manifest.assets={...assets,files:assetFiles.map(file=>({path:file.name,sha256:hash(file.data),size_bytes:file.data.length,content_type:file.content_type}))};
 const parts=[tarFile('manifest.json',Buffer.from(JSON.stringify(manifest)))];for(const module of modules)parts.push(tarFile('modules/'+module.name,module.data));for(const file of assetFiles)parts.push(tarFile('assets/'+file.name,file.data));const archive=gzipSync(Buffer.concat([...parts,Buffer.alloc(1024)]),{level:6});
 const upload=await fetch(input.upload_url,{method:'PUT',body:archive,signal:AbortSignal.timeout(15000)});if(!upload.ok)throw new Error('Artifact upload failed ('+upload.status+')');
 console.log('Wrangler:',WRANGLER_VERSION);console.log('Modules:',modules.length,'files,',modules.reduce((sum,module)=>sum+module.data.length,0),'bytes');if(assets)console.log('Assets:',assetFiles.length,'files');console.log('Archive SHA-256:',hash(archive));console.log('Uploaded; awaiting platform finalize');return {archive,manifest};
}

export async function buildWranglerProject(input,workRoot){
 if(Object.hasOwn(input,'upload_url'))throw new Error('Wrangler build input must not contain upload_url');if(!input.source_url)throw new Error('Source bundle URL missing');
 await mkdir(workRoot,{recursive:true});const canonicalWorkRoot=await realpath(workRoot),projectRoot=path.join(canonicalWorkRoot,'src'),response=await fetch(input.source_url,{signal:AbortSignal.timeout(15000)});if(!response.ok)throw new Error('Source bundle download failed ('+response.status+')');const compressed=await responseBuffer(response,SOURCE_COMPRESSED_MAX,'Source bundle download'),files=unpackWranglerSource(compressed);await writeProject(files,projectRoot);
 const initial=await resolveWranglerConfig(projectRoot,input.config_path);normalizeCompatibility(input,initial.config);const pkg=await projectPackage(projectRoot);rejectCloudflareDeploy(pkg?.scripts?.build,'package.json build script');rejectCloudflareDeploy(initial.config.build?.command,'Wrangler custom build command');const manager=await installProject(projectRoot,canonicalWorkRoot,pkg);await prepareBuildEnvironment(canonicalWorkRoot);await runPackageBuild(projectRoot,canonicalWorkRoot,pkg,initial.config,manager);
 const effectiveBefore=await resolveWranglerConfig(projectRoot,input.config_path),result=await runWranglerCLI(projectRoot,canonicalWorkRoot,effectiveBefore.absolute,input),effective=await resolveWranglerConfig(projectRoot,input.config_path);normalizeCompatibility(input,effective.config);
 await writeFile(path.join(canonicalWorkRoot,'build-result.json'),JSON.stringify({schema_version:1,wrangler_version:WRANGLER_VERSION,config_path:effective.relative,output:path.basename(result.output),metafile:path.basename(result.metafile)}),{mode:0o600});console.log('Wrangler build complete:',WRANGLER_VERSION);
}

export async function runWranglerMode(input,_inputPath,workRoot,phase){
 if(input.build_mode!=='wrangler')throw new Error('Invalid Wrangler build mode');if(phase==='build')return buildWranglerProject(input,workRoot);if(phase==='pack')return packWranglerProject(input,workRoot);if(phase==='all'){if(!input.upload_url)throw new Error('Artifact upload URL missing');const buildInput={...input};delete buildInput.upload_url;await buildWranglerProject(buildInput,workRoot);return packWranglerProject(input,workRoot);}throw new Error('KRUN_BUILD_PHASE must be build, pack, or all');
}

export {WRANGLER_VERSION};
