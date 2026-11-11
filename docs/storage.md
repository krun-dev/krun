# 控制服务事务存储

控制面按**一实体一行**存放在 PostgreSQL。`State` 不再是持久化文档，而是「本次请求可能碰到的行」的工作副本：`Update` 只加载相关分区，执行回调，然后只写回发生变化的行。

设计约束见 [AGENTS.md 第 9 节](../AGENTS.md)：不用外键、事务尽力而为、构建/发布状态最终一致、不留无界数据、热路径不写。

## 代码分层

HTTP 入口位于 `internal/control`，认证、IAM、Workers、仓库、Runtime 配置及审计均有独立业务模块；网络和 SDK 实现集中在 `internal/adapters/`。完整边界见 [代码架构](architecture.md)。

`internal/storage` 集中连接池、SQL、行映射、事务、CAS、租约和清理，对外不暴露驱动类型。共享实体在 `internal/domain`。待提交登录、审计和密文排除在 JSON 之外；授权缓存由授权模块持有，不属于 `State` 或存储层。

`control/models.go` 和 `control/storage.go` 保留命令入口使用的类型转接。表结构与查询的定位入口见 [storage 包说明](../internal/storage/README.md)。边界测试递归检查全部模块的依赖，并禁止 HTTP handler 直接修改实体。

## ORM 与表模型

Bun 只在存储层使用，连接适配复用现有 pgx 连接池；原生批量查询与 ORM 共用连接上限，不额外创建独立业务连接池。

- `internal/storage/models/*_gen.go` 为全部基线表提供类型化行模型，包含列名、主键、JSONB、数组和可空时间映射。共享 domain 类型不带 ORM 标签。
- 模型由唯一 DDL 生成：`go generate ./internal/storage/models`；`make check-storage-models` 和 `make test` 会检查模型是否过期。不能手改生成文件来变更表结构。
- IAM 实体及关联、身份来源、会话和浏览器、登录挑战、仓库连接的普通读写，以及 ORM 事务内的审计写入使用模型。写入显式指定更新列，空字符串、`false` 和 `NULL` 都能被正确保存。
- revision 校验、唯一键冲突和影响行数仍显式处理。没有通用 `Save`、自动关联保存、级联删除、自动建表或迁移。
- 有范围的批量读取、Workers 条件状态迁移、刷新租约、队列认领、配置增量和清理继续使用专用 PostgreSQL 语句。ORM 与这些语句的边界集中在存储层，不扩散到业务包。

ORM 的映射测试对照真实数据库列集，并验证后续 CAS 失败会回滚前面的写入、零值和空值不会被忽略、连接被及时归还。

## 分区

IAM 与 Workers 是两套独立分区，**不共享事务**：

| 分区 | 表 |
| --- | --- |
| IAM | `krun_users`、`krun_organizations`、`krun_org_sources`、`krun_members`、`krun_groups`、`krun_group_members`、`krun_assignments`、`krun_resources`、`krun_api_keys`、`krun_api_key_hashes`、`krun_invitations`、`krun_identity_sources`、`krun_identity_providers`、`krun_meta` |
| Workers | `krun_workers`、`krun_worker_domains`、`krun_worker_environments`、`krun_worker_secret_versions`、`krun_worker_environment_secrets`、`krun_worker_builds`、`krun_worker_build_private`、`krun_worker_artifacts`、`krun_worker_versions`、`krun_worker_deployments`、`krun_worker_version_secrets`、`krun_worker_operations`、`krun_worker_history`、`krun_worker_idempotency`、`krun_repository_connections`、`krun_repository_connect_states` |
| 会话（不属于任何分区） | `krun_sessions`、`krun_browsers`、`krun_login_states`、`krun_email_logins`、`krun_oauth_authorizations`、`krun_oauth_grants`、`krun_oauth_refresh_tokens` |
| 组织邀请（类型化方法，不进入 State） | `krun_org_invitation_links`、`krun_org_invitation_redemptions`；邀请、名额、成员和直接角色在有界 IAM 事务内提交 |
| 旁路 | `krun_audits`、`worker_config_revisions`、`worker_config_routes`、`worker_config_versions`、`worker_config_ops`、`worker_config_dirty`、`worker_config_checkpoints`、`worker_asset_objects`、`worker_retention_scopes`、`worker_runtime_acks` |

仓库 OAuth 的访问令牌与刷新令牌使用 `workers.secrets_keys` 中的版本化密钥做 AES-256-GCM
加密。加密验证绑定用户、仓库提供商与字段。轮换时保留旧密钥版本，已有连接仍可读取，新连接
及刷新后的令牌使用当前密钥。旧库按 schema fingerprint 校验，受支持的版本可显式升级。

构建日志正文仅存于对象存储；`krun_worker_build_private` 只保留 `log_key` 引用，不含日志正文列。

完整 DDL 在 `internal/storage/schema.sql`，由同目录 `schema.go` 通过 `go:embed` 原样嵌入。新库直接初始化；已有库校验 `schema_baseline` fingerprint，匹配时不重复执行 DDL。

## 读路径

- 认证：会话/API Key 按键查询；登录只加载当前 identity 和邀请，常用请求按 actor / organization / service 过滤；批量行解码复用 `pgx.Batch`，不读取全平台站点。边缘配置投影显式接收 service ID 列表，不通过修改 SQL 文本插入过滤条件。
- `seen_at` 用**节流写**：`UPDATE ... WHERE seen_at < now() - interval '30 seconds'`，不是每请求都写。读请求本身不持久化任何状态。
- 站点列表与详情在分区内进一步按资源范围过滤；资源列表默认每页 100 条，以 `next_after` 翻页。Runtime 元数据按 Version/Artifact 定向读取。

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

发布提交要求环境行的 `deployment_revision` 仍等于操作校验时的值，否则操作失败而不是覆盖：

```sql
UPDATE krun_worker_environments SET version_id=$4, deployment_revision=deployment_revision+1
 WHERE worker_id=$1 AND env_name=$2 AND deployment_revision=$3 AND enabled;
```

## 边缘收敛

站点变更进入有界 dirty 队列，发布者只读取变更站点并写入 normalized delta；对象存储保存 compact head 与 host-hash shard checkpoint。Runtime 通过增量 feed 收敛，冷 host 按需读取，ACK 仍用于观察收敛。细节及保留/清理机制见 [规模边界与验证](performance-scaling.md)。

**Hosts 不再持久化。** 环境的主机名由域名目录 + 服务的域名选择现算，`workers.Service.NormalizeWorker`、`reconcile` 的 HTTPRoute 生成、边缘增量投影共用同一套推导，因此三者不会漂移。

## 认证数据

登录事务先原子消费（`UPDATE ... WHERE consumed=false`），再在锁外认证，最后写入用户与会话。会话与 `auth_version` 不再同一事务：半写只会导致这次登录需要重来，不会放行已撤销的身份。不需要应用加密密钥；PKCE verifier / nonce、CSRF 与待领取 CLI 凭据直接保存，state、browser token、Session 与 API Key 的认证值保存 SHA-256 摘要。

## 数据保留

数据库 retention 每分钟执行一次，有 20 秒预算；对象 GC 独立运行，每 10 秒有 8 秒预算：

| 表 | 保留 |
| --- | --- |
| `krun_sessions` | 30 分钟未活动或超过 8 小时绝对上限 |
| `krun_browsers` / `krun_login_states` / `krun_email_logins` / `krun_oauth_authorizations`、`krun_oauth_grants`、`krun_oauth_refresh_tokens` / `krun_repository_connect_states` / `krun_invitations` | 过期即删 |
| `krun_audits` | 7 天 |
| `krun_org_invitation_links` / `krun_org_invitation_redemptions` | 链接到期后 7 天一并清理，每批 1000 条；删除组织显式删除关联行；清理孤儿 redemption |
| `krun_worker_builds` | 每服务最近 100 条终态；临时日志及对象键 7 天后清空；`build-logs/` 私有对象由后台清理器保留 7 天 |
| `krun_worker_operations` | 终态保留 30 天 |
| `krun_worker_artifacts` | 无 version 引用且超过 30 天 |
| `krun_worker_idempotency` | 7 天 |
| `krun_worker_history` | 每 (service, env) 最近 50 条 |
| `krun_worker_secret_versions` | 无环境或 Version 引用后保留 30 天 |
| `krun_worker_asset_sessions` / `krun_worker_asset_session_files` | 资产上传能力及逐文件清单，一小时过期；文件先删、会话后删 |
| `worker_config_revisions` | 512 条 |

CF 资产临时对象在写入前登记 `krun_worker_source_uploads`，由现有 upload GC 在 24 小时后
删除对象并移除登记；会话过期不会丢失临时对象的清理索引。变量仍属于对应环境或版本行的
配置字段，支持 JSON 值；版本元数据 binding、标签及说明使用显式列，不引入整状态快照。

## Schema 基线

`OpenStore` 在空库创建表、索引、触发器和 `schema_baseline` fingerprint。已有库启动时
必须匹配当前基线。受支持的旧库停写后使用 `krun-server migrate --config /config/control.yaml`
升级。命令锁定该 schema 的 fingerprint 行，将增量 DDL 和 fingerprint 更新提交为一个事务。
同一目标重复执行会返回已是当前版本。空库、缺少 fingerprint 或没有完整升级路径的库会被拒绝。

当前支持从两份基线升级。指纹 `8f802b41ccf74f398f6c4c8ede9c480ad29b7e29ae95d7febda6046baa3842ef`
对应提交 `96b6dd4` 所用的 schema，先为已有构建补充 `config_path`，旧行取空字符串默认值。
指纹 `791bb9ebf10e4652f69918635175574492833a5b55b53db9806f0a604b333a30`
已包含该列，从此步继续为发布 Operation 增加 `claim_generation` 和 `edge_pool`。已有 `validating` 行在下一次认领时采用新租约。
仍指向当前部署的 `committed` 行重新确认边缘 revision，失去部署目标的行标记为 `superseded`。
新增迁移时，在 `schema_migrate.go` 按发布顺序追加增量 SQL 和准确的源、目标文件 SHA-256，
并更新建库 DDL。维护窗、备份与恢复步骤见[生产部署](../deploy/production/README.md#更新与恢复)。

## 已知限制

- 站点列表已按组织/资源 ID 游标下推，默认 100 条、最多 200 条；批量 IAM 管理仍有独立的加载语义。
- `ensureAdmin`（至少保留一个系统管理员）是「先写、再查、不满足就写回」的补偿，并发降级可能短暂出现零管理员，需要人工 `krun-server bootstrap` 恢复。
- 常用身份与站点路径按主体裁剪授权上下文，权限缓存仅限单次只读请求。
- 数据库集成测试需要 `KRUN_TEST_POSTGRES_URL`，未提供时跳过。存储包的数据库测试使用独立 schema，控制层集成测试通过独立测试连接布置数据，不开放生产连接池。

部署专属地址保留 30 天；部署记录到期或目标环境删除后由后台有界清理，删除同时入队路由撤销。保留中的 Deployment 会保护其 Version，Version 继续保护 Artifact 与 Secret。地址携带到期时间，Runtime 即使暂未收到清理增量也会拒绝过期部署请求。

OAuth 请求保留 5 分钟；access Session 最长 1 小时；授权与旋转 refresh 记录最长 30 天。
授权码与刷新令牌仅保存摘要，单次兑换使用条件更新；过期及孤儿行接入统一后台清理。

Worker Version 的 `created_by` 与 `created_by_name` 保存创建者 ID 和保存时的姓名，
作为不可变版本元数据，与 `created_at` 一同持久化；相同版本再次部署时不覆盖。
新增列直接纳入 `schema.sql` 基线，不执行启动迁移或旧版本作者回填。

## Worker 命名配置历史

`krun_worker_configurations` 一份配置一行，普通变量在该实体的 `variables` 列中；
Secret 引用与 R2 引用分别按绑定名称保存在 `krun_worker_configuration_secrets`、
`krun_worker_configuration_r2`。配置不可变，不包含代码、域名和流量指针。
`krun_worker_versions.configuration_id` 记录配置来源，发布仍使用版本中冻结的完整配置。

创建时校验环境 revision，在同一小事务中保存配置及引用，不新增外键。删除预览显式删除
配置绑定及配置行；删除 Worker 的后台清理也覆盖这三张表。非活动配置 30 天过期，当前、
已确认和进行中的部署引用受保护；`RetainExpired` 每批清理最多 1000 份。Secret 版本清理
同时检查配置引用，防止尚存配置丢失 Secret。配置接口见 [Workers 后端](workers-backend.md)。
