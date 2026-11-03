# 本地开发环境（local-first）

只有一套开发环境。目标是把能跑在开发机上的组件都跑在开发机上，集群只保留无法本地替代的 serving runtime、候选 validator（均为 workerd）和 kgateway。

## 拓扑

```text
开发机                                远端主机 <dev-host>
├─ krun-server (Go)  :8080             ├─ nginx 80/443
├─ PostgreSQL        :25432            └─ kind: runtime-default + runtime-validator-default + kgateway
├─ 对象存储          本地 MinIO :9000 或外部 S3/TOS
├─ builder (node)    本地子进程
└─ Vite              :5173
        │                                     │
        ├─ 38443 → kind API                   │
        ├─ 38084 → runtime-validator-default:8083 ─┘
        ├─ 38080 → workers-mvp:8080
        └─ 38081 → workers-public:8080
```

- 控制面、Postgres、builder、前端都在开发机；对象存储可使用本地 MinIO 或外部 S3/TOS；改后端或 builder 不需要构建镜像。
- 本地 MinIO 模式通过 selectorless `static-assets` Service/EndpointSlice 访问开发机；外部 S3/TOS 模式由 runtime 直接访问 HTTPS，不固定云存储 IP。两种模式都不需要集群内 MinIO Pod。
- 集群里只保留 `runtime-default`、不接业务 Gateway 的 `runtime-validator-default` 和 kgateway；旧的 backend/postgres/minio/tester deployment 已 scale 到 0。
- 业务 Worker 的 host 仍然是 `w-xxx-<env>.p-<pool-hash>.workers.test`；nginx 把外网域名改写成它。

业务入口统一使用共享路由，DNS / nginx / TLS 必须覆盖 pool 标签。规则见 [域名与路由](workers-domains.md)。

## 唯一配置

`.data/dev.yaml` 是唯一的本地开发配置，已被 `git` 忽略。不要再创建 `config/development.yaml`、`.data/development-*.yaml`、`.data/sso-local.yaml` 之类的变体；需要改端口、数据库、Worker 参数或 SSO 来源时直接改这一份。

真实 token、密码、数据库/S3 凭据和内网主机/IP 只放在 `.data/`；提交前运行 `make check-secrets`（`make test` 也会执行），tracked 文件里只保留占位符或 `${KRUN_*}` 引用。

关键字段：

- `builder_mode: local`：builder 在本机以 `node workers/builder/build-worker.mjs` 运行，不创建 Kubernetes Job。Wrangler/custom build 会执行仓库代码，因此此模式只用于开发者自己的可信仓库；面向不可信租户必须使用 Kubernetes builder，并建议配置 `builder_runtime_class` 为 gVisor/Kata 等强隔离运行时。
- `gateways` / `domains`：网关实例与域名清单。每个域名绑定一个网关，并带一个只用于展示的 `label`；服务从中选择要用哪些域名。
- 网关的 `base`（如 `http://127.0.0.1:38080`）：控制台 invoke 经 SSH 隧道打到对应的集群网关实例。
- `runtime_pools[].validator_service: runtime-validator-default`：候选只在独立 validator 中加载，不进入 serving runtime。
- `runtime_pools[].validator_management_base: http://127.0.0.1:38084`：local-first 控制面通过隧道访问 validator；生产集群内通常留空，直接访问 Pod IP。
- `s3_endpoint` 和 `s3_builder_endpoint` 指向开发机上的 MinIO（通常是 `http://127.0.0.1:9000`）；本地 builder 使用同一地址。
- `s3_runtime_endpoint` 是 runtime 下载制品和静态文件所见的地址。local-first 应设为
  `http://<minio-host>:9000`；该开发机地址由 `dev-runtime-config.sh` 同时加入 workerd
  `restrictPeers` allowlist 和 NetworkPolicy。不能使用 runtime 自己的 `127.0.0.1`，也
  不能使用 `static-assets` Service DNS（workerd 会在 kube-proxy 转发前拒绝其 ClusterIP）。
- `config_base_url` 使用集群可达的开发机地址（`http://<minio-host>:9000/<bucket>/config`）；runtime 不使用控制面的 loopback 地址。桶名从同一份主 YAML 读取。

## S3 兼容对象存储

在同一份 `.data/dev.yaml` 的 `workers:` 中设置：

```yaml
s3_endpoint: https://s3.example.com
s3_builder_endpoint: https://s3.example.com
s3_runtime_endpoint: https://s3.example.com
s3_region: us-east-1
s3_addressing_style: path
s3_bucket: replace-me
s3_prefix: dev/run
s3_bucket_mode: external
s3_access_mode: signed
s3_location_id: external-dev
s3_access_key: replace-me
s3_secret_key: replace-me
```

省略 `config_base_url`，由 runtime endpoint、寻址方式、桶及前缀自动生成。
桶管理和读取方式独立：`external` 不创建桶、不读取或改写桶策略；`managed` 可以自动建桶。
`signed` 保持私有并签名读取；`direct` 使用静态源站直接读取，仅 `managed + direct`
会写入匿名读取策略。所有对象、预签名 URL、列表及删除都经过
统一前缀适配器。storage location id 对应固定的桶和前缀，更换存储位置时使用新的 id。

Runtime 从挂载的 Kubernetes Secret 获取配置读取凭据，以 SigV4 读取私有配置；
代码和资产使用短期预签名 URL。业务 Worker 不接收存储凭据，也不获得 runtime 的
出站网络能力。静态域名经 serving runtime 校验资产路径、SHA-256 和大小，使用
签名 URL 读取私有对象，并保留有界缓存；不需要把共享桶改成公开访问。

`dev-runtime-config.sh` 通过 `LoadConfig` 渲染 workerd 配置、Secret 和 NetworkPolicy；
不再从 shell 变量维护第二套桶地址。签名模式支持公网 HTTPS，或显式 IPv4 地址的私有 HTTP/HTTPS 源站，端口从 endpoint
读取；公网域名允许 DNS 和对应 HTTPS 端口，私有 IP 仅放行指定 IP 和端口。
local-first 的私有 DNS 源站应配置成集群可达 IP；当前渲染器不放行私有 DNS 网段。
workerd 镜像包含系统 CA 证书并启用 TLS 验证。应用新 runtime 镜像及配置后，重跑 `dev-up.sh`
刷新指向旧 validator Pod 的隧道：

```sh
./scripts/dev-runtime-image.sh --build-only
./scripts/dev-runtime-config.sh
./scripts/dev-up.sh --refresh-tunnels
```

本地 MinIO 可选 `managed + direct`，由控制面管理专用桶及公开资产策略，静态源站
使用集群可达的 HTTP IPv4 地址；也可以选 `external + signed` 使用已存在的私有桶。
AWS S3、TOS 和其他兼容服务使用相同适配器，按服务能力选择 `path` 或 `virtual`。
完整配置及支持范围见 [对象存储](object-storage.md)。

## 本地变量（gitignored）

`.data/dev.env` 保存环境相关的非代码值；`dev-up.sh` / `dev-runtime-*.sh` 会自动读取，但不会覆盖命令行已设置的变量：

```bash
KRUN_DEV_REMOTE=<dev-host>
KRUN_DEV_KUBECONFIG=~/kind/kubeconfig
KRUN_DEV_CONTROL_HOST=<control-host>
KRUN_DEV_MINIO_HOST=<minio-host>
KRUN_DEV_MINIO_PORT=9000
KRUN_DEV_MINIO_LOCAL_URL=http://127.0.0.1:9000
KRUN_DEV_CLUSTER=<kind-cluster>
KRUN_DEV_RUNTIME_IMAGE=krun-worker-runtime:dev
```

`.data/dev.yaml` 中的 `s3_runtime_endpoint` 和 `config_base_url` 可以直接引用
`${KRUN_DEV_MINIO_HOST}`；`dev-server.sh` 同样会先读取 `dev.env`，避免同一地址在两份
gitignored 配置中漂移。

仓库里不保存这些值；缺少 `.data/dev.env` 时脚本会直接报错，而不是使用内网默认值。

## 启动 / 停止

```bash
./scripts/dev-up.sh      # 检查本地 MinIO，确保隧道、后端、Vite 都在运行
./scripts/dev-down.sh    # 停脚本管理的本地进程；MinIO / runtime 不动
```

打开：

- 控制台：http://127.0.0.1:5173/
- API：http://127.0.0.1:8080/api/

MinIO 由开发者在本机独立启动，凭据只放 `.data/dev.yaml`；`dev-up.sh` 只探测健康状态，不会把凭据复制到 `.data/dev.env`。脚本会构建一次后端二进制；之后改后端代码只需重新启动 `./scripts/dev-server.sh`（或再跑一次 `dev-up.sh`）。改 builder 代码直接生效，下一次构建就会用新脚本。

## Runtime 镜像与集群配置

serving runtime 与 validator 共用同一个 runtime 镜像。修改 `workers/runtime/` 后执行：

```bash
./scripts/dev-runtime-image.sh
```

它会构建 `krun-worker-runtime:dev`、加载到 kind、更新 `runtime-default` 与 `runtime-validator-default` 并等待 rollout。

如果集群重建，或开发机/MinIO 地址变化，重新应用 runtime 配置：

```bash
./scripts/dev-runtime-config.sh
```

脚本从 `.data/dev.env` 读取主机地址；需要临时改地址时直接覆盖环境变量，例如：

```bash
KRUN_DEV_CONTROL_HOST=<new-control-host> ./scripts/dev-runtime-config.sh
```

该脚本会写入 `runtime-config` 和不启动配置跟随/Ack 的 `validator-config`，确保独立
validator Deployment/Service 存在，创建供 Gateway 静态域使用、指向本地 MinIO 的
`static-assets` Service/EndpointSlice，更新 runtime/validator NetworkPolicy，并把 in-cluster 的
backend/postgres/minio/tester 缩到 0。业务 runtime 下载制品时使用
`s3_runtime_endpoint` 的开发机地址，不经过 `static-assets` Service。

validator 或 Gateway rollout 会让本地 38084/38080/38081 的 port-forward 失效；`./scripts/dev-up.sh` 会探测 `/ready` 并自动重建隧道。

## 外网域名

Worker 的外网访问：

```text
https://w-<hash>-<env>.p-<pool-hash>.<external-domain>/
```

内网入口：

```text
https://w-<hash>-<env>.p-<pool-hash>.<internal-domain>/
```

nginx 保留完整 Host，分别转发到域名绑定的 Gateway。

两个入口是**真正分开的两条数据路径**：

| 域名 | nginx upstream | 集群 Gateway 实例 |
| --- | --- | --- |
| `<external-domain>` | NodePort → `workers-public` | `workers-public`（listener `public`） |
| `<internal-domain>` | NodePort → `workers-mvp` | `workers-mvp`（listener `private`） |

- 域名本身就是路由 host，nginx 不再改写 Host，只把它转发到该域名绑定的网关实例。
- 服务在「路由与域名」里选择用哪些域名；每个环境在选中的每个域名下各有一个地址。
- **隔离边界**：每个网关实例的路由表里只有绑定到它的域名。在 A 实例上伪造 B 的 Host 匹配不到任何路由，返回 404；静态资产同理。

在 kind 宿主机上验证（NodePort 32284 = 公网，30739 = 内网）：

```bash
curl -o /dev/null -w '%{http_code}\n' -H 'Host: w-x-production.p-<pool-hash>.workers.test'        http://<node>:32284/  # 404
curl -o /dev/null -w '%{http_code}\n' -H 'Host: w-x-production.p-<pool-hash>.public.workers.test' http://<node>:32284/  # 200
```

## 远端 runtime 的前置条件

执行 `dev-runtime-config.sh` 后，kind runtime 指向开发机：

- `platform` 地址：`<control-host>:9001`（开发机控制面的 internal listener）。
- `CONFIG_BASE`：`http://<minio-host>:9000/<bucket>/config/pools/default`。
- `storage` network allow：`<minio-host>/32`、`<control-host>/32`。
- NetworkPolicy：允许 runtime 到上述两个地址的 9000/9001，以及本地管理隧道访问 8083。

如果开发机 IP 变了，需要同步改 runtime ConfigMap 里的 `<control-host>`、`CONFIG_BASE` 的 MinIO 地址（如果 MinIO 主机也变了）和 NetworkPolicy，然后重启 runtime。

## 构建与发布流程

1. 在控制台创建服务，或调用 `POST /api/v1/orgs/{org}/services`。
2. 上传源码包（`POST .../workers/source-uploads` → PUT `source.tar.gz`）。
3. `POST .../workers/builds`：控制面在**本机**启动 builder，约 1 秒完成；不创建 Job、不拉镜像。
4. `POST .../environments/production/deployments`：控制面在单个 runtime Pod 上 `/validate`，commit，写入本地控制面的 config revision；runtime 通过 `PLATFORM` 拉取并 ack。
5. 打开 `https://<external_host>/`，或用控制台的 invoke 调 `/headers` 等路径。

存储集成验证使用真实 local builder、对象存储、serving runtime 和浏览器；先登录并
完成本地用户的系统管理员初始化，再运行：

```sh
KRUN_UI_ORIGIN=http://127.0.0.1:5173 \
KRUN_DEV_GATEWAY_ORIGIN=http://127.0.0.1:38080 \
KRUN_DEV_STATIC_HOST=static.example.com \
KRUN_STORAGE_PREFIX=dev/run \
node deploy/workers-dev/storage.e2e.mjs
```

静态域名应填写主 YAML 中私有域对应的 `static_domain`。结果和浏览器截图保存到
被忽略的 `.data/dev-storage-e2e/`；测试组织和 Worker 保留，便于检查已部署页面。
