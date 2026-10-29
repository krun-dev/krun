import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {mkdtemp,mkdir,rm,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {promisify} from 'node:util';
import {managerInvocation} from '../wrangler-project.mjs';

const execute=promisify(execFile);
const registry='https://registry.invalid/';
const scratch=await mkdtemp(path.join(tmpdir(),'krun-manager-smoke-'));

async function run(file,args,cwd,env){
 return execute(file,args,{cwd,env,timeout:30000,maxBuffer:1024*1024});
}

function generationInvocation(manager){
 const install=managerInvocation(manager,'install');
 if(manager.name==='npm')return {file:install.file,args:['install','--package-lock-only','--ignore-scripts','--no-audit','--no-fund','--registry='+registry]};
 if(manager.name==='pnpm')return {file:install.file,args:['install','--lockfile-only','--ignore-scripts','--reporter=append-only','--registry='+registry]};
 if(manager.name==='yarn')return {file:install.file,args:[install.args[0],'install',...(manager.flavor==='berry'?['--no-immutable','--mode=skip-build']:['--ignore-scripts','--non-interactive','--registry',registry])]};
 if(manager.name==='bun')return {file:install.file,args:['install','--ignore-scripts','--registry',registry]};
 throw new Error('unsupported manager fixture');
}

function frozenInvocation(manager,userConfig,globalConfig){
 const invocation=managerInvocation(manager,'install');
 if(manager.name==='npm')invocation.args.push('--userconfig='+userConfig,'--globalconfig='+globalConfig,'--registry='+registry);
 if(manager.name==='yarn'&&manager.flavor==='classic')invocation.args.push('--registry',registry);
 if(manager.name==='bun')invocation.args.push('--registry',registry);
 return invocation;
}

try{
 const dependency=path.join(scratch,'fixture-dependency');
 await mkdir(dependency,{recursive:true});
 await writeFile(path.join(dependency,'package.json'),JSON.stringify({name:'fixture-dependency',version:'1.0.0'}));
 for(const fixture of [
  {label:'npm',manager:{name:'npm',version:'11.21.1'}},
  {label:'pnpm',manager:{name:'pnpm',version:'12.16.1'}},
  {label:'yarn-classic',manager:{name:'yarn',flavor:'classic',version:'1.22.22'}},
  {label:'yarn-berry',manager:{name:'yarn',flavor:'berry',version:'4.18.1'}},
  {label:'bun',manager:{name:'bun',version:'1.4.4'}}
 ]){
  const root=path.join(scratch,fixture.label),home=path.join(root,'.home'),cache=path.join(root,'.cache');
  await mkdir(home,{recursive:true});await mkdir(cache,{recursive:true});
  const userConfig=path.join(home,'user.npmrc'),globalConfig=path.join(home,'global.npmrc');
  await writeFile(userConfig,'');await writeFile(globalConfig,'');
  const base={name:'manager-smoke-'+fixture.label,version:'1.0.0',private:true};
  await writeFile(path.join(root,'package.json'),JSON.stringify(base));
  const env={
   PATH:process.env.PATH||'/usr/local/bin:/usr/bin:/bin',HOME:home,XDG_CONFIG_HOME:path.join(home,'.config'),CI:'1',NO_COLOR:'1',FORCE_COLOR:'0',
   npm_config_cache:cache,npm_config_userconfig:userConfig,npm_config_globalconfig:globalConfig,npm_config_registry:registry,
   YARN_NPM_REGISTRY_SERVER:registry.replace(/\/$/,''),BUN_CONFIG_REGISTRY:registry,NO_PROXY:'*',no_proxy:'*'
  };
  const generate=generationInvocation(fixture.manager);await run(generate.file,generate.args,root,env);
  const frozen=frozenInvocation(fixture.manager,userConfig,globalConfig);await run(frozen.file,frozen.args,root,env);
  // Bun currently permits adding a local file dependency under
  // --frozen-lockfile, so its smoke proves the production invocation itself;
  // the other managers also prove that manifest/lock drift is rejected.
  if(fixture.manager.name!=='bun'){
   await writeFile(path.join(root,'package.json'),JSON.stringify({...base,dependencies:{'fixture-dependency':'file:../fixture-dependency'}}));
   await assert.rejects(run(frozen.file,frozen.args,root,env),error=>error&&typeof error.code==='number'&&error.code!==0,fixture.label+' accepted a stale lockfile');
  }
  console.log('  ok ',fixture.label,'frozen install');
 }
 console.log('ALL PACKAGE MANAGER SMOKES PASSED');
}finally{
 await rm(scratch,{recursive:true,force:true});
}
