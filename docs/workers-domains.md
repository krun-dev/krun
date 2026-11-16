# Workers 域名与共享路由

平台在主配置的 `workers.domains` 和 `workers.gateways` 中声明域名后缀和 Gateway。
每条后缀用 `types` 声明一种或多种用途。同一个后缀只配置一次，后缀之间不能嵌套。
同一后缀的所有用途共享 `access`、Gateway、通配 DNS 和 TLS。

| `types` 中的值 | 示例后缀 | 用途 |
| --- | --- | --- |
| `worker_default` | `workers.example.com` | 平台为 Worker 环境生成前缀。启用此类型后可以设置 `default` 或 `static_domain` |
| `worker_custom` | `workers.example.com` | 用户填写前缀，在 Worker 详情绑定整域，目标为当前 Worker 的生产环境 |
| `application` | `apps.example.com` | 组织申请域名并配置路径，目标可以是同组织的 Worker 或平台登记的静态站点、HTTP 服务 |

例如，`types: [worker_default, worker_custom]` 允许同一个后缀同时提供
`w-<worker-hash>-production.workers.example.com` 和 `orders.workers.example.com`。
如果再加入 `application`，也可以在该后缀下申请应用域名，如 `shop.workers.example.com`。
每个完整主机名只对应一种用途。

Network Profile 匹配后缀的访问类型和标签。每个后缀只能选中一个 Worker 运行池。
每个 Profile 必须有可用的 `worker_default` 后缀，应用路由的 Worker 也须匹配该后缀的
Profile 和运行池。

## 默认地址和共享入口

Worker 的环境默认地址格式为 `w-<worker-hash>-<environment>.<domain-suffix>`。
主机名由组织、Worker ID、环境和 Network Profile 允许的后缀共同推导。
Cloudflare 兼容接口的 `workers_dev` 可关闭默认生产地址，`preview_urls` 可关闭部署版本的
固定预览地址；显式创建的命名 Preview、Custom Domains、Workers Routes 和 Service Bindings
独立工作。控制台的默认生产地址调用随 `workers_dev` 关闭；可通过配置的生产路由访问。

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
   修改前缀。`w-` 开头的前缀由平台保留。控制台、平台静态入口和后缀自身不可申请。
2. 按 [主配置示例](../config/example.yaml) 填写 `workers.managed_origins`：使用 `kind: static`
   或 `kind: http`，指定 Kubernetes Service 和端口，以及可用组织和后缀。该登记的
   `host` 用于应用后缀的 Origin Rules；连接地址来自 Service。默认转发原 Host，只有登记了
   `host_header` 才改写。跨命名空间目标需由目标命名空间授予 Gateway API
   [ReferenceGrant](https://gateway-api.sigs.k8s.io/api-types/referencegrant/)。
3. 安装支持 [DirectResponse](https://kgateway.dev/docs/envoy/2.2.x/traffic-management/direct-response/)
   的 kgateway CRD，并赋予控制面的 Kubernetes 身份在 Workers 命名空间内读取、创建和更新该资源的权限。
4. 升级已有数据库时，停服务后运行
   `krun-server migrate --config "${KRUN_CONFIG}"`，其中 `KRUN_CONFIG` 为主 YAML 路径。
   更新 serving Runtime 和控制面后再开放后缀。

升级已有配置时，将 `type` 改为 `types` 列表。更早的配置需删除 `managed_subdomains`，
并保持后缀 ID 和域名。原后缀同时提供默认地址、整域绑定和路径路由时，配置
`types: [worker_default, worker_custom, application]` 可保留现有入口。迁移会把仅有一条
整域绑定且未使用专用 Gateway 路由的主机名记为 `worker_custom`，其余已有申请记为
`application`。混合主机名原有的整域绑定保留为应用域名的根路由。

域名变更按 Host revision 做并发保护，兼容 API 的批量操作一次提交。含非 Worker 目标的
域名上限为 16 条路由；无根前缀时预留一条 404，只能配置 15 条。曾有非 Worker 目标的
主机名继续保留精确 Host 路由和上述上限，退役时用于阻止请求落到通配入口。

用户操作见 [域名与路径路由](../user-docs/src/content/docs/guides/workers-hostnames.md)，
Cloudflare 接口见 [兼容 API](cloudflare-api.md)。
