# Workers 出网与 PostgreSQL 托管接入

业务可通过 [控制台和 Wrangler 指南](../user-docs/src/content/docs/guides/workers-hyperdrive.md)
创建 Hyperdrive 配置并绑定到 Worker。源库可位于公网或内网，当前只支持 PostgreSQL。
Wrangler 接受以 `postgres://` 或 `postgresql://` 开头的源库连接串，控制台按字段填写。
业务驱动使用平台生成的代理连接串。

## 池级出网

池省略 `public_egress` 和 `private_egress` 时默认 `deny`。显式 `public_egress: direct`
给 serving Worker 开放公网 HTTP、HTTPS 和 TCP。独立 validator 与 serving 中的候选校验仍关闭出网。

Hyperdrive 使用同一组池字段判断代理能拨号的范围。公网需要 `public_egress: direct`，
内网需要 `private_egress: direct`。业务 Worker 的全局内网直连出口保持关闭。代理在建连时
解析源库域名，过滤实际拨号地址，使用源库域名验证 TLS。DNS 缓存及 TTL 由宿主机解析器负责。
平台保护地址通过 `workers.protected_egress_cidrs` 配置。未来的域名规则可接在代理的目标地址检查处。

## 代理部署

使用主 YAML 的 `workers.hyperdrive` 段启用代理。`listen` 使用独立私有 IP 和 TCP 端口。
HTTP `listen` 和 `internal_listen` 使用不同的明确 IP，代理专用地址上只运行 PostgreSQL
监听。`address` 使用 Worker 可解析到该专用 IP 的 DNS 名称与同一端口，TLS 证书应匹配此名称，
且证书链须受 serving workerd 的浏览器 CA 根证书信任。源库若使用内部 CA，可配置
`origin_ca_file`。

~~~yaml
workers:
  hyperdrive:
    listen: ${KRUN_HYPERDRIVE_LISTEN}
    address: ${KRUN_HYPERDRIVE_ADDRESS}
    tls_cert_file: ${KRUN_HYPERDRIVE_CERT_FILE}
    tls_key_file: ${KRUN_HYPERDRIVE_KEY_FILE}
    origin_ca_file: ${KRUN_HYPERDRIVE_ORIGIN_CA_FILE:-}
~~~

代理在 `krun-server` 进程中运行。只向持有 Hyperdrive 绑定的业务 Version 放行代理地址。
启用时先滚动更新 serving 和 validator 的 workerd 镜像与配置，再发布带绑定的版本。
每个客户端会话直接占用一条源库连接。首版每个源库地址最多 16 条代理会话，代理进程最多
256 条并发连接。源库凭据使用现有 Workers 密钥环加密，Worker 只获得签名的版本绑定凭据。

## 验证

- 重新渲染 serving workerd，确认公网 HTTP、HTTPS、TCP 和受保护地址的拒绝行为。
- 用原版 Wrangler 创建并绑定配置，用实际 Worker 验证 TLS 登录、参数化查询和事务。
- 分别用公网池、内网池验证源库范围，在新连接上检查密码轮换和配置删除。
- 在同一环境比较直连源库与经代理连接的 P95/P99、超时率和源库连接峰值。
