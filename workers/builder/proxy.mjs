import http from 'node:http';
for(const [port,internal] of [[9000,false],[9001,true]])http.createServer((req,res)=>{
 const isInternal=req.url.startsWith('/internal/workers/');if(internal!==isInternal){res.writeHead(404);res.end();return;}
 const upstream=http.request({hostname:'127.0.0.1',port:8080,path:req.url,method:req.method,headers:req.headers,timeout:22000},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res);});
 upstream.on('error',()=>{if(!res.headersSent)res.writeHead(503);res.end();});upstream.on('timeout',()=>upstream.destroy());req.on('aborted',()=>upstream.destroy());req.pipe(upstream);
}).listen(port,'0.0.0.0');
