import {readFile,mkdir,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {gzipSync} from 'node:zlib';
import {build} from 'esbuild';
const hash=raw=>createHash('sha256').update(raw).digest('hex');
function tarFile(name,bytes){
 const header=Buffer.alloc(512);header.write(name,0,100,'utf8');
 const octal=(offset,length,n)=>header.write(n.toString(8).padStart(length-1,'0')+'\0',offset,length,'ascii');
 octal(100,8,0o644);octal(108,8,0);octal(116,8,0);octal(124,12,bytes.length);octal(136,12,0);header.fill(32,148,156);header[156]=48;header.write('ustar\0',257,6,'ascii');header.write('00',263,2,'ascii');
 const sum=header.reduce((n,b)=>n+b,0);header.write(sum.toString(8).padStart(6,'0')+'\0 ',148,8,'ascii');
 return Buffer.concat([header,bytes,Buffer.alloc((512-bytes.length%512)%512)]);
}
try {
 const input=JSON.parse(await readFile('/input/input.json','utf8'));
 const root='/work/src';await mkdir(root,{recursive:true});
 const entries=Object.entries(input.files);if(entries.length===0||entries.length>32)throw new Error('Invalid source file count');let total=0;
 for(const [name,code] of entries){if(path.basename(name).startsWith('__krun_')||!/^[a-zA-Z0-9_./-]+$/.test(name)||path.posix.normalize(name)!==name||name.startsWith('/')||name.startsWith('../')||!['.js','.mjs','.ts','.json'].includes(path.extname(name)))throw new Error('Invalid source path');total+=Buffer.byteLength(code);if(total>256*1024)throw new Error('Source too large');const target=path.join(root,name);await mkdir(path.dirname(target),{recursive:true});await writeFile(target,code);}
 if(!Object.hasOwn(input.files,input.entrypoint))throw new Error('Entrypoint missing');
 const wrapper=`import worker from ${JSON.stringify('./'+input.entrypoint)};\nimport {WorkerEntrypoint} from 'cloudflare:workers';\nexport default worker;\nexport class KrunValidation extends WorkerEntrypoint {ping(){if(!worker || typeof worker.fetch!=='function')throw new Error('Default export must provide fetch');return {environment:this.env.KRUN_ENVIRONMENT,deployment_id:this.env.KRUN_DEPLOYMENT_ID};}}\n`;
 await writeFile('/work/src/__krun_entry__.mjs',wrapper);
 console.log('Profile: esbuild-esm-v1');console.log('Compiling',input.entrypoint);
 const result=await build({absWorkingDir:root,entryPoints:['__krun_entry__.mjs'],bundle:true,write:false,format:'esm',platform:'neutral',target:'es2022',external:['cloudflare:workers','cloudflare:sockets'],logLevel:'warning',plugins:[{name:'source-boundary',setup(build){build.onResolve({filter:/.*/},args=>{if(['cloudflare:workers','cloudflare:sockets'].includes(args.path))return {path:args.path,external:true};if(args.kind==='entry-point')return;if(!args.path.startsWith('./')&&!args.path.startsWith('../'))return {errors:[{text:'Only relative source imports and approved cloudflare builtins are supported'}]};const resolved=path.resolve(args.resolveDir,args.path);if(!resolved.startsWith(root+'/'))return {errors:[{text:'Import escapes source directory'}]};});build.onLoad({filter:/.*/},args=>{if(!args.path.startsWith(root+'/'))return {errors:[{text:'Source access outside project is forbidden'}]};});}}]});
 const module=Buffer.from(result.outputFiles[0].contents);if(module.length>2*1024*1024)throw new Error('Compiled module too large');
 const manifest=Buffer.from(JSON.stringify({schema_version:1,profile:'esbuild-esm-v1',main_module:'index.js',modules:[{name:'index.js',path:'modules/index.js',type:'esm',sha256:hash(module),size_bytes:module.length}]}));
 const archive=gzipSync(Buffer.concat([tarFile('manifest.json',manifest),tarFile('modules/index.js',module),Buffer.alloc(1024)]),{level:6});
 const response=await fetch(input.upload_url,{method:'PUT',body:archive,signal:AbortSignal.timeout(15000)});if(!response.ok)throw new Error('Artifact upload failed ('+response.status+')');
 console.log('Compiled bytes:',module.length);console.log('Archive SHA-256:',hash(archive));console.log('Uploaded; awaiting platform finalize');
}catch(error){console.error('Build failed:',error.message);process.exitCode=1;}
