# Workers 域名与共享路由

平台在主配置的 `workers.domains` 和 `workers.gateways` 中声明域名后缀和 Gateway。
每条后缀用 `types` 声明一种或多种用途。同一个后缀只配置一次，后缀之间不能嵌套。
同一后缀的所有用途共享 `access`、Gateway、通配 DNS 和 TLS。

| `types` 中的值 | 示例后缀 | 用途 |
| --- | --- | --- |
| `worker_default` | `workers.example.com` | 平台为 Worker 环境生成前缀。启用此类型后可以设置 `default` 或 `static_domain` |
| `worker_custom` | `workers.example.com` | 用户填写前缀，在 Worker 详情绑定整域，默认开放当前 Worker 的生产和 Preview |
| `application` | `apps.example.com` | 组织申请域名并配置路径，目标可以是同组织的 Worker 或平台登记的静态站点、HTTP 服务 |

例如，`types: [worker_default, worker_custom]` 允许同一个后缀同时提供
`orders.workers.example.com` 和用户绑定的其他子域名。
如果再加入 `application`，也可以在该后缀下申请应用域名，如 `shop.workers.example.com`。
每个完整主机名只对应一种用途。

Network Profile 匹配后缀的访问类型和标签。每个后缀只能选中一个 Worker 运行池。
每个 Profile 必须有可用的 `worker_default` 后缀，应用路由的 Worker 也须匹配该后缀的
Profile 和运行池。

## 默认地址和共享入口

Worker 的生产默认地址为 `<worker-name>.<domain-suffix>`；命名 Preview 的地址为
`<preview-name>-<worker-name>.<domain-suffix>`。Worker 名称是全局唯一的小写 DNS 标签，
创建后不可修改；Preview 名称和 Worker 名称拼接后不能超过 63 个字符。平台在创建时
检查生产地址、Preview、固定部署地址与托管域名的冲突。共享 Worker 默认后缀的托管域名前缀
也在所有 Worker 默认后缀之间统一保留。名称超过 40 个字符时固定部署地址无法生成；超过 54 个
字符时 Version URL 无法生成，因为附加前缀后会超出 DNS 标签长度限制。
Cloudflare 兼容接口的 `workers_dev` 控制默认生产地址，`preview_urls` 控制默认后缀上的
命名 Preview、部署固定地址和上传后的 Version URL。上传版本通过独立运行时校验后，
Version URL 可访问该版本，生产部署保留原状。Version URL 自上传起保留 30 天。

Custom Domain 默认开放命名地址 `<preview>.<custom-domain>` 和固定部署地址。
Wrangler 可用 `previews_enabled: false` 关闭预览，或用 `enabled: false` 只开放 Preview。控制面将 `*.<custom-domain>` 作为 HTTPRoute 绑定到该后缀的
Gateway。DNS 与 TLS 由部署侧提供，单层证书 `*.<domain-suffix>` 只覆盖生产自定义域名。
应用域名的生产路由继续使用配置的目标；整站 staging 需要在独立主机名配置自己的路径和各目标。

部署侧需配置 `*.<domain-suffix>` 的 DNS 和 TLS，保留完整 Host；如果从展示域改写到测试域，
只替换后缀。不同访问边界使用不同 Gateway listener 和互不重叠的通配域名。
静态资产专用域名默认为 `static.<domain>`，可配置 `static_domain`。

| HTTPRoute | 目标 |
| --- | --- |
| 每个后缀的共享业务路由 | 该后缀运行池的 serving Runtime Service；纯 Worker 自定义域名也走这里 |
| 每个 `worker_default` 后缀的静态资产路由 | 对象存储静态 Service 的 `pub/` 或 `int/` 前缀 |
| 含静态站点或 HTTP 服务路由的主机名 | 精确 Host；按路径选择登记的 Service 或该后缀的 serving Runtime Service |

Worker 发布、灰度和回滚更新 Runtime 的版本配置。流量入口不携带版本。每个后缀只指向一个
Worker 运行池。含非 Worker 目标的域名由 kgateway 匹配路径；对转发到 Runtime 的请求，
Runtime 同时校验 Gateway 命中的规则和目标。匹配不到的请求返回 404；停用的路径返回 410；
平台移除非 Worker 目标后，对应路径返回 503，直到用户重新绑定或删除规则。目标 Worker
删除后路径保留并返回 410。路由更新异步生效。

## 部署配置

1. 先配置通配 DNS、TLS 和 Gateway。按 [主配置示例](../config/example.yaml) 声明后缀的
   `types`，并在 `reserved_hosts` 填入其他服务已使用的完整主机名。自定义和应用域名都只允许
   修改前缀。控制台、平台静态入口和后缀自身不可申请。
2. 按 [主配置示例](../config/example.yaml) 填写 `workers.managed_origins`：使用 `kind: static`
   或 `kind: http`，指定 Kubernetes Service 和端口，以及可用组织和后缀。该登记的
   `host` 用于应用后缀的 Origin Rules；连接地址来自 Service。默认转发原 Host，只有登记了
   `host_header` 才改写。跨命名空间目标需由目标命名空间授予 Gateway API
   [ReferenceGrant](https://gateway-api.sigs.k8s.io/api-types/referencegrant/)。
3. 安装支持 [DirectResponse](https://kgateway.dev/docs/envoy/2.2.x/traffic-management/direct-response/)
   的 kgateway CRD，并赋予控制面的 Kubernetes 身份在 Workers 命名空间内读取、创建和更新该资源的权限。
4. 通过[手动在线迁移](storage.md#手动执行在线迁移)完成所需数据库变更，再滚动部署控制面和 serving Runtime。

组织的应用域名页管理完整路由表。Worker 详情页可以添加、修改和移除指向当前 Worker 的路由。
两个入口使用同一个 Host revision、路由校验和权限检查，修改 Worker 路由需要组织的
`worker.hostname.manage` 与对应生产 Worker 的 `worker.update`。Worker 入口提交的是当前
Worker 在该域名下的完整规则集合，其余目标的规则保留。域名退役由组织域名页管理。

域名变更按 Host revision 做并发保护，兼容 API 的批量操作一次提交。含非 Worker 目标的
域名上限为 16 条路由；无根前缀时预留一条 404，只能配置 15 条。曾有非 Worker 目标的
主机名继续保留精确 Host 路由和上述上限，退役时用于阻止请求落到通配入口。

用户操作见 [域名与路径路由](../user-docs/src/content/docs/guides/workers-hostnames.md)，
Cloudflare 接口见 [兼容 API](cloudflare-api.md)。
