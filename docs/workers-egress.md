# Workers 出网与 PostgreSQL 托管接入

用 [控制台或 Wrangler](../user-docs/src/content/docs/guides/workers-hyperdrive.md)
创建 Hyperdrive 配置，并绑定到 Worker 版本。workerd 将源库连接串交给绑定了数据库的
Worker。Worker 使用 PostgreSQL 驱动直接连接源库，连接串要求 `sslmode=verify-full`。
数据库域名和证书必须匹配，驱动须信任证书链。使用私有 CA 时，可在渲染 workerd
配置的机器上通过 `workers.database_origin_ca_file` 指定 CA PEM 文件的绝对路径。

## 池级出网

池省略 `public_egress` 和 `private_egress` 时默认 `deny`。显式 `public_egress: direct`
给 serving Worker 开放公网 HTTP、HTTPS 和 TCP。显式 `private_egress: direct`
给同池**绑定了 Hyperdrive 的版本**开放私网 HTTP、HTTPS 和 TCP。workerd 按连接目的地址
执行池规则，含域名解析后的地址。私网规则覆盖允许的网段，超出已绑定数据库的单个地址。
独立 validator 和 serving 中的候选校验均关闭出网，使用占位连接串。

`workers.protected_egress_cidrs` 禁止访问部署方指定的网段。启用私网出网时，将控制面、
集群管理端点、对象存储管理端点和其他平台专用网段列入该配置。workerd 还拒绝
回环、链路本地和其他保留地址。Kubernetes 部署的 Runtime NetworkPolicy
也须允许目标网段及 PostgreSQL 端口。本地开发见 [local-first 配置](local-dev.md)。

## 升级与凭据

控制面使用 Workers 密钥环加密源库密码。只有负责该版本的 serving pool 可以读取连接串。
连接串含源库账号和密码。请将可绑定该配置和可发布绑定版本的成员视为拥有该源库账号的权限。
为 Worker 使用单独的数据库账号，并在数据库侧限制权限和连接数。
轮换密码时先在源库修改，再更新 Hyperdrive 配置并发布新版本，使 workerd 重新加载连接串。
删除配置会停止签发连接串。已发放凭据及已建连接需在数据库侧撤销。
仍有效的旧部署 URL 可能在冷启动时读取更新后的密码。要撤销旧版本的访问，需停用相应部署，
或为新版本使用单独的配置及数据库凭据。

升级时从主配置删除 `workers.hyperdrive` 旧代理段。曾配置 `origin_ca_file` 时，将其路径移至
`workers.database_origin_ca_file`。更新控制面、serving 和 validator 镜像，
并重新生成 workerd 配置。旧 runtime 与新控制面之间的 Hyperdrive 数据接口不兼容，
应在同一维护窗口更新。发布依赖运行时报告 `hyperdrive_protocol: 2`。
发布检查验证绑定和 runtime 能力，源库连通性由 serving Worker 的数据库请求验证。

## 验证

- 用 [PostgreSQL 示例](../user-docs/src/content/docs/guides/examples-postgres-todo.md) 完成
  部署，确认 TODO 查询返回 HTTP 200，新增条目后刷新仍存在。
- 如果发布状态显示 `Hyperdrive is unavailable in the serving pool`，为数据库所在网段
  选择启用相应出网策略的池。如果状态显示 `Waiting for a runtime image supporting Hyperdrive`，
  检查 validator 和 serving 是否已滚动到支持直连的镜像及配置。
- 如果数据库请求连接失败，检查池出网、运行时 NetworkPolicy、源库防火墙、凭据和 TLS 信任链。
