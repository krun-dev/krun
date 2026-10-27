# 控制面存储重构方案（评审稿）

状态：**已实现**（见 [storage.md](storage.md) 的实际表结构与约束）。本文保留为设计与取舍记录。
前置结论：现行 `krun_control_state` 单行 jsonb 快照是首版有意取舍（见 `docs/storage.md`），
真正的问题不是「选了快照」，而是**热路径和追加型数据也挂在同一个 39 KB 文档上**。
本方案把它拆成关系表，并把一致性目标降到与业务重要性匹配的水平。

---

## 1. 目标与非目标

### 目标

1. 所有实体落关系表，一实体一行；不做「整文档读写」。
2. 每个请求的写放大与文档总体积无关。认证 GET 不再产生全量写。
3. 事务只做尽力而为的保证：**单个元数据对象内部一致**，不承诺跨实体强一致。
4. 构建 / 发布状态只要求最终一致；操作行（operation）就是持久化工作队列。
5. 保留现有的授权语义（Cedar 策略、角色、环境条件）与全部外部 API 形状。

### 非目标

- 不做事件溯源、不做 outbox、不做 CDC。
- 不引入 Redis / 消息队列 / 外部迁移框架。
- 不为「跨实体强一致」设计分布式锁或两阶段提交。
- 不改前端契约；`/api/*` 路径与响应结构保持不变。

---

## 2. 表结构

统一约定：

- 主键用现有 ID 字符串（`usr_` / `org_` / `svc_` / `bld_` / `op_` / `rel_` …），不改 ID 生成方式。
- **不使用外键**（见 `AGENTS.md` 新增第 9 节）。引用完整性由写入路径校验 + 删除路径显式级联。
- `UNIQUE` 约束、`CHECK`、部分索引照常使用。
- 时间列统一 `timestamptz`，应用侧写 UTC。
- 需要乐观并发的行保留 `revision integer`（assignment / key / resource / group / environment）。
- 所有 DDL 幂等（`CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`），启动时执行。

### 2.1 元数据与身份源

```sql
CREATE TABLE IF NOT EXISTS krun_meta (
  key   text PRIMARY KEY,
  value text NOT NULL
);                                  -- schema_version / bootstrapped / revision_seq cursor

CREATE SEQUENCE IF NOT EXISTS krun_revision_seq;   -- 全局单调修订号，取代 State.Revision

CREATE TABLE IF NOT EXISTS krun_identity_sources (
  source      text PRIMARY KEY,
  fingerprint text NOT NULL
);

-- 一个 provider / namespace 只能绑定一个 source；同一 source 可被多个 provider 复用。
CREATE TABLE IF NOT EXISTS krun_identity_providers (
  provider_id text PRIMARY KEY,
  source      text NOT NULL,
  namespace   text NOT NULL,
  UNIQUE (provider_id, namespace)
);
CREATE UNIQUE INDEX IF NOT EXISTS krun_identity_providers_ns
  ON krun_identity_providers (namespace);
```

对应现行 `State.Sources` 那个三重语义的 map（`provider:<id>`、`namespace:<x>`、`<source>`），
拆开后三类约束分别由主键和唯一索引表达，不再靠字符串前缀编码。

### 2.2 主体与授权

```sql
CREATE TABLE IF NOT EXISTS krun_users (
  user_id      text PRIMARY KEY,
  source       text NOT NULL,
  subject      text NOT NULL,
  name         text NOT NULL DEFAULT '',
  email        text NOT NULL DEFAULT '',
  status       text NOT NULL DEFAULT 'active',
  auth_version integer NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source, subject)
);

CREATE TABLE IF NOT EXISTS krun_organizations (
  org_id     text PRIMARY KEY,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS krun_org_sources (
  org_id text NOT NULL,
  source text NOT NULL,
  PRIMARY KEY (org_id, source)
);

CREATE TABLE IF NOT EXISTS krun_members (
  org_id     text NOT NULL,
  user_id    text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS krun_members_user ON krun_members (user_id);

CREATE TABLE IF NOT EXISTS krun_groups (
  group_id   text PRIMARY KEY,
  scope_kind text NOT NULL,      -- system | organization | service | model
  scope_id   text NOT NULL,
  name       text NOT NULL,
  revision   integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_groups_scope ON krun_groups (scope_kind, scope_id);

CREATE TABLE IF NOT EXISTS krun_group_members (
  group_id text NOT NULL,
  user_id  text NOT NULL,
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS krun_group_members_user ON krun_group_members (user_id);

CREATE TABLE IF NOT EXISTS krun_assignments (
  assignment_id text PRIMARY KEY,
  subject_kind  text NOT NULL,   -- user | group
  subject_id    text NOT NULL,
  role          text NOT NULL,
  scope_kind    text NOT NULL,
  scope_id      text NOT NULL,
  conditions    jsonb NOT NULL DEFAULT '{}'::jsonb,  -- 仅 environments
  revision      integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_assignments_subject
  ON krun_assignments (subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS krun_resources (
  resource_id text PRIMARY KEY,
  kind        text NOT NULL,     -- service | model
  org_id      text NOT NULL,
  name        text NOT NULL,
  revision    integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_resources_org ON krun_resources (org_id, kind);

CREATE TABLE IF NOT EXISTS krun_api_keys (
  key_id        text PRIMARY KEY,
  owner_user_id text NOT NULL,
  org_id        text NOT NULL DEFAULT '',
  scope_kind    text NOT NULL,
  name          text NOT NULL,
  prefix        text NOT NULL,
  policy        jsonb NOT NULL,
  expires_at    timestamptz,
  revoked       boolean NOT NULL DEFAULT false,
  revision      integer NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_api_keys_owner ON krun_api_keys (owner_user_id);
CREATE INDEX IF NOT EXISTS krun_api_keys_org   ON krun_api_keys (org_id);

-- 认证值单独一表：元数据查询永远不 select 它。
CREATE TABLE IF NOT EXISTS krun_api_key_hashes (
  key_id text PRIMARY KEY,
  hash   text NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_api_key_hashes_hash ON krun_api_key_hashes (hash);

CREATE TABLE IF NOT EXISTS krun_invitations (
  source     text NOT NULL,
  subject    text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (source, subject)
);
```

### 2.3 会话与登录事务

```sql
CREATE TABLE IF NOT EXISTS krun_sessions (
  session_hash text PRIMARY KEY,        -- 已存 SHA-256，不存明文
  user_id      text NOT NULL,
  csrf         text NOT NULL,
  auth_version integer NOT NULL,
  cli          boolean NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL,
  seen_at      timestamptz NOT NULL,
  absolute_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_sessions_user   ON krun_sessions (user_id);
CREATE INDEX IF NOT EXISTS krun_sessions_expiry ON krun_sessions (absolute_at);

CREATE TABLE IF NOT EXISTS krun_browsers (
  browser_hash text PRIMARY KEY,
  csrf         text NOT NULL,
  generation   integer NOT NULL DEFAULT 0,
  expires_at   timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS krun_login_states (
  state_hash   text PRIMARY KEY,
  provider     text NOT NULL,
  browser_hash text NOT NULL,
  fingerprint  text NOT NULL,
  verifier     text NOT NULL,
  nonce        text NOT NULL,
  return_to    text NOT NULL,
  generation   integer NOT NULL,
  expires_at   timestamptz NOT NULL,
  consumed     boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS krun_email_logins (
  challenge_hash text PRIMARY KEY,
  provider       text NOT NULL,
  browser_hash   text NOT NULL,
  fingerprint    text NOT NULL,
  email          text NOT NULL,
  code_hash      text NOT NULL,
  return_to      text NOT NULL,
  ip             text NOT NULL,
  generation     integer NOT NULL,
  attempts       integer NOT NULL DEFAULT 0,
  sent_at        timestamptz NOT NULL,
  expires_at     timestamptz NOT NULL,
  consumed       boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS krun_email_logins_ip
  ON krun_email_logins (ip, sent_at DESC);

CREATE TABLE IF NOT EXISTS krun_device_codes (
  code_hash  text PRIMARY KEY,
  user_code  text NOT NULL,
  token      text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL,
  approved   boolean NOT NULL DEFAULT false,
  consumed   boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS krun_device_codes_user_code ON krun_device_codes (user_code);
```

会话表是本方案收益最大的一块，但它自己的行为也随之改变：

- `seen_at` 用**节流写**，不是每请求都写：
  `UPDATE krun_sessions SET seen_at = now() WHERE session_hash = $1 AND seen_at < now() - interval '30 seconds'`
- 过期清理从「每个请求遍历全表」改为后台每分钟一次 `DELETE ... WHERE absolute_at < now()`。
- 认证从「读整个状态文档」变成一次主键查 + 一次 user 主键查。

### 2.4 仓库连接

```sql
CREATE TABLE IF NOT EXISTS krun_repository_connections (
  user_id      text NOT NULL,
  provider_id  text NOT NULL,
  access_token text NOT NULL,
  connected_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, provider_id)
);

CREATE TABLE IF NOT EXISTS krun_repository_connect_states (
  state        text PRIMARY KEY,
  user_id      text NOT NULL,
  provider_id  text NOT NULL,
  browser_hash text NOT NULL,
  expires_at   timestamptz NOT NULL
);
```

### 2.5 Workers

```sql
CREATE TABLE IF NOT EXISTS krun_worker_services (
  service_id          text PRIMARY KEY,
  org_id              text NOT NULL,
  repository          jsonb,                      -- WorkerRepositorySource 或 NULL
  repository_revision integer NOT NULL DEFAULT 0,
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_services_org ON krun_worker_services (org_id);

-- 只存「选了哪些域名」。Hosts / DisplayHosts 从配置里的 domain catalog 现算，
-- 不再持久化 —— 这是现在 syncEnvironmentHosts 反复漂移的根因。
CREATE TABLE IF NOT EXISTS krun_worker_service_domains (
  service_id text NOT NULL,
  domain_id  text NOT NULL,
  ordinal    integer NOT NULL DEFAULT 0,
  PRIMARY KEY (service_id, domain_id)
);

CREATE TABLE IF NOT EXISTS krun_worker_environments (
  service_id         text NOT NULL,
  env_name           text NOT NULL,
  enabled            boolean NOT NULL DEFAULT true,
  config_revision    integer NOT NULL DEFAULT 1,
  variables          jsonb NOT NULL DEFAULT '{}'::jsonb,
  release_revision   integer NOT NULL DEFAULT 0,
  release_id         text NOT NULL DEFAULT '',
  confirmed_release_id text NOT NULL DEFAULT '',
  updated_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (service_id, env_name)
);

CREATE TABLE IF NOT EXISTS krun_worker_builds (
  build_id      text PRIMARY KEY,
  org_id        text NOT NULL,
  service_id    text NOT NULL,
  env_name      text NOT NULL,
  state         text NOT NULL,           -- queued|fetching|building|validating|succeeded|failed
  entrypoint    text NOT NULL DEFAULT '',
  source_digest text NOT NULL DEFAULT '',
  profile       text NOT NULL DEFAULT '',
  job           text NOT NULL DEFAULT '',
  artifact_id   text NOT NULL DEFAULT '',
  error         text NOT NULL DEFAULT '',
  repository    jsonb,
  assets        jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  claimed_by    text NOT NULL DEFAULT '',
  claimed_at    timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_builds_service
  ON krun_worker_builds (service_id, created_at DESC);
-- 控制器只扫未完成项，不再全量遍历。
CREATE INDEX IF NOT EXISTS krun_worker_builds_pending
  ON krun_worker_builds (created_at)
  WHERE state NOT IN ('succeeded', 'failed');

-- 构建输入与日志单独一表：列表 / 详情查询永远不读 32 KiB 日志。
CREATE TABLE IF NOT EXISTS krun_worker_build_private (
  build_id           text PRIMARY KEY,
  actor_user_id      text NOT NULL,
  actor_key_id       text NOT NULL DEFAULT '',
  actor_auth_version integer NOT NULL,
  source_key         text NOT NULL DEFAULT '',
  files              jsonb,
  log                text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS krun_worker_artifacts (
  artifact_id    text PRIMARY KEY,
  org_id         text NOT NULL,
  state          text NOT NULL,
  archive_digest text NOT NULL DEFAULT '',
  manifest_digest text NOT NULL DEFAULT '',
  module_digest  text NOT NULL DEFAULT '',
  size_bytes     bigint NOT NULL DEFAULT 0,
  object_key     text NOT NULL DEFAULT '',
  storage_location text NOT NULL DEFAULT '',
  bucket         text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS krun_worker_releases (
  release_id     text PRIMARY KEY,
  org_id         text NOT NULL,
  service_id     text NOT NULL,
  env_name       text NOT NULL,
  artifact_id    text NOT NULL,
  archive_digest text NOT NULL DEFAULT '',
  module_digest  text NOT NULL DEFAULT '',
  config_revision integer NOT NULL DEFAULT 0,
  variables      jsonb NOT NULL DEFAULT '{}'::jsonb,
  pool           text NOT NULL DEFAULT 'default',
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_releases_service
  ON krun_worker_releases (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_operations (
  operation_id      text PRIMARY KEY,
  kind              text NOT NULL,        -- publish | rollback
  org_id            text NOT NULL,
  service_id        text NOT NULL,
  env_name          text NOT NULL,
  state             text NOT NULL,
  build_id          text NOT NULL DEFAULT '',
  release_id        text NOT NULL DEFAULT '',
  expected_revision integer NOT NULL DEFAULT 0,
  config_revision   integer NOT NULL DEFAULT 0,
  edge_revision     bigint NOT NULL DEFAULT 0,
  release_revision  integer NOT NULL DEFAULT 0,
  error             text NOT NULL DEFAULT '',
  steps             jsonb NOT NULL DEFAULT '[]'::jsonb,
  actor_user_id     text NOT NULL DEFAULT '',
  actor_key_id      text NOT NULL DEFAULT '',
  actor_auth_version integer NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  finished_at       timestamptz,
  claimed_by        text NOT NULL DEFAULT '',
  claimed_at        timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_operations_service
  ON krun_worker_operations (service_id, env_name, created_at DESC);
CREATE INDEX IF NOT EXISTS krun_worker_operations_pending
  ON krun_worker_operations (created_at)
  WHERE state NOT IN ('succeeded', 'failed', 'available', 'superseded');

CREATE TABLE IF NOT EXISTS krun_worker_history (
  service_id text NOT NULL,
  env_name   text NOT NULL,
  revision   integer NOT NULL,
  release_id text NOT NULL,
  operation_id text NOT NULL,
  kind       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (service_id, env_name, revision)
);
CREATE INDEX IF NOT EXISTS krun_worker_history_recent
  ON krun_worker_history (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_idempotency (
  idem_key   text PRIMARY KEY,
  digest     text NOT NULL,
  target_id  text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_idempotency_age
  ON krun_worker_idempotency (created_at);
```

`krun_worker_operations` 里的 `actor_*` 是从 `WorkerOperationPrivate` 内联进来的：它只有三个标量，
不需要单独一张表，但**绝不能出现在列表查询的 select 列表里**（沿用现在的隐藏语义）。

### 2.6 保留不动

- `krun_audits`（追加型事件流，已有独立表）
- `worker_config_revisions`（边缘配置不可变修订日志）
- `worker_runtime_acks`（每 Pod 心跳）

这三张已经是正确形状，只补索引与保留期，不改结构。

---

## 3. 一致性模型

按重要性分三档，写路径显式选择，不做统一抽象。

### 第 1 档：单语句（默认）

一条 `INSERT` / `UPDATE` / `DELETE` 自带原子性，不需要事务块。绝大多数写都属于这一档：
会话 `seen` 节流、`enabled` 开关、构建状态迁移、域名选择、变量更新、撤销 key。

### 第 2 档：小事务（少量）

只用于「半个写会让元数据自相矛盾」的地方，限定 2–5 条语句、同域、无网络调用：

| 场景 | 语句 |
| --- | --- |
| 建组织 | `INSERT org` + `INSERT member` + `INSERT assignment` |
| 删成员 | `DELETE member` + `UPDATE 组员` + `DELETE assignments` |
| 删组织/资源/组 | 主体 `DELETE` + 关联行 `DELETE` |
| 建 key | `INSERT krun_api_keys` + `INSERT krun_api_key_hashes` |
| 建构建 | `INSERT build` + `INSERT build_private` + `INSERT idempotency` |
| 建发布操作 | `INSERT operation` + `INSERT idempotency` |

### 第 3 档：补偿（不变量靠检查 + 回滚）

`ensureAdmin`（至少保留一个系统管理员）这类跨行不变量，**不**用锁去保证：

```
UPDATE ... SET status='disabled'
→ SELECT count(*) 存活的 system_admin
→ 0 个则 UPDATE 回滚并返回 409
```

两个并发降级可能同时通过检查。这是接受的：不是交易系统。副作用是可能短暂出现零管理员，
需要人工 `krun-server bootstrap`（该命令已存在）。实现时打一条 WARN 日志留下痕迹。

### 明确放弃的保证

- 登录写会话与更新 `auth_version` 不再同一事务。会话校验本来就会比对 `auth_version`，
  半写只会导致「这次登录要重来」，不会放行已撤销的身份。
- 跨域（IAM × Workers）永不共享事务。

---

## 4. 发布与构建：最终一致

### 4.1 操作行即队列，用 CAS 认领取代 advisory lock

现在 `workers_controller.run()` 每秒抢 `pg_try_advisory_lock(724618031)`，拿到锁的副本独占
全部 reconcile。改为每行独立认领：

```sql
UPDATE krun_worker_operations
   SET state = 'validating', claimed_by = $replica, claimed_at = now()
 WHERE operation_id = $1
   AND state = 'queued'
RETURNING *;          -- 0 行 = 别人先抢到，跳过
```

同样的模式用于构建（`queued|fetching → building`）和发布提交
（`validating → committed` 带 `expected_revision` 条件）。

收益：

- 不再需要选主。多个副本可以并行处理不同服务/环境的操作。
- 认领是原子的，不用行锁，不怕慢请求占住全局锁。
- 副本崩溃后，`claimed_at` 超时即可被其他副本接管（租约 60 秒）：

```sql
UPDATE krun_worker_operations SET claimed_by = $replica, claimed_at = now()
 WHERE operation_id = $1 AND state = 'validating'
   AND claimed_at < now() - interval '60 seconds'
RETURNING *;
```

### 4.2 reconcile 循环

保留 1 秒 ticker（延迟仍 ≤ 1 秒），但每次只查未完成项：

```
SELECT ... FROM krun_worker_operations WHERE state NOT IN (terminal) ORDER BY created_at LIMIT N;
SELECT ... FROM krun_worker_builds     WHERE state NOT IN (terminal) ORDER BY created_at LIMIT N;
```

两条查询都走部分索引，空闲时是 0 行。对比现在的「读 39 KB + 两次 JSON 往返」。

`publishConfig` 的 digest 幂等逻辑保持原样，但输入改为
`SELECT services + domains + environments + 被引用的 releases`，不再整份快照。

### 4.3 边缘收敛

不变：写入 `worker_config_revisions` → 快照进对象存储 → runtime 长轮询 + ack。
这条链路本来就是最终一致，且已有 resync 语义，不需要改。

---

## 5. 授权如何继续工作

`Authorize(st, p, action, target)` 和 Cedar 编译逻辑**一行不改**。改的是 `st` 的来源：
不再反序列化整份文档，而是按需求构造一个「授权视图」。

`Store.AuthzView(ctx, principal)` 用 4–6 条带索引的查询拼出 `*State`：

| 需要的行 | 查询 |
| --- | --- |
| 用户 | `krun_users` 主键 |
| 组织（全量，量小） | `krun_organizations` + `krun_org_sources` |
| 用户所属组 | `krun_group_members JOIN krun_groups WHERE user_id = $1` |
| 相关分配 | `krun_assignments WHERE (subject_kind='user' AND subject_id=$1) OR (subject_kind='group' AND subject_id = ANY($2))` |
| 分配引用的资源 | `krun_resources WHERE resource_id = ANY($3)` |
| 当前 key（若有） | `krun_api_keys` 主键 |

`compileUser` 只遍历 `Assignments` / `Groups` / `Members`，且全部以「该用户或该用户所在组」为条件，
所以这个子集是**语义等价**的，不是近似。`orgList` 需要全量组织，单独放开即可。

这样做的代价是每请求多几次索引查询（亚毫秒级），换来的是完全不动授权逻辑。
比在表上重写一遍 Cedar 组合安全得多。

---

## 6. 数据保留（新增，必须做）

这是当前唯一无界增长的部分：`Builds` / `BuildPrivate` / `Operations` / `Idempotency` /
`Histories` 一旦写入永不删除，`Histories` 还是无上限 `append`。

| 表 | 策略 |
| --- | --- |
| `krun_worker_builds` + `_build_private` | 每服务保留最近 100 条终态；`log` 在构建完成 7 天后清空 |
| `krun_worker_operations` | 终态保留 30 天 |
| `krun_worker_artifacts` | 无 release 引用且超过 30 天则删（对象存储同步删） |
| `krun_worker_idempotency` | 7 天 |
| `krun_worker_history` | 每 (service, env) 保留最近 50 条 |
| `krun_sessions` | `absolute_at` 过期即删；后台每分钟清理 |
| `krun_browsers` / `krun_login_states` / `krun_email_logins` / `krun_device_codes` | 过期即删，同上 |
| `krun_audits` | 维持 7 天 |
| `worker_config_revisions` | 维持 512 条 |
| `worker_runtime_acks` | 维持 1 分钟 |

清理统一放在一个后台 goroutine（每分钟），用 `DELETE ... WHERE ...` 单语句完成。
请求路径上不再有 `cleanup(st)`。

---

## 7. 迁移与回填

1. 启动时执行幂等 DDL，`krun_meta.schema_version` 记录版本。
2. 若 `krun_control_state` 存在且新表为空，则在一个事务内一次性导入：
   - `Sources` → `krun_identity_sources` + `krun_identity_providers`（按 `provider:` / `namespace:` 前缀拆解）
   - `Users` / `Organizations` / `Members` / `Groups` / `Assignments` / `Resources` / `Keys` / `KeyHashes`
   - `Sessions` / `Browsers` / `Logins` / `EmailLogins` / `Devices` / `Invitations`
   - `RepositoryConnections` / `RepositoryConnectStates`
   - `Workers.*` 全部子 map
   - `Histories` 的 `service/env` 复合键拆成两列
3. 导入成功后把旧表改名：`ALTER TABLE krun_control_state RENAME TO krun_control_state_legacy`
   （不删，留作回滚与对账）。
4. 导入失败则整体回滚，服务**拒绝启动**（fail closed），旧数据完好。
5. 回滚方式：把二进制回退一个版本 + 把 legacy 表改回原名。不做双写。

导入脚本建议放 `internal/control/migrate.go`，用现有 `pgx`，不引入迁移框架。

---

## 8. 代码改动清单

### 新增

- `internal/control/schema.sql`（或 `schema.go` 内嵌常量）
- `internal/control/store_iac.go`：用户 / 组织 / 成员 / 组 / 分配 / 资源 / key
- `internal/control/store_auth.go`：会话 / 浏览器 / 登录事务 / 设备码 / AuthzView
- `internal/control/store_workers.go`：服务 / 环境 / 构建 / 制品 / 发布 / 操作 / 历史 / 幂等
- `internal/control/migrate.go`：快照导入
- `internal/control/retention.go`：后台清理

### 改写

- `store.go`：删掉 `Update/View(fn(*State))` 这一层；保留 `OpenStore`、审计、config revision、ack。
- `server.go`：`protected()` 不再包 `Store.Update`；认证 → AuthzView → handler 直接执行 SQL。
  删除 `cleanup(st)` 调用与回滚用的 `json.Marshal(st)`。
- `provider.go` / `email.go`：9 处 `Update` 改为针对性语句（会话、浏览器、登录事务、设备码）。
- `orgs.go` / `members.go` / `groups.go` / `assignments.go` / `resources.go` / `keys.go` / `users.go`
  这批 handler 从「改内存 map」改为「构造 SQL」。它们的函数签名现在是 `func(*State, Principal, *http.Request)`，
  需要改成接收一个仓储接口（或 `*Store` 加 principal），不再接收整份 `State`。
- `workers_api.go` / `workers_controller.go`：这两处合计 13 处 `Update` 改为 CAS 认领 + 单行更新。
  删除 `workerService()` 的**读时自愈写**（GET 不再产生写）。
- `workers_repository_build.go`（4 处）、`workers_repository_connection.go`（1 处）、
  `workers_invoke.go`（1 处）：同上；`workers_invoke.go` 的那处只用于认证 + 取目标，改为纯读。
- `workers_model.go`：`syncEnvironmentHosts` / `syncServiceHosts` 退化为纯函数，
  由 catalog + 已选域名现算，不再持久化。

### 可以删除

- `State` / `WorkerState` 作为**持久化模型**（保留为授权视图形状 + 测试夹具）。
- `newState()` 的大部分 map 初始化。
- `workerConfigFromState` 里的 `st.Workers` 遍历，改为两个查询。

---

## 9. 测试策略

现行 43 处 `store.Update(func(st *State))` 测试直接改内存 map。拆表后不再成立。

**建议：删除内存 Store，测试统一跑真实 PostgreSQL。**

- 保留 `memoryStore()` 只给纯函数测试（authorizer、policy 编译、workerHost 推导）。
- handler 级测试改用 `postgresTestStore(t)`，每个测试用独立 schema 或临时数据库。
- `scripts/dev-postgres.sh` 已能起本地 PG；`make test` 前自动建 `krun_test` 库。
- CI 已有 PostgreSQL service，`KRUN_TEST_POSTGRES_URL` 已支持。

代价：单测变慢（每条几十毫秒），且本地没库时无法跑 handler 测试。
收益：只有一份实现，不会出现「内存 Store 通过、Postgres 行为不同」这类最难查的偏差。

必须新增的验收用例：

1. 并发 bootstrap 仍然只有一个成功（现有 `postgres_test.go` 保留）。
2. 同一构建被两个副本并发认领，只有一个 `claimed_by` 写入成功。
3. 会话 `seen` 节流：1 秒内 100 次认证请求只产生 1 次 UPDATE。
4. 删除组织后没有残留成员 / 分配（显式级联生效）。
5. 快照导入：一份含全部子结构的旧快照导入后，各表行数与内容与快照一致。
6. 零管理员窗口：并发降级两人，最终状态必须是「至少一个管理员」或明确报错。

---

## 10. 分步实施顺序

每步可独立提交、独立回滚，前一步不阻塞后一步。

| 步骤 | 内容 | 风险 |
| --- | --- | --- |
| 1 | schema + 幂等 DDL + `AuthzView` + 导入器（此时仍读旧表） | 低，纯新增 |
| 2 | **会话 / 登录事务 / 身份源**切到表（收益最大） | 中，登录链路必须回归 |
| 3 | IAM（组织 / 成员 / 组 / 分配 / 资源 / key）切表 | 中，授权用例必须全绿 |
| 4 | Workers 切表 + CAS 认领 + 租约，去掉 advisory lock | 中，e2e 必跑 |
| 5 | 删除 `State` 持久化路径，legacy 表改名归档 | 低 |
| 6 | 保留期清理上线 | 低 |

步骤 2–4 期间，未迁移的域仍读写 `krun_control_state`，Store 按域路由。
这会引入一段双源期，需要明确：**同一域不允许一半在表、一半在文档**。

---

## 11. 预期效果

| 指标 | 现在 | 之后 |
| --- | --- | --- |
| 认证 GET 的写 | 全量读 + 3 次全量序列化 + 全量写 + 行锁 | 1 次主键读 + 0 或 1 条节流 UPDATE |
| 单次写成本 | 与总体积成正比（当前 39 KB） | 与行大小成正比（几十字节 ~ 几 KB） |
| 并发写 | 全局单行锁串行 | 不同行并行；同状态机用 CAS |
| 空闲 reconcile | 2 次全量反序列化 + 13 处潜在全量写 | 2 条走部分索引的查询，0 写 || 数据增长 | Builds/Operations/Idempotency/History 无界 | 全部有保留期 |

---

## 12. 实施结果（实现后回填）

已按第 10 节的顺序落地，与本文的差异如下：

1. **`State` 保留为工作副本**。没有把 handler 改成仓储接口，而是让 `Store.Update(ctx, partition, fn)` 加载相关分区、执行回调、只写差异行。这样约 25 个 handler 一行未改，回收的收益（只写变化的行）同样成立。
2. **授权不做部分加载**。每次请求加载完整 IAM 分区，但 14 条查询合并在一个 `pgx.Batch` 里，实测约 0.2 ms。按主体裁剪被判定为安全风险：漏加载一行就会错误拒绝或错误放行。
3. **读请求不克隆前像**。`persist = 0` 时跳过 diff 用的快照，GET 只剩一次批量读。
4. **hosts 不再持久化**，由域名目录现算；`workerConfigFromState` 因此需要域名目录与 IAM 分区（判断服务是否仍存活）。
5. **CAS 认领取代 advisory lock**，但 reconcile 仍每秒一次。未改成事件驱动：见下方待办。
6. **内存 Store 保留**，仅用于测试。会话相关的单行语句在无数据库时退化为对内存副本的操作。

实测（同一份 38 KB 旧快照迁移后的库，本地 Postgres）：

| 指标 | 结果 |
| --- | --- |
| `loadIAM`（14 查一批） | ~0.18 ms |
| `loadAll`（IAM + Workers，25 查一批） | ~0.24 ms |
| 空 `Update(PartitionIAM)` | ~0.20 ms |
| 单会话主键查 | ~0.05 ms |
| 旧快照导入 | 成功，行数与快照逐表一致 |

新增的验收用例（`internal/control/postgres_split_test.go`）：

- 三个副本并发认领同一构建，只有一个成功；
- 两个副本并发认领同一操作，只有一个成功；过期租约可被接管，新鲜租约不可；
- `expected_revision` 不匹配时发布不提交，环境行保持不变；
- 会话 `seen_at` 在节流窗口内不写、窗口外刷新；
- 删除组织后成员与分配不残留（无外键，靠显式级联）。

## 13. 尚未完成

1. reconcile 仍是每秒轮询。改成「变更触发 + 30 秒兜底」需要跨副本广播，`SignalConfig` 目前是进程内的，要新增 `LISTEN/NOTIFY` 或用 revision 表做触发源。
2. 组织 / 资源列表未分页下推。
3. `ensureAdmin` 的补偿语义（并发降级可能短暂零管理员）。
4. `krun_control_state_legacy` 保留着，确认无回滚需求后可删除。

## 14. 需要评审决定的点

1. **测试是否彻底放弃内存 Store、改为必须 PG**？（影响本地方便程度与 CI 时长）
2. **第 3 档补偿语义是否接受**：并发降级可能短暂出现零管理员，需要人工 bootstrap 恢复。
3. **双源过渡期**（步骤 2–4）是否可接受，还是希望一次性切完（一次性提交更大、更冒险）。
4. `krun_worker_environments` 不再持久化 hosts，改为现算 —— 是否同意？
   这会让「控制台看到的 host」与「边缘投影里的 host」只能来自同一个函数，消除漂移，
   但配置目录变更后旧数据不再有可对照的历史快照。
5. 旧表改名归档是否保留，还是导入成功后直接删除。
