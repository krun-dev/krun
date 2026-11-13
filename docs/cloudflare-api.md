# Cloudflare Workers 协议适配

创建项目、构建和发布的简明步骤见 [使用 Wrangler 部署 Workers](../user-docs/src/content/docs/guides/cloudflare-workers.md)。本文记录适配接口与实现细节。

入口为同源 `/api/v1/cloudflare/client/v4`。使用 krun API Key 或 OAuth Token。
`account_id` 接受 krun 组织 ID 或组织短名。Script 名就是该 Worker 的定位符。
例如原生路径为 `platform/workers/tu-pian-chu-li` 时，Wrangler 可用
`CLOUDFLARE_ACCOUNT_ID=platform` 和 `name = "tu-pian-chu-li"`。控制台与 Wrangler
共同使用同一个 Worker 身份和版本历史。

Wrangler 的普通 `deploy` 和 `--env` 生成的不同 Script 名称对应不同服务。
`wrangler preview --name` 对应该服务下的 Preview。地址使用 krun 已配置域名。
Worker 定位符 `domains`、`network-profiles` 和 `repository-providers` 供原生集合接口使用。

```sh
export CLOUDFLARE_API_BASE_URL=https://console.example.com/api/v1/cloudflare/client/v4
export CLOUDFLARE_ACCOUNT_ID=replace-me
export CLOUDFLARE_API_TOKEN=replace-me
npx wrangler@4.155.0 deploy
npx wrangler@4.155.0 versions upload
npx wrangler@4.155.0 versions deploy VERSION_ID@100% --yes
```

浏览器登录使用与 krun CLI 相同的 OAuth issuer。Wrangler 固定版本 4.155.0 配置：

```sh
export WRANGLER_CLIENT_ID=krun-wrangler
export WRANGLER_AUTH_DOMAIN=console.example.com
export WRANGLER_AUTH_URL=https://console.example.com/api/auth/oauth/authorize
export WRANGLER_TOKEN_URL=https://console.example.com/api/auth/oauth/token
export WRANGLER_REVOKE_URL=https://console.example.com/api/auth/oauth/revoke
# 同时沿用上面的 CLOUDFLARE_API_BASE_URL；OAuth 登录时不要设置 API Token。
npx wrangler@4.155.0 login
npx wrangler@4.155.0 whoami
npx wrangler@4.155.0 deploy
npx wrangler@4.155.0 logout
```

使用现有 krun 登录页和独立授权确认页；同意后回到 Wrangler 的 localhost 回调。
Token 自动刷新、退出撤销、scope 和用户当前权限取交集。Wrangler 默认申请其全部产品
scopes，krun 只签发已支持的子集；`whoami` 对未签发的其他产品权限可能显示提示。Wrangler 的 `login --device`
将授权端点固定在根级 `/oauth2/device/auth`，当前没有绕过 `/api/` 路由契约提供该路径；
krun CLI 自身的 `login --device` 使用同一 issuer 的规范 `/api/auth/` 接口。

适配层复用原有权限、Cookie/CSRF、审计、不可变 Version、部署 CAS、独立 validator、
Runtime 配置分发和保留期。浏览器会话需要原有 CSRF 校验；CLI 使用 Bearer API Key 或 OAuth Token。
同步上传的制品读写和等待部署都在数据库事务之外。Script 名在组织内由资源行保证唯一。
删除后同名创建会获得新的 Worker ID，版本与密钥属于各自的 Worker 身份。

| 接口（相对于入口） | 行为 |
| --- | --- |
| `GET /accounts`、`GET /memberships`、`GET /user`、`GET /user/tokens/verify` | 可见组织、当前凭据状态 |
| `GET/POST /accounts/{account}/hyperdrive/configs`、`GET/PATCH/DELETE .../configs/{id}` | PostgreSQL 托管配置管理 |
| `GET /accounts/{account}/workers/subdomain` | Wrangler 所需的账户子域元数据 |
| `GET /accounts/{account}/workers/scripts` | 已登记且有权读取的 Script |
| `PUT /accounts/{account}/workers/scripts/{script}` | 上传已打包 ES modules，创建 Version 并部署 |
| `POST .../scripts/{script}/assets-upload-session` | 提交资产清单，取得限定 Worker 的一小时上传凭据和待传文件分组 |
| `POST /accounts/{account}/workers/assets/upload?base64=true` | 上传 multipart 资产分组；完整上传后取得部署所需 completion token |
| `POST /accounts/{account}/workers/assets/upload/{hash}` | 上传单个原始资产文件 |
| `DELETE /accounts/{account}/workers/scripts/{script}` | 删除 Worker 资源，后台清理关联实体 |
| `GET .../scripts/{script}/settings`、`.../script-settings` | 配置和脱敏绑定 |
| `POST/GET .../scripts/{script}/versions`、`GET .../versions/{id}` | 只上传版本、列出版本、读取版本 |
| `POST/GET .../scripts/{script}/deployments`、`GET .../deployments/{id}` | 单版本 100% 或双版本百分比分流，查询实际版本权重和说明 |
| `GET/PUT .../scripts/{script}/secrets`、`DELETE .../secrets/{name}` | Secret 元数据、修改及删除 |
| `PATCH .../scripts/{script}/secrets-bulk` | 有界 Secret 批量修改 |
| `GET/POST .../scripts/{script}/subdomain` | 已启用的 krun 托管 URL |
| `POST /accounts/{account}/workers/workers` | 创建 Worker 身份 |
| `GET/PATCH .../workers/{script}`、`GET .../services/{script}` | Worker 元数据、Previews Base 绑定 |
| `GET/POST .../workers/{script}/previews` | 列出、创建 Preview |
| `GET/DELETE .../workers/{script}/previews/{name}` | 读取、删除 Preview |
| `POST .../previews/{name}/deployments` | 上传并部署 Preview |
| `GET/PATCH .../previews/{name}/deployments/{id}` | 读取或修改 Preview Secret；支持 `latest` |

Version 固定代码、普通变量和 Secret 引用；只上传不会改变生产流量。Secret 修改基于
当前已部署代码生成并部署新版本；存在更新但未部署的 Version 时返回 `10215`。首次
`wrangler secret put` 沿用 Wrangler 创建占位 Worker 后再写 Secret 的流程。
Previews Base 只在创建 Preview 时复制；不会覆盖已存在 Preview 或生产环境。
单版本部署的固定地址保留 30 天，删除目标环境或服务后失效。灰度期间使用环境地址。

当前支持 `plain_text`、`json`、`secret_text`、`version_metadata`、`assets`、`hyperdrive`、`r2_bucket` 和 `service`，以及已打包 ESM、CommonJS 依赖、文本、数据、Wasm
和 source map 模块；主模块必须为 ESM。沿用 krun 的模块、变量、Secret、Preview 数量
和请求大小限制。错误采用 Cloudflare envelope 和数字 code，底层仍执行 krun 权限。

JSON 变量保留对象、数组、数字、布尔及 null 类型，随 Version 冻结；原生控制台同样可以编辑。
`keep_bindings: ["plain_text", "json"]` 支持 Wrangler `--keep-vars`，保留未提交的变量；
同名新值仍覆盖，普通上传默认替换变量。
`version_metadata` 将 `{id,tag,timestamp}` 注入指定 binding。上传 annotations 的
`workers/tag`、`workers/message` 分别保存版本标签和说明，长度上限为 100、1000 bytes。

官方 Wrangler 的资产部署、只上传 Version、Preview 和纯静态 Worker 均使用上述上传流程。
清单逐文件保存，按 Wrangler 的内容 hash 和声明大小校验；上传凭据不能跨组织，completion
token 只能绑定到签发时的 Worker，且未传完或过期时不能部署。资产打包进现有不可变制品，
因此部署、回滚和固定地址复用原有运行链路；`keep_assets` 可以保留当前部署的资产。
会话和文件元数据保留一小时，临时对象登记到现有 upload 清理任务，最长保留 24 小时。

托管 URL 关闭、Cloudflare 自定义域名/Zone/Routes、定时任务、tail、KV/D1/DO/Queue、
Service Worker 格式仍超出兼容范围。资产路由支持 `run_worker_first`、`base_path`，
`none` / `single-page-application` / `404-page`，以及 `none` / `auto-trailing-slash` /
`force-trailing-slash` / `drop-trailing-slash`。`_headers` 支持路径、域名、占位符、
合并和删除响应头；`_redirects` 支持 301/302/303/307/308 和站内 200 重写，
不代理外部站点。规则仅作用于静态响应，随资产版本冻结。部署说明使用 `annotations["workers/message"]`，版本标签和说明随版本保存。
缺失接口和不支持的绑定会返回错误。静态资产也可以继续走 krun 原有构建入口。

## 版本灰度

先用 `versions list` 获取当前 Worker 的两个版本 ID，将它们设为 `STABLE_VERSION` 和
`CANDIDATE_VERSION`。沿用上方的 Wrangler 连接配置和具备该环境 `worker.publish` 的凭据。

```sh
npx wrangler@4.155.0 versions deploy "${STABLE_VERSION}@100%" "${CANDIDATE_VERSION}@0%" --yes
npx wrangler@4.155.0 versions deploy "${STABLE_VERSION}@95%" "${CANDIDATE_VERSION}@5%" --yes
npx wrangler@4.155.0 deployments status
npx wrangler@4.155.0 rollback "$STABLE_VERSION" --yes --message "Restore stable"
```

krun CLI 同样支持 `versions deploy VERSION_ID@PERCENTAGE%` 的双版本语法，查询与回滚示例见
[krun CLI 双版本灰度](../user-docs/src/content/docs/guides/krun-cli.md#双版本灰度)。

Cloudflare 协议使用 `strategy: "percentage"` 和 `versions` 列表。最多两个同一环境的版本，
百分比合计 100，0% 版本可定向验证。非法权重或重复版本返回 HTTP 422 / Cloudflare 错误
`10021`（`UNSUPPORTED_DEPLOYMENT`）。跨 Worker 的版本返回 HTTP 404 / `10007`。
修正版本 ID 或百分比后重新提交。

`Krun-Workers-Version-Key` 和 `Cloudflare-Workers-Version-Key` 使用相同分桶，同时出现时
以 krun Header 为准。正式域名使用 `Cloudflare-Workers-Version-Overrides: SCRIPT="VERSION"`
定向验证，其中 `SCRIPT` 为 Worker 定位符，`VERSION` 为当前部署成员的版本 ID。验证要求
`worker.publish` 权限，通过 `Authorization: Bearer …` 提交 krun 凭据。需要传入业务
`Authorization` 时改用 `Krun-Workers-Verify-Authorization` 提交 krun 凭据。权限不足
返回 HTTP 403，需更换为有目标 Worker 和环境发布权的凭据。核对响应的 `X-Krun-Version`。
鉴权与验证请求绕过共享响应缓存。公开响应缓存按实际版本隔离。

已承流版本的不可变哈希文件可以在过渡期通过原路径读取。平台通过
`workers.gradual_asset_retention_seconds` 设置保留窗口，默认为 24 小时。环境可通过原生 API 的
`gradual_asset_retention_seconds` 覆盖该设置。值为 `0` 时停止保留后续退场版本的哈希资产。
固定部署地址继续使用其独立保留期。

## 服务绑定

Wrangler 配置中的 `services` 绑定跟随同一组织内目标 Worker 的发布。目标使用 Worker
定位符，调用方版本冻结 binding 配置，调用者通过 `env.BINDING.fetch(request)` 发起 HTTP 请求。
绑定建立时要求来源的 `worker.publish` 和目标的 `worker.invoke`，运行时复核目标调用权限。

```json
{
  "services": [{ "binding": "CATALOG", "service": "catalog", "environment": "production" }]
}
```

krun 原生环境配置的 `service_bindings` 可以指定同一目标环境的 `version_id`，用于固定版本调用。
默认 HTTP 入口支持同池和跨池。目标被禁用、删除或授权被撤销时，调用失败。需要沿用会话选版时，
在发给 binding 的 Request 中传入 `Cloudflare-Workers-Version-Key`。

## 端到端验证

先按[独立 kind 验收步骤](../deploy/workers-kind/README.md)启动集群和控制台同源入口。
沿用该步骤的 `KUBECONFIG`、`KIND_CLUSTER_NAME` 以初始化本次测试的管理员。
将 kind 的私网 `workers-mvp` 和公网 `workers-public` Gateway 服务的 8080 端口
分别转发到本机 4382 和 4383，再从仓库根目录运行：

```sh
npm ci --prefix workers/builder
KRUN_UI_ORIGIN=http://127.0.0.1:4381 \
  KRUN_CF_E2E_GATEWAY_ORIGIN=http://127.0.0.1:4382 \
  KRUN_CF_E2E_PUBLIC_GATEWAY_ORIGIN=http://127.0.0.1:4383 \
  node deploy/workers-kind/cloudflare.e2e.mjs
```

脚本使用 Wrangler 4.155.0，验证上传、双版本部署、分桶、定向验证、回滚、资产、缓存、
服务绑定和原有 Secret / Preview 操作。成功时输出 `CF WRANGLER E2E PASSED` 并保存本地结果。

## R2 bucket bindings

支持标准上传 metadata `r2_bucket` 的 `name`、`bucket_name`，以及 `keep_bindings`
中的 `r2_bucket` 和按名称 `inherit`。binding 按组织解析并随 Version 冻结。
`default` 以外的 jurisdiction 会明确拒绝，不把它映射成另一个区域。

桶元数据接口为 `/accounts/{account}/r2/buckets` 的 GET/POST 和
`/accounts/{account}/r2/buckets/{bucket}` 的 GET，均位于本文开头的适配前缀下。
创建桶需要 `organization.update`，读取需要 `organization.read`；部署 binding 使用
既有 `worker.publish` 权限，不要求发布者额外持有对象访问凭据。
支持范围及现有限制见 [R2 binding](workers-r2.md)。

`GET /accounts/{account}/r2/buckets/{bucket}/objects` 还支持 `prefix`、`delimiter`（空或 `/`）、
`cursor`、`start_after`、`per_page`（1–1000），需要独立 `storage.object.read`。
返回逻辑 key、size、etag、last_modified，以及 result_info 中的 cursor、delimited 和
is_truncated、per_page；列表不逐项 HEAD，不包含对象 version 或 HTTP/custom metadata。
对象内容上传下载使用 [原生直传接口](bucket-objects.md)，不实现经控制面传输对象内容的
Cloudflare REST GET/PUT 代理。

## 控制台配置版本与兼容接口

控制台命名配置库使用原生 `/api/v1/.../configurations` 接口，独立于 Cloudflare
协议资源。应用配置时复用当前代码创建普通 Worker Version，再通过相同 Deployment
队列发布；配置名称与说明写入版本的 `workers/tag`、`workers/message` annotations。
Cloudflare 客户端读取版本仍得到标准 bindings 和 annotations，不需要识别原生
`configuration_id`。原生配置库可以复制兼容 API 写入的当前环境或版本配置。

代码更新且绑定未变时保留已有命名配置的关联；Cloudflare 或原生 API 改变绑定后，
下一版本不再沿用旧配置关联。Previews Base 仍只在创建 Preview 时复制，直接 Secrets
API 的自动发布行为也保持不变。

Hyperdrive 配置接口仅接受 PostgreSQL。原版 Wrangler 将 `--connection-string` 解析为
源库字段后提交，响应只返回源库元数据。绑定使用配置 ID，部署后注入源库直连串。
创建、绑定和密码轮换见 [PostgreSQL 托管接入](../user-docs/src/content/docs/guides/workers-hyperdrive.md)。
