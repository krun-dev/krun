# Workers 域名与共享路由

域名与 Gateway 在主 YAML 的 `workers.domains` / `workers.gateways` 中声明。组织侧
只能选择 Network Profile 允许的域名，不传任意 Host、Gateway 地址或资源池。

## 主机名

每个 Worker、环境和选中域名生成一个地址：

```text
w-<worker-hash>-<environment>.<domain-suffix>
```

Worker hash 来自组织与Worker ID；域名不包含运行池 ID。
production 和 preview 使用相同的服务 placement。主机名由一套公共推导函数产生，
不会持久化在环境行中；控制台、invoke 和边缘配置均复用这个结果。

部署侧为 `*.<domain-suffix>` 配置 DNS 和 TLS。代理必须保留完整 Host；如需从展示域
改写到集群测试域，只替换 domain suffix，保留服务和环境标签。

## 路由与隔离

控制面只创建以下两类 HTTPRoute：

| 类型 | 数量 | 目标 |
| --- | --- | --- |
| 业务共享路由 | 每个 domain 一条 | pool 的 serving Runtime Service |
| 静态资产路由 | 每个 domain 一条 | 对象存储静态 Service 与对应 `pub/` 或 `int/` 前缀 |

每个 domain 经 Network Profile 唯一选择一个运行池；配置拒绝多个池争用同一通配域名。
同一池可以有多个副本。domain 显式绑定一个 Gateway 实例与 listener。不同访问边界使用独立 Gateway，
并为它们分配互不重叠的通配域名，例如 `*.internal.workers.example.com` 和
`*.public.workers.example.com`。如果一个通配域名覆盖另一个入口的主机名，分开 Gateway
也不能保证跨实例伪造 Host 返回 404。静态域默认 `static.<domain>`，可通过
`static_domain` 显式配置。

Runtime 先按完整 Host 解析路由；自定义子域名再按路径选择规则，然后加载不可变 Version。HTTPRoute 不携带站点
代码或 Version 版本。创建、发布和回滚不会创建站点专属 HTTPRoute，也不会重启 Runtime。
删除或调整配置目录后，协调器会修复期望路由并删除不再需要的受管路由。

## 控制台调用

invoke 使用当前 placement 和服务选中域名推导 Host，再请求该域名绑定的 Gateway。
客户端只能提供相对 URI；控制面不跟随业务重定向，也不转发平台凭据。

## 开放自定义子域

确认通配 DNS、TLS 和共享 Gateway 已就绪，且不同入口的通配覆盖不重叠。将主配置中
对应 `workers.domains` 条目的 `managed_subdomains` 设为 `true`，并在 `reserved_hosts`
填入其他服务已使用的完整域名。控制台、静态域名、后缀自身和 `w-` 系统名称会保留。
设为 `false` 可关闭新申请；存量域名仍可管理。不要原地重用已分配后缀 ID。

已有数据库使用当前版本的 `krun-server migrate --config <主配置>` 显式升级。
先更新全部 serving Runtime，再开放后缀；新配置将自定义规则放在独立字段，旧 Runtime
不会把任一自定义规则误认作整域规则。域名变更以 Host revision 为并发条件，数据面按
现有增量游标和懒加载协议收敛；冷加载会返回同一快照里的整组规则。用户用法见
[自定义子域名与路径路由](../user-docs/src/content/docs/guides/workers-hostnames.md)。

组织持有 Host；路径规则独立存储。目标停用或删除后，相应规则保留并返回 410，直到
域名管理员重绑或移除。发布、灰度和回滚只改变目标环境的版本。资源池配置见
[Workers 资源池](workers-pools.md)，资产行为见 [静态资产](workers-assets.md)。
