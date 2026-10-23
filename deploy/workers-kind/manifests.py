#!/usr/bin/env python3
import json,pathlib,secrets,os,ipaddress,socket
root=pathlib.Path(__file__).resolve().parent
out=root/'generated';out.mkdir(exist_ok=True)
credential_file=out/'credentials.json'
if credential_file.exists(): credentials=json.loads(credential_file.read_text())
else:
 credentials={k:secrets.token_hex(24) for k in ['db_password','s3_secret','runtime_token','management_token']};credentials['s3_access']='krun-workers';credential_file.write_text(json.dumps(credentials));credential_file.chmod(0o600)
api_ips=[str(ipaddress.ip_address(x)) for x in os.environ['KRUN_KUBE_API_IPS'].split(',')]
ns='workers-mvp';tag='20261013';items=[]
def resource(kind,name,**fields):
 return {'apiVersion':fields.pop('apiVersion','v1'),'kind':kind,'metadata':{'name':name,'namespace':ns},**fields}
items.append({'apiVersion':'v1','kind':'Namespace','metadata':{'name':ns,'labels':{'lab':'krun-workers-mvp'}}})
items.append(resource('Secret','workers-credentials',stringData={'KRUN_DATABASE_URL':f'postgres://postgres:{credentials["db_password"]}@postgres:5432/postgres?sslmode=disable','POSTGRES_PASSWORD':credentials['db_password'],'KRUN_S3_ACCESS':credentials['s3_access'],'KRUN_S3_SECRET':credentials['s3_secret'],'KRUN_RUNTIME_TOKEN':credentials['runtime_token'],'KRUN_MANAGEMENT_TOKEN':credentials['management_token'],'MINIO_ROOT_USER':credentials['s3_access'],'MINIO_ROOT_PASSWORD':credentials['s3_secret']}))
def claim(name,size):items.append(resource('PersistentVolumeClaim',name,spec={'accessModes':['ReadWriteOnce'],'resources':{'requests':{'storage':size}}}))
claim('postgres-data','1Gi');claim('minio-data','1Gi')
def service(name,ports,selector=None):items.append(resource('Service',name,spec={'selector':selector or {'app':name},'ports':[{'name':f'p{p}','port':p,'targetPort':p} for p in ports]}))
def deployment(name,containers,replicas=1,labels=None,**spec):items.append(resource('Deployment',name,apiVersion='apps/v1',spec={'replicas':replicas,'selector':{'matchLabels':{'app':name}},'template':{'metadata':{'labels':{'app':name,**(labels or {})}},'spec':{'automountServiceAccountToken':False,'containers':containers,**spec}}}))
def env(name):return {'name':name,'valueFrom':{'secretKeyRef':{'name':'workers-credentials','key':name}}}
def limits(memory='256Mi',request='64Mi'):return {'requests':{'cpu':'50m','memory':request},'limits':{'cpu':'1','memory':memory}}
def secure():return {'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'runAsNonRoot':True,'runAsUser':1000,'capabilities':{'drop':['ALL']},'seccompProfile':{'type':'RuntimeDefault'}}
deployment('postgres',[{'name':'postgres','image':'postgres:18-alpine','imagePullPolicy':'Never','env':[env('POSTGRES_PASSWORD')],'volumeMounts':[{'name':'data','mountPath':'/var/lib/postgresql'}],'readinessProbe':{'exec':{'command':['pg_isready','-U','postgres']},'periodSeconds':2},'resources':limits('1Gi','256Mi')}],volumes=[{'name':'data','persistentVolumeClaim':{'claimName':'postgres-data'}}]);service('postgres',[5432])
deployment('minio',[{'name':'minio','image':'quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z','imagePullPolicy':'Never','command':['minio','server','/data','--console-address',':9001'],'env':[env('MINIO_ROOT_USER'),env('MINIO_ROOT_PASSWORD')],'volumeMounts':[{'name':'data','mountPath':'/data'}],'readinessProbe':{'httpGet':{'path':'/minio/health/ready','port':9000},'periodSeconds':2},'resources':limits('512Mi')}],volumes=[{'name':'data','persistentVolumeClaim':{'claimName':'minio-data'}}]);service('minio',[9000]);service('static-assets',[9000],selector={'app':'minio'})
config='''listen: 0.0.0.0:9000
internal_listen: 0.0.0.0:9001
public_url: http://127.0.0.1:4380
development: true
db: ${KRUN_DATABASE_URL}
providers:
  - id: local
    source_id: workers-kind-development
    type: dev
    name: kind 开发登录
workers:
  namespace: workers-mvp
  builder_image: krun-worker-builder:20261013
  builder_pull_policy: Never
  s3_location_id: primary
  clone_directory: /workdir
  runtime_service: runtime-default
  gateways:
    - id: internal
      name: workers-mvp
      namespace: kgateway-system
      section: private
    - id: external
      name: workers-public
      namespace: kgateway-system
      section: public
  domains:
    - id: internal
      domain: workers.test
      label: 内网
      gateway: internal
      default: true
      static_domain: static.internal.workers.test
    - id: external
      domain: public.workers.test
      label: 公网
      gateway: external
      static_domain: static.public.workers.test
  static_service: static-assets
  static_service_port: 9000
  runtime_token: ${KRUN_RUNTIME_TOKEN}
  management_token: ${KRUN_MANAGEMENT_TOKEN}
  s3_endpoint: http://minio:9000
  s3_bucket: krun-artifacts
  config_base_url: http://minio:9000/krun-artifacts/config
  s3_access_key: ${KRUN_S3_ACCESS}
  s3_secret_key: ${KRUN_S3_SECRET}
  repository_providers:
    - id: company-gitlab
      type: gitlab
      name: 公司 GitLab
      enabled: true
      priority: 10
      base_url: https://gitlab.example.internal
    - id: github
      type: github
      name: GitHub
      enabled: true
      priority: 20
      base_url: https://github.com
    - id: gitlab
      type: gitlab
      name: GitLab.com
      enabled: true
      priority: 30
      base_url: https://gitlab.com
'''
items.append(resource('ConfigMap','control-config',data={'control.yaml':config}))
items.append(resource('ServiceAccount','workers-control'))
items.append(resource('Role','workers-control',apiVersion='rbac.authorization.k8s.io/v1',rules=[{'apiGroups':[''],'resources':['pods','pods/log'],'verbs':['get','list']},{'apiGroups':[''],'resources':['configmaps'],'verbs':['get','create','patch','delete']},{'apiGroups':['batch'],'resources':['jobs'],'verbs':['get','list','create','delete']},{'apiGroups':['discovery.k8s.io'],'resources':['endpointslices'],'verbs':['get','list']},{'apiGroups':['gateway.networking.k8s.io'],'resources':['httproutes'],'verbs':['get','list','create','patch','delete'] }]))
items.append(resource('RoleBinding','workers-control',apiVersion='rbac.authorization.k8s.io/v1',roleRef={'apiGroup':'rbac.authorization.k8s.io','kind':'Role','name':'workers-control'},subjects=[{'kind':'ServiceAccount','name':'workers-control','namespace':ns}]))
items.append({'apiVersion':'rbac.authorization.k8s.io/v1','kind':'Role','metadata':{'name':'workers-mvp-reader','namespace':'kgateway-system'},'rules':[{'apiGroups':['gateway.networking.k8s.io'],'resources':['gateways'],'resourceNames':['workers-mvp'],'verbs':['get']}]})
items.append({'apiVersion':'rbac.authorization.k8s.io/v1','kind':'RoleBinding','metadata':{'name':'workers-mvp-reader','namespace':'kgateway-system'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'Role','name':'workers-mvp-reader'},'subjects':[{'kind':'ServiceAccount','name':'workers-control','namespace':ns}]})
deployment('backend',[{'name':'backend','image':f'krun-workers-backend:{tag}','imagePullPolicy':'Never','env':[env(n) for n in ['KRUN_DATABASE_URL','KRUN_S3_ACCESS','KRUN_S3_SECRET','KRUN_RUNTIME_TOKEN','KRUN_MANAGEMENT_TOKEN']],'securityContext':secure(),'volumeMounts':[{'name':'config','mountPath':'/config','readOnly':True},{'name':'workdir','mountPath':'/workdir'}],'resources':limits('1Gi','128Mi'),'readinessProbe':{'httpGet':{'path':'/api/healthz','port':9000},'periodSeconds':5}}],serviceAccountName='workers-control',automountServiceAccountToken=True,volumes=[{'name':'config','configMap':{'name':'control-config'}},{'name':'workdir','emptyDir':{'sizeLimit':'1Gi'}}],terminationGracePeriodSeconds=10);service('backend',[9000,9001])
# Runtime has no Kubernetes token, DB identity, S3 credentials or public
# network; it reaches only the control plane and object storage (via
# short-lived presigned URLs issued by the control plane). workerd's network
# allow-list takes CIDR blocks only (host:port is rejected at startup), so it
# must cover the object-storage Service ClusterIP; NetworkPolicy still pins the
# real destination to the minio Pods on :9000.
capnp=f'''using W = import "/workerd/workerd.capnp";
const config :W.Config = (
 services = [
  (name="entry",worker=(modules=[(name="entry.js",esModule=embed "/app/entry.js")],compatibilityDate="2026-09-28",compatibilityFlags=["experimental"],bindings=[
   (name="LOADER",workerLoader=(id="workers-mvp-default")),
   (name="PLATFORM",service="platform"),
   (name="POD_NAME",text=embed "/pod/name"),
   (name="POD_UID",text=embed "/pod/uid"),
   (name="RUNTIME_TOKEN",text=embed "/secrets/KRUN_RUNTIME_TOKEN"),
   (name="MANAGEMENT_TOKEN",text=embed "/secrets/KRUN_MANAGEMENT_TOKEN"),
   (name="CONFIG_BASE",text="http://minio:9000/krun-artifacts/config"),
   (name="ASSETS_CACHE_MAX",text="67108864")
  ],globalOutbound="storage")),
  (name="platform",external=(address="backend:9001",http=())),
  (name="storage",network=(allow=["10.96.0.0/12","192.168.0.0/16"])),
  (name="deny",network=(allow=[]))
 ], sockets=[
  (name="private",address="*:8081",http=(),service=(name="entry",entrypoint="Private")),
  (name="management",address="*:8083",http=(),service=(name="entry",entrypoint="Management"))
 ]
);'''
items.append(resource('ConfigMap','runtime-config',data={'runtime.capnp':capnp}))
deployment('runtime-default',[{'name':'workerd','image':f'krun-worker-runtime:{tag}','imagePullPolicy':'Never','securityContext':secure(),'volumeMounts':[{'name':'config','mountPath':'/config','readOnly':True},{'name':'pod','mountPath':'/pod','readOnly':True},{'name':'secrets','mountPath':'/secrets','readOnly':True}],'readinessProbe':{'httpGet':{'path':'/ready','port':8083},'periodSeconds':1,'timeoutSeconds':5,'failureThreshold':2},'resources':limits('1Gi')}],replicas=2,labels={'role':'krun-runtime','pool':'default'},volumes=[{'name':'config','configMap':{'name':'runtime-config'}},{'name':'pod','downwardAPI':{'items':[{'path':'name','fieldRef':{'fieldPath':'metadata.name'}},{'path':'uid','fieldRef':{'fieldPath':'metadata.uid'}}]}},{'name':'secrets','secret':{'secretName':'workers-credentials','items':[{'key':n,'path':n} for n in ['KRUN_RUNTIME_TOKEN','KRUN_MANAGEMENT_TOKEN']]}}],terminationGracePeriodSeconds=10);service('runtime-default',[8081,8083])
deployment('tester',[{'name':'tester','image':f'krun-worker-builder:{tag}','imagePullPolicy':'Never','command':['node','-e','setInterval(()=>{},100000)'],'resources':limits('128Mi')}])
def selector(app):return {'podSelector':{'matchLabels':{'app':app}}}
def tcp(port):return {'protocol':'TCP','port':port}
def policy(name,labels,ingress=None,egress=None):items.append(resource('NetworkPolicy',name,apiVersion='networking.k8s.io/v1',spec={'podSelector':{'matchLabels':labels},'policyTypes':['Ingress','Egress'],'ingress':ingress or [],'egress':egress or []}))
policy('deny',{})
policy('dns',{},egress=[{'to':[{'namespaceSelector':{'matchLabels':{'kubernetes.io/metadata.name':'kube-system'}},'podSelector':{'matchLabels':{'k8s-app':'kube-dns'}}}],'ports':[{'protocol':'UDP','port':53},tcp(53)]}])
policy('backend',{'app':'backend'},ingress=[{'from':[selector('tester')],'ports':[tcp(9000)]},{'from':[{'podSelector':{'matchLabels':{'role':'krun-runtime'}}}],'ports':[tcp(9001)]}],egress=[{'to':[selector('postgres')],'ports':[tcp(5432)]},{'to':[selector('minio')],'ports':[tcp(9000)]},{'to':[{'namespaceSelector':{'matchLabels':{'kubernetes.io/metadata.name':'kgateway-system'}},'podSelector':{'matchLabels':{'gateway.networking.k8s.io/gateway-name':'workers-mvp'}}}],'ports':[tcp(8080)]},{'to':[{'podSelector':{'matchLabels':{'role':'krun-runtime'}}}],'ports':[tcp(8083)]},{'to':[{'ipBlock':{'cidr':ip+('/32' if ':' not in ip else '/128')}} for ip in api_ips],'ports':[tcp(443),tcp(6443)]}])
# Only the control plane fetches repositories; builders and runtimes keep their existing egress.
repository_ips=set()
if os.environ.get('KRUN_REPOSITORY_EGRESS_IPS'):
 repository_ips.update(str(ipaddress.ip_address(v)) for v in os.environ['KRUN_REPOSITORY_EGRESS_IPS'].split(','))
else:
 for host in ['api.github.com','codeload.github.com','gitlab.com','gitlab.example.internal']:
  try:
   repository_ips.update(item[4][0] for item in socket.getaddrinfo(host,443,type=socket.SOCK_STREAM))
  except OSError:
   print('Repository DNS not available while generating:',host)
if repository_ips:
 policy('repository-egress',{'app':'backend'},egress=[{'to':[{'ipBlock':{'cidr':ip+('/32' if ':' not in ip else '/128')}} for ip in sorted(repository_ips)],'ports':[tcp(443)]}])
policy('postgres',{'app':'postgres'},ingress=[{'from':[selector('backend')],'ports':[tcp(5432)]}])
# Both network scopes are separate Gateway instances. The gateway-name label
# selector keeps each instance's traffic distinct at the NetworkPolicy layer.
gw_selector=[{'namespaceSelector':{'matchLabels':{'kubernetes.io/metadata.name':'kgateway-system'}},'podSelector':{'matchLabels':{'gateway.networking.k8s.io/gateway-name':g}}} for g in ['workers-mvp','workers-public']]
policy('minio',{'app':'minio'},ingress=[{'from':[selector('backend'),selector('krun-builder'),selector('runtime-default'),selector('tester')],'ports':[tcp(9000)]},{'from':gw_selector,'ports':[tcp(9000)]}])
policy('builder',{'app':'krun-builder'},egress=[{'to':[selector('minio')],'ports':[tcp(9000)]}])
policy('runtime',{'role':'krun-runtime'},ingress=[{'from':gw_selector,'ports':[tcp(8081)]},{'from':[selector('backend')],'ports':[tcp(8083)]}],egress=[{'to':[selector('backend')],'ports':[tcp(9001)]},{'to':[selector('minio')],'ports':[tcp(9000)]}])
policy('tester',{'app':'tester'},egress=[{'to':[selector('backend')],'ports':[tcp(9000)]},{'to':[selector('minio')],'ports':[tcp(9000)]},{'to':[{'namespaceSelector':{'matchLabels':{'kubernetes.io/metadata.name':'kgateway-system'}}}],'ports':[tcp(8080)]}])
# Private instance: the only Gateway any private host may attach to.
items.append({'apiVersion':'gateway.networking.k8s.io/v1','kind':'Gateway','metadata':{'name':'workers-mvp','namespace':'kgateway-system'},'spec':{'gatewayClassName':'kgateway','listeners':[{'name':'private','port':8080,'protocol':'HTTP','allowedRoutes':{'namespaces':{'from':'Selector','selector':{'matchLabels':{'lab':'krun-workers-mvp'}}}}}]}})
# Public instance: a distinct proxy deployment. Isolation lives on this
# instance boundary, so no private hostname ever exists in its route table.
items.append({'apiVersion':'gateway.networking.k8s.io/v1','kind':'Gateway','metadata':{'name':'workers-public','namespace':'kgateway-system'},'spec':{'gatewayClassName':'kgateway','listeners':[{'name':'public','port':8080,'protocol':'HTTP','allowedRoutes':{'namespaces':{'from':'Selector','selector':{'matchLabels':{'lab':'krun-workers-mvp'}}}}}]}})
(out/'manifests.json').write_text(json.dumps({'apiVersion':'v1','kind':'List','items':items},indent=2))
(out/'manifests.json').chmod(0o600)
print('Generated isolated workers-mvp manifests; credentials retained locally')
