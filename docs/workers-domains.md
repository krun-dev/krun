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

Runtime 以完整 Host 定向解析 route，再加载不可变 Version；HTTPRoute 不携带站点
代码或 Version 版本。创建、发布和回滚不会创建站点专属 HTTPRoute，也不会重启 Runtime。
删除或调整配置目录后，协调器会修复期望路由并删除不再需要的受管路由。

## 控制台调用

invoke 使用当前 placement 和服务选中域名推导 Host，再请求该域名绑定的 Gateway。
客户端只能提供相对 URI；控制面不跟随业务重定向，也不转发平台凭据。

域名变更使用 expected revision 校验，Profile、池和域名能力必须匹配。自定义域名、
逐域名审批和自有 dataplane router 不在当前支持范围。资源池配置见
[Workers 资源池](workers-pools.md)，资产行为见 [静态资产](workers-assets.md)。
