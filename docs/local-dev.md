# 本地开发环境（local-first）

只有一套开发环境。目标是把能跑在开发机上的组件都跑在开发机上，集群只保留无法本地替代的 runtime（workerd）和 kgateway。

## 拓扑

```text
开发机（Mac）                         远端主机 <dev-host>
├─ krun-server (Go)  :8080             ├─ MinIO (in cluster) minio:9000
├─ PostgreSQL        :25432            ├─ nginx 80/443
├─ builder (node)    本地子进程         └─ kind: runtime-default + kgateway
└─ Vite              :5173
        │                                     │
        ├─ 38443 → kind API                   │
        ├─ 38083 → runtime-default:8083  ─────┘
        └─ 38080 → workers-mvp:8080
```

- 控制面、Postgres、builder、前端都在 Mac；改后端或 builder 不需要构建镜像。
- MinIO 跑在集群里（`workers-mvp/minio`），和 runtime、gateway 同一网络面；控制面经 `<dev-host>:38090` 端口转发访问。
- 集群里只保留 `runtime-default`（workerd）和 kgateway；旧的 backend/postgres/minio/tester deployment 已 scale 到 0。
- 业务 Worker 的 host 仍然是 `w-xxx-<env>.workers.test`；nginx 把外网域名改写成它。

## 唯一配置

`.data/dev.yaml` 是唯一的本地开发配置，已被 `git` 忽略。不要再创建 `config/development.yaml`、`.data/development-*.yaml`、`.data/sso-local.yaml` 之类的变体；需要改端口、数据库、Worker 参数或 SSO 来源时直接改这一份。

真实 token、密码、数据库/S3 凭据和内网主机/IP 只放在 `.data/`；提交前运行 `make check-secrets`（`make test` 也会执行），tracked 文件里只保留占位符或 `${KRUN_*}` 引用。

关键字段：

- `builder_mode: local`：builder 在本机以 `node workers/builder/build-worker.mjs` 运行，不创建 Kubernetes Job。
- `external_domain`：控制台展示用的 HTTPS 后缀；当前是 `<external-domain>`（对外入口）。`<internal-domain>` 是内网地址，不作为控制台展示的浏览器链接。
- `gateway_base: http://127.0.0.1:38080`：控制台 invoke 经过 SSH 隧道打到集群 gateway。
- `runtime_management_base: http://127.0.0.1:38083`：候选验证和运行时管理走隧道。
- `s3_endpoint` 指向控制面可达的地址（本机经 `<dev-host>:38090` 端口转发）；`s3_builder_endpoint` 用集群内 Service 名，这样 builder/runtime 拿到的 presigned URL 在集群内可直达。

## 本地变量（gitignored）

`.data/dev.env` 保存环境相关的非代码值；`dev-up.sh` / `dev-runtime-*.sh` 会自动读取，但不会覆盖命令行已设置的变量：

```bash
KRUN_DEV_REMOTE=<dev-host>
KRUN_DEV_KUBECONFIG=~/kind/kubeconfig
KRUN_DEV_CONTROL_HOST=<control-host>
KRUN_DEV_MINIO_HOST=<minio-host>
KRUN_DEV_CLUSTER=<kind-cluster>
KRUN_DEV_RUNTIME_IMAGE=krun-worker-runtime:dev
```

仓库里不保存这些值；缺少 `.data/dev.env` 时脚本会直接报错，而不是使用内网默认值。

## 启动 / 停止

```bash
./scripts/dev-up.sh      # 确保 MinIO、隧道、后端、Vite 都在运行
./scripts/dev-down.sh    # 停本地进程；远端 MinIO / runtime 不动
```

打开：

- 控制台：http://127.0.0.1:5173/
- API：http://127.0.0.1:8080/api/

`scripts/dev-up.sh` 会构建一次后端二进制；之后改后端代码只需重新启动 `./scripts/dev-server.sh`（或再跑一次 `dev-up.sh`）。改 builder 代码直接生效，下一次构建就会用新脚本。

## Runtime 镜像与集群配置

runtime 是唯一需要镜像的组件。修改 `workers/runtime/` 后执行：

```bash
./scripts/dev-runtime-image.sh
```

它会构建 `krun-worker-runtime:dev`、加载到 kind、更新 `runtime-default` 并等待 rollout。

如果集群重建，或开发机/MinIO 地址变化，重新应用 runtime 配置：

```bash
./scripts/dev-runtime-config.sh
```

脚本从 `.data/dev.env` 读取主机地址；需要临时改地址时直接覆盖环境变量，例如：

```bash
KRUN_DEV_CONTROL_HOST=<new-control-host> ./scripts/dev-runtime-config.sh
```

该脚本会写入 `runtime-config`（`PLATFORM`、`CONFIG_BASE`、`storage` allow）、更新 runtime NetworkPolicy，并把 in-cluster 的 backend/postgres/minio/tester 缩到 0。

runtime rollout 会让本地 38083/38080 的 port-forward 失效；`./scripts/dev-up.sh` 会探测 `/ready` 并自动重建隧道。

## 外网域名

Worker 的外网访问（浏览器用 out）：

```text
https://w-<hash>-<env>.<external-domain>/
```

内网入口（in，内部地址，不作为控制台展示的浏览器链接）：

```text
https://w-<hash>-<env>.<internal-domain>/
```

nginx 只改 Host，两个入口分别指向各自的上游：

```text
w-xxx-production.<external-domain> → w-xxx-production.public.workers.test
w-xxx-production.<internal-domain> → w-xxx-production.workers.test
```

两个入口是**真正分开的两条数据路径**：

| 入口 | 浏览器域名后缀 | nginx upstream | 集群 Gateway 实例 |
| --- | --- | --- | --- |
| 公网 `out` | `<external-domain>` | NodePort → `workers-public` | `workers-public`（listener `public`） |
| 内网 `in` | `<internal-domain>` | NodePort → `workers-mvp` | `workers-mvp`（listener `private`） |

- 路由 host 用集群内部后缀匹配：私有 `workers.test`、公网 `public.workers.test`；浏览器不解析这两个名字。
- 控制台的 `external_host` 由 `external_domain` / `internal_domain` 推导，只影响展示和链接。
- 环境默认 `private`。在「环境配置 → 网络访问范围」显式切成 `public` 才会生成公网 host 与公网 HTTPRoute。
- **隔离边界**：公网实例的路由表里没有任何 `*.workers.test`，内网实例的也没有 `*.public.workers.test`。在公网入口伪造内网 Host 返回 404；静态域同理（`static.public` / `static.internal` 各挂一边）。

在 kind 宿主机上验证（NodePort 32284 = 公网，30739 = 内网）：

```bash
curl -o /dev/null -w '%{http_code}\n' -H 'Host: w-x-production.workers.test'        http://<node>:32284/  # 404
curl -o /dev/null -w '%{http_code}\n' -H 'Host: w-x-production.public.workers.test' http://<node>:32284/  # 200
```

## 远端 runtime 的前置条件

当前 kind 里的 runtime 已经指向开发机：

- `platform` 地址：`<control-host>:9001`（开发机控制面的 internal listener）。
- `CONFIG_BASE`：`http://<minio-host>:9000/krun-artifacts/config`。
- `storage` network allow：`<minio-host>/32`、`<control-host>/32`。
- NetworkPolicy：允许 runtime 到上述两个地址的 9000/9001，以及本地管理隧道访问 8083。

如果开发机 IP 变了，需要同步改 runtime ConfigMap 里的 `<control-host>`、`CONFIG_BASE` 的 MinIO 地址（如果 MinIO 主机也变了）和 NetworkPolicy，然后重启 runtime。

## 构建与发布流程

1. 在控制台创建服务，或调用 `POST /api/v1/orgs/{org}/services`。
2. 上传源码包（`POST .../workers/source-uploads` → PUT `source.tar.gz`）。
3. `POST .../workers/builds`：控制面在**本机**启动 builder，约 1 秒完成；不创建 Job、不拉镜像。
4. `POST .../environments/production/releases`：控制面在单个 runtime Pod 上 `/validate`，commit，写入本地控制面的 config revision；runtime 通过 `PLATFORM` 拉取并 ack。
5. 打开 `https://<external_host>/`，或用控制台的 invoke 调 `/headers` 等路径。
