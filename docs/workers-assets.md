# Workers 静态资产设计

状态：设计稿（review 中）。实现位置：`internal/control/workers_*.go`、`workers/builder`、`workers/runtime`。域名模型、routing 模式、多池见 [workers-domains.md](workers-domains.md)，本文不重复。语义对照 Cloudflare Workers Static Assets（`run_worker_first` / `not_found_handling` / `html_handling` / ASSETS binding 均来源于此）。

## 1. 目标与非目标

目标：

1. 静态资产与 Worker 代码随**同一个 Deployment 原子发布**：一次 adopt 同时切换代码与资产，回滚自然连资产一起回滚。
2. 主域 `{slug}[-{env}].{suffix}` 同源交付：页面与 API 同 host，零 CORS。静态文件走独立静态域名，流量不经 runtime。
3. 路径分流收敛在 Pod 内入口 Worker；网关（HTTPRoute / workers-router）只按 host 转发，不配 path 规则。
4. 未配置资产的服务行为不变。

非目标（本期不做）：

- `run_worker_first: true` 布尔全量模式（只做路由数组）。
- `html_handling` 的 `force-trailing-slash` / `drop-trailing-slash`（只做 `auto-trailing-slash` 与 `none`）。
- adopt 期 presigned 直拉 S3（5.2 方式 B）——性能优化，规模上来再开。
- Range 请求、预压缩、按文件名 hash 自动 immutable（开放问题）。
- 大媒体（视频等）：运行期数据，不是部署资产，见第 10 节。

## 2. 分流位置：入口 Worker，不是网关

### 2.1 为什么

1. **发布原子性**。Cloudflare 的核心是「assets + 代码是一个部署单元」；krun 的对应物是 Deployment + runtime adopt 一步切换。若在 HTTPRoute 把 `/api/*` 指向 runtime、其余指向资产 backend，一次发布就拆成「网关规则 + 双 backend」两件事，两者之间没有事务，回滚窗口必然出现新旧混合。
2. **表达能力**。Gateway API 写不出 try-files（「资产未命中 → 返回 index.html + 200」），也写不出 `/about` → `about.html` 这种 307——能不能这样映射取决于本次制品里实际有哪些文件，静态路由配置预知不了。
3. **一套代码两种模式**。gateway 和 dataplane 两种 routing 模式的唯一公共 serving 层就是入口 Worker；路径逻辑放网关要写两遍。
4. **信任边界**。runtime 不持任何 S3 凭证（现状），对象存储不进数据面。

静态域名（第 6 节）是唯一例外：它整域都是静态文件，没有内容相关的路由，所以可以在网关 / CDN 层直接指向对象存储。

### 2.2 请求期决策流

```text
请求（Host = 主域，网关整域转发到入口 Worker）
→ 1. run_worker_first 顺序匹配，首个命中决定：
     正向命中（/api/*）       → 业务 Worker
     反向命中（!/api/docs/*） → 走静态（进入第 2 步）
→ 2. 资产查找：归一化 path 对文件表精确匹配（含 html_handling 规范化与 307 表）
     命中 → 返回文件（ETag = 文件 sha256，If-None-Match → 304）
→ 3. 未命中：
     导航请求（Sec-Fetch-Mode: navigate）且配置了资产
       → not_found_handling：single-page-application → index.html + 200；none → 404
     否则 → 业务 Worker（未配置资产的服务即现状行为）
```

与 Cloudflare 相同的已知行为：浏览器**导航**到 Worker 路径拿到的是 HTML。所以所有业务路径（含 OAuth 回调这类浏览器直接打开的）要写进 `run_worker_first`。

## 3. 配置面

构建请求（`POST /builds`，仓库构建同字段）增加 `assets` 块：

```json
{
  "environment": "production",
  "entrypoint": "src/index.ts",
  "files": {"src/index.ts": "..."},
  "assets": {
    "directory": "public",
    "run_worker_first": ["/api/*"],
    "not_found_handling": "single-page-application",
    "html_handling": "auto-trailing-slash"
  }
}
```

- `directory` 是源码树内的相对目录，builder 原样复制，不执行 npm install、不跑自定义构建命令（对应 Cloudflare「预构建 dist」模型）。
- 配置固化进制品 manifest（第 4 节），不可变；Deployment 引用制品即间接持有，Deployment ID 已含归档 digest。
- pattern 校验在提交时做（语法错返回 400）：仅前缀匹配、`*` 只能是尾段、可选 `!` 前缀、≤16 条 × ≤128 字符。`directory` 缺失时其余字段必须为空。

## 4. 制品

归档在 schema 1 之上新增 schema 2：

```text
bundle.tar.gz
├── manifest.json          # + assets 段：相对路径 → sha256、大小、content-type；
│                          #   固化第 3 节的路由配置
├── modules/index.js
└── assets/
    ├── index.html
    └── app-a1b2c3.js
```

- 校验复用现有清单逻辑：拒绝目录越界、符号链接、重复条目、非预期文件、超限大小、hash 不符；content-type 按扩展名白名单。
- 存储键不变（`v1/{org}/sha256/{archive_sha256}/`）：资产变了就是新 hash、新 Artifact，CAS 去重自动成立。

## 5. Runtime：本地缓存与 ASSETS binding

### 5.1 存储关系

- S3 制品归档是唯一事实源；runtime 落盘的是 adopt 期一次性填充的缓存：填充时逐文件核验 sha256，之后请求期零网络 IO；pod 重启后重新填充、重新核验。
- adopt 期取归档两条路：A（本期）控制面代理，与模块代码现状加载方式一致；B（后续）控制面发短时效 presigned GET，runtime 直拉 S3，省控制面带宽。区别只在谁去下载，都保留「填充后全量核验、请求期读本地」。

### 5.2 磁盘：有上界的缓存

- 常驻只含当前 serving 的 deployment（每 deployment ≤ 约 22 MiB 解压后）；被替换的版本 5 分钟 TTL 后清除，回滚重新下载一次（秒级）。
- readiness / adopt 验证本来就要全量下载归档，顺手落盘即完成预热。
- 每 pod 硬上限 `assets_cache_max`（默认 1 GiB）+ LRU；活动文件不淘汰；emptyDir `sizeLimit` 同值兜底。
- 容量 ≈ Σ(该 pool 活动 deployment) × 22 MiB；超了就调大上限或拆 pool（多池后磁盘随 pool 分片）。

### 5.3 快照与 binding

- 快照的 deployment 描述符只增加几十字节的路由配置摘要；文件表（manifest，几 KB～几十 KB）常驻内存不落快照。
- 新增 `ASSETS` service binding（与 `PLATFORM` 同机制），业务 Worker 可 `env.ASSETS.fetch()` 取资产，语义同 Cloudflare。

## 6. 静态域名：静态文件的对外服务

### 6.1 决策

1. **静态文件走独立静态域名** `static-{slug}.{suffix}`，主域不挂 CDN。
2. **全平台共用一个公开只读源站**（一个 bucket 或统一前缀 `{org}/{service}/{env}/{archive_sha}/`），finalize 时写入。不按服务拆源站。
3. **内网不走 CDN**：直接暴露代理后的 OSS 地址（经一层 kgateway 代理，或内网 DNS 直解 OSS 内网 endpoint）。**公网**静态域 CNAME 到 CDN，CDN 源站指同一个 bucket。

```text
主域 {slug}.{suffix} ──── DNS 直指 LB ──► 网关 ──► runtime
（API、index.html、SPA 回落——不经 CDN）

公网静态域 static-{slug}.{suffix} ── CNAME ──► CDN ──── 共享公开 bucket
内网静态域 static-{slug}.{internal} ──► 内网 LB ──► kgateway ┘
```

### 6.2 为什么主域不挂 CDN（同域方案弃用）

1. **API 为透传付费**。一个域名没法按路径分 DNS，API 请求全过 CDN；透传按流量计费，全站加速（DCDN 类）动态单价更高，API 大响应 / 流式场景成本失控。
2. **多一层故障面**：CDN 故障即全站故障；静态域独立后 CDN 挂了只影响静态文件，主域照常服务。
3. **缓存配置复杂**：同域要维护「哪些路径可缓存」的规则表，配错会把动态响应缓存进边缘；静态域独立后整个域都可缓存，无路径规则。

### 6.3 网关 / CDN 侧要做的事

- **一条共享路由，不随业务增长**：网关上静态域只需要一条 HTTPRoute（host 匹配 `static-*` 通配），`URLRewrite` 改写 Host 到 bucket endpoint、Path 到 `/{org}/{service}/{env}/{archive_sha}/` 前缀；`Backend`（`type: Static`）+ `BackendTLSPolicy` 做 TLS（kgateway 能力已核实）。500 服务也是这一条。kind 用 MinIO endpoint 当源站，同一条路由。
- **CORS 接入层统一加**：静态域只读、无 Cookie，`Access-Control-Allow-Origin: *` 一条响应头全覆盖（module 脚本 / 字体 / GET fetch 都不产生预检）。加在一层（CDN 响应头配置或网关 ResponseHeaderModifier），用 `*` 不带 `Vary: Origin`，缓存键不分片。
- **缓存策略**：指纹路径（URL 含 archive_sha）长 TTL（30d 级）；未指纹化文件短 TTL + 回源 ETag 协商。
- **通配符证书**一张覆盖全部静态域。

### 6.4 平台侧配套

1. `staticHostFor(slug, suffix)` = `static-{slug}.{suffix}`，一层子域，随 Release 进快照，控制台展示。
2. 业务构建时把资源 URL 指到静态域（Vite / webpack 的 `base`，一次性配置）。平台不注入、不改写 HTML；`index.html` 与 SPA 回落留在主域由 runtime 服务（量小）。
3. **没有「刷新 CDN」动作**：URL 按内容寻址，新版 HTML 引用新 URL，旧对象永久有效；未指纹化文件的短 TTL 就是生效延迟上界。回滚同理。
4. 主域不变量不动：零 CORS、内部头清洗、快照寻址、发布原子性。

## 7. 请求期行为细则

- `html_handling: auto-trailing-slash`（默认）：`/about` → 200 服务 `about.html`；`/about.html` → 307 `/about`；`/folder/index.html` → 307 `/folder/`。307 表 adopt 时按实际文件集合预计算。`none`：仅精确路径。
- 缓存：强 ETag（文件 sha256）+ `If-None-Match` → 304；`Cache-Control: public, max-age=300`。
- 出站响应补 `x-krun-deployment` / `x-krun-pod` 便于排障；入站继续剥除 `x-krun-*`（domains 第 7 节）。
- 控制台 invoke 路径不变：经入口 Worker，配置了资产的服务 invoke `/` 返回 index.html。

## 8. 安全不变量

1. 资产查找是归一化路径对内存文件表的精确匹配，无文件系统路径拼接，`..` 穿越 by construction 不可能。
2. 未知 host 404、禁用 410、未发布 503 沿用现有语义。
3. 静态域只读无凭证，`ACAO: *` 不构成暴露面；主域零跨域不变。
4. bucket 同步在 finalize（Release 生效前）完成，且路径按 archive_sha 组织——新 URL 指向新内容，不存在指向未同步对象的窗口。

## 9. 第一版边界

资产 ≤ 500 文件 / 合计 20 MiB（解压后）/ 单文件 5 MiB；含资产时归档上限 8 → 32 MiB；`run_worker_first` 仅数组形式；`html_handling` 仅 `auto-trailing-slash` / `none`；`assets_cache_max` 默认 1 GiB。其余沿用 workers-backend.md。

## 10. 大媒体（视频等）：不属部署资产

视频、音频、数据集是**运行期数据**：不随 Release 切换、体积无上界、需要 Range——与部署资产性质相反。Cloudflare 同此边界（静态资产单文件上限 25 MiB，大媒体走 R2 / Stream）。krun 的路径：

- **上传**：业务 Worker 经 service binding 拿 presigned 上传 URL，转交前端直传 bucket——字节不经过 Worker（workerd 请求体 MB 级上限，代理视频必然失败）。bucket 与制品桶分离，按服务隔离前缀。
- **访问**：同源路径 `/data/{key}` 由入口 Worker 转发对象存储并透传 `Range` / `If-Range`；私有内容用短时效签名 URL。流量大时同静态域机制接 CDN。
- 边界一句话：走构建、随发布切换、20 MiB 级 → 制品资产；运行期上传、GB 级、要 Range → 对象存储 + bindings（随 Service / 存储 bindings 规划，不在 A1–A3 内）。

## 11. 现状与迁移

现状：无任何资产概念（workers-backend.md 第一版边界明确「尚无静态资源制品」）；入口 Worker 仅按 host 整域转发（`workers/runtime/entry.js`）；归档仅 manifest + modules。

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| A1 | builder 复制资产目录 + manifest schema 2 + finalize 校验 | 单元：归档结构、越界 / 符号链接拒绝 |
| A2 | runtime：落盘核验、ASSETS binding、决策流、307 表、ETag | kind e2e：资产 200/304、SPA 回落、run_worker_first 命中与 `!` 反向、307、导航到 Worker 路径返回 HTML、未配置资产服务回归 |
| A3 | 控制台展示资产配置与大小；workers-backend.md 补路由说明 | 控制台验收 |
| A4 | 静态域名 + 共享 bucket 同步 + 网关共享路由（kind 用 MinIO） | kind e2e：静态域 200、CORS 头、指纹 TTL |

## 12. 开放问题

1. 文件名含 hash 的资源自动 `immutable`（A2 后按需）。
2. 预览环境是否默认带 SPA 配置模板。
3. dataplane router 的边缘缓存与缓存键（P4 之后）。
4. CDN 供应商与通配接入形态：生产公网入口落地时定。
