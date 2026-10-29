# Workers 静态资产设计

状态：设计稿（review 中）。实现位置：`internal/control/workers_*.go`、`workers/builder`、`workers/runtime`。域名模型、routing 模式、多池见 [workers-domains.md](workers-domains.md)，本文不重复。语义对照 Cloudflare Workers Static Assets（`run_worker_first` / `not_found_handling` / `html_handling` / ASSETS binding 均来源于此）。

## 1. 目标与非目标

目标：

1. 资产与 Worker 代码打进**同一个制品归档**、同属一个版本记录（Release）：提交后通过一次 config revision 切换同时生效新代码与新资产；回滚即把 route 指回历史 Release，代码与资产一并回到旧版本。不存在「代码已新、资产还旧」的中间状态。
2. 主域 `{slug}[-{env}].{suffix}` 同源交付：页面与 API 同 host，零 CORS。静态文件走独立静态域名，流量不经 runtime。
3. 路径分流收敛在 Pod 内入口 Worker；网关（HTTPRoute / workers-router）只按 host 转发，不配 path 规则。
4. 未配置资产的服务行为不变。

非目标（本期不做）：

- `run_worker_first: true` 布尔全量模式（只做路由数组）。
- `html_handling` 的 `force-trailing-slash` / `drop-trailing-slash`（只做 `auto-trailing-slash` 与 `none`）。
- 配置生效期 presigned 直拉 S3（5.2 方式 B）——性能优化，规模上来再开。
- Range 请求、预压缩、按文件名 hash 自动 immutable（开放问题）。
- 大媒体（视频等）：运行期数据，不是部署资产，见第 10 节。

## 2. 分流位置：入口 Worker，不是网关

### 2.1 为什么

1. **发布的单步切换**。Cloudflare 的核心是「assets + 代码是一个部署单元」；krun 的对应物是「一个归档 + 一次 config revision 切换」。若在 HTTPRoute 把 `/api/*` 指向 runtime、其余指向资产 backend，一次发布就变成「网关规则更新」和「Release 生效」两步，两步之间没有事务，期间会出现「路由已切、Release 未生效」（或相反）的混合窗口。
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

资产配置（`POST /builds` 的 `assets` 块）与源码一起提交：

```json
{
  "environment": "production",
  "entrypoint": "src/index.ts",
  "source_upload": "uploads/{org}/src/src_xxx/source.tar.gz",
  "assets": {
    "directory": "public",
    "binding": "ASSETS",
    "run_worker_first": ["/api/*"],
    "not_found_handling": "single-page-application",
    "html_handling": "auto-trailing-slash"
  }
}
```

- **源码包是大项目与资产的入口**：`POST /source-uploads` 返回 presigned PUT，客户端上传 `source.tar.gz`（direct 模式顶层两段：`src/` 代码、`assets/` 资产），build 请求引用 `source_upload`。普通 API 请求体仍限制为 1 MiB；只有 build-create 为容纳 2 MiB 源码及 JSON escaping 使用独立的 16 MiB 包络。builder 输入使用短时 Secret URL，20 MiB 资产必须走对象存储。
- 仓库构建由服务端自动完成：fetch 仓库 → 安全保留完整 Workers 项目 → Wrangler/custom build → 从构建后的 `assets.directory` 收集资产。Vite/adapter 在构建阶段生成的 dist 资产无需提前存在。
- 轻量模式：内联 `files` 仍可用于 ≤512 文件 / 2 MiB 的纯代码 Worker；带 `assets` 时必须用 `source_upload`。
- `directory` 是项目树内的相对目录。仓库模式按 npm/pnpm/Yarn/Bun 锁文件安装完整构建依赖，并在隔离 Job 中运行 Wrangler/custom build；direct 上传模式仍保持较窄的构建边界。
- 配置固化进制品 manifest（第 4 节），不可变；Release 引用制品即间接持有，Release 的版本 ID 已含归档 digest。
- pattern 校验在提交时做（语法错返回 400）：仅前缀匹配、`*` 只能是尾段、可选 `!` 前缀、≤16 条 × ≤128 字符。`directory` 缺失时其余字段必须为空。

## 4. 制品

归档在 schema 1 之上新增 schema 2（`schema_version: 2`）：

```text
bundle.tar.gz
├── manifest.json          # assets 段：配置 + files[{path,sha256,size_bytes,content_type}]
├── modules/index.js
└── assets/
    ├── index.html
    └── assets/app-a1b2c3.js
```

- 校验：拒绝目录越界、符号链接、重复条目、未声明条目；资产 ≤500 文件 / 合计 20 MiB / 单文件 5 MiB；Content-Type 按扩展名白名单，扩展名与声明必须一致；`single-page-application` 必须有 `index.html`。schema 1（无资产）继续接受。
- 存储键不变（`v1/{org}/sha256/{archive_sha256}/`）：资产变了就是新 hash、新 Artifact，CAS 去重自动成立。
- finalize 同时把逐文件对象写到 `pub/`、`int/`：`{prefix}/{org}/{service}/{archive_sha}/{path}`，并设置 Content-Type 与 Cache-Control。

## 5. Runtime：本地缓存与 ASSETS binding

### 5.1 存储关系

- S3 制品归档是唯一事实源。runtime 用控制面换取的短时效 presigned GET **直接从 S3 下载归档**（控制面只签名、不经手字节），下载后核验 archive / module sha256。
- **发布不重启 runtime pod，也不新建任何 Kubernetes 对象**。krun 的 Deployment 是控制面里的版本记录（制品 + 环境 + 配置；本文统一称「版本记录」，避免与 Kubernetes Deployment 混淆）。发布链路是：`/validate` 在单个候选成员上冷加载并核验 → commit → 生成 config revision（`routes + releases`）→ 写入对象存储快照与 `latest` 指针 → 各成员通过 `/config/changes` 增量应用并 ack。controller 不对每个 Pod 做管理 RPC，坏成员不会阻塞发布流水线。资产挂在与模块完全相同的这条加载链路上。
- 重启后的 readiness 只要求配置视图已加载；活动 Release 在首次请求时懒加载，本地缓存随之重建，启动成本不随服务数量线性增长。

### 5.2 资产加载：按文件懒加载 + 内存 LRU

- runtime 容器只有 workerd 进程，**没有文件系统**，所以资产不落盘，也不随归档整包展开。入口 Worker 命中资产时按文件从桶的 `pub/` 前缀取：控制面签发 15 分钟 presigned GET → runtime 直连对象存储 → 核验 manifest 里的 sha256 → 放进进程内 LRU。
- 每 pod 上限 `ASSETS_CACHE_MAX`（kind 默认 64 MiB），LRU 淘汰；未命中才回源，首访多一次控制面 RPC + 一次桶 GET（同 region 几十 ms），命中后内存直出。
- 资产 manifest（≤500 条）走内部 API `GET /releases/{id}/assets`，按 Release 缓存在内存，不进快照。
- 归档下载仍发生在 `/validate`（单台候选成员）和成员首次请求（懒加载）——但那只是模块代码；readiness 不再遍历全部 Release，资产的磁盘约束因此消失。
- 配置 `assets.binding`（wrangler 通常使用 `ASSETS`）后，runtime 通过 `ctx.exports` 给业务 Worker 注入 release-scoped Fetcher。`env.ASSETS.fetch(request)` 复用同一份资产 manifest、校验与 LRU，且 binding 不持有平台 token；名称冲突或平台保留名称会在验证阶段拒绝。

### 5.3 快照

- 快照的版本描述符不含文件表；route 条目仍是整 host 一条，资产配置与文件表按 Release 单独拉取并在 runtime 内缓存。

## 6. 静态域名与桶布局：静态文件的对外服务

### 6.1 决策

1. **一个桶，目录分三段**，靠桶策略（OSS/S3 均支持按前缀配匿名读）做访问隔离：

```text
bucket: krun-workers
├── v1/{org}/sha256/{archive_sha}/bundle.tar.gz   # 制品归档 · 禁止匿名 · 控制面/runtime 凭证读
├── pub/{org}/{service}/{archive_sha}/…     # 公网资产 · 匿名读（公网可达）
└── int/{org}/{service}/{archive_sha}/…     # 内网资产 · 匿名读（仅内网 endpoint 可达）
```

2. **workers 归档（含代码）走正常认证下载**：`v1/` 前缀不给匿名读，runtime 加载链路维持现状（控制面代理 / presigned），源代码不经静态域泄漏。
3. **公网资产 `pub/` 公开、内网资产 `int/` 仅内网公开**：finalize 从同一归档解出逐文件对象，`pub/` 与 `int/` 两段都写（当前还没有服务级网络面模型，双写最省事且供将来切换）。同一路径内容同一 `archive_sha`，无不同步问题。
4. **全平台一个静态域名**（不含 slug）：`static.{suffix}` 与 `static.{internal_suffix}` 各一条 kgateway 共享路由，分别把路径改写到桶的 `pub/`、`int/` 前缀；**本期不接 CDN**（用户决策），路由直接反代桶。生产公网 listener 落地时，再把公网静态域切到 CDN 源站。

```text
主域 {slug}.{suffix} ──── DNS 直指 LB ──► 网关 ──► runtime
（API、index.html、SPA 回落；归档加载走认证，与静态域无关）

static.{suffix}/{org}/{service}/{sha}/…          ──► LB ──► kgateway ──► 桶 pub/ 前缀
static.{internal}/{org}/{service}/{sha}/…        ──► 内网 LB ──► kgateway ──► 桶 int/ 前缀
（本期无 CDN；两条路由只做 host 匹配 + 前缀改写）
```

**为什么一个桶按目录拆而不是两个桶**：OSS/S3 桶策略原生支持按前缀配匿名读，隔离用一条 policy 表达即可；桶数量减半（endpoint、凭证、生命周期、监控都少一套）；finalize 在一个桶内写对象，失败语义单一。内外网可达性差异由「桶的公网 / 内网 endpoint + 前缀 policy」承担——`int/` 前缀的匿名读 policy 只在内网 endpoint 上生效（或干脆 `int/` 不配公网匿名，仅内网 endpoint 网络层可达）。
**为什么不带 slug**：防枚举由路径末段 `archive_sha` 承担——内容寻址、不可猜测，sha 只出现在已发布版本的 HTML 引用里；`{org}/{service}` 段可见不构成泄漏（等价于「拿到过 URL 就能访问」，静态资产的合理边界）。换来静态域恒定（业务的 `base` 前缀全平台一个）、无按服务发域 / 回收、无通配证书。

### 6.2 为什么主域不挂 CDN（同域方案弃用）

1. **API 为透传付费**。一个域名没法按路径分 DNS，API 请求全过 CDN；透传按流量计费，全站加速（DCDN 类）动态单价更高，API 大响应 / 流式场景成本失控。
2. **多一层故障面**：CDN 故障即全站故障；静态域独立后 CDN 挂了只影响静态文件，主域照常服务。
3. **缓存配置复杂**：同域要维护「哪些路径可缓存」的规则表，配错会把动态响应缓存进边缘；静态域独立后整个域都可缓存，无路径规则。

### 6.3 网关 / CDN 侧要做的事

- **两条共享路由，不随业务增长**：每个静态域一条 HTTPRoute（host 精确匹配），`URLRewrite` 把 Path 前缀 `/` 替换为 `/{bucket}/{pub|int}/`（`/{org}/...` → `/{bucket}/pub/{org}/...`，即对象 key），backendRef 指向 `static-assets` Service（kind 指向 MinIO；生产可换 kgateway `Backend`/外部 origin）。路由由 controller 幂等创建，500 服务也是这两条。
- **CORS 接入层统一加**：路由的 `ResponseHeaderModifier` 统一加 `Access-Control-Allow-Origin: *`（只读、无 Cookie，module 脚本 / 字体 / GET fetch 都不产生预检；`*` 不带 `Vary: Origin`）。
- **缓存策略**：桶对象在写入时设置 Content-Type 与 Cache-Control——文件名带 hash 的资源 `immutable` 长缓存，其余 `max-age=300`。
- **证书**：静态域是普通域名（非通配），单张证书；主域通配证书不变。

### 6.4 平台侧配套

1. finalize 写逐文件对象：解出 `assets/`，同时写 `pub/` 与 `int/` 的 `{org}/{service}/{archive_sha}/` 前缀（kind 里都在 MinIO）；`v1/` 归档段保持匿名不可读。
2. 业务构建时把资源 URL 指到静态域（Vite / webpack 的 `base`，一次性配置，全平台恒定值）。平台不注入、不改写 HTML；`index.html` 与 SPA 回落留在主域由 runtime 服务（量小）。
3. **没有「刷新 CDN」动作**：URL 按内容寻址，新版 HTML 引用新 URL，旧对象永久有效；未指纹化文件的短 TTL 就是生效延迟上界。回滚同理。
4. 主域不变量不动：零 CORS、内部头清洗、revision 寻址；发布仍是归档 + config revision 的单步切换。

## 7. 请求期行为细则

- `html_handling: auto-trailing-slash`（默认）：`/about` → 200 服务 `about.html`；`/about.html` → 307 `/about`；`/folder/index.html` → 307 `/folder/`。入口 Worker 按 release 的实际文件集合做动态判定。`none`：仅精确路径。
- 缓存：强 ETag（文件 sha256）+ `If-None-Match` → 304；`Cache-Control: public, max-age=300`。
- 出站响应补 `x-krun-release` / `x-krun-pod` 便于排障；入站继续剥除 `x-krun-*`（domains 第 7 节）。
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
| A1 ✅ | 源码包通道（presigned PUT）+ builder 打包 assets + manifest schema 2 + finalize 校验并写 pub/int | 单元：schema 1/2、类型白名单、上限、SPA 要求、hash 不符 |
| A2 ✅ | runtime：manifest/presign 内部 API + 入口决策流（run_worker_first、SPA 回落、307、ETag/304）+ 按文件懒加载 + 内存 LRU | kind e2e：资产 200/304、SPA 回落、`!` 反向、307、导航到 Worker 路径返回 HTML、未配置资产回归 |
| A3 | 控制台展示资产配置与大小；workers-backend.md 补路由说明 | 控制台验收 |
| A4 ✅（无 CDN） | 静态域 ×2 + 网关闭环路由 + MinIO Service + 桶策略（pub/int 匿名、v1 拒绝） | kind e2e：静态域 200、CORS 头、匿名 pub 200、匿名 v1 403 |

## 12. 开放问题

1. 文件名含 hash 的资源自动 `immutable`（A2 后按需）。
2. 预览环境是否默认带 SPA 配置模板。
3. dataplane router 的边缘缓存与缓存键（P4 之后）。
4. CDN 供应商与通配接入形态：生产公网入口落地时定。
