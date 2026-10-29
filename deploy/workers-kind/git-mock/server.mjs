import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFile} from 'node:fs/promises';
import https from 'node:https';

const repository='/repos/fixture/worker.git';
const certificate=await readFile('/tls/tls.crt');
const privateKey=await readFile('/tls/tls.key');
const honoArchive=await readFile('/registry/hono-4.13.14.tgz');
const honoIntegrity='sha512-'+createHash('sha512').update(honoArchive).digest('base64');
const maxRequestBytes=1024*1024;

function gitEnvironment(request){
 const protocol=request.headers['git-protocol'];
 return protocol?{...process.env,GIT_PROTOCOL:String(protocol)}:process.env;
}

function uploadPack(request,response,args,advertisement=false){
 const child=spawn('git',['upload-pack','--stateless-rpc',...args,repository],{
  env:gitEnvironment(request),stdio:['pipe','pipe','pipe']
 });
 let stderr='';
 child.stderr.setEncoding('utf8');
 child.stderr.on('data',chunk=>{if(stderr.length<4096)stderr+=chunk;});
 child.once('error',error=>{
  if(!response.headersSent)response.writeHead(500,{'content-type':'text/plain'});
  response.end('git upload-pack unavailable\n');
  console.error(error.message);
 });
 child.once('close',code=>{
  if(code!==0)console.error('git upload-pack failed',code,stderr.slice(0,4096));
  if(!response.writableEnded)response.end();
 });
 response.writeHead(200,{
  'content-type':advertisement?'application/x-git-upload-pack-advertisement':'application/x-git-upload-pack-result',
  'cache-control':'no-cache, max-age=0, must-revalidate'
 });
 if(advertisement)response.write('001e# service=git-upload-pack\n0000');
 child.stdout.pipe(response,{end:false});
 return child;
}

const server=https.createServer({key:privateKey,cert:certificate},(request,response)=>{
 const url=new URL(request.url||'/', 'https://git-mock');
 if(request.method==='GET'&&url.pathname==='/healthz'){
  response.writeHead(200,{'content-type':'text/plain'});response.end('ok\n');return;
 }
 if(request.method==='GET'&&url.pathname==='/npm/hono'){
  const body=Buffer.from(JSON.stringify({name:'hono','dist-tags':{latest:'4.13.14'},versions:{'4.13.14':{name:'hono',version:'4.13.14',dist:{tarball:'https://git-mock/npm/hono/-/hono-4.13.14.tgz',integrity:honoIntegrity}}}}));
  response.writeHead(200,{'content-type':'application/json','content-length':body.length,'cache-control':'public, max-age=3600'});response.end(body);return;
 }
 if(request.method==='GET'&&url.pathname==='/npm/hono/-/hono-4.13.14.tgz'){
  response.writeHead(200,{'content-type':'application/octet-stream','content-length':honoArchive.length,'cache-control':'public, max-age=31536000, immutable'});response.end(honoArchive);return;
 }
 if(request.method==='GET'&&url.pathname==='/fixture/worker.git/info/refs'&&url.searchParams.get('service')==='git-upload-pack'){
  const child=uploadPack(request,response,['--advertise-refs'],true);child.stdin.end();return;
 }
 if(request.method==='POST'&&url.pathname==='/fixture/worker.git/git-upload-pack'){
  const declared=Number(request.headers['content-length']||0);
  if(!Number.isFinite(declared)||declared<0||declared>maxRequestBytes){response.writeHead(413);response.end();return;}
  const child=uploadPack(request,response,[]);let received=0;
  request.on('data',chunk=>{received+=chunk.length;if(received>maxRequestBytes){request.destroy();child.kill('SIGTERM');}});
  request.pipe(child.stdin);return;
 }
 response.writeHead(404,{'content-type':'text/plain'});response.end('not found\n');
});

server.listen(8443,'0.0.0.0',()=>console.log('git mock listening on 8443'));
