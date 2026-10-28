# 控制服务事务存储

控制面按**一实体一行**存放在 PostgreSQL。`State` 不再是持久化文档，而是「本次请求可能碰到的行」的工作副本：`Update` 只加载相关分区，执行回调，然后只写回发生变化的行。

设计约束见 [AGENTS.md 第 9 节](../AGENTS.md)：不用外键、事务尽力而为、构建/发布状态最终一致、不留无界数据、热路径不写。完整方案见 [storage-redesign.md](storage-redesign.md)。

## 分区

IAM 与 Workers 是两套独立分区，**不共享事务**：

| 分区 | 表 |
| --- | --- |
| IAM | `krun_users`、`krun_organizations`、`krun_org_sources`、`krun_members`、`krun_groups`、`krun_group_members`、`krun_assignments`、`krun_resources`、`krun_api_keys`、`krun_api_key_hashes`、`krun_invitations`、`krun_identity_sources`、`krun_identity_providers`、`krun_meta` |
| Workers | `krun_worker_services`、`krun_worker_service_domains`、`krun_worker_environments`、`krun_worker_secret_versions`、`krun_worker_environment_secrets`、`krun_worker_builds`、`krun_worker_build_private`、`krun_worker_artifacts`、`krun_worker_releases`、`krun_worker_release_secrets`、`krun_worker_operations`、`krun_worker_history`、`krun_worker_idempotency`、`krun_repository_connections`、`krun_repository_connect_states` |
| 会话（不属于任何分区） | `krun_sessions`、`krun_browsers`、`krun_login_states`、`krun_email_logins`、`krun_device_codes` |
| 旁路 | `krun_audits`、`worker_config_revisions`、`worker_runtime_acks` |

DDL 在 `internal/control/schema.go`，全部 `IF NOT EXISTS`，启动时幂等执行。

## 读路径

- 认证：一次 `krun_sessions` 主键查 + 一次批量的 IAM 快照（14 条查询走在同一个 `pgx.Batch` 里，一次往返）。
- `seen_at` 用**节流写**：`UPDATE ... WHERE seen_at < now() - interval '30 seconds'`，不是每请求都写。读请求本身不持久化任何状态。
- 列表与详情按分区加载，`WorkerState.Revision` 这类只写不读的计数器已删除。

## 写路径

`Store.Update(ctx, partition, fn)` 的语义是「加载分区 → 执行 → 写差异」：

- 只写真正变化的行；未变的行不会被重写，所以改一个用户不会重写所有构建。
- 变化的判定用字段级比较（`sameBuild` / `sameOperation` / `sameEnvironment` 等），不是整行覆盖。
- 回调返回错误则不落库。
- 读请求传 `persist = 0`，跳过用于 diff 的前像克隆。

## 并发

不使用 advisory lock，也不做选主。

构建与发布的状态迁移是**条件更新（CAS）**：

```sql
UPDATE krun_worker_builds SET state='building', claimed_by=$2, claimed_at=now()
 WHERE build_id=$1 AND state IN ('queued','fetching');          -- 0 行 = 别人先认领
```

副本崩溃后，`claimed_at` 超过 60 秒的 `validating` 操作可被其他副本接管。所有下游副作用必须幂等，因为同一操作可能执行两次。

发布提交要求环境行的 `release_revision` 仍等于操作校验时的值，否则操作失败而不是覆盖：

```sql
UPDATE krun_worker_environments SET release_id=$4, release_revision=release_revision+1
 WHERE service_id=$1 AND env_name=$2 AND release_revision=$3 AND enabled;
```

## 边缘收敛

不变：写入 `worker_config_revisions` → 快照进对象存储 → runtime 长轮询 `latest.json` + 增量 feed → 每 Pod 一个 ack。收敛只靠 revision 与 ack 观察，不阻塞提交。

**Hosts 不再持久化。** 环境的主机名由域名目录 + 服务的域名选择现算，`normalizeService`、`reconcile` 的 HTTPRoute 生成、`workerConfigFromState` 共用同一套推导，因此三者不会漂移。

## 认证数据

登录事务先原子消费（`UPDATE ... WHERE consumed=false`），再在锁外认证，最后写入用户与会话。会话与 `auth_version` 不再同一事务：半写只会导致这次登录需要重来，不会放行已撤销的身份。不需要应用加密密钥；PKCE verifier / nonce、CSRF 与待领取 CLI 凭据直接保存，state、browser token、Session 与 API Key 的认证值保存 SHA-256 摘要。

## 数据保留

后台每分钟执行一次（`internal/control/retention.go`），取代原先挂在每个请求上的 `cleanup`：

| 表 | 保留 |
| --- | --- |
| `krun_sessions` | 30 分钟未活动或超过 8 小时绝对上限 |
| `krun_browsers` / `krun_login_states` / `krun_email_logins` / `krun_device_codes` / `krun_repository_connect_states` / `krun_invitations` | 过期即删 |
| `krun_audits` | 7 天 |
| `krun_worker_builds` | 每服务最近 100 条终态；日志 7 天后清空 |
| `krun_worker_operations` | 终态保留 30 天 |
| `krun_worker_artifacts` | 无 release 引用且超过 30 天 |
| `krun_worker_idempotency` | 7 天 |
| `krun_worker_history` | 每 (service, env) 最近 50 条 |
| `krun_worker_secret_versions` | 无环境或 Release 引用后保留 30 天 |
| `worker_config_revisions` | 512 条 |

## Schema 基线

项目尚未发布，`OpenStore` 直接在一个事务中创建 `schema.go` 定义的唯一基线。当前没有
版本迁移表、旧快照导入器、双写或兼容读取路径；模型变化后开发和测试数据库直接重建。
正式发布前如需演练升级，再从届时冻结的首个生产 schema 开始建立迁移账本。

## 已知限制

- 组织 / 资源列表仍在内存中过滤，没有分页下推。
- `ensureAdmin`（至少保留一个系统管理员）是「先写、再查、不满足就写回」的补偿，并发降级可能短暂出现零管理员，需要人工 `krun-server bootstrap` 恢复。
- 授权每次请求加载完整 IAM 快照（约 0.2 ms），未按主体裁剪。
- 数据库集成测试需要 `KRUN_TEST_POSTGRES_URL`，未提供时跳过。
