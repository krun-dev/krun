# Cloudflare Workers API 兼容规范

状态：**已确定的设计规范，尚未实现**。实现和验收完成前，不得在用户文档中声称 `wrangler deploy` 已可直连 krun。

本文定义 krun 对 Cloudflare Workers Script / Versions / Deployments API 的兼容边界。目标是让已在本地或外部 CI 完成构建的 Module Worker 通过 Wrangler 或 Cloudflare multipart 客户端上传到 krun，而不是复制 Cloudflare 全部资源管理 API。

本规范与 [Workers 构建与发布后端](workers-backend.md)、[Cloudflare Workers 构建兼容](workers-build-compatibility.md) 和 [静态资产设计](workers-assets.md) 共同约束实现。

## 1. 路由与配置

兼容 API 使用独立前缀：

```text
/api/cloudflare/client/v4
```

这是 krun 的对外 API 前缀，不是新的根级出口。它仍在 `/api/*` 下，生产网关仍只需：

```text
/api/* -> Go server
/*     -> OSS static site
```

Wrangler 通过官方支持的环境变量切换 API origin：

```bash
export CLOUDFLARE_API_BASE_URL=https://console.example.com/api/cloudflare/client/v4
export CLOUDFLARE_ACCOUNT_ID=<krun-organization-id>
export CLOUDFLARE_API_TOKEN=<krun-api-key>
```

第一阶段的 `wrangler.jsonc` 应使用 krun Service ID 作为 `name`，并关闭 Cloudflare 专有的 `workers.dev` 触发器管理：

```jsonc
{
  "name": "svc_xxx",
  "main": "dist/worker.js",
  "compatibility_date": "2026-11-02",
  "workers_dev": false
}
```

krun 不接受 `/client/v4/*` 根级路由，也不得为模仿 Cloudflare 新增 `/accounts/*` 根级路由。

## 2. 双 API 与实现复用

krun 同时保留两份对外 API：

| API | 前缀 | 定位 |
| --- | --- | --- |
| krun 原生 Workers API | `/api/v1/orgs/{org}/services/{service}/workers/*` | krun 完整能力、异步 Build / Operation、环境与权限模型 |
| Cloudflare 兼容 API | `/api/cloudflare/client/v4/*` | Wrangler 和 Cloudflare multipart / Versions / Deployments 客户端兼容 |

两份 API 都是正式公开契约。Cloudflare 兼容层不替代 krun 原生 API，原生 API 也不因为兼容 Wrangler 而被强制改成 Cloudflare 的资源命名或响应格式。

实现遵循以下规则：

1. **语义对齐时共用同一应用层实现**。例如模块校验、Artifact 内容寻址存储、Release 快照和 100% Deployment 提交，应由两份 HTTP API 调用相同 command / service，不复制业务逻辑。
2. **语义无法对齐时保留两份编排**。例如 krun 原生 Build 是异步源码构建，Cloudflare Script Upload 是已构建 multipart 直传；两者可复用 packer、validator 和 Artifact store，但不应强行共用一个 HTTP handler 或一套请求 DTO。
3. **不允许 handler 通过内部 HTTP 调用另一份 API**。复用点必须下沉到 Go 应用层或领域层，避免重复认证、二次 JSON / multipart 转换、错误丢失和事务边界混乱。
4. **原生契约和兼容契约分别测试**。共用实现的单元测试只证明业务逻辑；两份 API 仍各自需要 HTTP 契约和端到端验收。

推荐的分层：

```text
krun native HTTP adapter ---------+
                                  +--> Workers application facade
Cloudflare HTTP adapter ----------+      |-- artifact ingestion service
                                         |-- version/release service
                                         |-- deployment/operation service
                                         |-- binding/secret service
                                         `-- assets service
```

### 2.1 中间层形态

`Workers application facade` 是两份 API 之间的协议无关中间层，是 Go 进程内部的应用服务，不是第三份 HTTP API、独立网络服务或新的持久化快照。

它负责：

- 接收已认证的 `WorkerActor` 和已规范化的 Organization / Service / Environment Target；
- 统一执行授权动作、幂等、限额和状态转移；
- 编排对象存储与 PostgreSQL 之间的分段写入，但不在数据库事务中发起网络 I/O；
- 返回类型化内部结果和错误，由原生 adapter 编码为 krun JSON，由兼容 adapter 编码为 Cloudflare envelope；
- 为 controller 恢复和 HTTP 请求使用同一组状态转移原语，不把业务规则只放在 handler 中。

建议的中间层命令与查询边界：

```text
PrepareWorkerArtifact   已构建 modules/assets -> 确定性 Artifact
FinalizeWorkerArtifact  staging object -> finalized Artifact
CreateWorkerVersion     Artifact + binding snapshot -> immutable Release
DeployWorkerVersion     Release + target + expected state -> Operation
PutWorkerSecret         SecretVersion + current Artifact -> Release + Operation
DeleteWorkerSecret      current Artifact without named Secret -> Release + Operation
Get/ListWorkerVersions  internal Release view
Get/ListDeployments     internal Operation/history view
```

这些名称是责任边界，不要实现成一个持有所有依赖的巨型 service。Artifact、Version、Deployment、Binding 和 Assets 仍应有独立的小服务/仓储边界，facade 只组合一次用例需要的步骤。

HTTP adapter 仅负责：

- 路由参数和 account / service 身份映射；
- JSON 或 multipart 的有限解码；
- Cloudflare DTO / krun DTO 与内部 command 之间的转换；
- HTTP status、headers 和错误 envelope 编码。

adapter 不直接操作 Artifact / Release / Operation 表，也不自行实现第二套发布状态机。

具体复用边界：

| 能力 | HTTP / 编排 | 可共用核心 |
| --- | --- | --- |
| 仓库或内联源码 Build | krun 原生 API 独有 | 最终 Artifact 校验与存储 |
| Cloudflare multipart 直传 | 兼容 API 独有 | module 规范化、wrapper、packer、Artifact 存储 |
| Version / Release | 响应 ID 和 DTO 分开 | 不可变 Release 创建与查询 |
| Deployment / Publish | Cloudflare 第一阶段固定 production + 100%；krun 原生 API 支持显式环境和 revision | candidate validation、CAS commit、config revision、ack 收敛 |
| Bindings / Secrets | 两份 API 各自解析和错误映射；Script Secret PUT/DELETE 在兼容 API 中同步等待 committed | 名称校验、Release 快照、Secret 加密与版本、发布 Operation |
| Static Assets | krun source bundle 与 Cloudflare upload session 分开 | asset path/hash 校验、内容寻址 blob、manifest、保留与 GC |
| 错误 | krun problem JSON 与 Cloudflare envelope 分开 | 内部类型化错误和稳定错误分类 |

共用 command 接收的必须是已认证、已规范化的内部参数，不接收 `http.Request`、Cloudflare envelope 或 krun 原生 DTO。授权统一映射到现有 action / Target，但两份 adapter 各自负责路径身份映射、请求解码和响应编码。

如果 Cloudflare 语义与 krun 的安全、持久化或发布不变量冲突，优先在兼容 API 中显式返回 unsupported，不修改 krun 原生语义，也不返回假成功。

## 3. 身份映射和授权

| Cloudflare 概念 | krun 实体 |
| --- | --- |
| Account ID | Organization ID |
| Worker name | Service ID |
| Worker Script | Service |
| Worker Version | 不可变 Release |
| Deployment | Operation + Environment 当前 Release |
| multipart modules | `workerd-bundle-v2` Artifact |
| production | `production` Environment |

`Authorization: Bearer` 继续使用 krun 现有 CLI Token 或 API Key；不引入第二套 Cloudflare Token 表。自动化应使用只授予目标 Service 的 API Key。

兼容层必须继续执行 Cedar 授权：

- 上传 Version 需要 `service.version.create`；
- 直接 `deploy` 同时需要 `service.version.create` 和 `service.publish`；
- 读取 Version / Deployment 需要 `service.version.read`；
- Account ID 与 Service 所属组织不匹配时统一返回 not found，不泄露跨组织资源。

`script_name` 第一阶段只接受 Service ID。不用可重名的显示名称做隐式解析。

## 4. API 响应包装

兼容路由使用 Cloudflare envelope，不返回 krun 原生 problem JSON：

```json
{
  "success": true,
  "result": {},
  "errors": [],
  "messages": []
}
```

失败响应：

```json
{
  "success": false,
  "result": null,
  "errors": [
    {
      "code": 10021,
      "message": "Unsupported binding type: kv_namespace"
    }
  ],
  "messages": []
}
```

必须维持的 Wrangler 关键错误语义：

- `10007`：Worker / Service 不存在；
- `10021`：multipart metadata、binding、compatibility 或 runtime validation 失败；
- `10027`：上传制品超过 krun 限制。

其他 krun 错误应映射为稳定数字 code 和可读 message。不得将 PostgreSQL、S3、Kubernetes 或内网信息写入 `errors[].message`。

## 5. 第一阶段端点

### 5.1 Script 直接上传并发布

```text
PUT /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}
```

请求为 Cloudflare `multipart/form-data`，语义为：

1. 校验已构建 modules 和 metadata；
2. 生成确定性 Artifact；
3. 创建已成功的直传 Build 记录；
4. 创建 production Release；
5. 创建 production publish Operation。

响应表示制品、Release 和 Operation 已持久化，不表示所有 runtime 已 ack。真正生效仍经过现有 candidate validation、CAS commit 和 runtime ack 收敛流程。

### 5.2 只上传 Version

```text
POST /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/versions
```

用于：

```bash
npx wrangler versions upload --no-bundle
```

它创建 Artifact 和未发布 Release，不改变 production 流量。

### 5.3 发布已有 Version

```text
POST /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/deployments
```

第一阶段只接受一个 Version 且 `percentage=100`。多 Version、部分流量或权重之和不为 100 时显式返回 unsupported，不做静默降级。

### 5.4 Wrangler 预检和查询

```text
GET   /api/cloudflare/client/v4/accounts/{org}/workers/services/{service}
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/versions
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/versions/{version}
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/deployments
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/secrets
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/secrets/{secret}
GET   /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/subdomain
POST  /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/subdomain
PATCH /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/script-settings
```

Wrangler 在重复 deploy 的预检中会读取 Secret 名称，以避免普通 variable 意外覆盖远端 Secret；该 `GET /secrets` 只返回名称和安全元数据，永不返回值。Wrangler 在 deploy 后即使配置 `workers_dev=false` 也会同步 script subdomain 状态；第一阶段的 subdomain 接口因此必须稳定返回 `enabled=false` 和 `previews_enabled=false`，且不创建额外路由。

`script-settings` 第一阶段只接受已明确映射的字段。Wrangler 默认发送的 `observability.enabled=false` 可作为等价的安全 no-op；`enabled=true`、非空 tags 或 tail consumers 在尚未实现时必须显式拒绝。其他字段只能在 Wrangler 契约测试证明语义等价时忽略，不得为了让 CLI 退出码为 0 而返回假成功。

### 5.5 Script Secret 直接对齐

```text
PUT    /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/secrets
DELETE /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/secrets/{secret}
```

这两个接口与 krun 现有 Secret 轮换/删除语义一致，第一阶段直接对齐 Cloudflare：

Cloudflare 的公开契约同样规定 [`wrangler secret put/delete`](https://developers.cloudflare.com/workers/configuration/secrets/) 会创建新 Version 并立即部署；对应的 [PUT](https://developers.cloudflare.com/api/resources/workers/subresources/scripts/subresources/secrets/methods/update/) 和 [DELETE](https://developers.cloudflare.com/api/resources/workers/subresources/scripts/subresources/secrets/methods/delete/) Script API 分别通过“带新 Secret 的 Version”和“不带该 Secret 的 Version”完成变更。

- `PUT` 新增 Secret；同名 `PUT` 表示轮换；
- `DELETE` 删除指定 Secret；
- 两者都复用当前 production Release 的 Artifact，不重新构建代码；
- 两者都创建新的不可变 Release，并立即创建 production publish Operation；
- Secret 值只写不读；GET、LIST、响应和日志都不得返回明文或密文；
- Service 尚无可复用的已发布 Artifact 时返回 Worker 不存在或不可更新，不创建只有 Secret、没有代码的 Release。

`PUT` 接受 Cloudflare 的 `secret_text` 请求：

```json
{
  "name": "TOKEN",
  "text": "replace-me",
  "type": "secret_text"
}
```

成功响应不包含 `text`：

```json
{
  "success": true,
  "result": {
    "name": "TOKEN",
    "type": "secret_text"
  },
  "errors": [],
  "messages": []
}
```

`DELETE` 成功时 `result` 为 `{}`。兼容 adapter 调用与原生 API 相同的 Secret application service，但响应契约不同：原生 API 可以返回 `202 + Operation`；Cloudflare adapter 只有在 Operation 至少到达 `committed` 后才返回成功。runtime 的全部 ack 仍可在稍后收敛，不要求 HTTP 请求等待 `available`。

这一直接对齐仅适用于 Script 级 `wrangler secret put/delete`。`wrangler versions secret put/delete/bulk` 的语义是只创建新 Version、不部署；它不能复用当前“Secret 变更后立即发布”的 HTTP 编排，需在后续阶段由同一 Secret / Release 核心实现增加 version-only command。`PATCH /secrets-bulk` 还要求多项 Secret 原子形成一个 Version，也不在第一阶段伪装支持。

## 6. Version 和 Deployment ID

Cloudflare 兼容 API 对外使用 UUID 形式的 Version / Deployment ID，不直接暴露 `rel_*` 或 `op_*`。

- `krun_worker_releases` 保存唯一的 `cloudflare_version_id`；
- `krun_worker_operations` 可选保存 `cloudflare_deployment_id`；
- 重试同一幂等请求必须返回相同 ID；
- ID 映射是实体字段，不新增无界的整份响应快照表。

Version 保存代码、Artifact、compatibility 和 binding 快照。Deployment 只指向 Version，不复制代码或 bindings。

## 7. multipart 和模块边界

metadata 至少需要：

```json
{
  "main_module": "worker.js",
  "compatibility_date": "2026-11-02",
  "compatibility_flags": []
}
```

第一阶段支持：

| Content-Type | krun module type |
| --- | --- |
| `application/javascript+module` / `text/javascript+module` | ESM |
| 受支持的 CommonJS module content type | CommonJS 附加模块 |
| `application/wasm` | Wasm |
| `text/plain` | Text |
| `application/octet-stream` | Data |
| source-map content type | Source map |

限制：

- `main_module` 必须存在且为 Module Worker JavaScript；
- 拒绝 legacy Service Worker `body_part`；
- 拒绝 Python Worker 和 `python_workers`；
- 所有模块名、Content-Disposition name / filename 和 import 相对路径必须通过现有路径边界校验；
- 拒绝重名 part、重复 metadata、未声明文件、绝对路径、反斜杠和 `..`；
- Wasm magic、UTF-8 文本和 source-map JSON 继续由可信 packer 校验。

上传内容已是构建产物，因此不调度 npm/esbuild/Wrangler 构建 Job。可信 packer 需保留用户入口的命名导出，注入 `KrunValidation` wrapper，生成确定性 `workerd-bundle-v2`。发布前仍执行真实 workerd candidate validation。

## 8. Bindings

第一阶段只保证：

- `plain_text` -> Release `Variables`；
- Script Secret API 的 `secret_text` 新增、同名轮换、查询安全元数据和删除；
- 已存在 Secret 的显式 inherit，前提是可以严格解析到前一 Version；
- 名称冲突、平台保留名、数量和总大小继续使用 krun 限制。

multipart Version 上传内联新的 `secret_text`、`secret_key`、KV、R2、D1、Queue、Service、Durable Object、Hyperdrive、AI 等 binding，在没有完成精确映射前一律显式拒绝。不根据 binding ID 自动创建平台资源。

Version 上传时的 variables 固定在 Release 中，不立即改变 production Environment。Deployment 成功提交该 Version 时，Environment 的期望配置与已发布 Release 同步，避免之后 Secret 轮换或重新发布意外恢复为旧 variables。

## 9. Static Assets（第二阶段）

Wrangler Assets 不会把全部文件直接放入 Script multipart，而是独立的 manifest / upload-session 协议。完整兼容至少需要：

```text
POST /api/cloudflare/client/v4/accounts/{org}/workers/scripts/{service}/assets-upload-session
POST /api/cloudflare/client/v4/accounts/{org}/workers/assets/upload?base64=true
POST /api/cloudflare/client/v4/accounts/{org}/workers/assets/upload/{hash}
```

实现必须包含：

- path -> content hash + size manifest；
- 已有内容去重与缺失 bucket 返回；
- 只允许上传当前 session 缺失 hash 的短期 token；
- 完成 token 只能绑定到同一 Organization / Service / manifest；
- 未完成 session、未引用 asset blob 和失败上传的保留期与清理语句；
- 不把整份 asset manifest 当作无界 JSON 文档反复覆盖。

在该协议完成前，兼容 API 对 metadata 中的 `assets` 显式返回 unsupported，不退化为内联 Script 文件。

## 10. 大小、流式处理和清理

API 外形兼容不等于立即放宽到 Cloudflare 的 64 MiB。未修改 profile 前使用 krun 现有限制：

- modules 最多 512 个；
- 单 module 最多 2 MiB；
- modules 合计最多 8 MiB；
- assets 最多 500 个、单文件 5 MiB、合计 20 MiB。

如将 Worker 限制提升到 64 MiB，兼容 handler 必须流式解析 multipart 到有限临时目录或对象存储，不得复用通用 JSON middleware 把整个 body 读入 Go heap。每个 part 和总 body 都要在读取过程中限额。

对象存储和 PostgreSQL 不能在一个数据库事务中做网络 I/O。直传必须使用小步骤和幂等状态：

1. 创建有保留期的 uploading/staging 实体；
2. 在事务外上传并校验对象；
3. 用条件 `UPDATE` 将 Artifact / Release 推进到可用状态；
4. 失败和超时由后台清理回收临时对象。

新增的每次上传行必须在同一实现中加入保留期、限量 GC 查询和对象存储删除流程。

## 11. 不支持范围

下列能力不属于第一阶段：

- Python Worker、Rust `worker-build` 和 legacy Service Worker；
- Cloudflare 资源自动创建或资源 ID 兼容；
- Durable Object migrations；
- Containers；
- routes、custom domains、cron、queues consumer 和 email routing；
- `workers.dev` subdomain 注册和 Cloudflare Version URL 域名格式；
- `placement=smart`；
- 两个 Version 的流量拆分；
- `secret_key`、Version-only Secret 变更和原子 `secrets-bulk`；
- Cloudflare 账户、成员、计费或 zone API。

兼容层是有限子集，不得通过返回空成功响应伪装支持上述能力。

## 12. 安全边界

- 直传路径不运行 npm lifecycle、用户 shell 或构建命令；
- multipart 文件名、metadata 和压缩/归档内容都按不可信输入处理；
- 客户端不得上传 `KrunValidation`、`PLATFORM`、`LOADER` 或其他平台保留 binding 来覆盖平台模块；
- 用户入口由 packer 改名，平台 wrapper 占据原入口名并保留命名导出；
- 上传阶段不获得 runtime token、S3 长期凭据、Kubernetes ServiceAccount 或数据库凭据；
- 只有通过真实 workerd candidate validation 的 Release 才能 commit。

## 13. Wrangler 契约验收

不能只用手写 `curl` 声称 Wrangler 兼容。实现完成的最低验收为：

1. 固定与 builder 相同的 Wrangler 版本；
2. 使用 `CLOUDFLARE_API_BASE_URL` 实际执行 `wrangler deploy --no-bundle`；
3. 再次执行 deploy，覆盖已有 Worker，不只测首次创建；
4. 执行 `wrangler versions upload --no-bundle`，确认 production 未变；
5. 用 `wrangler versions deploy` 将该 Version 100% 发布；
6. 执行 `wrangler secret put` 新增 Secret、同名再次执行完成轮换，再执行 `wrangler secret delete`；确认每次只复用 Artifact 创建新 Release 并发布，任何查询和响应都不泄露值；
7. 验证 ESM 入口、命名导出、Text/Data/Wasm/source map 和 `nodejs_compat`；
8. 验证跨组织、只读 Key、未知 binding、超限模块和错误 compatibility 均以 Cloudflare envelope 失败；
9. 等待 Operation `available`，经同源 Gateway 访问真实 Worker；
10. `make test`、race/vet、builder 测试、workerd 测试和 `make check-secrets` 全部通过。

对 Wrangler 版本升级必须重跑该契约组；Cloudflare 未文档化的预检请求变化不得在生产中靠宽松 catch-all 隐藏。

## 14. 实施阶段

| 阶段 | 范围 | 完成标准 |
| --- | --- | --- |
| C0 | 提取协议无关的 Workers application facade 和类型化错误 | 现有 krun 原生 API 全部回归通过，handler 不再独占可复用的 Artifact / Release / Deployment 业务逻辑 |
| C1 | 代码 modules、plain text variables、Script `secret_text` CRUD、Version 和 100% Deployment | `deploy --no-bundle` / `versions upload --no-bundle` / `versions deploy` / `secret put` / `secret delete` 真实契约通过 |
| C2 | Static Assets upload session 和内容去重 | Wrangler 资产三段上传、重试和 GC 验收通过 |
| C3 | Version-only Secret 变更、原子 bulk Secret、multipart Secret upload 及选定 binding | 加密存储、回滚、权限和泄露测试通过 |
| C4 | 有明确产品需求的触发器或灰度 | 不破坏现有 Domain/Gateway/Operation 模型，再单独评审 |
