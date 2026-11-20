---
title: 应用域名与 Worker 自定义域名
description: 在 Worker 上绑定整域，或通过应用域名把路径分给多个 Worker、静态站点和 HTTP 服务。
---

平台可以为同一个后缀开通多种用途，并负责通配 DNS 和 TLS。申请时可选择已开放的后缀并填写一层前缀。
例如，Worker 的默认地址和 `orders.workers.example.com` 可以共用 `workers.example.com`。
每个完整主机名只对应一种用途，不能与其他 Worker 的生产或预览地址冲突。共享 Worker 默认后缀的托管域名前缀在所有 Worker 默认后缀之间统一保留。
Worker 名称超过 40 个字符时无法生成固定部署地址，超过 54 个字符时无法生成 Version URL；Preview 名称与 Worker 名称拼接后最多 63 个字符。

| 后缀类型 | 示例 | 在哪里配置 |
| --- | --- | --- |
| `worker_default` | `orders.workers.example.com` | Worker → 路由与域名 → 默认域名后缀。生产地址使用 Worker 名称，Preview 地址使用 `<preview>-<worker>` |
| `worker_custom` | `orders.workers.example.com` | Worker → 路由与域名 → Worker 自定义域名。整域绑定当前 Worker，默认提供生产和 Preview |
| `application` | `shop.apps.example.com` | 组织 → 网络 → 应用域名。组织配置同域的路径和目标 |

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

在组织侧栏打开 **网络 → 应用域名** ①。域名列表中的 **编辑路由** ② 打开完整路由表。

[![① 网络分组下的应用域名入口，② 编辑路由](/docs/screenshots/workers-hostnames/network-domain-list.png)](/docs/screenshots/workers-hostnames/network-domain-list.png)

点击 **申请域名**，在弹窗填写前缀 ①，选择后缀 ②，再点击 **申请域名** ③。

[![① 填写前缀，② 选择后缀，③ 申请域名](/docs/screenshots/workers-hostnames/claim-domain-prefix-first.png)](/docs/screenshots/workers-hostnames/claim-domain-prefix-first.png)

打开已申请域名的 **编辑路由**，添加规则，填写匹配方式和请求路径 ①，再选择目标 ②，最后点击 **保存** ③。
图中的四条规则把 `/` 给静态站点、`/api` 和 `/api/admin` 给不同 Worker、`/legacy` 给 HTTP 服务。

[![① 填写匹配方式和路径，② 选择目标，③ 保存](/docs/screenshots/workers-hostnames/edit-domain-routes.png)](/docs/screenshots/workers-hostnames/edit-domain-routes.png)

要停止特定路径，可取消勾选 **启用路由**。要移除整个域名的流量，在 **编辑路由** 中选择
**退役域名** 并确认。域名继续保留，不能重新申请。

打开目标 Worker 的 **路由与域名 → 应用域名**。为现有应用域名新增路径时，点击 **添加应用路由** ①。
修改已有绑定时，在对应域名上点击 **编辑路由** ②。这个入口编辑指向当前 Worker 的规则。

[![① 添加应用路由，② 编辑当前 Worker 的路由](/docs/screenshots/workers-hostnames/worker-application-routes.png)](/docs/screenshots/workers-hostnames/worker-application-routes.png)

在添加弹窗中，选择应用域名 ①，填写匹配方式和请求路径 ②，再点击 **保存** ③。保存后可回到应用域名页查看完整路由表。

[![① 选择应用域名，② 填写匹配方式和请求路径，③ 保存](/docs/screenshots/workers-hostnames/worker-add-application-route.png)](/docs/screenshots/workers-hostnames/worker-add-application-route.png)

在同一 Worker 页面，点击 **绑定自定义域名** ① 为 Worker 绑定整域。在弹窗中填写前缀、选择后缀 ②，然后点击 **绑定域名**。
绑定后默认提供生产和 Preview 地址 ③，命名 Preview `pr-123` 会获得 `pr-123.orders.workers.example.com`。
每次 Preview 部署也会显示一个固定的自定义地址。使用前让平台管理员为 `*.orders.workers.example.com` 配置 DNS 和证书。

[![① 绑定自定义域名入口，② 选择后缀，③ 绑定后自动提供生产和 Preview 地址](/docs/screenshots/workers-hostnames/worker-custom-domain.png)](/docs/screenshots/workers-hostnames/worker-custom-domain.png)

绑定后可在同一区域解绑。要长期验收整站，可为独立 staging Worker 和测试站点配置一套显式路由，例如 `staging.apps.example.com/api/*`。

## krun CLI

需要安装 krun CLI 和 curl。交互使用 `krun login`；CI 设置 `KRUN_ENDPOINT` 与
`KRUN_TOKEN`。组织管理员可以操作，API Key 还需要在组织资源上允许
`worker.hostname.read`、`worker.hostname.manage`，在目标 Worker 的 production
环境上允许 `worker.read` 和 `worker.update`。两个入口修改 Worker 路由时都使用这套权限；改绑时需要修改原目标与新目标的权限。按 [API Key 创建指南](/docs/guides/krun-cli.html) 授权，
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

要绑定整域，先把 `ORG_ID` 设为组织 ID，`WORKER` 设为 Worker 短名或 ID。从 `list`
的 `suffixes` 读取 Worker 自定义后缀 ID，设为 `CUSTOM_SUFFIX_ID`。绑定成功后，可从
`items` 中读取域名和 revision，设为 `CUSTOM_HOST`、`CUSTOM_REVISION` 后解绑。

```sh
krun workers custom-domains list --org "$ORG_ID" --worker "$WORKER"
krun workers custom-domains attach --org "$ORG_ID" --worker "$WORKER" --suffix "$CUSTOM_SUFFIX_ID" --label orders
krun workers custom-domains detach --org "$ORG_ID" --worker "$WORKER" --host "$CUSTOM_HOST" --revision "$CUSTOM_REVISION"
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

要从 Worker 的角度编辑，用 `krun domains list --worker` 查该 Worker 的绑定和可选域名
`application_hosts`。把 `HOST` 和 `REVISION` 设为所选域名及其最新 revision。
路由文件列出这个域名下指向当前 Worker 的全部规则，目标由 `--worker` 指定。

```sh
krun domains list --org "$ORG_ID" --worker "$API_WORKER"
cat > worker-hostname-routes.json <<'EOF_WORKER_ROUTES'
[
  {"match":"prefix","path":"/api","enabled":true},
  {"match":"exact","path":"/api/health","enabled":true}
]
EOF_WORKER_ROUTES
krun domains set --org "$ORG_ID" --worker "$API_WORKER" --host "$HOST" --revision "$REVISION" --routes worker-hostname-routes.json
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
与 [Custom Domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/) 的字段。`custom_domain: true` 用于整域绑定，使用启用 `worker_custom` 的后缀。普通 `routes`
用于应用域名路径，匹配 `application` 后缀。同一个 Worker 可同时声明两种绑定。
在已有 `wrangler.jsonc` 中加入这些字段，并将示例后缀换成平台开放的后缀。

```json
{
  "routes": [
    {"pattern": "orders.workers.example.com", "custom_domain": true},
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
再放到 API Worker 的 Wrangler 配置中。只需更新路由时，
也可直接调用 Zone 的 `/workers/routes` API。Wrangler 与两个控制台入口使用同一张应用域名路由表。
`triggers deploy` 更新该 Worker 通过 Wrangler 发布的路由集合，同时保留控制台独立添加的规则。

- Zone 对应平台开放的后缀。兼容精确主机名及精确路径、末尾 `/*`；`/api/*` 按路径段
  匹配，也包含 `/api`。精确规则只比较路径。通配主机名、`/api*`、任意外部 Zone 和自带域名不在范围内。
  应用域名内，同一匹配方式和路径只能配置一个目标。两类后缀各自使用对应的 API。
  普通 Routes 始终匹配配置的 Worker。Custom Domain 默认打开 Preview，Wrangler 可显式设置
  `previews_enabled: false` 关闭，或设置 `enabled: false` 只开放 Preview。
- 静态站点与 HTTP 服务使用 [Origin Rules](https://developers.cloudflare.com/rules/origin-rules/create-api/)
  的 `http_request_origin` 入口 API，也可在控制台或 `krun domains` 中编辑。表达式仅支持
  精确 Host 加精确路径或路径段前缀；按路径优先级选一个目标。停用的规则返回 410。
  不执行通用 Ruleset，也不会把同一次请求自动串联转发给多个目标。
  非 Worker 目标的域名最多 16 条路由；无根前缀时最多 15 条。
- `workers_dev` 控制默认生产地址。显式设置 `preview_urls` 控制平台后缀上的命名 Preview、固定部署地址和 Version URL。
  自定义域名上的 Preview 独立使用该域名的 `previews_enabled`。
- 在平台后缀的 Preview 地址已开启时，`npx wrangler@4.155.0 versions upload --config wrangler.jsonc`
  会在校验完成后返回 Version URL，可直接请求该版本，生产流量继续使用当前部署。平台保留该地址 30 天。

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
| HTTP 409 `HOSTNAME_ROUTE_CONFLICT` | 同一域名已有相同匹配方式和路径。在应用域名页确认占用规则，选择不同路径或修改原规则。路径段前缀会去掉尾斜杠。 |
| HTTP 409 `REVISION_CONFLICT` | 刷新列表，按最新 revision 重新编辑。 |
| HTTP 422 `HOSTNAME_INVALID`、`HOSTNAME_ROUTE_INVALID` 或 `HOSTNAME_TARGET_INVALID` | 检查是否使用已开放后缀、一层短名、合法路径，以及同组织且符合网络要求的目标。 |
| 请求返回 404 `unknown route`、410 `environment disabled` 或 503 `environment not published` | 分别检查域名是否已同步及配置匹配路由、规则是否启用、目标是否已发布。 |
