import {readFile,mkdir,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {gzipSync,gunzipSync} from 'node:zlib';
import {build} from 'esbuild';
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
})[path.extname(name).toLowerCase()];
const safeName=name=>name!==''&&!name.startsWith('/')&&path.posix.normalize(name)===name&&!name.startsWith('../')&&!name.includes('\\');
function tarFile(name,bytes){
 const header=Buffer.alloc(512);header.write(name,0,100,'utf8');
 const octal=(offset,length,n)=>header.write(n.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((n,b)=>n+b,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');
 return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}
// Source bundles have exactly two top sections: src/ (Worker code) and
// assets/ (files relative to assets.directory).
function untarSource(raw){
 const files={};const assets={};let off=0;
 while(off+512<=raw.length){
  const header=raw.subarray(off,off+512);const name=header.subarray(0,100).toString('utf8').replace(/\0.*$/,'');
  const size=parseInt(header.subarray(124,136).toString('ascii').replace(/[\0 ].*$/,'').trim(),8)||0;
  const type=header[156];
  if(!name)break;
  if(type!==48)throw new Error('Source bundle may only contain regular files');
  const data=raw.subarray(off+512,off+512+size);
  if(name.startsWith('src/'))files[name.slice(4)]=data;
  else if(name.startsWith('assets/'))assets[name.slice(7)]=data;
  else throw new Error('Unexpected source bundle entry: '+name);
  off+=512+Math.ceil(size/512)*512;
 }
 return {files,assets};
}
try {
 const input=JSON.parse(await readFile('/input/input.json','utf8'));
 if(!input.source_url)throw new Error('Source bundle URL missing');
 const response=await fetch(input.source_url,{signal:AbortSignal.timeout(15000)});
 if(!response.ok)throw new Error('Source bundle download failed ('+response.status+')');
 const source=untarSource(gunzipSync(Buffer.from(await response.arrayBuffer())));
 const entries=Object.entries(source.files);
 if(entries.length===0||entries.length>32)throw new Error('Invalid source file count');
 if(!Object.hasOwn(source.files,input.entrypoint))throw new Error('Entrypoint missing');
 const root='/work/src';await mkdir(root,{recursive:true});
 let total=0;
 for(const [name,data] of entries){
  if(path.basename(name).startsWith('__krun_')||!safeName(name)||!['.js','.mjs','.ts','.json'].includes(path.extname(name)))throw new Error('Invalid source path: '+name);
  total+=data.length;if(total>256*1024)throw new Error('Source too large');
  const target=path.join(root,name);await mkdir(path.dirname(target),{recursive:true});await writeFile(target,data);
 }
 const assetEntries=Object.entries(source.assets);
 const hasAssets=input.assets!=null;
 if(hasAssets){
  if(assetEntries.length===0)throw new Error('Assets directory is empty');
  if(assetEntries.length>500)throw new Error('Too many asset files');
  let assetTotal=0;
  for(const [name,data] of assetEntries){
   if(!safeName(name)||!contentType(name))throw new Error('Unsupported asset type: '+name);
   if(data.length>5*1024*1024)throw new Error('Asset exceeds 5 MiB: '+name);
   assetTotal+=data.length;if(assetTotal>20*1024*1024)throw new Error('Assets exceed 20 MiB');
  }
 }
 const wrapper=`import worker from ${JSON.stringify('./'+input.entrypoint)};\nimport {WorkerEntrypoint} from 'cloudflare:workers';\nexport default worker;\nexport class KrunValidation extends WorkerEntrypoint {ping(){if(!worker || typeof worker.fetch!=='function')throw new Error('Default export must provide fetch');return {environment:this.env.KRUN_ENVIRONMENT,release_id:this.env.KRUN_RELEASE_ID};}}\n`;
 await writeFile('/work/src/__krun_entry__.mjs',wrapper);
 console.log('Profile: esbuild-esm-v1');console.log('Compiling',input.entrypoint);
 const result=await build({absWorkingDir:root,entryPoints:['__krun_entry__.mjs'],bundle:true,write:false,format:'esm',platform:'neutral',target:'es2022',external:['cloudflare:workers','cloudflare:sockets'],logLevel:'warning',plugins:[{name:'source-boundary',setup(build){build.onResolve({filter:/.*/},args=>{if(['cloudflare:workers','cloudflare:sockets'].includes(args.path))return {path:args.path,external:true};if(args.kind==='entry-point')return;if(!args.path.startsWith('./')&&!args.path.startsWith('../'))return {errors:[{text:'Only relative source imports and approved cloudflare builtins are supported'}]};const resolved=path.resolve(args.resolveDir,args.path);if(!resolved.startsWith(root+'/'))return {errors:[{text:'Import escapes source directory'}]};});build.onLoad({filter:/.*/},args=>{if(!args.path.startsWith(root+'/'))return {errors:[{text:'Source access outside project is forbidden'}]};});}}]});
 const module=Buffer.from(result.outputFiles[0].contents);if(module.length>2*1024*1024)throw new Error('Compiled module too large');
 const manifest={schema_version:hasAssets?2:1,profile:'esbuild-esm-v1',main_module:'index.js',modules:[{name:'index.js',path:'modules/index.js',type:'esm',sha256:hash(module),size_bytes:module.length}]};
 if(hasAssets)manifest.assets={directory:input.assets.directory,run_worker_first:input.assets.run_worker_first||[],not_found_handling:input.assets.not_found_handling||'',html_handling:input.assets.html_handling||'',files:assetEntries.map(([name,data])=>({path:name,sha256:hash(data),size_bytes:data.length,content_type:contentType(name)}))};
 const parts=[tarFile('manifest.json',Buffer.from(JSON.stringify(manifest))),tarFile('modules/index.js',module)];
 if(hasAssets)for(const [name,data] of assetEntries)parts.push(tarFile('assets/'+name,data));
 const archive=gzipSync(Buffer.concat([...parts,Buffer.alloc(1024)]),{level:6});
 const upload=await fetch(input.upload_url,{method:'PUT',body:archive,signal:AbortSignal.timeout(15000)});if(!upload.ok)throw new Error('Artifact upload failed ('+upload.status+')');
 console.log('Compiled bytes:',module.length);if(hasAssets)console.log('Assets:',assetEntries.length,'files');
 console.log('Archive SHA-256:',hash(archive));console.log('Uploaded; awaiting platform finalize');
}catch(error){console.error('Build failed:',error.message);process.exitCode=1;}
