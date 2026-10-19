# AGENTS.md

本仓库 = **krun 控制台**（React + Vite 静态 SPA，`web/`）+ **Go 控制面**（`krun-server`，`internal/`）。

## 0. 两条硬规则

**规则 1 — 生产前端是静态导出，上传 OSS，运行期没有 Node。**
本地调试可以用 Nginx 或临时 Node 代理，但功能正确性不能依赖它们。

**规则 2 — 对外路由只有两个出口：`/` 给 OSS，`/api/*` 给 Go。**
所有后端接口统一挂在 `/api/` 下；不再有 `/auth`、`/healthz` 这种与 `/api` 平级的前缀。

对外路由契约：

| 路径 | 去向 | 说明 |
| --- | --- | --- |
| `/`、`/assets/*` | OSS | 静态导出产物，SPA 入口 |
| `/api/*` | Go | 全部接口：`/api/v1/*`、`/api/auth/*`、`/api/healthz`、`/api/internal/*` |
| 其他 | 404 | 没有第三个出口 |

网关只需一条 catch-all：

```
/api/*   ->  Go server
/*       ->  OSS 静态站点（default document: index.html）
```

这条契约同时覆盖生产、本地和验证环境。**不要再新增根级前缀。**

## 1. 接口一律收在 /api 下

后端只有一张路由表（`internal/control/server.go` 的 `routes()` + `apiRoutes` + `workersRoutes`）。规范前缀：

| 现在（禁止新增） | 目标 |
| --- | --- |
| `/auth/providers`、`/auth/csrf`、`/auth/{provider}/login`、`/auth/{provider}/callback`、`/auth/me`、`/auth/logout`、`/auth/cli/*` | `/api/auth/...` |
| `/healthz` | `/api/healthz` |
| `/internal/workers/*` | `/api/internal/workers/*` |
| `/api/v1/*` | 不变（已经是 `/api` 下） |

理由：网关不用再枚举 `location ~ ^/(auth|api)/`，也不用给 healthz 单独开规则；新增接口天然落在 `/api` 下，不需要动网关。

`/api/` 下的未知路径统一返回 `RESOURCE_NOT_FOUND`，与既有 `/api/` 兜底一致。

## 2. ⚠️ 生产：OSS 静态 + 一条 `/api/` 转发

**光把文件传上 OSS 不够——OSS 只服务静态文件，不会把 `/api/` 转发到后端。** 而当前前后端都要求同源：

- 后端**不返回任何 CORS 头**（全仓库零实现）。
- Session Cookie 是 `HttpOnly + SameSite=Lax`（生产 `Secure`，见 `internal/control/server.go` 的 `setCookie`）；跨 origin 的 `fetch` 带不上凭证。
- 前端请求全是同源相对路径（见 `web/src/lib/api.ts`）。

所以 OSS 前面**必须**有同源的 CDN / 反向代理按第 0 节的契约做路径分发：

- 三个部分对浏览器必须是**同一个 origin**（同域名、同 scheme、同端口）。
- `public_url` 配成这个对外 origin（如 `https://console.example.com`）；SSO 回调、CLI 都统一用它。
- 如果托管平台不支持同域路径转发（纯静态 Pages、无代理的 bucket 域名直连），这套控制台**跑不起来，不能发布**。
- 不要用「前端发到另一个域名」来绕：那需要后端加 CORS + `SameSite=None`，当前明确不支持。

## 3. 前端：base 就是 `/`

- 构建产物 `npm run build` → `dist/console/`，Vite `base: "/"`、`root: web`、`outDir: ../dist/console`。产物上传 OSS 根路径。
- 所有请求用**同源相对路径**：`/api/auth/*`、`/api/v1/*`。永远不要拼 `http://host:port`。
- 页面内导航保持**查询参数**（如 `/?page=workers&workspace=...`），不需要 SPA 路径回退。
- 不得在运行期注入后端域名 / 端口 / 凭据；不得读非公开环境变量决定行为。
- 前端**不得**写入任何需要服务端运行时的东西：SSR、Server Actions、API Routes、middleware、SSR-only 动态路由、服务端数据预取。
- 前端**不得** import 服务端模块：`fs` / `net` / `tls` / `http` / `https` / `child_process` / 数据库或 Redis 客户端 / 服务端 SDK。
- 不引入 Service Worker / 前端侧 serverless 运行时；不做 BFF、不做 CORS 代理、不做运行时代理注入。

## 4. 环境分工

| 环境 | 前端怎么跑 | 允许的做法 |
| --- | --- | --- |
| 本地开发 | 静态产物 + 本地代理 | ✅ Nginx ✅ 临时 Node 代理（Vite `server.proxy` 等） |
| 生产 | 静态导出上传 OSS | ❌ 任何 Node / 前端代理进程；✅ OSS + CDN / 反向代理 |

本地用 Nginx 或临时 Node 代理**没问题**，是工具不是架构。底线只有一条：**生产不能依赖它们**。写功能时如果发现「只有开了本地代理才行」，要么改成同源直连，要么让部署侧的 CDN / 反向代理补上。

本地推荐：

1. `npm run build`（改前端时可 `npx vite build --watch`）产出 `dist/console/`。
2. 让 `/api/*` 到 Go，三选一：本地 Nginx、Vite `server.proxy`、或让 Go 直接托管（`static_dir: dist`，访问 `http://127.0.0.1:8080/`）。
3. 远程 kind：`kubectl ... port-forward deployment/backend 4380:9000`，再按同样契约把浏览器请求分发出去。

本地这些进程只在开发机上存在，不进生产、不进发布产物。

## 5. 提交前检查清单

- [ ] `npm run build` 成功，产出 `dist/console/index.html` 与 `dist/console/assets/`。
- [ ] 新增接口都注册在 `/api/` 下，没有新增根级前缀。
- [ ] 全部网络请求都是同源相对路径。
- [ ] 没有新增对本地代理的**运行期**依赖。
- [ ] 发布前确认部署目标支持「OSS 静态 + 同域转发 `/api/*` 到 Go」；不支持就**不要发布**。
- [ ] 验证功能时打**同源入口**，不要只 curl Vite / 本地代理端口。

静态化发布前用发布拦截脚本自查：

    python3 $STATIC_EXPORT_GUARD/check_static_export.py .  # 本机工具路径，按需调整

（脚本会对 `scripts/`、`deploy/` 下的 Node 工具报 blocker；那些是开发工具，不是发布产物。真正要保证 `publish_allowed` 为 `true` 的是 `web/` 的构建产物。）

## 6. 迁移清单（/auth 收进 /api，base 改为 /）

后端（`internal/control/server.go`）：

- `/auth/*` → `/api/auth/*`；`/healthz` → `/api/healthz`；`/internal/workers/*` → `/api/internal/workers/*`。
- 根级 `/` 兜底与 `/api/` 兜底合并；删除 `/auth/` 前缀兜底。
- `internal/control/provider.go`：dev 分支 `begin()` 的 `/auth/<id>/callback`、OAuth `RedirectURL`、`loginReturnPath` 校验前缀、登录失败重定向、CLI `verification_uri` 里的 `/console/`。
- `internal/cli/cli.go`：`/auth/cli/start`、`/auth/cli/poll`、`/auth/logout`。
- 测试：`control_test.go` / `email_test.go` / `console_test.go` / `workers_test.go` / `cli_test.go` 里的路径与 `/console/` 断言；`deploy/workers-kind/e2e.mjs` 的 `/auth/local/login`、`/healthz`。
- 部署清单与 `control-config`：探针路径、`public_url`、`static_dir`。

前端（`web/`）：

- `vite.config.ts`：`base` 改 `"/"`；`server.proxy` 改为代理 `/api`。
- 所有 `/auth/...` → `/api/auth/...`（`lib/api.ts`、`components/login.tsx`、`components/email-login.tsx`、`App.tsx` 的 `/auth/me`、`/auth/logout`、`/auth/cli/approve`）。
- `/console/` → `/`（`login.tsx`、`email-login.tsx` 的 `return_to`、`App.tsx` 的 `replaceState` / `location.assign`）。

> OAuth callback 定为 `https://<origin>/api/auth/<provider-id>/callback`（GitHub / GitLab / 阿里云 IDaaS 在各自应用里填这个）。项目尚未上线，没有存量地址需要兼容，可以自由选择路径；等上线后再改这个地址才需要协调上游应用。

## 7. 当前状态（已核实）

- 后端路由仍在根级：`/auth/*`、`/healthz`、`/internal/workers/*`。第 1 节的迁移**尚未实施**。
- 前端 `base` 仍是 `/console/`。
- `scripts/kind-console.mjs` 把 Vite 当浏览器入口、经 `server.proxy` 转发到 SSH 隧道再到后端；**本地调试可接受**，不代表生产形态。
- 远端 `workers-mvp/control-config` 的 `control.yaml` 没有 `static_dir`，backend 镜像里只有 `krun-server`、没有 `dist/`；`GET <backend>/` 目前 404，kind 里的控制台完全靠 Vite。
- 生产 OSS + CDN 的路径转发规则尚未落地；`docs/deployment.md` 目前只有 Nginx 示例，待补 OSS / CDN 方案。
