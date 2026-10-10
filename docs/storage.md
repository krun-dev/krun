# 控制服务事务存储

首版存储使用完整 `State` 快照。开发和生产统一使用 PostgreSQL；内存 Store 的构造入口仅存在于测试文件中。

```sql
CREATE TABLE IF NOT EXISTS krun_control_state (
  id integer PRIMARY KEY CHECK (id = 1),
  data jsonb NOT NULL
);
INSERT INTO krun_control_state (id, data)
VALUES (1, '{}') ON CONFLICT DO NOTHING;
```

`Update` 读取该行并 `FOR UPDATE`，反序列化数据，执行有界本地逻辑，写入、提交。多个控制服务副本、bootstrap 与邀请命令使用同一锁；失败回滚。权限修改与审计提交在同一事务。API 请求体读取、OAuth token 交换、OIDC 验证与用户资料请求在共享锁外完成，避免网络操作阻塞 IAM。

登录事务先原子标记消费，再在锁外认证，最后验证 browser generation 并原子提交用户、会话、审计。上游中断不会再次兑换同一 code，迟到回调不能覆盖新登录或退出。不需要应用加密密钥。PKCE verifier / nonce、CSRF 与待领取 CLI 凭据直接保存；state、browser token、已领取 Session 与 API Key 的认证值保存 SHA-256 摘要。API Key 哈希单独存储，公开元数据 JSON 不包含它。

数据库连接在 `db` 中直接填写，或者写 `db: ${KRUN_DATABASE_URL}` 从环境变量读取。所有服务端字符串字段共享环境引用规则，详见 README。未配置连接或必需变量为空时启动失败。没有文件存储回退。开发可用 `scripts/dev-postgres.sh` 原生启动本机 PG，不依赖 Docker。

邮箱登录事务同样保存在 State 的 EmailLogins 中，包含验证码摘要、来源 / 浏览器绑定、代次、有效期、发送时间、失败次数和消费状态。发送邮件在数据库锁外执行；验证及消费在同一个 PG 事务中完成，输错计数会提交。过期事务由后续认证请求清理；不保存明文验证码或新增加密密钥。

快照模式的限制：所有请求共享一把 PG 行锁，反序列化与持久化成本随总数据增加；组织 / 资源等列表当前无分页；审计已提供游标分页，并按最近 7 天滚动清理，没有长期归档。它保证首版正确性，不提供生产规模吞吐承诺。正式大规模上线前需要：

1. 将 users/sources、浏览器/事务/sessions、组织/成员/组/角色、Key/策略、资源和审计拆为关系表，并加入身份唯一键、外键和过期索引。
2. 在目标行锁与事务 revision 下实现相同的授权及最后管理员不变量。
3. 下推组织、资源选择器与环境条件，使用绑定主体和权限修订的分页游标。
4. 审计按组织/日期索引、归档；Session last_seen 降低写热点。

已实现 PostgreSQL 集成测试验证跨 Store 实例串行 bootstrap、失败回滚和审计原子性。替换存储时需保留这些测试及权限验收用例。

审计保留期为 7 天，所有提交事务都会删除过期条目，服务还每分钟执行一次空闲清理。分页虽然在 API 层限制返回数量，当前仍读取和排序完整快照中的保留审计，尚未下推到 PostgreSQL 的独立审计表 / 索引。
