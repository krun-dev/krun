# Workers 资源池与网络配置

资源池、Network Profile、Gateway 和域名统一由主 YAML 的 `workers:` 段配置，字段以
[配置示例](../config/example.yaml) 和 `WorkersConfig` 为准。控制台没有资源池管理、策略
rollout 或跨池迁移 API。

## 资源池与服务分配

`runtime_pools` 定义池 ID、public/private 入口能力、标签、出口能力、serving Service、
独立 validator Service，以及两个不同的认证 token。serving 和 validator 不能复用同一
Service 或凭据。每个池对应部署侧维护的 workerd 运行环境。

组织用户选择 `network_profiles` 中公开且获准使用的产品配置。Profile 通过
`pool_selector`、`domain_selector` 与入口/出口 requirements 筛选基础设施；名称和 ID
不代表网络能力。每个 domain 经 Profile 必须只匹配一个运行池，否则配置加载失败。
服务 placement 仍持久化；扩容通过增加同一池的副本实现。

一个服务的 production 与所有 preview 环境共用一个 active primary placement。
Version 固定代码和配置，不携带 pool；路由和内部制品访问根据有效 placement 限定池。
首次发布后不能由组织侧切换 Network Profile。

## 入口与域名

业务 host 为 `w-<service-hash>-<environment>.<domain>`。
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
Secret 与资产读取继续校验该池有效 placement。validator 的 management token 用于
候选验证，不进入业务 binding。业务 Worker 无平台 token、对象存储密钥或控制面 binding。

出口约束由 workerd 配置和 Kubernetes NetworkPolicy 共同落实。主 YAML 描述能力并
校验匹配关系，不会凭空创建代理、网络规则或 Runtime Deployment。Secret 和 ASSETS
binding 已实现；Service/数据库 binding、资源池管理页面和跨池迁移均不在当前接口范围。

## 运维

基础设施目录变更通过重新加载主配置生效；影响路由的配置 hash 变化会将服务加入有界
重建队列。不能把已有池 ID 或存储位置 ID 静默指向不同的安全边界或数据位置。
开发使用 [local-first](local-dev.md)，完整验收使用独立 [kind 栈](../deploy/workers-kind/README.md)。
