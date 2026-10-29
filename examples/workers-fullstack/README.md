# Workers 全栈验证项目

这是一个可以直接作为 krun Workers 源码发布的完整示例：

- `src/` 是 Worker 后端，提供 `GET /api/health` 和 `POST /api/echo`；
- 后端使用真实 Hono npm 依赖与 `node:crypto`，由 package-lock 驱动构建；
- `frontend/public/` 是预构建静态前端，不需要 Node 运行时；
- `wrangler.jsonc` 把 `/api/*` 交给 Worker，其余请求交给静态资产；
- 未命中的浏览器导航使用 `index.html` 做 SPA fallback；
- `about.html` 用来验证 HTML 路径规范化；带 hash 的 CSS、JS、SVG 用来验证 immutable 缓存。
- `/api/health` 只报告 `VERIFICATION_SECRET` 是否已注入，从不回显 Secret 值。
- `/api/asset-check` 从 Hono 业务代码调用 release-scoped `env.ASSETS.fetch()`。

前端和 API 始终同源，不需要 CORS，也不读取运行期后端地址。

## 本地检查

```bash
npm ci --prefix examples/workers-fullstack
npm --prefix examples/workers-fullstack test
npm --prefix examples/workers-fullstack run build
python3 "$STATIC_EXPORT_GUARD/check_static_export.py" examples/workers-fullstack/frontend
```

local-first 环境启动后，可以把示例发布到一个已有、未绑定仓库的测试服务。脚本使用
本地开发登录 Session，不创建或保存 API Key：

```bash
KRUN_VERIFY_ORG=org_xxx \
KRUN_VERIFY_SERVICE=service_xxx \
npm --prefix examples/workers-fullstack run verify:local
```

默认控制面为 `127.0.0.1:8080`、浏览器 origin 为 `127.0.0.1:5173`、Gateway
隧道为 `127.0.0.1:38080`；需要时分别用 `KRUN_CONTROL_ORIGIN`、
`KRUN_BROWSER_ORIGIN`、`KRUN_GATEWAY_ORIGIN` 覆盖。验证会新增 Build 并发布到该服务的
`production`，因此只应指向测试服务。

## 平台验收

`deploy/workers-kind/e2e.mjs` 会读取本目录的真实文件，上传源码包并经过 Kubernetes
builder、MinIO、Release、配置分发、kgateway 和 workerd 发布。验收内容包括：

1. 首页、CSS、JS、SVG 和独立 HTML 都由静态资产 serving；
2. `/api/health` 与 `POST /api/echo` 进入业务 Worker；
3. `/dashboard` 的浏览器导航回落到 `index.html`；
4. ETag/304、带 hash 资产的 immutable cache、HTML 307 规范化；
5. 静态域可匿名读取 `pub/`，但不能匿名读取含业务代码的 `v1/` 归档。
6. Secret 新增和删除各自自动发布新 Release，复用同一个 Artifact，且公开 API 不泄漏值。

完整独立环境运行方式见 [`deploy/workers-kind/README.md`](../../deploy/workers-kind/README.md)。
