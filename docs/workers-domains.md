# Workers 域名接入设计

状态：**内外网拆分已实现**（P1 的一部分）。实现位置：`internal/control/workers_model.go`（host 推导与 scope 归类）、`workers_kubernetes.go`（按 scope 编排 HTTPRoute）、`deploy/workers-kind/manifests.py`、`deploy/workers-dev/networkpolicy.yaml.tmpl`、`workers/runtime`。

已落地的是「两个网络面」这件事本身：

- 每个环境同时持有 `host`（私有）与 `public_host`（公网，可选），由 `WorkersConfig` 的 `domain` / `public_domain` 推导。
- 两个 scope 各有一个 Gateway 实例（`gateway_name` + `public_gateway_name`），各有一组 HTTPRoute（`workers-<digest>` 与 `workers-<digest>-public`）。
- 静态资产同样按 scope 拆：`static.public.*` 走公网实例，`static.internal.*` 走私有实例。
- 环境默认 `private`；切到 `public` 是显式动作，且未配置 `public_domain` 时直接拒绝。

尚未实现：域名列表（多后缀叠加）、按域名审批、dataplane/workers-router 模式、自定义域名。本文其余部分描述这些目标形态；与现状的差异见文末「现状与迁移」。

## 1. 目标与非目标

目标：

1. Workers 默认通过随机前缀子域名直接访问，无需用户做任何 DNS 操作。
2. 平台维护主域名（domain）列表，含内网与公网后缀，如 `*.workers.kanyun.run`、`*.workers.kanyun.com`、`*.workers.internal.kanyun.run`；服务可同时选用多个域名。
3. **域名是叠加的**：为服务新增域名只增加新 host，不改变已有 host；增改永不断流，只有显式移除域名才下线对应 host。
4. 每个域名可配置「选用需审批」开关；开启的域名，服务选用后需管理员批准才生成 host。
5. 路由架构支持两种模式：标准模式（Gateway API，per-worker HTTPRoute）与自有数据面模式（直连 workers-router，不经 Gateway），由配置切换，URL 空间不变。
6. 为多 runtime pool 预留：host 到 pool 的映射由控制面下发，边缘不写死。

非目标（本期不做）：

- 自定义域名 / CNAME 接入（含所有权验证与逐域证书）。host 命名模型与路由快照已为其预留，实现延后；未来自定义域名与平台域名共用同一套「叠加 + 审批 + host 推导」机制。
- 跨 Pod 原子流量切换、灰度权重。
- 公网 TLS 的具体签发方案选型（本文只约定证书挂载点）。

## 2. 总览

```text
                    控制面（host 表唯一事实源）
                    slug × domains × approval × pool
                              │ reconcile
                              ▼
            ┌─ routing.mode: gateway（默认）─┐
            │ controller 编排 HTTPRoute      │
            │ LB → Gateway → runtime-<pool>  │
            └────────────────────────────────┘
            ┌─ routing.mode: dataplane ──────┐
            │ 零 K8s 路由对象                 │
            │ LB → workers-router → runtime  │
            └────────────────────────────────┘
```

两种模式共享同一套不变量，切换只改「谁做 host 匹配」：

- **host 命名模型**（第 3 节）：URL 空间完全一致，切模式对用户零感知，两套可并行灰度切流。
- **runtime 快照分发**（第 6 节）：host → release 的最终寻址始终在 runtime，边缘只负责把正确 host 的流量送到正确 pool。
- **安全不变量**（第 7 节）：未知 host 404；客户端伪造的内部头在边缘剥除；内网 host 不出现在公网入口的路由上；未获批域名不生成任何 host。
- **边缘不带内容相关的 path 规则**：主域的 HTTPRoute 与 workers-router 都只按 host 整域转发；路径级分流（静态资产 vs 业务 Worker）由 Pod 内入口 Worker 承担，见 [workers-assets.md](workers-assets.md)。静态字节走独立静态域（每条路由只按 host 匹配，路径仅做前缀改写），见 assets 第 6 节。

## 3. 域名模型

### 3.1 Domain（主域名）

平台在配置中维护域名列表。每个域名是一个受管 DNS 后缀，带网络 scope 与审批开关：

```yaml
workers:
  domains:
    - suffix: workers.kanyun.run
      scope: public          # 公网 DNS 解析，指向公网入口
      default: true          # 新建服务默认选用
      approval: false        # 选用免审批（默认）
    - suffix: workers.kanyun.com
      scope: public
      approval: true         # 选用需管理员审批
    - suffix: workers.internal.kanyun.run
      scope: private         # 仅内网解析，指向内网入口
      default: true
```

- `scope` 决定该域名下的 host 挂到哪个 Gateway listener（public / private section）以及路由编排分组（第 5 节）。内外网的网络隔离本身靠 DNS + LB 实现（公网后缀公网解析、内网后缀仅内网解析），不靠代码。
- `default: true` 的域名是新建服务默认选用的集合（可多个）。建议实践中内网域名设 default、公网域名不设：让「上公网」始终是显式动作（或经审批），而不是建服务就默认挂上。
- `approval` 控制选用该域名是否需要审批（第 3.3 节）。
- 域名下线：从配置移除后缀后，对应 host 随快照与路由消失，不迁移存量数据（host 由 `(slug, env, suffix)` 推导）。
- kind / 开发环境单域名：列表只放一个 `workers.test`（scope private），自然退化。

### 3.2 Slug 与 Host 规则

- 每个服务创建时一次性分配持久 slug：`w-` + 16 位 hex（`digest(org/service)[:16]` 起始，碰撞换 salt 重试）。slug 全局唯一、创建后不变、不可枚举。
- Host 拼接规则（单一实现 `hostFor(slug, env, suffix)`）：

```text
baseline（production）:   {slug}.{suffix}                 w-a3f9c2d1.workers.kanyun.run
非 baseline（preview 等）: {slug}-{env}.{suffix}           w-a3f9c2d1-pr-42.workers.kanyun.run
```

- **每个环境每个选用域名各一个 host**：`WorkerEnvironment.Hosts []string` 由 `选用域名 × 启用环境` 全量推导，不逐条存储。
- baseline 不带环境后缀：生产 URL 干净、不把内部环境术语烧进公网 URL。
- 子域严格保持一层。`*.suffix` 通配符只匹配一层子域；每个域名一张通配符证书盖住其下全部服务与环境，永不因新增环境签证书。禁止 `{env}.{slug}.{suffix}` 嵌套形式。
- slug 必须保持定长 hex。host 解析（slug 与 env 切分）依赖「前 18 字符是 slug、存在第 19 个字符起 `-` 即环境名」这一唯一性；不允许变长可读 slug。

### 3.3 域名选用与审批

服务模型：

```text
WorkerService:
  Slug            string                    // 持久随机 slug
  Domains         []string                  // 选中的域名后缀（创建时 = default 域名集合）
  DomainApprovals map[string]DomainApproval // 域名后缀 → {State: pending|approved|rejected, Actor, Time}
```

- `approval: false` 的域名：选用即生效，host 立即可用，不产生审批记录。
- `approval: true` 的域名：服务选用后状态 `pending`，管理员批准后 `approved`，host 才开始生成；`rejected` 可重新发起。撤销审批（`approved → pending`）立即停发该域名的 host。
- **审批是数据面隔离**：`pending` / `rejected` 的域名不生成任何 host、不进快照、不建路由——不是 UI 提示层。
- 审批粒度是「服务 × 域名」：批准某服务用 `workers.kanyun.com` 不影响其他服务。审批 API 挂系统管理员权限（复用现有系统管理员判定），操作记审计日志。

**叠加语义**（本设计的关键性质）：

- 为服务**新增**域名 = 在已有 host 之外多推导一批新 host。已有 URL 一个都不变，无迁移、无断流。内网服务上公网 = 选用公网域名（过审批），内网 host 原样保留——办公网用户继续走内网域名，公网用户走公网域名，同一服务双通道并存。
- **移除**域名是唯一断流操作，显式发起：控制台移除前确认提示该域名下所有环境的 URL 将下线。平台侧下线域名同理（配置移除即全体生效）。
- 换域名不掉链：先加新、后删旧，中间任意间隔，两批 host 同时服务。

## 4. 配置与数据模型变更

`WorkersConfig`（`workers_model.go`）：

```text
- Domain string                 → 删除（由 Domains 列表取代）
+ Domains []WorkerDomain        // {Suffix, Scope, Default, Approval}
- GatewayName / GatewayNamespace → 拆为 PublicGateway / PrivateGateway（各含 name + namespace + sectionName，
                                   // 缺省一组兼容 kind 单实例：只配 private）
+ RoutingMode string            // "gateway"（默认）| "dataplane"
+ RouterService string          // dataplane 模式的 router service 名，默认 "workers-router"
```

validate：Domains 非空、scope ∈ {public, private}、后缀 DNS 合法、default 唯一或空（为空时取列表第一个）；存在 public 域名时 PublicGateway 必填，存在 private 域名时 PrivateGateway 必填。

服务模型（`WorkerService`）：

```text
+ Slug            string
+ Domains         []string
+ DomainApprovals map[string]DomainApproval
- WorkerEnvironment.Host string → Hosts []string（推导生成；兼容读旧单值）
```

`WorkerEnvironment.Visibility` 字段不再使用（现状恒 `private`，可顺手移除）。

`WorkerRelease.Pool` 已存在（现恒 `default`）；`runtimeMembers` 的 `pool=default` selector（`workers_kubernetes.go:145`）参数化，多池为两种模式共同前置。

## 5. 路由

### 5.1 驱动接口

controller 不再直接编排路由对象，改为构造时按 `routing.mode` 选择驱动：

```go
type routeBackend interface {
    // desired 由 controller 从 WorkerState 统一推导：
    // (scope, pool) → host 列表，gateway 与 dataplane 驱动共用同一推导。
    ensure(ctx context.Context, desired routingDesired) (ready bool, err error)
}
```

`reconcile`（`workers_controller.go`）中 per-service 的 `ensureRoutes` 调用替换为 `routeBackend.ensure(desired)`；`routesReady` 闸门（`processOperation` 中等待 Gateway 确认处）在两种模式下语义一致。

### 5.2 模式一：gateway（默认，一个 worker 一组 HTTPRoute）

**Gateway 按 scope 拆实例**：public 与 private 各一个 GatewayClass + Gateway，对应不同网关实例（不同 deployment）。内外网隔离落在实例边界上，而非同一实例的 listener 隔离——即使某实例的 LB / 端口配置失误，暴露的也只是本 scope 的流量面，另一 scope 的数据路径上根本没有对方后缀的路由。

```yaml
# 公网入口：独立 GatewayClass + 实例
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: { name: workers-public, namespace: kgateway-system }
spec:
  gatewayClassName: workers-public            # 对应公网网关实例
  listeners:
    - name: public
      port: 443
      protocol: HTTPS
      tls:
        mode: Terminate
        certificateRefs:          # 每个公网域名一张通配符证书，或多 SAN 合一
          - { name: workers-kanyun-run-tls }   # *.workers.kanyun.run
          - { name: workers-kanyun-com-tls }   # *.workers.kanyun.com
      allowedRoutes: { namespaces: { from: Selector, selector: { matchLabels: { lab: krun-workers-mvp } } } }
---
# 内网入口：独立 GatewayClass + 实例
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: { name: workers-private, namespace: kgateway-system }
spec:
  gatewayClassName: workers-private           # 对应内网网关实例
  listeners:
    - name: private
      port: 8080
      protocol: HTTP              # kind / 内网；生产如需内网 TLS 同 public 写法
      allowedRoutes: { ... }
```

公网域名后缀全部挂 public 实例的 listener，内网域名后缀全部挂 private 实例。若希望每个 domain 一个 listener（证书粒度 / 流量治理按域名分开），同一 Gateway 内按域名开 listener、route 的 `sectionName` 指到对应域名 listener 即可；域名数量多时注意 kgateway 每实例 listener 上限（默认 64），超出再合并。是否拆 listener 不影响正确性——隔离由实例（GatewayClass）保证。

HTTPRoute per worker、**按 scope 拆组**（`workers-<digest>` 为 private 组、`workers-<digest>-public` 为 public 组）：

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: workers-<digest(org/service)[:24]>-public
  labels: { lab: krun-workers-mvp }
spec:
  parentRefs:
    - { name: workers-public, namespace: kgateway-system, sectionName: public }
  hostnames:                # 该服务在 public 域名下所有启用环境的 host
    - w-a3f9c2d1.workers.kanyun.run
    - w-a3f9c2d1-pr-42.workers.kanyun.run
  rules:
    - filters:
        - type: RequestHeaderModifier
          requestHeaderModifier:
            remove: [x-krun-scope, x-krun-release, x-forwarded-host, x-internal]
      backendRefs:
        - { name: runtime-<pool>, port: 8081 }
```

**为什么按 scope 拆组而不合一条**：hostname 匹配只看 Host 头。若一条 route 同时挂两个 Gateway（或同实例两个 listener）且 hostnames 混含内外网后缀，攻击者在公网入口伪造内网 Host 头即可命中路由打到 runtime，内网-only 服务被公网可达（TLS 也未必兜住：SNI 用公网域名、Host 用内网域名时多数网关照样转发）。拆组 + 拆实例后，公网侧的整条数据路径（实例 → 路由表 → runtime）上不存在内网后缀，伪造内网 Host 在公网入口不匹配任何路由，404。这是 DNS 隔离之外的强制边界。

实现要点（改造现有 `ensureRoutes`，`workers_kubernetes.go:224`）：

- parentRef 按 host 所属域名的 scope 指向对应 Gateway（`workers-public` / `workers-private`），不再写死单 Gateway 单 section；现配置 `gateway_name` 拆为 `public_gateway` / `private_gateway`（或 gateway + class 成对配置）。
- **hostname 分片**：Gateway API 每条 HTTPRoute 最多 16 个 hostname。baseline + preview × 多域名可能超限（`PreviewLimit` 上限 128）。controller 通用分片：组内首条沿用组名，超出递增 `-1`、`-2`；语义上仍是「一个 worker 一组 route」。
- **补 prune**：现实现只建和改不删。域名选用变更、preview 增删、服务删除后必须清理多余分片与整个 route，否则 hostnames 只涨不缩、对象残留。
- backend 按 pool 选择：同一服务的 host 固定指向 `runtime-<pool>`；pool 变更属重新发布（Release 引用 pool，已有约束）。
- 未获批域名不进 desired，不产生任何路由——审批在数据面生效的落点。

### 5.3 模式二：dataplane（自有数据面，预留口子）

```yaml
workers:
  routing:
    mode: dataplane
```

- LB / DNS 直接指向 `workers-router` Service，不经 Gateway；集群内零 Gateway / HTTPRoute 对象（workers 相关）。
- workers-router 是无状态薄转发层，建议以 runtime 二进制 `--mode=router` 实现，复用快照拉取、健康检查、鉴权代码：
  - 定期从控制面拉 `host → pool` 表（复用 `/config/changes` + revision + stale-ok 模式），拉取失败继续用旧表；
  - 请求查表：miss 一律 404；命中则 `ReverseProxy` 到 `http://runtime-<pool>.<ns>.svc:8081`，`req.Host` 原样透传，runtime 继续按快照做 host → release 解析；
  - 公网 / 内网两个入口各自面前放一组 router（或同一组、入口层分开），内网 host 表按入口过滤下发；TLS 在 LB 终止或 router 挂通配符证书，按生产入口形态定。
- dataplane 驱动的 `ensure()` 不编排路由对象：router deployment/service 由部署侧管理，controller 只读 endpoints 健康；readiness = router endpoints ready。

模式价值：后续限流、灰度、多区域调度、自定义域名（CNAME 直指 router，无需 HTTPRoute）都收敛在 router 一个组件演进，不与 Gateway API 的表达能力搏斗。

## 6. Runtime 配置与多池

`workerRuntimeConfigSnapshot` / `workerRuntimeConfigChanges`（`workers_runtime.go`、`workers_config.go`）调整：

- route 条目：`host`、`path`、`enabled`、`release` 之外增加 `pool`。原 `scopes` 数组移除（随 visibility 一并取消）：内外网区分由 listener / 入口层承担，runtime 只按 host 匹配。
- host 列表来源：该服务所有 `approved`（或免审批）域名 × 各启用环境。
- 未获批域名的 host 本就不在配置投影中（第 3.3 节），无泄漏面。
- 配置按 pool 过滤下发：各 pool 的 runtime 只拉自己的 hosts + releases（现为全局下发、releases 仅滤 `default`；`/config/changes` 和 `/config/snapshot` 增加 pool 参数）。router（dataplane 模式）拉独立的 `host → pool` 汇总表。
- runtime 侧只有内存视图：bootstrap 从对象存储拉不可变快照，之后用 `/config/changes` 增量 apply；不落盘、不落本地 DB。控制面不可用时保留最后一份视图继续服务。
- Pod 内入口 Worker 的 host → 环境 Release 查找逻辑不变。

多池前置改造：`runtimeMembers(ctx, pool)` selector 参数化；`WorkerRelease.Pool` 在服务 / 发布 API 层可选。

## 7. 安全不变量

1. **未知 host 一律 404**：寻址权唯一在快照，`host ∈ snapshot` 才放行。同时是防租户 Host 伪造的边界。
2. **内网 host 不出现在公网入口的数据路径上**：HTTPRoute 按 scope 拆组（第 5.2 节），且 public / private 各自独立 GatewayClass + Gateway 实例。公网实例的路由表只含公网后缀，公网入口伪造内网 Host（包括 SNI 用公网域名、Host 头用内网域名的组合）匹配不到任何路由，404。实例级拆分使隔离不依赖单实例 listener 隔离与端口配置正确性。dataplane 模式下由 router 的 host 表按入口过滤承担同一职责。
3. **网关实例与网络面对应**：public 网关实例的 LB / 端口只做公网暴露，private 网关实例只挂内网 LB（内网类型实例）。警惕 k8s 旁路：NodePort、节点 IP 直连、LoadBalancer Service 误用公网实例——一旦 private 实例端口公网可达，攻击者无需伪造即可直接访问内网路由。本条是部署不变量，列入发布检查。
4. **客户端伪造的内部头在边缘剥除**：`x-krun-scope`、`x-krun-release`、`x-forwarded-host`、`x-internal` 在 HTTPRoute filter（或 router）统一 remove（现 filter 已做，保持）。
5. **审批是数据面隔离**：未 `approved` 的域名不生成 host、不进快照、不建路由——不是 UI 提示层。
6. slug 不可枚举（16 位 hex 随机），host 无法被遍历猜测；未知 host 的 404 不区分「不存在 / 已禁用 / 未发布 / 未获批」。
7. 未发布环境返回 503、已禁用环境 runtime 拒绝执行——沿用现有语义。

## 8. 控制台 invoke 路径

`workers_invoke.go` 的 console invoke 现直连 Gateway svc（`workers-mvp.kgateway-system.svc:8080`）+ 伪造 Host。改为按 `routing.mode` 切换目标：gateway 模式下直连目标 host 所属 scope 对应的 Gateway svc（多 Gateway 后不再固定打 private）；dataplane 模式直连 `RouterService`（或按快照直连对应 pool 的 runtime svc）。invoke 的鉴权（`service.invoke`）与响应封装不变。

## 9. 现状与迁移

现状（已核实）：

- **网络面拆分已实现**。`WorkersConfig` 有 `domain`/`public_domain` 与 `gateway_name`/`public_gateway_name` 两组配置；`hostFor` 统一推导 host（`workers_model.go`），`syncEnvironmentHosts` 在每个写路径重算 `Host` / `PublicHost` / `ExternalHost`。
- 每个 scope 一条 HTTPRoute：私有 `workers-<digest>`、公网 `workers-<digest>-public`，各自 `parentRef` 到自己的 Gateway 实例与 listener；scope 无 host 时删除该条路由（prune 已补）。
- 静态资产两条共享路由按 scope 分挂（`workers-static-pub` → 公网实例，`workers-static-int` → 私有实例），默认后缀不会重合。
- 配置投影与 runtime 快照同时包含两套 host，runtime 仍只按 Host 匹配。
- `Visibility` 参与路由：`private`（默认）不生成公网 host；`public` 需配置 `public_domain`，否则 PATCH 返回 `PUBLIC_SCOPE_UNAVAILABLE`。
- 剩余缺口：域名仍是单一 public/private 后缀对（没有多后缀列表）、无审批、无分片、无 dataplane 模式。

迁移阶段：

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| P1 | scope 拆分的 host 模型（`hostFor` + `Host` / `PublicHost`） | ✅ 已完成 |
| P2 | gateway 驱动改造：scope 拆组、双 Gateway 实例、prune | ✅ 已完成（分片未做） |
| P3 | 审批 + 多域名列表 | 未开始 |
| P4（预留口子） | dataplane 模式 + workers-router | 未开始 |

P1–P2 已落地；P3 是下一步；P4 在需要自有数据面能力（限流 / 灰度 / 自定义域名）时启用。

## 10. 开放问题

1. preview 是否收紧为恒内网（不跟随服务选用的公网域名），待产品决策。
2. 公网 TLS 证书形态：ACME（cert-manager 挂 listener）vs 云上证书服务——取决于生产入口在阿里云 CDN/SLB 还是自建，落地 P2 前定。
3. 域名移除是否需要冷却期（先摘路由、保留 host 解析一段时间），还是即时生效；影响的是误操作恢复窗口，待产品决策。
4. dataplane 模式下 router 的多副本、内外网入口的 host 表过滤细节，P4 时随实现细化。
