# Workers kind 独立 E2E

本目录创建完整、独立的集群验收栈：backend、PostgreSQL、MinIO、builder、serving runtime、独立 validator
和 kgateway 都在 kind 内。它不是日常开发环境；不要与
[local-first](../../docs/local-dev.md) 同时运行。

## 前置条件

- Docker、kubectl、Python 3、Node.js、Go 1.26、OpenSSL
- 已安装 Gateway API、kgateway、支持 NetworkPolicy 的 CNI 和默认 StorageClass
- 明确指定的 kind kubeconfig 与集群名

实际主机、路径和集群名只放在 `.data/dev.env` 或当前 shell，不写入 tracked 文件：

```bash
export KUBECONFIG=/absolute/path/to/kind-kubeconfig
export KIND_CLUSTER_NAME=my-krun-e2e
```

kind 内的 TLS fixture 同时提供 Git smart-HTTP 与固定的 Hono npm 包；builder Job
只允许访问这个 fixture 和 MinIO，不在验收运行期访问公共 npm registry。fixture
镜像首次构建仍需下载依赖；受限网络可在当前 shell 临时设置标准 `HTTP_PROXY` /
`HTTPS_PROXY`（大小写均支持），部署脚本只把它们作为 Docker 的预定义 build args
传入，不写入文件或镜像环境。生产应把 `workers.npm_registry` 指向稳定的公司 npm
proxy，并由基础设施策略只放行该地址。

部署脚本会同时校验当前 context 必须等于 `kind-$KIND_CLUSTER_NAME`，防止把破坏性
故障注入发到错误集群。

## 部署

```bash
bash deploy/workers-kind/deploy.sh
```

脚本按目标 kind 节点的 `amd64` / `arm64` 架构构建控制面与镜像、导入目标节点、
生成 manifest、部署并等待 Gateway 和各 Deployment 就绪。MinIO 镜像从固定的官方
GitHub version 二进制构建，并校验仓库内固定的 SHA-256；不依赖已撤下的公共容器
镜像。脚本还会生成一个短期本地 CA 和服务端证书，用于集群内的 Git smart-HTTP
fixture；CA 只烘焙进本次 backend 与 kind 专用 builder 测试镜像，私钥只进入
gitignored 的生成目录和测试 namespace Secret。生成的凭据、manifest 与二进制位于 gitignored 的
`deploy/workers-kind/generated/`。凭据会复用；已有 PVC 对应的凭据丢失时应先恢复或
显式迁移，不能直接生成一套新凭据覆盖。

## E2E

```bash
cd deploy/workers-kind
KUBECONFIG="$KUBECONFIG" KIND_CLUSTER_NAME="$KIND_CLUSTER_NAME" node e2e.mjs
```

脚本会：

- 通过真实 `/api/auth/*` 登录并创建临时组织、服务和限时 API Key；
- 通过集群内 TLS Git smart-HTTP fixture 绑定仓库，验证固定 commit、Wrangler
  build init container、独立 pack container、制品 finalize、发布和 kgateway 请求；
- 提交 Kubernetes build Job，按锁文件安装真实 Hono 依赖，读写 MinIO、发布并经 kgateway 调用 workerd；
- 验证 `node:crypto`、version-scoped `env.ASSETS`、幂等、权限隔离、配置 revision/ack、回滚和静态资产；
- 发布 [`examples/workers-fullstack`](../../examples/workers-fullstack/README.md) 的真实前后端项目，
  验证同源 API、SPA fallback、HTML 规范化、ETag/304、immutable 缓存与静态域；
- 重启 backend、替换 serving runtime Pod，并验证认领租约与持久化恢复；候选只由不接 Gateway 的 validator 加载；
- 暂停一个承流 Pod 的配置同步，验证双版本发布停在 `committed`，恢复同步后进入
  `available`。扩缩容与调权后，从 Gateway 检查承流 Pod、版本和 HTTPRoute；
- 在目标 kind 节点做临时网络故障注入，随后撤销同一规则。

因此运行者需要 Docker 权限以及 kind 节点中的 `crictl`、`nsenter`、`iptables`。
控制面 API 通过脚本启动的 `kubectl port-forward` 访问，默认只绑定本机 `127.0.0.1:4380`。
集群内的 Gateway 和 MinIO 请求仍从 tester Pod 发出。GET 和带幂等键的写请求可有限重试。

证据写入 gitignored 的 `evidence/`；临时 API Key 只写入权限为 0600 的
`generated/api-key.json`。验收结束后应删除测试 namespace/集群或按组织 ID 清理测试
数据，不能把生成凭据提交到仓库。

Go 控制台和 Runtime API 都在 backend Pod 内绑定回环 IP。backend Service 只开放 9001，
由同 Pod 的转发容器连接到内部 Runtime API。NetworkPolicy 只允许 serving runtime 和
validator 访问该端口，Runtime API 仍验证池凭据。

## 同源 UI 与规模回归

测试镜像同时包含 `npm run build` 导出的静态控制台；`deploy.sh` 会在本机依赖可用时构建，
也可预先提供 gitignored 的 `generated/console/`。Go 在同一 origin 提供 `/` 与 `/api/*`，
便于真实浏览器验证；生产前端仍为 OSS 静态部署。

默认 `public_url` 为 `http://127.0.0.1:4380`。端口冲突时，在部署和 E2E 两步都设置
`KRUN_E2E_ORIGIN` 为同一个测试 origin，并在 UI 测试中使用对应的 `KRUN_UI_ORIGIN`：

```bash
# 在单独终端保持转发运行
kubectl -n workers-mvp port-forward deployment/backend 4380:9000 --address=127.0.0.1

npx playwright install chromium
KRUN_UI_ORIGIN=http://127.0.0.1:4380 npm run test:ui
```

扩展 E2E 覆盖同源静态入口、百站点游标分页、共享 HTTPRoute 数量、有界配置 head/shard，
以及实际过期 Artifact 的 indexed GC，确认回收不会删除仍在服务的资产。
百万 PostgreSQL 元数据测试及算法微基准见 [性能验证](../../docs/performance-scaling.md)。
原版 Wrangler 的双版本切流、定向验证、缓存和同池、跨池服务绑定见
[Wrangler 端到端验证](../../docs/cloudflare-api.md#端到端验证)。

## 数据基线

测试默认使用专用 PostgreSQL 数据库 `krun_e2e` 和对象桶 `krun-e2e`；部署和测试可同时
设置 `KRUN_E2E_DATABASE` / `KRUN_E2E_BUCKET` 选择另一套专用数据。数据库与对象桶必须
成对使用，避免从其他测试库读取配置 head。部署只创建不存在的测试库，不清空已有库。
基线不匹配时应由测试环境所有者显式重建专用数据库，或选择新的库和桶。
