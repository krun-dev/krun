import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtempSync,mkdirSync,readFileSync,realpathSync,rmSync,writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {gunzipSync,gzipSync} from 'node:zlib';
import {WRANGLER_VERSION,classifyWranglerModule,detectManager,managerInvocation,unpackWranglerSource} from '../wrangler-project.mjs';

function tarFile(name,bytes){
 let base=name,prefix='';if(Buffer.byteLength(base)>100){const parts=name.split('/');while(parts.length>1&&Buffer.byteLength(parts.join('/'))>100)prefix+=(prefix?'/':'')+parts.shift();base=parts.join('/');}
 const header=Buffer.alloc(512);header.write(base,0,100,'utf8');if(prefix)header.write(prefix,345,155,'utf8');
 const octal=(offset,length,value)=>header.write(value.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((total,byte)=>total+byte,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}

function archiveEntries(compressed){
 const raw=gunzipSync(compressed),entries={};
 for(let offset=0;offset+512<=raw.length;){
  const header=raw.subarray(offset,offset+512),base=header.subarray(0,100).toString('utf8').replace(/\0.*$/,''),prefix=header.subarray(345,500).toString('utf8').replace(/\0.*$/,'');if(!base)break;
  const name=prefix?prefix+'/'+base:base,size=Number.parseInt(header.subarray(124,136).toString('ascii').replace(/[\0 ].*$/,'').trim(),8)||0;entries[name]=raw.subarray(offset+512,offset+512+size);offset+=512+Math.ceil(size/512)*512;
 }
 return entries;
}

async function runBuilder(here,inputPath,work,phase){
 return new Promise(resolve=>{
  const child=spawn(process.execPath,[path.join(here,'..','build-worker.mjs')],{cwd:path.join(here,'..'),env:{...process.env,KRUN_BUILD_INPUT:inputPath,KRUN_BUILD_WORK:work,KRUN_BUILD_PHASE:phase,KRUN_WRANGLER_BIN:path.join(here,'..','node_modules','.bin','wrangler')},stdio:['ignore','pipe','pipe']});
  let output='';child.stdout.on('data',data=>{output+=data;});child.stderr.on('data',data=>{output+=data;});child.once('close',code=>resolve({code,output}));
 });
}

assert.equal(WRANGLER_VERSION,'4.155.0');
assert.equal(classifyWranglerModule('f572d396fae9206628714fb2ce00f72e94f2258f-message.html',[{type:'Text',globs:['message.html']}]),'text');
assert.equal(classifyWranglerModule('nested/c5d84736ba451747dd5f0eb9d17e104f3697ef47-payload.bin',[{type:'Data',globs:['nested/payload.bin']}]),'data');
const longSource='nested/'.repeat(18)+'entry.js',longArchive=gzipSync(Buffer.concat([tarFile('src/'+longSource,Buffer.from('export default {}')),Buffer.alloc(1024)]));assert.equal(unpackWranglerSource(longArchive).get(longSource).toString('utf8'),'export default {}');

const here=path.dirname(fileURLToPath(import.meta.url)),scratch=realpathSync(mkdtempSync(path.join(tmpdir(),'krun-wrangler-builder-'))),work=path.join(scratch,'work'),inputDir=path.join(scratch,'input');mkdirSync(work,{recursive:true});mkdirSync(inputDir,{recursive:true});
const managerRoot=path.join(scratch,'managers');mkdirSync(managerRoot,{recursive:true});
for(const fixture of [
 {name:'npm',file:'package-lock.json',contents:'{}',expected:'npm'},
 {name:'pnpm',file:'pnpm-lock.yaml',contents:'lockfileVersion: 9',expected:'pnpm'},
 {name:'yarn-classic',file:'yarn.lock',contents:'# yarn lockfile v1',expected:'yarn',flavor:'classic'},
 {name:'yarn-berry',file:'yarn.lock',contents:'__metadata:\n  version: 8',expected:'yarn',flavor:'berry'},
 {name:'bun',file:'bun.lock',contents:'{}',expected:'bun'}
]){const root=path.join(managerRoot,fixture.name);mkdirSync(root,{recursive:true});writeFileSync(path.join(root,fixture.file),fixture.contents);const manager=await detectManager(root,{packageManager:fixture.expected+'@0.0.0'});assert.equal(manager.name,fixture.expected);if(fixture.flavor)assert.equal(manager.flavor,fixture.flavor);assert.ok(managerInvocation(manager,'install').args.includes(manager.name==='npm'?'ci':'install'));}
const config=`{
  "name":"krun-wrangler-fixture",
  "main":"src/index.mjs",
  "compatibility_date":"2026-09-28",
  "find_additional_modules":true,
  "upload_source_maps":true,
  "rules":[
    {"type":"ESModule","globs":["lang/**/*.mjs"],"fallthrough":true},
    {"type":"Text","globs":["**/*.html"],"fallthrough":true},
    {"type":"Data","globs":["**/*.bin"],"fallthrough":true},
    {"type":"CompiledWasm","globs":["**/*.wasm"],"fallthrough":true}
  ],
  "assets":{"directory":"public","binding":"ASSETS","run_worker_first":["/api/*"],"not_found_handling":"single-page-application","html_handling":"auto-trailing-slash"}
}`;
const source={
 'wrangler.jsonc':Buffer.from(config),
 'src/index.mjs':Buffer.from(`import text from './message.html';import data from './payload.bin';import wasm from './empty.wasm';export class NamedEntrypoint {fetch(){return new Response('named')}}export default {async fetch(){const language=await import('./lang/'+ 'en.mjs');return new Response(text+':'+data.byteLength+':'+typeof wasm+':'+language.default)}};`),
 'src/message.html':Buffer.from('hello'),
 'src/payload.bin':Buffer.from([0,1,2,3]),
 'src/empty.wasm':Buffer.from([0,97,115,109,1,0,0,0]),
 'src/lang/en.mjs':Buffer.from(`export default 'en';`),
 'src/lang/fr.mjs':Buffer.from(`export default 'fr';`),
 'public/index.html':Buffer.from('<!doctype html><title>wrangler</title>'),
 'public/LICENSE':Buffer.from('wrangler fixture license')
};
const sourceArchive=gzipSync(Buffer.concat([...Object.entries(source).map(([name,data])=>tarFile('src/'+name,data)),Buffer.alloc(1024)]));let servedSource=sourceArchive,uploaded=null,uploadRequests=0;
const server=createServer((request,response)=>{const chunks=[];request.on('data',chunk=>chunks.push(chunk));request.on('end',()=>{if(request.url==='/source'){response.writeHead(200,{'content-type':'application/gzip'});response.end(servedSource);return;}uploadRequests++;uploaded=Buffer.concat(chunks);response.end();});});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const port=server.address().port;
try{
 const buildPath=path.join(inputDir,'build.json'),packPath=path.join(inputDir,'pack.json');
 writeFileSync(buildPath,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${port}/source`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const built=await runBuilder(here,buildPath,work,'build');assert.equal(built.code,0,built.output);assert.equal(uploadRequests,0,'untrusted build phase reached upload endpoint');assert.match(readFileSync(path.join(work,'build-result.json'),'utf8'),/4\.155\.0/);
 writeFileSync(packPath,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',upload_url:`http://127.0.0.1:${port}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const packed=await runBuilder(here,packPath,work,'pack');assert.equal(packed.code,0,packed.output);assert.equal(uploadRequests,1);assert.ok(uploaded);
 const entries=archiveEntries(uploaded),manifest=JSON.parse(entries['manifest.json'].toString('utf8')),types=new Map(manifest.modules.map(module=>[module.name,module.type]));
 assert.equal(manifest.profile,'workerd-bundle-v2');assert.equal(manifest.main_module,'index.js');assert.equal(types.get('index.js'),'esm');assert.equal(types.get('__krun_user_index.js'),'esm');assert.ok(entries['modules/index.js'].toString('utf8').includes('export * from "./__krun_user_index.js"'));
 assert.ok([...types.values()].includes('text'));assert.ok([...types.values()].includes('data'));assert.ok([...types.values()].includes('wasm'));assert.ok([...types.values()].includes('source-map'));assert.equal(types.get('lang/en.mjs'),'esm');assert.equal(types.get('lang/fr.mjs'),'esm');
 assert.equal(manifest.assets.binding,'ASSETS');assert.equal(entries['assets/index.html'].toString('utf8'),'<!doctype html><title>wrangler</title>');assert.equal(entries['assets/LICENSE'].toString('utf8'),'wrangler fixture license');assert.equal(manifest.assets.files.find(file=>file.path==='LICENSE')?.content_type,'application/octet-stream');assert.ok(!entries['modules/README.md']);

 const assetsWork=path.join(scratch,'assets-work'),assetsConfig=Buffer.from(`{"name":"assets-only","compatibility_date":"2026-09-28","assets":{"directory":"public"}}`);mkdirSync(assetsWork,{recursive:true});servedSource=gzipSync(Buffer.concat([tarFile('src/wrangler.jsonc',assetsConfig),tarFile('src/public/index.html',Buffer.from('assets only')),Buffer.alloc(1024)]));uploaded=null;
 const assetsBuild=path.join(inputDir,'assets-build.json'),assetsPack=path.join(inputDir,'assets-pack.json');writeFileSync(assetsBuild,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${port}/source`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const assetsBuilt=await runBuilder(here,assetsBuild,assetsWork,'build');assert.equal(assetsBuilt.code,0,assetsBuilt.output);writeFileSync(assetsPack,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',upload_url:`http://127.0.0.1:${port}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));const assetsPacked=await runBuilder(here,assetsPack,assetsWork,'pack');assert.equal(assetsPacked.code,0,assetsPacked.output);
 const assetsEntries=archiveEntries(uploaded),assetsManifest=JSON.parse(assetsEntries['manifest.json'].toString('utf8'));assert.equal(assetsManifest.main_module,'index.js');assert.match(assetsEntries['modules/index.js'].toString('utf8'),/Not Found/);assert.equal(assetsEntries['assets/index.html'].toString('utf8'),'assets only');

 const generatedWork=path.join(scratch,'generated-work'),generatedConfig=Buffer.from(`{"name":"generated","compatibility_date":"2026-09-28"}`),generatedPackage=Buffer.from(`{"private":true,"scripts":{"build":"node build.mjs"}}`),generatedLock=Buffer.from(`{"name":"generated","lockfileVersion":3,"requires":true,"packages":{"":{"name":"generated"}}}`),generatedScript=Buffer.from(`import {mkdirSync,writeFileSync} from 'node:fs';mkdirSync('.wrangler/deploy',{recursive:true});mkdirSync('dist/public',{recursive:true});writeFileSync('.wrangler/deploy/config.json',JSON.stringify({configPath:'../../dist/wrangler.json'}));writeFileSync('dist/wrangler.json',JSON.stringify({name:'generated',main:'worker.js',compatibility_date:'2026-09-28',assets:{directory:'public'}}));writeFileSync('dist/worker.js',\`export default {fetch(){return new Response('generated')}}\`);writeFileSync('dist/public/index.html','generated asset');`);mkdirSync(generatedWork,{recursive:true});servedSource=gzipSync(Buffer.concat([tarFile('src/wrangler.jsonc',generatedConfig),tarFile('src/package.json',generatedPackage),tarFile('src/package-lock.json',generatedLock),tarFile('src/build.mjs',generatedScript),Buffer.alloc(1024)]));uploaded=null;
 const generatedBuild=path.join(inputDir,'generated-build.json'),generatedPack=path.join(inputDir,'generated-pack.json');writeFileSync(generatedBuild,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${port}/source`,compatibility_date:'2026-09-28',compatibility_flags:[]}));const generatedBuilt=await runBuilder(here,generatedBuild,generatedWork,'build');assert.equal(generatedBuilt.code,0,generatedBuilt.output);assert.match(readFileSync(path.join(generatedWork,'build-result.json'),'utf8'),/dist\/wrangler\.json/);writeFileSync(generatedPack,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',upload_url:`http://127.0.0.1:${port}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));const generatedPacked=await runBuilder(here,generatedPack,generatedWork,'pack');assert.equal(generatedPacked.code,0,generatedPacked.output);
 const generatedEntries=archiveEntries(uploaded),generatedManifest=JSON.parse(generatedEntries['manifest.json'].toString('utf8'));assert.equal(generatedManifest.main_module,'worker.js');assert.equal(generatedEntries['assets/index.html'].toString('utf8'),'generated asset');assert.match(generatedEntries['modules/__krun_user_worker.js'].toString('utf8'),/generated/);

 const noBundleWork=path.join(scratch,'no-bundle-work'),noBundleConfig=Buffer.from(`{"name":"no-bundle","main":"src/index.mjs","compatibility_date":"2026-09-28","no_bundle":true,"find_additional_modules":true,"rules":[{"type":"ESModule","globs":["**/*.mjs"],"fallthrough":true}]}`);mkdirSync(noBundleWork,{recursive:true});servedSource=gzipSync(Buffer.concat([tarFile('src/wrangler.jsonc',noBundleConfig),tarFile('src/src/index.mjs',Buffer.from(`export default {async fetch(){return new Response((await import('./lang/en.mjs')).default)}}`)),tarFile('src/src/lang/en.mjs',Buffer.from(`export default 'en'`)),Buffer.alloc(1024)]));uploaded=null;
 const noBundleBuild=path.join(inputDir,'no-bundle-build.json'),noBundlePack=path.join(inputDir,'no-bundle-pack.json');writeFileSync(noBundleBuild,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${port}/source`,compatibility_date:'2026-09-28',compatibility_flags:[]}));const noBundleBuilt=await runBuilder(here,noBundleBuild,noBundleWork,'build');assert.equal(noBundleBuilt.code,0,noBundleBuilt.output);writeFileSync(noBundlePack,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',upload_url:`http://127.0.0.1:${port}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));const noBundlePacked=await runBuilder(here,noBundlePack,noBundleWork,'pack');assert.equal(noBundlePacked.code,0,noBundlePacked.output);
 const noBundleEntries=archiveEntries(uploaded),noBundleManifest=JSON.parse(noBundleEntries['manifest.json'].toString('utf8')),noBundleTypes=new Map(noBundleManifest.modules.map(module=>[module.name,module.type]));assert.equal(noBundleManifest.main_module,'index.mjs');assert.equal(noBundleTypes.get('__krun_user_index.mjs'),'esm');assert.equal(noBundleTypes.get('lang/en.mjs'),'esm');
 console.log('ALL WRANGLER BUILDER CHECKS PASSED');
}finally{server.close();rmSync(scratch,{recursive:true,force:true});}
