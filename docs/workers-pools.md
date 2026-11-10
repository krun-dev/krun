# Workers 资源池与网络配置

资源池、Network Profile、Gateway 和域名统一由主 YAML 的 `workers:` 段配置，字段以
[配置示例](../config/example.yaml) 和 `WorkersConfig` 为准。系统管理员可在
「平台管理 → 系统配置」只读查看当前控制面加载的资源池、默认池、入口与出口策略、
候选校验方式及匹配的 Network Profile。页面显示配置声明，实际副本数和健康状态由集群提供。
凭据、内部地址、服务名及标签和选择器原文仅保留在部署侧。

## 资源池与服务分配

`runtime_pools` 定义池 ID、public/private 入口能力、标签、出口能力、serving Service
及认证凭据。每个池对应部署侧维护的 workerd 运行环境。

候选校验有两种模式，在同一主配置的 `workers:` 段二选一：

- `validator_pool: ""`（默认）：从业务当前 placement 的 Ready serving Pod 中选一个。
  不额外创建 validator；候选使用独立 WorkerLoader 实例和 Cache API 命名空间，禁用
  对象存储签名和传输。候选与业务共享进程 CPU、内存，失败或资源耗尽仍可能影响业务。
- `validator_pool: <pool-id>`：所有业务 pool 共用所选池的独立 validator。所选池必须
  配置 `validator_service`，且与 serving Service 不同；渲染器只为该池创建独立 validator
  Deployment、管理 Service、配置及 NetworkPolicy，不接受 Gateway 入站。

控制面使用目标池的 management token，并仅发送本次候选的不可变描述符、短时制品 URL
及绑定的 Secret 值。不会把业务池 runtime token 交给共享 validator；内部数据接口的
placement 权限保持不变。校验后再次检查 Ready EndpointSlice 成员，变化时重试。
无可用成员时等待，不跨模式回退。

升级期间暂停发布，先升级 runtime 镜像，再升级控制面后恢复发布。控制面通过 `/ready` 的 `validation_protocol: 1`、
role、pool 和 Pod UID 确认能力，旧镜像不会执行候选。已有独立 validator 不会被渲染器
自动删除，切换模式并确认发布成功后，由运维显式清理未再使用的资源。

校验检查制品、模块初始化及 `KrunValidation`，不遍历业务 `fetch()` 分支，也不执行真实
对象存储读写。通过校验不代表目标业务 pool 的网络、存储权限、配置分发或资源容量已通过
验证；这些能力需要在目标 serving pool 上用真实请求检查。

组织用户选择 `network_profiles` 中公开且获准使用的产品配置。Profile 通过
`pool_selector`、`domain_selector` 与入口/出口 requirements 筛选基础设施；名称和 ID
不代表网络能力。每个 domain 经 Profile 必须只匹配一个运行池，否则配置加载失败。
服务 placement 仍持久化；扩容通过增加同一池的副本实现。

一个服务的 production 与所有 preview 环境共用一个 active primary placement。
Version 固定代码和配置，不携带 pool；路由和内部制品访问根据有效 placement 限定池。
首次发布后不能由组织侧切换 Network Profile。

## 入口与域名

业务 host 为 `w-<worker-hash>-<environment>.<domain>`。
控制面按 `(pool, domain)` 协调共享通配 HTTPRoute，后端是该池 serving Service，
因此网关资源数量由基础设施目录决定。静态资产按 domain 单独协调。

每个 domain 显式绑定 Gateway。不同入口的隔离来自独立 Gateway 实例及其路由集合；
在一个 Gateway 上伪造另一个 domain 的 Host 不会匹配路由。DNS、TLS 和部署侧代理
必须覆盖 `*.<domain>`。细节见 [域名与路由](workers-domains.md)。

## 配置分发与权限

每站点 dirty 队列驱动增量发布，Runtime 只跟随本池 ops，冷 host 定向读取。
对象存储为每个池保存紧凑 head 和 256 个 host-hash shard checkpoint。Runtime 内存
缓存有界，不在启动时加载全池站点。详见 [规模与验证](performance-scaling.md)。

内部 listener 要求 `X-Runtime-Pool` 和对应 Runtime token；路由、Version、Artifact、
Secret 与资产读取继续校验该池有效 placement。Version Secret 接口仅接受对应池的
serving runtime token。候选验证通过 management token 认证，所需的 Secret 随验证请求传递。
业务 Worker 无平台 token、对象存储密钥或控制面 binding。

## 公网出网

池显式配置 `public_egress: direct` 后，serving workerd 给业务 Worker 提供独立公网出口。
Worker 可通过 `fetch()` 访问公网 HTTP 和 HTTPS，通过 `cloudflare:sockets` 的 `connect()`
访问公网 TCP。workerd 按实际解析地址过滤，拒绝私有地址和
`workers.protected_egress_cidrs` 中的部署侧保护地址。部署方应在该列表加入公网可路由的
控制面和管理地址。候选校验保持关闭全局出网。

省略池的出口字段时默认使用 `deny`。配置改变后重新渲染并重启 serving workerd。
实际连通性还受宿主机网络和部署环境出口规则约束。
PostgreSQL 托管接入的池级限制和代理配置见 [Workers 出网](workers-egress.md)。

## 运维

基础设施目录变更通过重新加载主配置生效；影响路由的配置 hash 变化会将服务加入有界
重建队列。不能把已有池 ID 或存储位置 ID 静默指向不同的安全边界或数据位置。
开发使用 [local-first](local-dev.md)，完整验收使用独立 [kind 栈](../deploy/workers-kind/README.md)。

## Runtime API 地址

部署方在主配置设置 `workers.runtime_api_url`，例如 `https://console.example.com`。
留空时 workerd 使用控制面内部 Service；配置后 serving 和 validator 均通过该 HTTPS
origin 调用 `/api/internal/workers/*`。该设置会接收 pool 认证 token，不向组织/Worker
用户开放，也不进入业务 binding。只接受无凭据、路径、query、fragment 的 HTTPS origin。

workerd 验证服务器证书与 SNI，并发送配置域名的 Host。生产渲染器在控制台选中的
Gateway 上生成 `krun-runtime-api` HTTPRoute，只转发 Runtime API 前缀到内部 Service；
同时允许该 Gateway 访问内部端口，并允许 runtime/validator 访问 Gateway 的 HTTPS
端口。DNS、证书、TLS 终止与必要的边缘网络出口由部署方配置。若使用其他前置代理，
须保留 `Authorization`、`X-Runtime-Pool` 及请求路径；未知凭据继续返回 403。

地址修改后重新渲染并部署，ConfigMap hash 变化会滚动更新 workerd；不需要重新构建
Worker 制品。该配置不修改对象存储地址或控制面调用 runtime 管理端口的 Pod 选址。
