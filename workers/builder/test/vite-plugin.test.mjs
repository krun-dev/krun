import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtempSync,mkdirSync,readFileSync,readdirSync,realpathSync,rmSync,statSync,writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {gunzipSync,gzipSync} from 'node:zlib';

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

function fixtureFiles(root,relative=''){
 const files={};
 for(const entry of readdirSync(path.join(root,relative),{withFileTypes:true})){
  if(['node_modules','dist','.wrangler'].includes(entry.name))continue;
  const name=relative?relative+'/'+entry.name:entry.name,target=path.join(root,name);
  if(entry.isDirectory())Object.assign(files,fixtureFiles(root,name));
  else{assert(entry.isFile());assert(statSync(target).size<8*1024*1024);files[name]=readFileSync(target);}
 }
 return files;
}

async function runBuilder(builderRoot,inputPath,work,phase){
 return new Promise(resolve=>{
  const child=spawn(process.execPath,[path.join(builderRoot,'build-worker.mjs')],{cwd:builderRoot,env:{...process.env,KRUN_BUILD_INPUT:inputPath,KRUN_BUILD_WORK:work,KRUN_BUILD_PHASE:phase,KRUN_WRANGLER_BIN:path.join(builderRoot,'node_modules','.bin','wrangler'),KRUN_NPM_REGISTRY:'https://registry.npmjs.org/'},stdio:['ignore','pipe','pipe']});
  let output='';child.stdout.on('data',data=>{output+=data;});child.stderr.on('data',data=>{output+=data;});child.once('close',code=>resolve({code,output}));
 });
}

const here=path.dirname(fileURLToPath(import.meta.url)),builderRoot=path.join(here,'..'),fixtureRoot=path.join(here,'fixtures','vite-plugin');
const fixture=fixtureFiles(fixtureRoot);assert(fixture['package-lock.json']);assert(fixture['vite.config.mjs']);assert(fixture['wrangler.jsonc']);
const source=gzipSync(Buffer.concat([...Object.entries(fixture).sort(([a],[b])=>a.localeCompare(b)).map(([name,data])=>tarFile('src/'+name,data)),Buffer.alloc(1024)]));
const scratch=realpathSync(mkdtempSync(path.join(tmpdir(),'krun-vite-plugin-'))),work=path.join(scratch,'work'),inputDir=path.join(scratch,'input');mkdirSync(work,{recursive:true});mkdirSync(inputDir,{recursive:true});
let uploaded=null;
const server=createServer((request,response)=>{const chunks=[];request.on('data',chunk=>chunks.push(chunk));request.on('end',()=>{if(request.url==='/source'){response.writeHead(200,{'content-type':'application/gzip'});response.end(source);return;}uploaded=Buffer.concat(chunks);response.end();});});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const port=server.address().port;
try{
 const buildPath=path.join(inputDir,'build.json'),packPath=path.join(inputDir,'pack.json');
 writeFileSync(buildPath,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',source_url:`http://127.0.0.1:${port}/source`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const built=await runBuilder(builderRoot,buildPath,work,'build');assert.equal(built.code,0,built.output);assert.match(built.output,/Package manager: npm 11\.21\.1/);assert.match(built.output,/Wrangler build complete: 4\.155\.0/);
 const buildResult=JSON.parse(readFileSync(path.join(work,'build-result.json'),'utf8'));assert.match(buildResult.config_path,/^dist\/.+\/wrangler\.json$/);assert.equal(uploaded,null);
 writeFileSync(packPath,JSON.stringify({build_mode:'wrangler',config_path:'wrangler.jsonc',upload_url:`http://127.0.0.1:${port}/upload`,compatibility_date:'2026-09-28',compatibility_flags:[]}));
 const packed=await runBuilder(builderRoot,packPath,work,'pack');assert.equal(packed.code,0,packed.output);assert(uploaded);
 const entries=archiveEntries(uploaded),manifest=JSON.parse(entries['manifest.json'].toString('utf8'));
 assert.equal(manifest.profile,'workerd-bundle-v2');assert.equal(manifest.compatibility_date,'2026-09-28');assert.equal(manifest.assets.binding,'ASSETS');assert.equal(manifest.assets.not_found_handling,'single-page-application');
 assert(entries['assets/index.html'].toString('utf8').includes('Cloudflare Vite fixture'));assert.equal(entries['assets/fixture.txt'].toString('utf8').trim(),'cloudflare-vite-plugin-static-asset');assert.equal(entries['assets/LICENSE'].toString('utf8').trim(),'cloudflare-vite-plugin-license');assert.equal(manifest.assets.files.find(file=>file.path==='LICENSE')?.content_type,'application/octet-stream');assert(manifest.assets.files.some(file=>/^assets\/.+\.js$/.test(file.path)));assert(!entries['assets/.assetsignore']);assert(!entries['assets/wrangler.json']);
 const moduleText=manifest.modules.map(module=>entries[module.path]?.toString('utf8')||'').join('\n');assert(moduleText.includes('cloudflare-vite-plugin'));assert(entries['modules/'+manifest.main_module]);
 console.log('REAL CLOUDFLARE VITE PLUGIN CHECK PASSED');
}finally{
 server.close();
 if(process.env.KRUN_KEEP_TEST_TMP==='1')console.error('Kept Vite fixture work directory:',scratch);
 else rmSync(scratch,{recursive:true,force:true});
}
