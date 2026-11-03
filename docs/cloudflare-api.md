# Cloudflare Workers 协议适配

入口为同源 `/api/v1/cloudflare/client/v4`。使用 krun API Key 或 OAuth Token；`account_id` 是 krun
组织 ID。Script 名称在组织内稳定映射到服务 ID，不拆解环境后缀。Wrangler 的普通
`deploy` 和 `--env` 生成的不同 Script 名称对应不同服务；`wrangler preview --name`
对应该服务下的 Preview。地址使用 krun 已配置域名，不占用 `workers.dev`。

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
同步上传的制品读写和等待部署都在数据库事务之外。首次创建中断或通过原生接口删除
服务后，悬空的 Script 身份由后台清理（保留创建后 10 分钟的重试窗口）；清理前重新
创建返回冲突，不会复活旧服务身份、版本和密钥。

| 接口（相对于入口） | 行为 |
| --- | --- |
| `GET /accounts`、`GET /memberships`、`GET /user`、`GET /user/tokens/verify` | 可见组织、当前凭据状态 |
| `GET /accounts/{account}/workers/subdomain` | Wrangler 所需的账户子域元数据 |
| `GET /accounts/{account}/workers/scripts` | 已登记且有权读取的 Script |
| `PUT /accounts/{account}/workers/scripts/{script}` | 上传已打包 ES modules，创建 Version 并部署 |
| `DELETE /accounts/{account}/workers/scripts/{script}` | 删除服务及 Script 名称映射，后台清理关联实体 |
| `GET .../scripts/{script}/settings`、`.../script-settings` | 配置和脱敏绑定 |
| `POST/GET .../scripts/{script}/versions`、`GET .../versions/{id}` | 只上传版本、列出版本、读取版本 |
| `POST/GET .../scripts/{script}/deployments`、`GET .../deployments/{id}` | 单版本 100% 部署及部署记录 |
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
固定部署地址沿用 krun 的 30 天保留期，删除目标环境或服务后失效。

当前支持 `plain_text`、`secret_text`，以及已打包 ESM、CommonJS 依赖、文本、数据、Wasm
和 source map 模块；主模块必须为 ESM。沿用 krun 的模块、变量、Secret、Preview 数量
和请求大小限制。错误采用 Cloudflare envelope 和数字 code，底层仍执行 krun 权限。

这不是完整 Cloudflare 平台实现。资产 upload-session、灰度比例、托管 URL 关闭、
Cloudflare 自定义域名/Zone/Routes、定时任务、tail、KV/R2/D1/DO/Queue/Service bindings、
Service Worker 格式及版本标签/描述不在当前支持范围内。版本上传和 Preview 的 annotations
不会保存；部署接口拒绝非空描述。缺失接口和不支持的绑定会返回错误。静态资产仍可走
krun 原有构建入口。

协议验证使用仓库固定的 Wrangler 4.155.0。复现需先运行独立 kind 后端 E2E 完成初始化，
再让控制台同源入口可从本机访问：

```sh
KRUN_UI_ORIGIN=http://127.0.0.1:4381 node deploy/workers-kind/cloudflare.e2e.mjs
```

脚本在 `.data/` 下生成项目和命令日志，覆盖 deploy、只上传/独立部署、Secret、首次
Secret、Previews Base、Preview 部署/Secret/删除、流量比例拒绝、删除重建身份隔离
以及原生删除后的身份保护。
