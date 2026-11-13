---
title: 自定义子域名与路径路由
description: 从平台提供的后缀申请域名，把同一域名的路径分给多个 Worker。
---

平台开放 `apps.example.com` 时，填写 `shop` 即可申请 `shop.apps.example.com`。
平台维护通配 DNS 和 TLS，组织持有完整域名。短名只允许一层 ASCII DNS 标签，大小写等价；
`w-` 开头的名字、平台保留名和已占用的名字不可申请。

同一域名可以把 `/` 给 Web Worker、`/api` 给 API Worker、`/api/admin` 给 Admin Worker。
所有目标必须属于该组织，使用后缀允许的网络，且在同一运行池。每条路由指向目标的
production 环境；发布、回滚和普通灰度沿用现有规则。定向指定灰度版本时使用 Worker 的默认地址。
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

1. 在 **Workers → 域名与路由** 选择 **申请域名**，选择后缀并填入前缀。
2. 点击 **添加路由**，用 `/` 和 `/api` 配两条「路径段前缀」。目标填写 Worker 短名或 ID。点击 **保存**。
3. 保存后可通过 **编辑路由** 更改路径表。在任一目标 Worker 的 **路由与域名** 中可看到该 Worker 的绑定。
4. 退役域名前先打开 **编辑路由**，选择 **退役域名** 并确认。路由撤除后，域名仍保留，不能重新申请。

[![① 域名与路由入口，② 编辑共享域名](/docs/screenshots/workers-hostnames/catalog.png)](/docs/screenshots/workers-hostnames/catalog.png)

[![③ 选择后缀与前缀，④ 配置目标 Worker，⑤ 保存](/docs/screenshots/workers-hostnames/claim.png)](/docs/screenshots/workers-hostnames/claim.png)

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
krun workers hostnames list --org "$ORG_ID"
cat > hostname-routes.json <<EOF_ROUTES
[
  {"match":"prefix","path":"/","worker_id":"$WEB_WORKER","enabled":true},
  {"match":"prefix","path":"/api","worker_id":"$API_WORKER","enabled":true}
]
EOF_ROUTES
krun workers hostnames claim --org "$ORG_ID" --suffix "$SUFFIX_ID" --label shop --routes hostname-routes.json
krun workers hostnames list --org "$ORG_ID" --worker "$API_WORKER"
```

申请成功会返回 `host` 和 `revision`。后续从列表读取最新值并设为 `HOST` 和 `REVISION`。
`set` 提交完整路径表；改完文件后执行：

```sh
krun workers hostnames set --org "$ORG_ID" --host "$HOST" --revision "$REVISION" --routes hostname-routes.json
```

要撤除全部流量并保留域名占用，从列表重新读取 revision 后执行：

```sh
krun workers hostnames retire --org "$ORG_ID" --host "$HOST" --revision "$REVISION"
```

## 从 Cloudflare 迁移

[Cloudflare Custom Domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/)
把整个 Host 指给一个源站 Worker。迁到 krun 时申请平台子域，配置一条前缀 `/`。
Cloudflare [Workers Routes](https://developers.cloudflare.com/workers/configuration/routing/routes/#matching-behavior)
可以把同 Host 的不同 URL 模式给不同 Worker。迁移时按路径意图建立规则，并验证差异：

- `shop.example.com/*` 可迁成前缀 `/`；`shop.example.com/api/*` 可迁成前缀 `/api`，
  但 krun 也覆盖没有尾斜杠的 `/api`。
- `shop.example.com/api*` 在 Cloudflare 可匹配 `/apix`，krun 的路径段前缀 `/api` 不会。
  精确规则也有区别：Cloudflare 会考虑查询字符串，krun 只比较路径。
- Wrangler 可继续上传代码、发布或回滚。域名和路由通过 krun 控制台或
  `krun workers hostnames` 配置，Wrangler 的 `routes`、`custom_domain` 和 Zone 字段不会同步路由。
  平台后缀以外的域名不能在此申请。

## 验证

先让两个目标 Worker 发布可区分的响应，再请求同一个 HTTPS Host：

```sh
curl -isS "https://$HOST/"
curl -isS "https://$HOST/api/orders?limit=1"
curl -isS "https://$HOST/apix"
```

预期根路径与 `/apix` 进入 Web Worker，`/api/orders` 进入 API Worker。可对照各响应的
`X-Krun-Version` 和业务内容。域名配置异步同步；保存成功表示配置已写入。

| 现象 | 原因与处理 |
| --- | --- |
| HTTP 403 `PERMISSION_DENIED` | 检查组织域名权限以及目标 production 的 `worker.update`。 |
| HTTP 409 `HOSTNAME_UNAVAILABLE` | 名称被占用或保留；换一个短名。 |
| HTTP 409 `HOSTNAME_ROUTE_CONFLICT` | 删除重复匹配方式和路径，注意前缀会去掉尾斜杠。 |
| HTTP 409 `REVISION_CONFLICT` | 刷新列表，按最新 revision 重新编辑。 |
| HTTP 422 `HOSTNAME_INVALID`、`HOSTNAME_ROUTE_INVALID` 或 `HOSTNAME_TARGET_INVALID` | 检查是否使用已开放后缀、一层短名、合法路径，以及同组织且符合网络要求的目标。 |
| 请求返回 404 `unknown route`、410 `environment disabled` 或 503 `environment not published` | 分别检查域名是否已同步及配置匹配路由、规则是否启用、目标是否已发布。 |
