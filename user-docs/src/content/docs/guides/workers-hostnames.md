---
title: 自定义子域名与路径路由
description: 从平台提供的后缀申请域名，把同一域名的路径分给 Worker、静态站点或 HTTP 服务。
---

平台开放 `apps.example.com` 时，填写 `shop` 即可申请 `shop.apps.example.com`。
平台维护通配 DNS 和 TLS，组织持有完整域名。短名只允许一层 ASCII DNS 标签，大小写等价；
`w-` 开头的名字、平台保留名和已占用的名字不可申请。

同一域名可以把 `/` 给静态站点、`/api` 给 Worker、`/legacy` 给 HTTP 服务，
也可以把不同路径分别给多个 Worker。Worker 必须属于同一组织，使用后缀允许的网络，
并处于同一运行池；路由绑定 production，发布、灰度和回滚沿用现有规则。静态站点和 HTTP
服务由平台登记，只有授权给当前组织和后缀的服务才可选择。
同一域名下的应用共享浏览器 origin，应按同一应用边界管理。

| 请求 | 命中 |
| --- | --- |
| `/api`、`/api/`、`/api/orders?page=2` | 路径段前缀 `/api` |
| `/api/admin/users` | 更长的路径段前缀 `/api/admin` |
| `/api/health?full=1` | 若设置了精确路径 `/api/health`，精确规则优先 |
| `/apix` | 路径段前缀 `/` |

路径区分大小写，查询参数保留并转发，匹配过程不改写请求 URL。命中目标后，
其响应直接返回；API Worker 返回 404 时，Web Worker 不会再处理同一请求。
停用规则仍匹配并返回 HTTP 410。移除规则后，路径可能匹配更宽的规则。

## 控制台

1. 在组织侧栏打开 **域名** ①，点击 **申请域名**，选择后缀 ③，填入前缀 ④，再点击 **申请域名** ⑤。
2. 找到已申请的域名，点击 **编辑路由** ②。添加路由后，填写匹配方式和请求路径 ⑥，再选择目标 ⑦。截图中的
   三条规则把 `/` 给静态站点、`/api` 给 Worker、`/legacy` 给 HTTP 服务。点击 **保存** ⑧。
   目标 Worker 的 **路由与域名** 页仍可查看绑定。
3. 要停止特定路径，可取消勾选 **启用路由**。要移除整个域名的流量，在 **编辑路由** 中选择
   **退役域名** 并确认；域名继续保留，不能重新申请。

[![① 组织域名入口，② 编辑路由](/docs/screenshots/workers-hostnames/catalog.png)](/docs/screenshots/workers-hostnames/catalog.png)

[![③ 选择后缀，④ 填写前缀，⑤ 申请域名](/docs/screenshots/workers-hostnames/claim.png)](/docs/screenshots/workers-hostnames/claim.png)

[![⑥ 填写路径，⑦ 选择目标，⑧ 保存路由](/docs/screenshots/workers-hostnames/routes.png)](/docs/screenshots/workers-hostnames/routes.png)

## krun CLI

需要安装 krun CLI 和 curl。交互使用 `krun login`；CI 设置 `KRUN_ENDPOINT` 与
`KRUN_TOKEN`。组织管理员可以操作，API Key 还需要在组织资源上允许
`worker.hostname.read`、`worker.hostname.manage`，在目标 Worker 的 production
环境上允许 `worker.read` 和 `worker.update`。按 [API Key 创建指南](/docs/guides/krun-cli.html) 授权，
将以下策略中的 `ORG_ID`、`WEB_ID`、`API_ID` 替换为真实 ID：

```yaml
schema_version: 1
statements:
  - effect: allow
    actions: [worker.hostname.read, worker.hostname.manage]
    resource: {organization_id: ORG_ID, kind: organization, ids: [ORG_ID]}
    conditions: {}
  - effect: allow
    actions: [worker.read, worker.update]
    resource: {organization_id: ORG_ID, kind: worker, ids: [WEB_ID, API_ID]}
    conditions: {environments: [production]}
```

先把 `ORG_ID` 设为组织 ID，执行 `list`，从 `suffixes` 中取得后缀 ID。
把 `SUFFIX_ID`、`WEB_WORKER`、`API_WORKER` 设为后缀 ID 和两个目标 Worker 的短名或 ID。

```sh
krun domains list --org "$ORG_ID"
cat > hostname-routes.json <<EOF_ROUTES
[
  {"match":"prefix","path":"/","worker_id":"$WEB_WORKER","enabled":true},
  {"match":"prefix","path":"/api","worker_id":"$API_WORKER","enabled":true}
]
EOF_ROUTES
krun domains claim --org "$ORG_ID" --suffix "$SUFFIX_ID" --label shop --routes hostname-routes.json
krun domains list --org "$ORG_ID" --worker "$API_WORKER"
```

申请成功会返回 `host` 和 `revision`。后续从列表读取最新值并设为 `HOST` 和 `REVISION`。
`set` 提交完整路径表；改完文件后执行：

```sh
krun domains set --org "$ORG_ID" --host "$HOST" --revision "$REVISION" --routes hostname-routes.json
```

要撤除全部流量并保留域名占用，从列表重新读取 revision 后执行：

```sh
krun domains retire --org "$ORG_ID" --host "$HOST" --revision "$REVISION"
```

`krun domains list` 中的 `origins` 是可用的静态站点和 HTTP 服务列表。在路由文件
中，把对应规则的 `worker_id` 换成 `"origin":"<origins 中的 host>"`，然后执行同一个 `set`。
例如下列配置保留 Worker 的 API 路由，并把根路径指向静态站点。把 `STATIC_ORIGIN`
设成列表里的静态站点 `host`，把 `REVISION` 更新到当前值：

```sh
cat > hostname-routes.json <<EOF_ROUTES
[
  {"match":"prefix","path":"/","origin":"$STATIC_ORIGIN","enabled":true},
  {"match":"prefix","path":"/api","worker_id":"$API_WORKER","enabled":true}
]
EOF_ROUTES
krun domains set --org "$ORG_ID" --host "$HOST" --revision "$REVISION" --routes hostname-routes.json
```

## Cloudflare API 与 Wrangler

沿用 [Cloudflare Workers Routes](https://developers.cloudflare.com/api/resources/workers/subresources/routes/)
与 [Custom Domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/) 的
字段：`routes` 配置路径，`custom_domain: true` 把整站入口指给一个生产 Worker，
同一域名的更具体 Routes 仍可匹配其他 Worker。以已部署 Worker 为例，在原有
`wrangler.jsonc` 中加上这些字段，示例的主机名和 Zone 换成平台开放的后缀：

```json
{
  "routes": [
    {"pattern": "shop.apps.example.com", "custom_domain": true},
    {"pattern": "shop.apps.example.com/api/*", "zone_name": "apps.example.com"}
  ]
}
```

使用已经安装的 Wrangler（验证版本 4.155.0）。设 `KRUN_ENDPOINT` 为平台控制台地址
（末尾不加斜杠），`KRUN_TOKEN` 为上述策略签发的 API Key；只有发布路由时使用
`triggers deploy`，同时发布代码时用 `deploy`，代码发布另需 [Wrangler 权限](/docs/guides/wrangler-configuration.html)。

```sh
export CLOUDFLARE_API_BASE_URL="$KRUN_ENDPOINT/api/v1/cloudflare/client/v4"
export CLOUDFLARE_API_TOKEN="$KRUN_TOKEN"
export CLOUDFLARE_ACCOUNT_ID="$ORG_ID"
npx wrangler@4.155.0 triggers deploy --config wrangler.jsonc
```

把 `/api/*` 指给另一 Worker 时，先从现有 Worker 的路由中撤下这条规则，
再放到 API Worker 的 Wrangler 配置中；根 Custom Domain 可以保留。只需更新路由时，
也可直接调用 Zone 的 `/workers/routes` API。

- Zone 对应平台开放的后缀。兼容精确主机名及精确路径、末尾 `/*`；`/api/*` 按路径段
  匹配，也包含 `/api`。精确规则只比较路径。通配主机名、`/api*`、任意外部 Zone 和自带域名不在范围内。
  同一匹配方式和路径只能配置一个目标；不能把同一个 `/*` 同时配置为 Workers Route 和 Custom Domain。
  Custom Domain 不为命名 Preview 自动生成更深一层的子域。
- 静态站点与 HTTP 服务使用 [Origin Rules](https://developers.cloudflare.com/rules/origin-rules/create-api/)
  的 `http_request_origin` 入口 API，也可在控制台或 `krun domains` 中编辑。表达式仅支持
  精确 Host 加精确路径或路径段前缀；按路径优先级选一个目标。停用的规则返回 410。
  不执行通用 Ruleset，也不会把同一次请求自动串联转发给多个目标。
  非 Worker 目标的域名最多 16 条路由；无根前缀时最多 15 条。
- `workers_dev` 控制默认生产地址，`preview_urls` 控制部署固定预览地址。
  这两个字段不改变 Custom Domain、Workers Route 或显式创建的命名 Preview。

## 验证

先让各目标提供可区分的响应，再请求同一个 HTTPS Host：

```sh
curl -isS "https://$HOST/"
curl -isS "https://$HOST/api/orders?limit=1"
curl -isS "https://$HOST/apix"
```

预期根路径与 `/apix` 进入根路径目标，`/api/orders` 进入 API Worker。可对照各响应的
`X-Krun-Version` 和业务内容。域名配置异步同步；保存成功表示配置已写入。

| 现象 | 原因与处理 |
| --- | --- |
| HTTP 403 `PERMISSION_DENIED` | 检查组织域名权限以及目标 production 的 `worker.update`。 |
| HTTP 409 `HOSTNAME_UNAVAILABLE` | 名称被占用或保留；换一个短名。 |
| HTTP 409 `HOSTNAME_ROUTE_CONFLICT` | 删除重复匹配方式和路径，注意前缀会去掉尾斜杠。 |
| HTTP 409 `REVISION_CONFLICT` | 刷新列表，按最新 revision 重新编辑。 |
| HTTP 422 `HOSTNAME_INVALID`、`HOSTNAME_ROUTE_INVALID` 或 `HOSTNAME_TARGET_INVALID` | 检查是否使用已开放后缀、一层短名、合法路径，以及同组织且符合网络要求的目标。 |
| 请求返回 404 `unknown route`、410 `environment disabled` 或 503 `environment not published` | 分别检查域名是否已同步及配置匹配路由、规则是否启用、目标是否已发布。 |
