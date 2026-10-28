# Workers 资源池与网络策略设计

状态：**M1 已实现；M2–M4 为后续演进设计**。

本文定义 krun Workers 的多资源池模型。资源池不是单纯的 Kubernetes
调度标签，而是 Worker 的完整安全边界：它同时决定 runtime 成员、入口域名、
出站网络、允许的 binding 类型、配置分发范围和发布确认范围。

当前 M1 已实现配置中的多个 runtime pool、服务隐藏分配、按 pool 的 Kubernetes 成员
发现和 HTTPRoute backend、按 pool 过滤的 snapshot/changes，以及 Artifact、Secret、
Assets 内部接口隔离。组织 API/UI 只暴露 `public` / `private` 网络意图；单一公网部署不
显示选择器。M1 仍使用全局 revision CAS，并为每个 pool 写独立过滤快照；runtime ACK
按 Pod UID 有界覆盖写，用于控制面无法直连 Pod 时观察收敛。

M2–M4 再实现本文后半部分的 pool policy 数据库资源、独立 per-pool revision head、
Release placement、后台无损迁池、CONNECT proxy/NetworkPolicy 生成和 system 管理页面。
这些目标模型不应被误解为当前已经可用的接口。

当前实现中的 `WorkerRelease.pool` 是 M1 的运行授权边界。等 M3 引入独立 placement 后，
才将 Release 本体恢复为 pool-neutral，并迁移该字段的语义。

域名的命名、Gateway 拆分与静态资源路由见
[Workers 域名接入设计](workers-domains.md)；构建、Release、配置 revision 与 runtime
加载流程见 [Workers 构建与发布后端](workers-backend.md)。

## 1. 背景与决策

平台需要支持不同安全等级的 Worker。下面故意使用中性名称，强调 pool 元数据不表达
也不暗示能力：

| 资源池 | 典型用途 | 出站网络 | 可绑定入口域名 |
| --- | --- | --- | --- |
| `pool-amber` | 示例组合 A | 仅内网/集群/VPC直连，禁止公网 | 企业内网域名 |
| `pool-birch` | 示例组合 B | 仅公网域名，强制指定 CONNECT proxy | 公网受管域名 |
| `pool-cedar` | 示例组合 C | 内网直连；公网域名强制 CONNECT proxy | 两类受管域名 |

核心决策：

1. **资源池是服务级属性**。第一阶段一个服务的 production 和所有 preview
   环境使用同一个资源池；环境级覆盖不在本期范围内。
2. **业务侧不能选择或迁移资源池**。服务有且只有一个权威 pool；不可变 Release
   本身不绑定 pool，同一个 Release 可以在迁移窗口内同时具有源、目标 pool placement。更换 pool
   只能由平台系统管理员通过后台重分配操作完成。`pool_migration` 只是控制面内部
   operation kind，不是业务产品概念。
3. **一个 pool 对应一个 Kubernetes Deployment**，并拥有配套 Service、
   ServiceAccount、NetworkPolicy、runtime 身份和 config revision。policy 更新作用于
   整个 Deployment；需要长期并存不同权限时必须创建不同 pool。
4. **入口、出口和 bindings 是 pool 自己的策略维度**。平台只提供 `private`、
   `public` 等可强制执行的原语，不预先定义权限套餐。域名绑定 API、路由投影和
   Gateway reconcile 都校验 pool 的有效 policy revision，不能只靠控制台隐藏选项。
5. **每个 pool 独立分发配置和确认发布**。一个池故障不能阻塞另一个池发布，
   runtime 也不能读取其他池的 host、Release 或 Secret。
6. **pool 元数据不承载能力语义**。平台管理员为每个 pool 直接组合 private/public、
   入口域名和 binding 权限；pool id、label、数量和组合都由管理员决定。代码不得
   解析 id、label、description 或 tag 推断能力，也不保留任何特殊名称。策略模型仍然
   不提供公网直连模式。
7. **HTTPS 不做中间人解密**。CONNECT proxy 只建立隧道和统计流量，不要求
   平台维护业务 CA 或目标站点证书。
8. **入口域名和出站目标是两套规则**。pool policy 的 ingress domains 管浏览器从
   哪些域名访问 Worker；public policy 负责拒绝 IP literal、私网解析结果、集群
   Service 和 metadata 地址。第一阶段不做每服务独立的公网目标域名 allowlist。

## 2. 目标与非目标

### 2.1 目标

1. 服务明确绑定一个权威资源池；Release 与 pool placement 分离，placement 可审计地
   记录承载 pool 和当时的 policy revision/fingerprint。
2. 不同 pool 使用不同 runtime Deployment / Service、配置视图、runtime 身份和
   NetworkPolicy。
3. 有效 private egress mode 为 `direct` 的 pool 对内网服务不做平台级目的地址限制；
   有效 public egress mode 为 `connect-proxy` 的 pool，其所有公网业务出站必须经过
   指定 CONNECT proxy，并使用独立公网出口。
4. 域名只能绑定到允许该 pool 的服务；不兼容组合在写入前返回明确错误。
5. 后台重分配先在目标池完成候选验证和配置预热，再切 Gateway，失败不影响旧池。
6. 发布、Secret 轮换、回滚、预览环境、静态资产与现有不可变 Release 模型兼容。
7. 保持“网络调用不进入数据库事务”和“操作行即持久化工作队列”的现有约束。

### 2.2 非目标

- 不在控制面内实现 HTTP forward proxy 或 TLS MITM。
- 不实现每服务独立 Kubernetes NetworkPolicy；同一 pool 的服务共享网络边界。
- 不实现环境级 pool、请求级 pool 或一次请求跨池调度。
- 不实现跨池灰度流量权重；迁移只在准备完成后一次切换入口 backend。
- 不在本期实现多 region、自动容量调度和按负载自动迁池。
- 不允许页面修改 runtime selector、proxy 地址、NetworkPolicy 或 pool 的网络能力；
  这些基础设施安全边界仍由主 YAML 和部署清单发布，页面只读展示其状态。
- 不允许业务 Worker 获取 proxy、runtime、控制面、对象存储等平台凭据。

## 3. 总体架构

```text
                         控制面
        service.pool ── one Deployment per pool ── active policy revision
              │                    │
              │                    ├── config/pools/<pool>/...
              │                    └── HTTPRoute backend=runtime-<pool>
              ▼
    ┌───────────────────────┐  ┌───────────────────────┐
    │ pool=pool-amber       │  │ pool=pool-birch       │
    │ policy r1             │  │ policy r2             │
    │ private: direct       │  │ private: deny         │
    │ public: deny          │  │ public: proxy         │
    └───────────┬───────────┘  └───────────┬───────────┘
                │                          │
       VPC / cluster only       hostname-preserving CONNECT
                                           │
                                  corp-egress:3128

                 ┌──────────────────────────┐
                 │ pool=pool-cedar          │
                 │ policy r4                │
                 │ private: direct          │
                 │ public: proxy            │
                 └────────────┬─────────────┘
                              ├── VPC / cluster
                              └── CONNECT → corp-egress:3128
```

安全边界有三层：

1. **控制面准入**：读取 pool policy revision，决定服务可以绑定哪些域名和 bindings。
2. **workerd capability**：按 policy 中的 private/public 访问模式生成 Network；
   公网连接是否必须走 proxy 由部署层继续强制。
3. **Kubernetes 网络强制**：按同一份有效策略生成 NetworkPolicy 和透明导流。public
   egress mode 为 `connect-proxy` 的 pool 在透明导流或 proxy 故障时必须 fail closed。

### 3.1 入口请求如何到达正确的 pool

入口不按 pool label 做请求时动态调度，也不在请求热路径查询控制面数据库。控制面把
`domain -> Gateway`、`Host -> service/environment`、`service -> pool` 三段关系提前编译
成 Gateway API 资源和 pool 内配置：

```text
DNS / LB
  -> domain 绑定的 Gateway 实例
  -> HTTPRoute 精确 Host 匹配
  -> backendRef: runtime-<service.pool>:8081
  -> 该 Service 的 selector: role=krun-runtime,pool=<service.pool>
  -> 该 pool 唯一 Deployment 的 Ready Pods
  -> runtime 用 Host 在本 pool config revision 中选择 environment / Release
```

因此入口有两次不同职责的决策：

1. Gateway 只根据精确 Host 找到服务并选择该服务所属 pool 的 Kubernetes Service；
2. pool 内 runtime 再根据相同 Host 找到具体 environment 和活动 Release。

pool/domain labels 和 selector 只在创建、域名变更、policy activate、后台迁池和 reconcile
时做准入校验。校验不通过就不生成 HTTPRoute，也不生成 pool 内 route 投影；它们不能
成为请求时的软路由条件。

### 3.2 HTTPRoute 的归属与 backend

维持现有「每个 `(service, Gateway instance)` 一组 HTTPRoute」的模型：一个服务的所有
启用环境 Host 按 domain 所属 Gateway 分组。不同 Gateway 上的路由可以有不同 hostname，
但它们的 backend 都由同一个 `service.pool` 解析：

```yaml
spec:
  parentRefs:
    - name: workers-public
      namespace: kgateway-system
      sectionName: public
  hostnames:
    - w-example-production.workers.example.com
  rules:
    - backendRefs:
        - name: runtime-pool-7f34a1
          port: 8081
```

`runtime-pool-7f34a1` 由稳定 pool ID 派生，不含名称、能力、label 或 policy revision，
避免重命名和 policy rollout 改变入口地址。HTTPRoute 必须带受 controller 管理的
service/pool 标识；reconcile 每次都从数据库中的 `service.pool` 重新计算 backend，不能
信任对象上次残留的 annotation。

Gateway 实例负责入口网络面的隔离，pool Service 负责把流量交给正确 Deployment；两者
是正交关系。同一个 pool 可以同时接收 private/public Gateway 的流量，但前提是对应域名
通过该 pool active policy 的双向 label/selector 校验。

### 3.3 哪些变更会修改入口路由

- **普通发布、回滚、Secret 发布**：service.pool 不变，HTTPRoute backend 不变；只推进
  对应 pool 的 config revision，runtime 在 Pod 内切换 Release。
- **启停环境或修改域名**：更新 HTTPRoute hostnames 和 pool 内 route 投影，backend
  仍指向当前 pool。
- **pool policy rollout**：仍是同一个 Deployment 和 Service，HTTPRoute 完全不变。
- **平台后台迁池**：先把 Release 和 Host 投影预热到目标 pool，并确认目标 Ready Pod
  都已应用目标 revision，再把
  该服务全部 HTTPRoute 的 backendRef 改到目标 pool Service；确认
  `Accepted=True`、`ResolvedRefs=True` 和主动健康探测后，才 CAS 提交 service.pool，
  最后清理源 pool 投影。

Gateway API 不能让多个 HTTPRoute 对象做跨对象原子提交。服务同时挂多个 Gateway 时，
后台迁池会有一个很短的逐对象切换窗口。该窗口内源、目标 pool 都必须已具备相同 Release
和 Host 配置，且 preflight 必须确认全部域名同时被两边允许；失败时恢复已修改对象的旧
backend。若将来要求跨 Gateway 严格单点原子切流，应启用单一 workers-router，并通过
一份 CAS 路由快照切换，而不是假设 Kubernetes API 能提供跨 HTTPRoute 事务。

### 3.4 开源产品只暴露网络意图

组织用户永远不选择 pool，也看不到 pool ID、Deployment、Gateway、proxy 或 policy
revision。开源 API/UI 只暴露两个跨部署可理解的 primitive：

```text
public   用户通过部署方配置的公网入口访问
private  用户通过部署方配置的内网入口访问
```

`WorkerDomain.access` 必须是 `public | private`；Gateway 绑定只保留在平台配置和 system
API，不返回组织域名目录。服务当前的用户网络意图由所选域名的 access 集合表达，控制面
再用该集合、bindings 与 domain selectors 选择兼容 pool。普通用户不能指定或推断被选中
的 pool。

UI 必须按部署能力退化，而不是写死三个套餐：

- 只有一种 access 时不显示“网络访问”选择器，直接使用部署默认域名；典型开源公网
  部署因此只看到访问地址，不会被迫理解内网或 pool。
- 同时配置 public/private 时才显示两个可组合选项；启用某项会绑定该 access 的默认
  domain，关闭会解绑该 access 的全部域名。
- 同一种 access 有多个域名时可以继续显示高级“访问域名”管理，但不能显示 Gateway
  或 pool 名称。
- label 是部署方备注；不能用“办公网”“特权池”等内部文案作为协议值或安全判断。

M1 已让域名目录和静态资产前缀使用显式 access，完成自适应 UI，并让控制面按 access
组合映射到配置中的 runtime pool。后续 scheduler/policy/placement 继续复用同一组织
API，不再修改用户概念。

## 4. 配置模型

M1 在主 `Config` 中声明 runtime pool，这是部署者管理的基础设施映射，不是组织产品
资源。M2 引入 policy 后，pool 元数据与策略才迁到 system scope API/页面并持久化到
数据库；组织侧始终只看到 public/private。

基础设施仍只能从 `LoadConfig` 进入，例如：

```yaml
workers:
  runtime_pools:
    - id: pool-amber
      access: [private]
      runtime_service: runtime-private
      runtime_token: ${KRUN_PRIVATE_RUNTIME_TOKEN}
      management_token: ${KRUN_PRIVATE_MANAGEMENT_TOKEN}
      default: true
    - id: pool-birch
      access: [public]
      runtime_service: runtime-public
      runtime_token: ${KRUN_PUBLIC_RUNTIME_TOKEN}
      management_token: ${KRUN_PUBLIC_MANAGEMENT_TOKEN}

  domains:
    - id: corp-zone
      domain: workers.internal.example.com
      label: 企业内网
      access: private
      gateway: internal
    - id: edge-zone
      domain: workers.example.com
      label: 互联网
      access: public
      gateway: external
```

每个 M1 pool 的 `access` 是规范化集合，允许 `[public]`、`[private]` 或
`[public, private]`。同一组合只能有一个 pool，避免业务意图解析歧义；必须且只能有一个
default，且默认域名的 access 集合必须与它相同。每个 pool 使用不同 runtime token 与
management token。runtime Deployment 设置 `RUNTIME_POOL=<id>`，其 `CONFIG_BASE` 指向
`.../config/pools/<id>`。

未配置 `runtime_pools` 时自动归一为兼容的 `default` 单池，access 为当前域名目录中出现
的全部类型；这保证开源默认公网安装无需理解或配置 pool。

页面不能提交 Service 名、selector、token、Namespace 或 NetworkPolicy YAML，只能引用
配置中存在的 proxy/domain id。controller 为每个 pool 生成一套且仅一套 runtime
Deployment、Service、身份和 NetworkPolicy；policy revision 只更新这套资源的模板。

### 4.1 WorkerPool 与 Policy Revision

```go
type WorkerPool struct {
    ID                   string
    Label                string
    Description          string
    Tags                 []string
    Default              bool
    Status               string // draft | provisioning | ready | updating | error | deleting
    ActivePolicyRevision int
    Revision             int
}

type WorkerPoolPolicy struct {
    PoolID         string
    Revision       int
    PrivateAccess  string   // deny | direct
    PublicAccess   string   // deny | connect-proxy
    EgressProxyID  string   // PublicAccess=connect-proxy 时必填
    Labels          map[string]string // 安全标签，属于 policy revision
    DomainSelector  map[string]string // 精确匹配 domain labels
    ResolvedDomains []string          // activate 时解析的 domain ids 快照
    BindingTypes   []string
    Fingerprint    string
    State          string   // draft | deploying | active | superseded | failed
}
```

创建示例：

```json
{
  "id": "pool-amber",
  "label": "资源池 A",
  "description": "由平台团队维护",
  "tags": ["managed"],
  "default": true,
  "policy": {
    "private": "direct",
    "public": "deny",
    "labels": {"domain-access": "corp"},
    "domain_selector": {"network": "corp", "managed": "true"},
    "binding_types": ["variable", "secret", "assets", "database"]
  }
}
```

另一个 pool 可以使用任意名称并直接组合 `private=deny, public=connect-proxy`，或者同时
启用 `private=direct, public=connect-proxy`。平台不先创建“内网策略”“公网策略”供它
引用；policy 就属于该 pool revision。

约束：

- id、label、description、tags 只用于身份、展示和检索，不参与能力判断；tags 最多
  32 个并规范化去重，避免每个 pool 产生无界行。
- policy labels 是安全输入，与 tags 不同；它们随 policy revision 固化并进入
  fingerprint。页面必须在不同区域展示二者。
- private 只允许 `deny` / `direct`；public 只允许 `deny` / `connect-proxy`，没有
  public direct。
- public 为 `connect-proxy` 时必须引用主配置中的 proxy id；不同 pool 可以引用同一
  proxy，但使用独立身份做审计、配额和限流。
- labels 与 selector 只支持规范化 `key=value` 精确匹配；第一版不支持正则、通配符、
  `NotIn` 或基于名称的隐式规则。selector 必须非空，空 selector 匹配不到任何域名。
- binding types 必须来自二进制支持的固定枚举，规范化排序、去重，未知值拒绝。
- 平台中必须且只能有一个 default pool；它可以采用任意有效策略。default pool 没有
  active policy 或 Deployment 未 Ready 时禁止创建 Worker 服务。
- 新建服务只记录当时的 default pool；业务 API 不能覆盖。
- 每个 pool 固定对应一个 Deployment、Service、ServiceAccount/workload identity、
  NetworkPolicy 和 config head。policy revision 只驱动这一个 Deployment 的更新，
  不能生成第二套长期并存的数据面。
- Deployment/Service 名从 pool id 的稳定摘要生成，名称中不包含 policy revision；
  controller reconcile 以 pool id 为唯一 owner key。
- 尚未首次 activate 的 draft pool 可以暂时没有 Deployment；一旦可接收服务，就必须
  恰好有一个 Deployment。删除 pool 前先确认无服务并删除这套资源。

### 4.2 有效策略与 Fingerprint

`Fingerprint` 由 private/public、proxy id、pool policy labels、domain selector、
activate 时解析的 domain ids 快照和 binding types 计算，不包含 pool id、label、
description、tags、revision、Pod 数或健康状态。准入校验、workerd Network、
NetworkPolicy、配置投影、后台 preflight、风险提示和页面都必须消费同一份有效 policy，
不能各自重新解释字段。

active policy revision 一经生效即不可修改。编辑 pool 策略创建下一条 draft；activate
启动一次 pool-wide Deployment rollout，成功后 CAS 更新 `active_policy_revision`，
旧 revision 标为 superseded。池内所有服务共享同一 Deployment，因此不能按服务停留
在旧 revision；需要长期使用不同策略的服务必须迁到另一个 pool。

只修改 pool 的 label、description、tags 不创建 policy revision。策略修改的 blast
radius 是整个 pool，页面必须列出受影响的全部服务并二次确认。

### 4.3 WorkerDomain

每个 domain 在主配置中有安全 labels 和 `pool_selector`；pool policy revision 有自己
的安全 labels 和 `domain_selector`。兼容条件为：

```text
matchLabels(pool_policy.domain_selector, domain.labels)
AND matchLabels(domain.pool_selector, pool_policy.labels)
```

两边都按 selector 中的全部 key/value 精确匹配。不存在“同名标签自动匹配”，也不把
tags 当 labels。policy activate 时控制面根据当时的 domain catalog 解析出明确 domain
ids，写入 policy-domain 明细并进入 fingerprint；该 revision 生效后整个 pool 使用这份
快照。

后续新增 domain 或修改 domain labels / pool_selector 不会让已有 policy 自动扩权。
后台显示 selector drift，管理员创建新 policy revision、重新 activate 并 rollout 后
才生效。一个 domain 可以被多个 pool policy 使用，但一个具体 host 任何时刻只能
路由到服务固定的 pool；该 pool 的所有 Pod 使用同一个 active policy。静态域跟随
业务域名权限，不单独配置。

label key/value 采用 Kubernetes label 的长度和字符约束，每个 domain 或 policy 最多
32 项；重复 key、空 key 和超限输入拒绝。`krun.io/` 前缀保留给平台，其余前缀由平台
管理员自行约定。

### 4.4 基础设施变更策略

删除或修改 proxy/domain/runtime template 前，部署工具必须查询数据库并 preflight：

- 是否仍有 ready 或被引用的 policy revision 使用该 proxy/domain；
- domain labels 或 pool_selector 变化会让哪些 active policy 出现 selector drift；
- 是否会使现有 Release 无法重建；
- 是否仍有未完成 operation；
- 新模板能否表达所有仍生效的 private/public 组合。

控制面发现数据库策略与主配置冲突时报告 `policy_conflict` 并 fail closed，不得把服务
静默改到 default pool。基础设施变更和 policy rollout 都不能在数据库事务内包含
Kubernetes、Gateway 或 proxy 网络调用。

## 5. 持久化模型

遵循一实体一行、不使用外键和小事务约束。

### 5.1 Pool、Policy、服务、Release 与 Placement

```sql
CREATE TABLE krun_worker_pools (
  pool_id                  text        PRIMARY KEY,
  label                    text        NOT NULL,
  description              text        NOT NULL DEFAULT '',
  is_default               boolean     NOT NULL DEFAULT false,
  status                   text        NOT NULL DEFAULT 'draft',
  active_policy_revision   integer     NOT NULL DEFAULT 0,
  revision                 integer     NOT NULL,
  created_at               timestamptz NOT NULL,
  updated_at               timestamptz NOT NULL
);

CREATE UNIQUE INDEX krun_worker_pools_one_default
  ON krun_worker_pools (is_default) WHERE is_default;

CREATE TABLE krun_worker_pool_tags (
  pool_id text NOT NULL,
  tag     text NOT NULL,
  PRIMARY KEY (pool_id, tag)
);

CREATE TABLE krun_worker_pool_policy_revisions (
  pool_id          text        NOT NULL,
  policy_revision  integer     NOT NULL,
  private_access   text        NOT NULL,
  public_access    text        NOT NULL,
  egress_proxy_id  text        NOT NULL DEFAULT '',
  fingerprint      text        NOT NULL,
  state            text        NOT NULL,
  created_by       text        NOT NULL,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  PRIMARY KEY (pool_id, policy_revision)
);

CREATE TABLE krun_worker_pool_policy_domains (
  pool_id          text    NOT NULL,
  policy_revision  integer NOT NULL,
  domain_id        text    NOT NULL,
  PRIMARY KEY (pool_id, policy_revision, domain_id)
);

CREATE TABLE krun_worker_pool_policy_labels (
  pool_id          text    NOT NULL,
  policy_revision  integer NOT NULL,
  label_key        text    NOT NULL,
  label_value      text    NOT NULL,
  PRIMARY KEY (pool_id, policy_revision, label_key)
);

CREATE TABLE krun_worker_pool_policy_domain_selector (
  pool_id          text    NOT NULL,
  policy_revision  integer NOT NULL,
  label_key        text    NOT NULL,
  label_value      text    NOT NULL,
  PRIMARY KEY (pool_id, policy_revision, label_key)
);

CREATE TABLE krun_worker_pool_policy_bindings (
  pool_id          text    NOT NULL,
  policy_revision  integer NOT NULL,
  binding_type     text    NOT NULL,
  PRIMARY KEY (pool_id, policy_revision, binding_type)
);

ALTER TABLE krun_worker_services
  ADD COLUMN pool text NOT NULL DEFAULT 'default',
  ADD COLUMN pool_revision integer NOT NULL DEFAULT 0;

CREATE TABLE krun_worker_release_placements (
  pool_id             text        NOT NULL,
  release_id          text        NOT NULL,
  state               text        NOT NULL,
  policy_revision     integer     NOT NULL,
  policy_fingerprint  text        NOT NULL,
  operation_id        text        NOT NULL DEFAULT '',
  created_by          text        NOT NULL,
  created_at          timestamptz NOT NULL,
  expires_at          timestamptz,
  PRIMARY KEY (pool_id, release_id)
);

ALTER TABLE krun_worker_operations
  ADD COLUMN source_pool text NOT NULL DEFAULT '',
  ADD COLUMN source_pool_policy_revision integer NOT NULL DEFAULT 0,
  ADD COLUMN source_pool_policy_fingerprint text NOT NULL DEFAULT '',
  ADD COLUMN target_pool text NOT NULL DEFAULT '',
  ADD COLUMN target_pool_policy_revision integer NOT NULL DEFAULT 0,
  ADD COLUMN target_pool_policy_fingerprint text NOT NULL DEFAULT '';

CREATE TABLE krun_worker_pool_migration_items (
  operation_id     text NOT NULL,
  env_name         text NOT NULL,
  release_id       text NOT NULL,
  PRIMARY KEY (operation_id, env_name)
);
```

所有表都不使用外键。policy labels、selector、resolved domains、bindings 和 tags
分别一项一行，不把集合塞进 JSON 文档。Release 保存 Artifact、变量、Secret version、
binding 声明和资产配置，是与 pool 无关的不可变业务版本；placement 才表示某个 pool
当前获准加载该 Release。`policy_revision/fingerprint` 是创建 placement 时的审计和
preflight 依据，运行期能力始终使用 pool 当前 active policy。

现有 `krun_worker_releases.pool` 在迁移阶段只解释为 `created_in_pool` 来源审计，不能用于
内部 API 授权，也不能参与 Release ID；完成 placement 回填后将其显式重命名或删除，
不能让同一字段同时具有“来源”和“当前可运行位置”两种语义。

placement state 为 `warming | active | draining`。普通发布在 service.pool 创建 active
placement；迁池先在目标创建带 operation_id 和 expires_at 的 warming placement，切流
提交后转 active，源 placement 转 draining 并在源投影清理、Pod revision 收敛后删除。失败或
超时的 warming placement 最多保留 24 小时；后台 cleanup 使用有界批次删除，避免临时
placement 无界增长。不能删除仍被当前 route 或进行中 operation 引用的 placement。

policy revision 有界：7 天未更新的 abandoned draft 自动删除；superseded 且 90 天
没有 operation 引用的 revision 连同 labels、selector、resolved
domains、bindings 明细一起删除。删除前用对账查询再次验证无引用，
删除使用不超过 5 条显式语句的小事务，不使用外键或网络调用。审计记录按平台统一
审计保留期保存 fingerprint、actor 和 request id，不依赖永久保留 policy 行。

不在 environment 行重复存 pool：environment 始终继承 service.pool；正常态每个当前
Release 必须具有 service.pool 的 active placement。`pool_revision` 只作为服务分配
CAS，不代表 policy revision。

pool migration 是服务级操作，会为每个已有活动 Release 的环境生成一条 migration
item。表中一行只记录该环境正在复用的同一 release_id，不把多环境映射塞进 operation
的 JSON 文档，也不使用外键。没有活动 Release 的环境无需 item，但迁移提交后同样继承
新的 service.pool。目标 policy revision 和 fingerprint 固定在 operation 与目标
placement；服务随后跟随该 pool 的整体策略更新。

### 5.2 配置分发

配置 revision 按 pool 分区：

```text
worker_pool_config_heads       一 pool 一行，当前 config revision 与 active policy revision
worker_config_revisions        一 (pool, config_revision) 一行
worker_config_ops              一 op 一行，增加 pool
worker_config_routes           一 (pool, host) 一行
worker_config_releases         一 (pool, Release) 一行，仅含该 pool 的有效 placement
```

policy rollout 期间同一 Deployment 可能短暂同时存在新旧 ReplicaSet，但它们仍属于同一
pool 和同一份业务 route/release 视图。controller 通过 Pod `/ready` 返回的 observed
policy/config revision 确认 Deployment rollout 收敛。不使用全局 config revision 的理由不变：一个 pool 的发布不应
等待其他 pool。

`worker_config_routes` 以 `(pool, host)` 为键，`worker_config_releases` 以
`(pool, release_id)` 为键。pool 重分配的 warm/switch 阶段，同一 host 和 release_id
会短暂同时存在于源/目标 pool 配置，但 Gateway backend 任何时刻只指向一个 pool。

revision 分配仍使用短 CAS：

```sql
UPDATE worker_pool_config_heads
   SET revision = revision + 1
 WHERE pool = $1 AND revision = $2;
```

影响行数为 0 表示并发写先提交，调用方重新计算该 pool 的增量。网络调用不进入事务。

### 5.3 分区后的资源开销

pool 分区的主要收益不是减少 Release/Artifact 的全局总数，而是避免把全平台配置复制到
每个 runtime Pod：

```text
全局快照模式的单 Pod 配置内存 ≈ 全平台 routes + releases
pool 快照模式的单 Pod 配置内存 ≈ 本 pool routes + releases + 少量迁移中 placements

全局配置变更 fan-out ≈ 一次变更 × 全部 runtime Pods
pool 配置变更 fan-out ≈ 一次变更 × 所属 pool Pods
迁池窗口 fan-out       ≈ 一次变更 × 源 pool Pods + 目标 pool Pods
```

Artifact 继续按 digest 在对象存储保存一份；同一 release_id 在多个 pool 的 placement
只重复很小的描述符和 route，不复制代码包。Pod 只把本 pool 的描述符放进内存，实际
workerd module、Secret 和资产仍按首次请求懒加载并使用有界缓存。因此服务总量远大于
单 pool 服务量时，单 Pod 内存、snapshot 大小、增量 fan-out 和无关收敛等待都会显著
下降。

控制面也必须按 pool 查询和生成增量，相关索引都以 pool 为首列，不能先构造全局快照再
过滤。收益不会消除 pool 自身的固定成本：每个 pool 都有一个 Deployment/Service、至少
一组 Pod 和长轮询；pool 数量过多时，Pod 基线资源和连接数会成为主要开销。因此
pool 应代表安全/资源边界，而不是为每个服务创建一个 pool。

## 6. 发布、Secret、回滚与后台重分配

### 6.1 普通发布

普通发布不接受客户端提供的 `pool` 或 `pool_policy_revision`：

1. 读取 service.pool 和该 pool 当前 active policy。
2. 校验服务当前 domains 和 bindings 仍被 active policy 允许。
3. 创建 pool-neutral 的不可变 Release，并在 service.pool 创建 active placement，记录
   当时观察到的 policy revision/fingerprint。
4. 只在该 pool Deployment 的候选 Pod 验证。
5. 只推进该 pool 的 config revision。
6. 只查询该 pool 当前 Ready Pod 的 `/ready`，确认它们已应用目标 revision。

这避免调用方把一个允许内部 DB binding 的 Release 发布到公网 pool，或通过请求体
绕过管理员的 pool 分配。

### 6.2 Secret 与普通配置

Secret PUT / DELETE 继续复用当前 Artifact 并自动创建 pool-neutral Release，再在
service.pool 创建 placement。普通变量在下一次发布时采用同样流程。

Secret 内部查询必须校验：

```text
exists placement(
  pool_id = authenticated_runtime_pool,
  release_id = requested_release,
  state in (warming, active, draining)
)
```

不能再把 `pool == default` 写死，也不能只信任可伪造的 `X-Runtime-Pool`。

### 6.3 回滚

普通 rollback 可以选择该服务任意不可变历史 Release，但必须先用当前 service.pool 的
active policy 重新校验其 domains、bindings 和 Secret 引用。兼容时为该 Release 创建或
恢复当前 pool placement，再推进 route；不兼容时返回：

```json
{
  "code": "WORKER_RELEASE_POOL_INCOMPATIBLE",
  "message": "The selected release is not compatible with the current runtime pool policy"
}
```

rollback 不能创建其他 pool placement，也不能改变 service.pool；跨池恢复仍必须由系统
管理员执行后台重分配。这样“回滚代码”不会意外变成“改变网络安全域”。

### 6.4 后台 Pool 重分配

后台重分配的内部 operation kind 仍命名为 `pool_migration`，但它不注册在组织或业务
服务 API 下，也不出现在业务侧 Workers 页面。第一阶段只有平台系统管理员可以调用：

接口草案：

```http
POST /api/v1/system/workers/services/{service}/pool-reassignments
Content-Type: application/json

{
  "target_pool": "pool-birch",
  "target_policy_revision": 3,
  "expected_pool_revision": 2,
  "idempotency_key": "pool-migration-001"
}
```

`target_policy_revision` 必须是目标 pool 当前 active revision；省略时由服务端读取并
写入 operation，不能在后台执行期间随 active 指针漂移。后台重分配覆盖服务的全部环境：

- 每个存在当前 Release 的环境都复用同一个 release_id，在目标 pool 创建 warming
  placement；不复制 Artifact、变量、Secret versions 或业务 Release。
- enabled、disabled 和 preview 环境都创建目标 placement，避免迁移后重新启用环境时
  缺少目标池授权。
- 尚未发布的环境没有 Release，只随 service.pool 一起迁移。
- 第一阶段不允许重分配请求为单个环境替换代码。需要恢复旧池历史版本时，先显式迁回
  该 pool，再通过普通 rollback 选择历史 Release；两个安全动作不能合并隐藏。

迁移操作持久化在现有 operation 表，kind 为 `pool_migration`，步骤为：

| 步骤 | 行为 | 失败语义 |
| --- | --- | --- |
| `policy` | 检查 target pool revision、domains、bindings、系统管理员权限 | 不改变数据面 |
| `placement` | 为各环境的同一 Release 创建目标 warming placement 并核验 Artifact | 不改变数据面 |
| `candidate` | 在目标池单成员逐一冷加载验证 | 旧池继续服务 |
| `warm` | 向目标池发布临时 route + release 投影并查询所有目标 Ready Pod revision | 旧池继续服务 |
| `switch` | HTTPRoute backend 切到 `runtime-<target>` 并等 Accepted/ResolvedRefs | 失败保持或恢复旧 backend |
| `commit` | CAS 更新 service.pool；目标 placement 转 active，源 placement 转 draining | 恢复任务可继续提交 |
| `prune` | 从源池配置移除 host/release，确认源 Ready Pod 已应用删除 revision | 已切流，不回滚提交 |

目标池预热 route 在 Gateway 切换前不可从该 Gateway 到达，但 runtime 已能按 Host 找到
Release。切换后即使控制面短暂不可用，目标池也能继续服务。

这里的“预热”是配置预热，不是给 Deployment 下发命令，也不是因为迁池而 rollout：

1. controller 向目标 pool 的配置流提交 `release_put` 和 `route_upsert`；
2. 目标 Deployment 的每个 Ready Pod 通过 snapshot/changes 长轮询把新 revision 原子应用
   到内存视图；controller 查询各 Pod `/ready` 的
   `{pod_uid, pool, config_revision, policy_revision}` 确认收敛；
3. controller 仅选择一个目标 Pod 调用管理面 `/validate`，使用短时 Artifact URL 冷加载
   并执行 `KrunValidation`。这是候选校验，不是 Deployment 指令；
4. 其他 Pod 不提前下载代码。入口切换后，首次命中某 Pod 的请求才按 Release 描述符懒加载
   Artifact、Secret 和资产 manifest，随后使用该 Pod 的有界缓存。

因此不能把任意流量直接打到任意 pool：没有目标 Host 的 route 时返回 404；有 route 但
没有对应 Release 描述符时返回 503；目标 pool 没有该 Release 的有效 placement 时，
Artifact、Secret 和 assets 内部 API 必须拒绝。只有目标 pool 已收到配置、全部目标 Ready Pod
都报告已应用目标 revision，
且候选校验成功后，Gateway 才能切流。

这种模型避免一个 pool 中服务数量增加时，Deployment 启动和 policy rollout 需要下载全部
业务代码。代价是切流后某些 Pod 的第一条请求仍可能有冷启动延迟；如以后需要严格消除
迁池冷启动，应新增“逐 Pod prewarm + 有界并发/缓存预算”步骤，不能把所有 Release 无界
预装进 Pod。本期不做全成员 prewarm。

warm 阶段的目标池投影来自未完成的 `pool_migration` operation 及其 migration items，
而不是提前修改 service.pool。这样源池的权威路由保持不变，目标池只得到本次迁移
所需的临时副本。prune 完成后删除 migration items。

`switch` 与 `commit` 之间发生崩溃时，operation 的持久化步骤负责恢复；不把 Gateway
API 调用放入数据库事务。重复执行每一步都必须幂等。

commit 使用 `pool_revision` CAS，并再次确认目标 policy 仍 active、domains revision、
preview revision、各环境当前 Release 和 binding revision 与 policy 阶段一致。任一
对象发生变化都让迁移失败并恢复旧 Gateway backend，不能提交一半环境。

迁移期间禁止：

- 修改服务域名；
- 修改 binding；
- 新建普通发布、Secret 发布或回滚；
- 发起第二个 pool migration。

目标或源 pool 正在执行 policy rollout 时不得开始服务迁移；反过来，pool 存在进行中的
迁入/迁出 operation 时不得 activate 新 policy。使用 operation 状态的条件写和唯一
active-operation 约束仲裁，不用 advisory lock，也不把网络调用放进事务。

### 6.5 Policy rollout

激活 policy revision 就是一次 pool-wide Deployment rollout：

```http
POST /api/v1/system/workers/pools/{pool}/policy-revisions/{revision}/activate
```

operation kind 为 `pool_policy_rollout`，影响该 pool 内全部服务，但始终只更新同一个
Kubernetes Deployment。Kubernetes 可能在滚动更新期间短暂保留新旧 ReplicaSet；这不
等于两个 pool Deployment，也不能让服务选择 ReplicaSet。

rollout 期间 pool 标记为 `updating`：现有流量继续，但禁止向该 pool 创建/迁入服务，
并暂缓普通发布、Secret 发布和回滚，避免 candidate/配置收敛检查与 Deployment 更新交叉。只读
操作不受影响，失败回滚或成功 commit 后解除。

安全顺序采用 deny-biased：

1. `preflight`：列出全部受影响服务、失效域名/binding、能力增减和 proxy 状态；存在
   不兼容服务时拒绝开始。
2. `restrict`：先应用本次收紧的 NetworkPolicy，允许短暂失败但不能继续使用已删除能力。
3. `deploy`：更新同一个 Deployment 的 Pod template、workerd Network、proxy sidecar
   和 policy revision label，等待 rollout complete。
4. `expand`：旧 Pod 全部退出后，才应用本次新增的 NetworkPolicy 能力。
5. `verify`：查询所有 Ready Pod `/ready`，确认 observed policy revision 等于目标 revision，
   并执行候选网络探测。
6. `commit`：CAS 更新 pool.active_policy_revision，旧 revision 标为 superseded。
7. `cleanup`：清理临时策略与失败候选资源。

中途失败时 operation 把 Deployment template 回滚到旧 revision；先恢复旧 Pod，再恢复
旧 NetworkPolicy，保证不会因回滚顺序产生额外权限。由于策略属于整个 pool，不能只让
部分服务停留旧 revision。如果不能接受全池影响，应创建新 pool（新 Deployment）并
通过 6.4 分批迁移服务。

## 7. 域名准入与路由

域名绑定条件：

```text
domain exists
AND domain.id IN active_policy(service.pool).resolved_domain_ids
AND caller has domain binding permission
AND domain approval (if any) is approved
```

写 API 返回明确错误：

```json
{
  "code": "WORKER_DOMAIN_POOL_NOT_ALLOWED",
  "message": "Domain edge-zone is not available to runtime pool pool-amber"
}
```

这项校验执行三次：

1. 控制台只展示允许的域名，提供正常用户体验；
2. 域名 PUT API 做权威准入校验；
3. config projection 和 HTTPRoute reconcile 再做防御性校验，非法组合不进入数据面。

HTTPRoute backend 不再使用全局 `RuntimeService`，而是由 service.pool 解析：

```text
(gateway, service.pool) → 该 pool 唯一的 runtime Service
```

同一服务的多个域名可以挂不同 Gateway，但必须全部被 pool 当前 policy 允许。Gateway
只决定入口网络面，pool policy 决定实际 runtime 与出口安全域，两者不能互相替代。

## 8. Runtime 配置与身份隔离

### 8.1 内部 API

```text
GET /api/internal/workers/config/snapshot?pool=<pool>
GET /api/internal/workers/config/changes?pool=<pool>&since=<revision>
GET /api/internal/workers/releases/<release>/assets
GET /api/internal/workers/releases/<release>/secrets
GET /api/internal/workers/artifacts/<artifact>
```

认证结果必须直接绑定 pool。query/header 只能与身份绑定值相等，不能用来选择任意
pool。迁移期可以使用每 pool 独立 token；最终使用 Kubernetes ServiceAccount、mTLS
或 workload identity。

Artifact 和不可变 Release 都可跨 pool 复用；内部 API 不是根据 Release 的来源 pool，
而是根据“已认证 runtime pool 是否存在该 Release 的有效 placement”授权。Secret、
配置同样必须按 pool 校验。

### 8.2 对象存储快照

```text
config/pools/<pool>/latest.json
config/pools/<pool>/snapshots/<config-revision>.json.gz
```

每个快照只包含该 pool 的 routes 和 releases，并标明当前 policy revision。对象存储
策略应尽量让 runtime 只能读本 pool 前缀；如果当前 S3 能力无法做 workload 级前缀授权，控制面认证与内容过滤仍
是必须边界，存储前缀隔离作为后续加固。

Pod 启动时用不可变快照 bootstrap，之后只维持一个 pool 级长轮询：

```text
GET /config/changes?pool=<authenticated-pool>&since=<revision>&wait=25s
```

有新 revision 时立即返回 `release_put/route_upsert/route_delete` 增量；无变化时到期返回
204，Pod 带同一个 since 重新请求；since 已落后于保留窗口时返回 409，Pod 重新读取全量
快照。长轮询只同步小配置描述符，不传业务请求、Artifact 字节或 Secret 值。

长轮询等待期间不能持有数据库连接、事务或 goroutine 内的逐请求 ticker 查库。每个控制面
副本按 pool 维护一个共享 watcher，配置提交后通过本地通知和 PostgreSQL `LISTEN/NOTIFY`
唤醒该副本，再从 `worker_pool_config_heads`/ops 读取一次并在内存 fan-out 给本副本上的
等待连接；通知只作唤醒，数据库 head 仍是事实来源。25 秒超时后做一次兜底 head 检查，
避免丢通知。这样数据库读取接近“副本数 × 活跃 pool × 变更/超时”，而不是“Pod 数 ×
每秒轮询”。断线使用带 jitter 的指数退避，避免控制面恢复时惊群。

### 8.3 runtime 成员与配置收敛

成员发现使用 pool 固定 selector；policy revision 只是 Pod template label：

```text
Deployment / Service selector: role=krun-runtime,pool=<pool>
Pod template label:         pool-policy-revision=<n>
```

正确性需要“观察到 Pod 已应用目标 revision”，但不需要 Pod 持续向 PostgreSQL 写 ACK。
controller 已经能通过 Kubernetes API 发现成员并访问管理端口，因此在需要安全闸门时
主动查询每个 Ready Pod：

```text
GET http://<pod-management-address>/ready

{
  "pod_uid": "...",
  "pool": "pool-amber",
  "config_revision": 42,
  "policy_revision": 7
}
```

查询结果必须满足：workload identity/管理凭据属于该 pool，响应 pod_uid 与 Kubernetes
对象一致，config revision 不低于 operation 目标且不高于 pool head；policy rollout
期间 observed policy revision 必须等于目标 revision。`/ready` 只证明内存配置视图已
应用，不证明 Artifact 已下载。

需要按 Pod 检查收敛的安全闸门只有：

- 迁池 `warm`：目标 pool 全部目标 Ready Pod 已看到临时 route/release 后才能切入口；
- 源池 `prune`：源 Ready Pod 已删除 route 后才能删除 draining placement；
- policy rollout `verify`：新 Pod 同时观察到目标 policy 与当前 config；
- 普通发布/回滚：可用同一检查标记 `converged/available` 和排障，但不声称所有 Pod
  同时原子切换，也不表示代码已经预热。

operation 在每轮检查时重新读取 Deployment 的 Ready Pod UID 集合：新加入且会接流量的
成员必须达到目标 revision，已被 Kubernetes 删除的成员不再等待。检查结果不持久化为
每 Pod 心跳行；operation 只持久化目标 revision、步骤和错误摘要。控制面重启后从
operation 恢复并重新查询实时成员即可，避免 `worker_runtime_acks`、freshness 窗口和
高频 UPSERT。若 `/ready` 暂时不可达，视为尚未收敛并按有界退避重试，不能猜测成功。

## 9. 出站网络

### 9.1 workerd 边界

workerd 的 `globalOutbound` 可以指向 network/service 或设为 `null`，但其 Network
配置没有标准 HTTP forward-proxy / CONNECT upstream 字段。实现必须根据 pool policy
生成配置，不能按 pool id 写分支。组合规则为：

- private egress 为 `direct` 时 network 允许 private，业务 Worker 可直接访问集群
  Service、VPC/内网地址；为 `deny` 时不提供这项能力；
- public egress 为 `connect-proxy` 时 network 允许 public，但所有公网连接继续被透明
  导向 policy 引用的 CONNECT proxy；为 `deny` 时不提供公网能力；
- 两项同时启用时 private 目标直连，public 目标进入代理链，不能互相旁路；
- 不能依赖给 workerd 设置 `HTTP_PROXY` / `HTTPS_PROXY`；
- 不把 CONNECT proxy、透明连接器或 proxy 凭据作为业务 Worker binding。

workerd 配置参考：
<https://github.com/cloudflare/workerd/blob/main/src/workerd/server/workerd.capnp>。

### 9.2 Private-direct only 组合

组合 `private=direct, public=deny` 时，对私网目的地址不做平台级限制，但没有公网能力：

```text
Worker
  ├── Kubernetes Service / Pod 地址
  ├── VPC / 办公网 / 内部域名
  └── 公网地址 → 拒绝
```

该 runtime 不安装强制 CONNECT 的透明规则，也不要求经过公网 proxy。
Kubernetes 基础安全策略仍可保护 runtime management 端口、控制面凭据和其他命名
空间；对集群/VPC/内网目标不再做 krun 级域名或地址 allowlist。平台凭据仍不作为
binding 暴露；能在网络上到达某个内部服务，不等于自动获得该服务的认证权限。

### 9.3 Public-connect only 组合

组合 `private=deny, public=connect-proxy` 时，业务网络路径固定为：

```text
Worker 请求 https://api.example.com
  → workerd public-address 检查
  → Pod 内透明连接器
  → CONNECT api.example.com:443
  → 指定标准 HTTP proxy
  → proxy 再解析并确认目标是公网地址
  → 独立公网 NAT / 出口 IP
```

“只能访问公网域名”是硬约束：

1. 请求 hostname 必须是 DNS 名称，拒绝 IPv4/IPv6 literal。
2. workerd 首次解析的所有地址必须是 public；任一 private、loopback、link-local、
   multicast、documentation、metadata 或集群地址都拒绝。
3. 透明连接器必须把原 hostname 保留为 `CONNECT hostname:port`，不能退化成
   `CONNECT ip:port`。无法保留 hostname 的组件不满足本设计。
4. proxy 必须自己重新解析 hostname，并再次拒绝 private/reserved 结果，不能信任
   runtime 提交的解析 IP；建立隧道时固定本次校验后的地址，防止 DNS rebinding。
5. redirect 后的新 URL 重新经过同样校验，不能从公网域名跳转到私网目标。
6. 该 runtime 只允许使用平台指定 DNS，不能访问任意内部 DNS 服务。

该 pool 的 NetworkPolicy 只允许：

- 平台指定 DNS；
- 控制面 internal listener；
- Artifact / config 对象存储；
- policy 引用的 CONNECT proxy；

并拒绝：

- 直接公网出口；
- Kubernetes Service / Pod 网段和 VPC 私网；
- 其他 pool runtime、proxy 管理端点或内部身份端点；
- metadata 地址及未显式授权的集群服务。

透明连接器或 proxy 故障时，请求必须失败；不得绕过代理回退到直连。不能依赖业务
Worker 自报 `X-Pool`、目标 hostname 或其他 header 作为隔离边界。

### 9.4 Private-direct + Public-connect 组合

同时启用两项能力是前两种网络路径的并集，但不是“无限制网络”：

```text
Worker
  ├── private destination → 直接访问 Kubernetes / VPC / 内网
  └── public hostname     → hostname-preserving CONNECT → 指定 proxy
```

runtime 使用目的地址分类路由：private/reserved 地址走内网直连，public 地址进入
policy 引用 proxy 对应的透明连接器和校验链。它必须满足：

- 公网目标同样拒绝 IP literal，要求 CONNECT hostname 并由 proxy 二次解析；
- 公网流量不得直连或使用 private 的普通网络路径绕过 proxy；
- proxy 故障只影响公网访问，私网直连继续可用；
- 入站只能绑定该 policy revision 直接允许的域名；
- 第一阶段所有 pool 重分配都只能由系统管理员从平台后台执行；高风险组合的正式
  审批对象留待后续设计。

### 9.5 HTTPS 与可观测边界

HTTPS TLS 在 workerd 与目标站点之间端到端建立，proxy 不持有平台 CA、不解密请求，
因此平台不需要维护业务证书。

CONNECT proxy 可以可靠管理：CONNECT hostname、端口、连接数、持续时间、字节数、
代理账号、限速和固定出口 IP；不能看到 HTTPS path、header、body 或响应状态码。
这与本期“只允许公网域名并统一出口”的目标一致。

如果未来需要基于 URL 的租户级策略，应单独设计应用层 Outbound Worker / egress
gateway，不能在本设计中悄悄开启 TLS MITM。

## 10. Binding 策略

pool 是 binding 的第二道准入边界。创建或修改 binding 时要求：

```text
binding.type IN pool.allowed_bindings
```

第一阶段类型枚举：

| 类型 | 含义 |
| --- | --- |
| `variable` | 普通环境变量 |
| `secret` | 版本化 Secret |
| `assets` | 当前 Release 静态资产 |
| `service` | Worker / 平台服务 binding |
| `database` | 受控数据库 capability |
| `object` | 对象存储 capability |

实际尚未实现的 binding 类型可以出现在设计枚举中，但配置加载只能接受当前二进制
声明支持的类型，避免管理员以为配置即代表能力已经可用。

没有任何 binding 类型被隐式允许；pool policy 可以选择任意受支持子集，甚至为空。
迁移或 rollout 到不允许现有 variable/Secret/assets 的策略时，preflight 必须列出
阻塞项。未来 binding 的底层凭据仍不能进入 Release JSON、对象存储快照或业务日志。

## 11. 平台后台与权限

### 11.1 权限边界

资源池不是组织侧可配置资源，不新增 `service.pool.migrate`，也不允许通过组织角色、
用户组或组织 API Key 获得修改能力。业务侧发布、回滚、Secret 和域名 API 都不接受
`pool` 或 `target_pool` 字段。

平台后台新增 system scope action：

```text
system.workerPool.read
system.workerPool.create
system.workerPool.update
system.workerPool.delete
system.workerPool.policy.create
system.workerPool.policy.update
system.workerPool.policy.delete
system.workerPool.policy.activate
system.workerPool.preflight
system.workerPool.reassign
system.workerPool.operation.read
```

action 拆开是为了以后可以组合只读运维、变更准备和正式执行职责，不使用一个宽泛的
`manage`。第一阶段这些 action 全部只属于内置 `system_admin`，不能创建组织范围的
授权。重分配是高风险操作，第一阶段只接受带 CSRF 保护的系统管理员交互会话，不接受
API Key；`reassign` 权限在提交 operation 和后台执行 `policy` 步骤时各检查一次，
沿用现有撤权语义。

### 11.2 平台 API

API 只注册在 `/api/v1/system/`：

```text
GET  /api/v1/system/workers/pools
POST /api/v1/system/workers/pools
GET  /api/v1/system/workers/pools/{pool}
PATCH /api/v1/system/workers/pools/{pool}
DELETE /api/v1/system/workers/pools/{pool}
POST /api/v1/system/workers/pools/{pool}/policy-revisions
PATCH /api/v1/system/workers/pools/{pool}/policy-revisions/{revision}
DELETE /api/v1/system/workers/pools/{pool}/policy-revisions/{revision}
POST /api/v1/system/workers/pools/{pool}/policy-revisions/{revision}/activate
PUT  /api/v1/system/workers/pools/{pool}/default
GET  /api/v1/system/workers/pool-assignments?organization_id=&pool=&cursor=
GET  /api/v1/system/workers/services/{service}/pool
POST /api/v1/system/workers/services/{service}/pool-reassignments/preflight
POST /api/v1/system/workers/services/{service}/pool-reassignments
GET  /api/v1/system/workers/pool-reassignments/{operation}
```

pool catalog、分配列表和服务当前 pool 的读取要求 `system.workerPool.read`；pool 与
policy 的 create/update/delete、activate 分别使用对应 action。只有 draft
可以 PATCH；删除 pool、active/superseded revision 前必须确认无引用。operation 详情要求
`system.workerPool.operation.read`；服务重分配 preflight
和正式提交分别要求 `system.workerPool.preflight` 与 `system.workerPool.reassign`。
不能因为能创建 draft 或做 preflight 就获得 activate、rollout 或 reassign 权限。

切换 default 复用 `system.workerPool.update`，只允许指向 active 且数据面 Ready 的
policy revision。readiness 在事务前检查；数据库内只用带 revision CAS 的两条更新
清除旧 default、设置新 default，不包含网络调用。

列表使用游标分页。pool 响应把 `metadata`（id、label、description、tags）和
`effective_policy`（网络、policy labels、domain selector、resolved domains、binding
能力）分成两个对象，避免调用方误把名称或 tags 当成安全 labels。响应还可返回
runtime Ready 数和代理健康状态，但不返回 runtime selector、proxy
地址、token 或内部 Service 名。preflight 不写数据库，返回域名、binding、runtime
容量和当前 operation 等阻塞项；真正提交时必须重新检查，不能信任页面缓存的结果。

组织与业务服务 API 不提供重分配路由。业务 Workers 页面可以只读展示“平台管理的
网络能力”摘要，帮助解释为什么某类请求被拒绝，但不展示目标池列表、迁移历史或
修改按钮。

### 11.3 平台管理页面

系统空间增加“Worker 资源池”页面，例如：

```text
/?workspace=system&page=worker-pools
```

页面只对 `system_admin` 可见，包含：

1. Pool 创建/编辑器：维护中性的名称、描述和 tags；policy builder 直接组合 private
   (`deny/direct`)、public (`deny/connect-proxy`)、proxy id、pool labels、domain label
   selector 和 binding types，不选择平台预设的权限套餐。
2. Policy revisions：draft diff、校验结果、fingerprint、activate/Deployment rollout
   进度和历史 revision。metadata 修改与 policy 修改使用不同表单和 API。
3. 所有 pool 卡片把 metadata 与 active policy 分区显示，同时展示 runtime Ready 数、
   config revision、Pod 配置收敛状态和 proxy 健康状态；不能用名称颜色或图标替代实际
   能力说明。
4. Domain catalog：只读显示主配置中的 domain、labels、pool_selector，以及当前匹配和
   selector drift；tags 与 labels 使用不同控件和说明。
5. 服务分配表：组织、服务、当前 pool 及其 active policy、绑定域名、binding 摘要、最近一次后台操作；
   支持按组织、pool 和异常状态筛选。
6. “调整资源池”对话框：选择目标 pool active revision 后先执行 preflight，逐项显示会失效的域名、
   不兼容 binding、目标 runtime 状态和预期影响，再要求二次确认。
7. 任何有效组合同时满足 private mode 为 `direct`、public mode 为 `connect-proxy`
   时都使用独立的高风险提示，不能按名称判断；本期仍由系统管理员直接确认，页面
   预留审批状态位置但不伪造尚未实现的审批能力。
8. operation 详情：展示 policy、candidate、warm、switch、commit、prune 进度，失败
   原因、request id、操作者和审计入口；刷新页面后从服务端恢复，不靠前端内存。

页面可以定义 pool policy，但不得编辑 runtime template、proxy 实际地址、selector、
NetworkPolicy YAML、token 或 ServiceAccount。这些基础设施后端来自主 YAML/部署清单；
policy 只能引用它们的安全 id。controller 负责把 policy 编译成数据面，不能让浏览器
直接提交 Kubernetes 对象。

### 11.4 后续审批

正式审批暂不实现。未来如果“私网直连 + 代理公网”等高风险组合需要申请、审批、
有效期或自动回收，审批按有效 capability 判断而不是按 pool 名称判断。审批完成后
仍调用同一个后台重分配 operation，并在 `policy` 步骤验证批准记录；不能让审批直接
改数据库中的 `service.pool`。是否允许业务用户发起申请另行决定，不影响本期“只有
平台后台能修改”的边界。

## 12. 故障语义

| 故障 | 预期行为 |
| --- | --- |
| 目标 pool 无 Ready 成员 | 发布/迁移保持等待或失败，旧池不受影响 |
| 目标 pool candidate 失败 | 迁移 failed，不修改 Gateway 和 service.pool |
| policy activation candidate 失败 | draft 保留失败信息，active policy 指针不变 |
| policy rollout 失败 | 同一 Deployment 回滚旧 Pod template/NetworkPolicy，active policy 不变 |
| CONNECT proxy 不可用 | 所有 policy revision 引用该 proxy 的公网访问失败；其 private 能力不受影响 |
| 控制面不可用 | 已加载 Release 继续服务；各 pool 使用自己的最后快照 |
| 对象存储不可用 | 已缓存 Release 继续；新冷启动失败，不跨池回退 |
| 某 pool Pod revision 停滞 | 只影响该 pool 的发布、迁移或 policy rollout |
| Gateway 切换后控制面崩溃 | 恢复 migration operation，完成 commit/prune |
| 删除仍被引用的 pool | preflight 拒绝；不得自动迁到 active default pool |
| runtime 伪造 pool header | 认证 pool 不匹配，返回 403 |

## 13. 迁移与实施阶段

### M0：文档与兼容基线

- 明确 pool 元数据和 policy 都由 system scope 资源管理，主配置不创建任何权限预设；
  业务侧不提供 pool 选择器。
- 升级后先保留 legacy `default` 的只读数据面，但禁止创建新 Worker 服务；系统管理员
  在后台创建第一个 pool、定义与现状匹配的 policy revision 并设为 default。
- 为当前活动的 legacy Release 回填该 pool 的 active placement，再切换 service.pool；
  不复制业务 Release。历史 Release 保持不可变，只作历史记录，按需在 rollback 时创建
  当前 pool placement。
- 所有活动环境完成切换且没有未完成 operation 后删除 legacy 兼容分支。
- 更新现有文档中“单 pool”的边界描述。
- 为所有写死 `default` 的位置增加测试清单。

### M1：Pool 资源与准入

- 主配置增加 runtime template 和 egress proxy 目录，不增加 pool 权限套餐。
- pool、tags、policy revisions、policy labels、domain selectors、resolved domains、
  policy bindings 的 normalized tables 与 system scope CRUD API。
- 主配置 domain labels / pool_selector 的加载校验和 selector drift 检测。
- abandoned draft、superseded policy 和 operation 的 retention/cleanup。
- active policy 不可变、唯一 default、proxy/domain 引用和 policy fingerprint 校验。
- `krun_worker_services.pool`；service 不保存 policy revision。
- Release placement 的 policy 审计字段，以及 operation 的目标 policy revision/fingerprint。
- `krun_worker_services.pool_revision` 与 migration items。
- 服务创建时读取数据库中的 default pool；其 Deployment 或 active policy 不 Ready 时
  明确拒绝。
- 域名写 API 增加 pool 校验。
- 普通发布拒绝请求体指定 pool，为 pool-neutral Release 创建当前 pool placement。

此阶段先固定模型和 API；只有 active policy 与唯一 Deployment 都 Ready 的 pool 能
设为 default 或接收服务。

### M2：配置与身份隔离

- 配置 head/revision/op 按 pool 分区。
- snapshot/changes 只返回认证 pool 的 route/release placement；Secret、Artifact、assets
  API 以 `(authenticated pool, release_id)` placement 授权。
- 对象存储快照使用 `config/pools/<pool>/`。
- 每个 pool 使用独立身份，随后切 workload identity。
- 删除 runtime 周期 ACK 写入、`worker_runtime_acks` 和对应 retention；operation 需要
  收敛证据时实时查询目标 Deployment Ready Pods 的 `/ready` revision。

### M3：多 runtime 与路由

- controller 为每个 pool 生成且只生成一个 Deployment、Service、selector、身份和
  NetworkPolicy。
- 成员发现、candidate 和 `/ready` revision 检查按 pool 参数化；policy rollout 通过
  Pod label 与实时管理面响应观察同一 Deployment 是否收敛。
- HTTPRoute backend 始终指向该 pool 的唯一 runtime Service。

### M4：出站网络

- `private=direct, public=deny` 的组合使用 private-only network，不安装代理导流。
- `private=deny, public=connect-proxy` 的组合使用 public-only network、可保留
  hostname 的透明连接器、标准 CONNECT proxy 和拒绝直连的 NetworkPolicy。
- 两项同时启用的组合按目的地址分类：private 直连，public 进入 policy 引用 proxy 的
  透明连接器和 CONNECT proxy。
- 验证 IP literal、私网解析、DNS rebinding、redirect 到私网和直接公网绕过均失败。
- 验证 proxy 故障时所有引用它的公网能力 fail closed，组合中的私网能力仍正常，
  不引用它的 pool 不受影响，并确认 HTTPS 不解密。

### M5：Policy rollout、后台重分配与平台控制台

- `pool_migration` operation 与恢复逻辑。
- pool-wide `pool_policy_rollout`、失败回滚与 operation retention。
- 仅 system scope 注册 pool/policy CRUD、activate、rollout、preflight、重分配与
  operation API。
- 系统管理页面提供 pool policy builder、revision diff、状态、服务分配、重分配确认
  和进度展示。
- 业务 Workers 页面没有 pool 修改入口；最多显示只读网络能力摘要。
- 历史跨池 Release 的显式恢复入口。

## 14. 验收清单

### 14.1 单元与 PostgreSQL 集成测试

- pool id 重复、policy revision 非连续、未知 proxy/domain/binding 和 public direct
  均拒绝；空 domain/binding 列表允许。
- active policy revision 不可修改；编辑生成 draft，activate 验证后使用 revision CAS
  更新 active policy 指针。
- 平台中最多一个 default；其 active policy 或唯一 Deployment 未 Ready 时服务创建失败。
- 修改 label、description 或 tags 不改变 fingerprint、准入结果和网络策略；使用完全
  中性的 pool id 也能得到相同能力。
- policy labels 与 domain labels 双向精确匹配；任一 selector 不满足都不能绑定。
- tags 永远不参与 selector；修改 domain labels / pool_selector 不自动改变 active policy
  的 resolved domains，只产生 drift，新 policy revision activate 后才得到新快照。
- 服务只持久化权威 pool；Release 不绑定 pool，placement 准确记录 pool 与创建时
  policy revision/fingerprint。
- 请求体伪造 pool 不生效。
- domain × pool 不兼容时 API 返回 `WORKER_DOMAIN_POOL_NOT_ALLOWED`。
- Secret 查询和 snapshot 使用错误 pool 身份返回 403/404；伪造 `/ready` 中的 pool、
  pod_uid 或 observed policy revision 不能通过 Kubernetes 成员与身份交叉校验。
- per-pool config revision CAS 并发推进互不阻塞。
- 普通 rollback 只允许在当前 service.pool 创建 placement，不兼容当前 policy 时拒绝。
- migration 重试和控制面重启后可恢复。
- 删除仍被 policy 引用的 proxy/domain 能被 preflight 发现。
- policy activation 只更新该 pool 的同一个 Deployment；失败时整体回滚，active
  revision 不变，不产生第二个长期 runtime Deployment。
- rollout 期间 pool status 为 updating，迁入、服务创建、发布、Secret 和回滚均返回
  409；成功或完成回滚后恢复 ready。
- cleanup 不删除仍被 operation 引用的 policy；过期 draft、superseded
  revision 和 operation 会被实际清理。
- 组织角色、用户组和组织 API Key 即使拥有 `service.update` 也不能修改 pool；组织
  路由下不存在重分配 API。
- 非 system_admin 调用 system pool API 返回 403，API Key 调用高风险重分配返回 403。
- system_admin 的 preflight 不写状态，提交后 operation 再次校验权限与兼容性。

### 14.2 kind E2E

system_admin 通过真实平台 API 创建三个不表达能力的 pool：`e2e-alpha`、
`e2e-bravo`、`e2e-charlie`，再分别提交并激活 private-only、proxy-only 和 combined
三种 policy revision：

1. 三个 pool 各自且仅有一个 Kubernetes Deployment/Service；系统管理员把三个服务
   后台分配到不同 pool，伪造 `pool` 字段不能改变分配，流量命中对应 Deployment。
2. 每个 runtime snapshot 只包含本 pool 有效 placement 对应的 host / Release；迁移窗口内
   同一 release_id 可以同时出现在源、目标 pool 快照。
3. `e2e-alpha` Worker 可直连 Kubernetes Service 和 VPC 私网 HTTP/TCP 服务，公网域名
   与公网 IP 均失败。
4. `e2e-bravo` Worker 的 HTTPS 以 hostname 形式经过 CONNECT proxy 成功。
5. `e2e-bravo` Worker 访问 Kubernetes Service、私网 IP、内部 DNS 名、metadata 地址及
   public IP literal 均失败。
6. 公网域名解析到 private/reserved 地址、解析结果切换为私网、或 30x 跳转到
   私网目标时均失败。
7. `e2e-charlie` Worker 可直连同一个私网服务，同时公网 HTTPS 经 proxy 成功；
   公网 IP literal 和公网直连旁路失败。
8. 停止 proxy 后 proxy-only 请求失败，combined 的私网仍正常但公网失败，
   private-only 的私网直连保持正常。
9. proxy 日志同时包含 proxy-only/combined 的 CONNECT hostname，并能按代理
   身份区分 pool、观测连接和流量。
10. Pool policy 的 domain_selector 与 domain 的 pool_selector 双向匹配后才进入
    resolved domains；任一方向不匹配时 API、投影和 HTTPRoute 都拒绝。
11. 伪造 Host、pool header、runtime token 都不能跨池读取或调用。
12. 同一个 Artifact 和 release_id 可以在三个 fixture pool 建 placement，但 Secret 只由
    具有有效 placement 的认证 pool 获取；删除 placement 后立即拒绝新获取。
13. pool migration 在目标 candidate / warm 失败时旧服务持续可用。
14. migration 成功后 Gateway、service.pool、目标 active placement 和 Pod observed revision
    全部指向目标池；
    源 draining placement 在配置收敛后删除，Release ID 不变。
15. migration 在 switch 前后分别重启控制面，最终状态仍收敛。
16. 删除 preview、域名解绑、服务删除会清理各池的临时/历史路由。
17. 平台管理页面完成三个 fixture pool 之间的重分配和回迁；
    preflight、二次确认、operation 进度、失败恢复与审计记录均来自真实 API。
18. 组织侧 Workers 页面没有 pool 选择或迁移入口，直接访问 system 页面或 API 也不能
    越权。
19. 把三个 fixture 的 id 和 label 全部替换后重复关键用例，行为只随 policy 变化，
    证明实现没有按名称写分支。
20. 为 `e2e-alpha` 创建下一 policy revision 并 rollout；所有池内服务共享同一个
    Deployment 更新，成功后统一使用新能力，失败则整体回滚且 active revision 不变。
21. 新增一个标签可匹配的 domain 后，旧 policy 不自动获得它；页面先显示 drift，创建
    并 rollout 新 revision 后才可绑定。

### 14.3 生产前验证

- public mode 为 `connect-proxy` 的组合使用实际 CNI 验证 NetworkPolicy、目的地址
  分类和透明导流，而非只检查 YAML；private mode 为 `direct` 的组合验证集群/VPC
  服务确实可直连。
- 确认 private Gateway、runtime management 端口和 proxy 管理端口无公网旁路。
- 确认 proxy/NAT 的容量、连接上限、超时与高可用。
- 持续流量下执行发布、Secret 轮换、回滚和 pool migration。
- 记录 pool 维度的请求数、失败率、出站连接、字节数和代理可用性。

## 15. 安全不变量

1. 服务只有一个权威 pool；普通发布、Secret、回滚不能隐式改变它。
2. Release 是 pool-neutral 的不可变业务版本；placement 固化 pool 及创建时观察到的
   policy revision/fingerprint，Release 和 placement 都不被原地改写安全语义。
3. runtime 身份绑定 pool；header/query 不能提升到其他 pool。
4. runtime 只接收本 pool 有有效 placement 的 route、Release 和 Secret；请求参数中的
   pool 或 release_id 不能自行创建授权。
5. domain 不在 pool 当前 active policy 中时，host 不进快照、不建路由。
6. pool 只能获得自己 policy 声明的能力；任何名称和组合都不能获得公网直连旁路。
7. 一个 pool 的故障、config revision 收敛或 policy rollout 不阻塞其他 pool。
8. 每个 pool 始终只有一个 Kubernetes Deployment；active policy 不可原地修改，
   权限扩大或收紧必须创建 revision 并执行全池 rollout。
9. 组织角色、业务 API 和普通发布永远不能修改 service.pool；只有 system scope 的
   平台后台操作可以改变它。
10. 已生效策略的安全语义由 revision + fingerprint 固定，不能热改数据库静默扩权；
    需要并存不同策略时必须创建另一个 pool/Deployment。
11. pool 的 id、label、description 和 tags 不表达能力，也不能参与任何安全决定。
12. pool/domain labels 是安全输入，必须经过双向 selector、revision、fingerprint 和
    rollout；tags 永远不能替代 labels。
13. 跨池迁移和 policy rollout 的所有外部调用均幂等、可恢复，不在数据库事务内执行。
14. 任何平台 token、proxy 凭据、数据库凭据和对象存储凭据都不进入业务 Worker。

## 16. 开放问题

以下问题不阻塞 M1–M3，但需在对应阶段前决定：

1. `connect-proxy` mode 的透明连接器选用 sidecar、CNI 扩展还是节点级 daemon；
   候选组件必须能保留 CONNECT hostname，并在双网组合中可靠区分 private/public。
2. pool migration 的 Gateway 切换确认是否需要额外业务 Host 健康请求，还是只依赖
   Accepted / ResolvedRefs + target runtime `/ready` revision 收敛。
3. 高风险 capability 组合的正式申请、审批、有效期和自动回收模型；本期只做系统
   管理员后台操作，不向业务侧开放申请或迁移。
4. 多 region 到来后 pool 是全局安全等级，还是另加独立 region 调度维度；
   在此前不得把 region 编码进 pool id 后再依赖字符串解析。
5. pool 的数量上限、runtime 最小副本和空闲回收策略；没有明确上限与回收前不允许
   把 pool 变成业务自助创建资源。
6. 同一 Deployment 做 policy rollout 时的 `maxSurge` / `maxUnavailable`、超时和容量
   预留；无论取值如何，都不能为了可用性绕过 deny-biased 网络策略顺序。
