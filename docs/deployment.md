# 前后端分离部署

前端是 Vite 静态构建，后端是独立 Go 服务，可以部署在不同机器或容器。浏览器通过一个公开 origin 访问前端：`/console/` 由静态服务器提供，`/auth/` 和 `/api/` 代理到 Go。前端不需要 Node.js 运行时。

Workers 的构建与发布为后端可选能力，见 [接口与流程](workers-backend.md) 和 [kind 部署](../deploy/workers-kind/README.md)。该实验部署仅运行后端，不包含控制台前端。

`static_dir` 只控制 Go 是否托管静态文件：

| 配置 | 后端行为 |
| --- | --- |
| `static_dir: dist` | 合并部署，Go 同时提供 dist/console/ 与 API |
| `static_dir: ""` 或省略字段 | 分离部署，Go 只提供认证、API 与 healthz；不读取或暴露静态文件 |

## 后端

从 [统一配置示例](../config/example.yaml) 复制自己的配置，填写数据库与需要的登录来源。分离部署时调整：

```yaml
listen: 0.0.0.0:8080
public_url: https://console.example.com
development: false
static_dir: ""
```

`public_url` 填浏览器使用的前端 origin。OAuth 回调和 CLI 浏览器确认链接会使用这个地址；不填后端内网地址。后端不根据 Host / X-Forwarded-* 推导公开地址。

```sh
go build -o bin/krun-server ./cmd/krun-server
./bin/krun-server serve --config config/production.yaml
```

后端机器只需要 Go 二进制、YAML 配置和 PostgreSQL 连接，不需要复制 dist。监控直接访问后端 `/healthz`。

## 前端

```sh
npm ci
npm run build
```

把生成的整个 dist 目录复制到前端静态服务器，例如 `/srv/krun/dist`。构建产物为 dist/console/index.html 和 dist/console/assets/，访问路径保持 `/console/`。

以下 Nginx 配置假设前端入口的 HTTPS 已由 Ingress / 负载均衡终止，`krun-backend:8080` 可从 Nginx 内网访问；替换成实际后端地址。如由 Nginx 自己终止 HTTPS，应使用其正常的 TLS server 配置。

```nginx
server {
    listen 80;
    server_name console.example.com;
    root /srv/krun/dist;

    location = / {
        return 302 /console/;
    }
    location = /console {
        return 302 /console/;
    }
    location /console/assets/ {
        try_files $uri =404;
    }
    location /console/ {
        try_files $uri $uri/ /console/index.html;
        add_header Cache-Control "no-cache";
    }
    location ~ ^/(auth|api)/ {
        proxy_pass http://krun-backend:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Connection "";
        proxy_cache off;
    }
    location / {
        return 404;
    }
}
```

`proxy_pass` 不带路径，保留原始 `/auth/...` 与 `/api/...`；不要去掉前缀，也不要把认证回调落到静态首页。保留客户端的 Origin、Cookie、Authorization、X-CSRF-Token 与响应 Set-Cookie，认证和 API 请求不缓存。相关指令见 [Nginx proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass) 与 [try_files](https://nginx.org/en/docs/http/ngx_http_core_module.html#try_files)。

前端代码使用同源相对地址，不需要为后端注入域名。生产 Session Cookie 使用 Secure / HttpOnly / SameSite=Lax，因此浏览器入口必须 HTTPS。SSO 应用的 Redirect URI 填 `https://console.example.com/auth/<provider-id>/callback`；CLI 也连接公开地址 `krun login --server https://console.example.com`。

前端可放对象存储 / CDN，但公开入口仍需将 `/auth/` 与 `/api/` 路由到后端。只有静态文件、没有 API 代理的托管无法完成登录和管理操作。当前不提供浏览器跨 origin API 模式；独立服务器 / 容器部署通过上述同域代理实现。

本地 Vite 已代理 /auth 与 /api 到 8080，可把开发配置的 static_dir 留空，并以 `http://127.0.0.1:5173` 为 public_url 验证分离模式。
