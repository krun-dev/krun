# 部署

生产控制台是纯静态站点：`npm run build` 只生成 `dist/console/index.html` 和
`dist/console/assets/`。把 **`dist/console/` 的内容** 上传到 OSS bucket 根目录；生产
环境不运行 Node，也不要把仓库、`dist/` 的其他内容或配置文件一起上传。

浏览器只访问一个公开 origin。这个 origin 前的 CDN / 反向代理必须实现且只实现：

| 外部路径 | 上游 |
| --- | --- |
| `/api/*` | Go 控制面，保留原始路径 |
| `/`、`/assets/*` | OSS 静态 bucket |
| 其他路径 | 404 |

页面状态保存在查询参数中，因此不需要把未知路径重写到 `index.html`。OSS 的 default
document 设为 `index.html` 即可。只有 bucket 域名、没有同域 `/api/*` 转发能力的
静态托管不能运行本控制台。

## 后端配置

```yaml
listen: 0.0.0.0:8080
public_url: https://console.example.com
development: false
static_dir: ""
trusted_proxy_cidrs:
  - 10.20.0.0/16 # 只填写真实 CDN / 反向代理的源网段
db: ${KRUN_DATABASE_URL}
```

`public_url` 必须是浏览器实际使用的 HTTPS origin。OAuth 回调地址是
`https://console.example.com/api/auth/<provider-id>/callback`，CLI 使用
`krun login --server https://console.example.com`。后端不会从 Host 或
`X-Forwarded-*` 猜测公开地址。

`trusted_proxy_cidrs` 只决定哪些直接上游有权提供 `X-Forwarded-For`；未列出的来源
即使伪造该头也不会改变认证限流身份。生产应在防火墙层同时限制后端只能由这些上游
访问。

浏览器认证依赖同源 Cookie（Secure、HttpOnly、SameSite=Lax），后端不提供 CORS。
不要把前端改成请求另一个域名，也不要在前端注入后端地址或凭据。

## 路由示例

仓库中的 [`deploy/production/nginx.conf.example`](../deploy/production/nginx.conf.example)
是同一契约的 Nginx 模板；替换证书路径、域名、上游和静态根目录后再执行 `nginx -t`。
使用云 CDN 时配置等价的两个 origin behavior：优先匹配
`/api/*` 到 Go，其余请求到 OSS；OSS 对不存在的对象返回 404。

代理必须透传 Host、Cookie、Authorization、Origin、X-CSRF-Token 和响应
Set-Cookie；`/api/*` 不得缓存。每一层代理都应把自己的直接下游地址追加到
`X-Forwarded-For`；最外层 CDN 应清洗公网传入的同名头。Go 会从右向左剥离
`trusted_proxy_cidrs`，并采用遇到的第一个不可信地址作为客户端地址。

`index.html` 必须 `no-store`（或至少每次重新验证），带内容哈希的 `/assets/*` 才能
长期 immutable 缓存。静态入口还应下发 CSP、`nosniff`、点击劫持和 Referrer-Policy
等浏览器安全头；Nginx 模板给出了与当前纯静态前端兼容的基线。

健康检查访问 `/api/healthz`。它执行轻量数据库连通性检查，不加载完整 IAM 或
Workers 状态。启用 `workers:` 时必须配置 `internal_listen`，并通过内部 Service 与
NetworkPolicy 仅暴露给 runtime；该端口承载配置流、制品下载和 ACK，不得接入公网
网关。任一监听器绑定失败都会让进程退出，不会留下半健康实例。

## 本地

日常开发遵循 local-first 拓扑，详见 [local-dev.md](local-dev.md)。Vite 只在本机把
`/api/*` 代理到 Go。也可以执行 `npm run build` 后让 Go 用 `static_dir: dist` 直接
托管 `dist/console/`，访问 `http://127.0.0.1:8080/`。

## 发布门禁

```bash
npm ci
npm run build
make check-secrets check-static-build
```

只有输出包含 `publish_allowed=true` 且 `dist/console/` 中仅有静态产物时才上传。
CI 还会运行 Go race 测试以及 builder/runtime 测试。
