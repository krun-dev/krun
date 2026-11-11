# AGENTS.md

本仓库 = **krun 控制台**（React + Vite 静态 SPA，`web/`）+ **Go 控制面**（`krun-server`，`internal/`）。

## 0. 两条硬规则

**规则 1 — 生产前端是静态导出，上传 OSS，运行期没有 Node。**
本地调试可以用 Nginx 或临时 Node 代理，但功能正确性不能依赖它们。

**规则 2 — 对外路由只有两个出口：`/` 给 OSS，`/api/*` 给 Go。**
所有后端接口统一挂在 `/api/` 下；不再有 `/auth`、`/healthz` 这种与 `/api` 平级的前缀。

对外路由契约：

| 路径 | 去向 | 说明 |
| --- | --- | --- |
| `/`、`/assets/*` | OSS | 静态导出产物，SPA 入口 |
| `/api/*` | Go | 全部接口：`/api/v1/*`、`/api/auth/*`、`/api/healthz`、`/api/internal/*` |
| 其他 | 404 | 没有第三个出口 |

网关只需一条 catch-all：

```
/api/*   ->  Go server
/*       ->  OSS 静态站点（default document: index.html）
```

这条契约同时覆盖生产、本地和验证环境。**不要再新增根级前缀。**

## 1. 接口一律收在 /api 下

后端只有一张路由表（`internal/control/server.go` 的 `routes()` + `apiRoutes` + `workersRoutes`）。规范前缀：

| 前缀 | 用途 |
| --- | --- |
| `/api/auth/*` | 登录、身份、会话与 CLI 授权 |
| `/api/healthz` | 控制面健康检查 |
| `/api/internal/workers/*` | 内部 listener 的 Runtime API |
| `/api/v1/*` | 业务与管理 API |

新增接口必须使用这些规范前缀，使网关始终只需转发 `/api/*`。

`/api/` 下的未知路径统一返回 `RESOURCE_NOT_FOUND`，与既有 `/api/` 兜底一致。

## 2. ⚠️ 生产：OSS 静态 + 一条 `/api/` 转发

**光把文件传上 OSS 不够——OSS 只服务静态文件，不会把 `/api/` 转发到后端。** 而当前前后端都要求同源：

- 后端**不返回任何 CORS 头**（全仓库零实现）。
- Session Cookie 是 `HttpOnly + SameSite=Lax`（生产 `Secure`，见 `internal/control/server.go` 的 `setCookie`）；跨 origin 的 `fetch` 带不上凭证。
- 前端请求全是同源相对路径（见 `web/src/lib/api.ts`）。

所以 OSS 前面**必须**有同源的 CDN / 反向代理按第 0 节的契约做路径分发：

- 三个部分对浏览器必须是**同一个 origin**（同域名、同 scheme、同端口）。
- `public_url` 配成这个对外 origin（如 `https://console.example.com`）；SSO 回调、CLI 都统一用它。
- 如果托管平台不支持同域路径转发（纯静态 Pages、无代理的 bucket 域名直连），这套控制台**跑不起来，不能发布**。
- 不要用「前端发到另一个域名」来绕：那需要后端加 CORS + `SameSite=None`，当前明确不支持。

## 3. 前端：base 就是 `/`

- 构建产物 `npm run build` → `dist/console/`，Vite `base: "/"`、`root: web`、`outDir: ../dist/console`。产物上传 OSS 根路径。
- 所有请求用**同源相对路径**：`/api/auth/*`、`/api/v1/*`。永远不要拼 `http://host:port`。
- 页面内导航保持**查询参数**（如 `/?page=workers&workspace=...`），不需要 SPA 路径回退。
- 不得在运行期注入后端域名 / 端口 / 凭据；不得读非公开环境变量决定行为。
- 所有前端页面都必须支持浅色与深色两种配色，并提供可见的主题切换入口；首页、登录/加载页和登录后的控制台页面都要覆盖。
- 主题状态全站共享：用根元素 `.dark` class 和 `localStorage` 的 `krun.theme` 保存；页面切换不得重置用户选择，也不得另设页面专属主题键。
- 新增或修改页面时，检查两种主题下的文字、背景、边框、控件与状态提示对比度；优先使用主题 token 和 `dark:` 样式，避免只适用于单一配色的硬编码颜色。
- 前端**不得**写入任何需要服务端运行时的东西：SSR、Server Actions、API Routes、middleware、SSR-only 动态路由、服务端数据预取。
- 前端**不得** import 服务端模块：`fs` / `net` / `tls` / `http` / `https` / `child_process` / 数据库或 Redis 客户端 / 服务端 SDK。
- 不引入 Service Worker / 前端侧 serverless 运行时；不做 BFF、不做 CORS 代理、不做运行时代理注入。

## 4. 环境分工

| 环境 | 前端怎么跑 | 允许的做法 |
| --- | --- | --- |
| 本地开发 | 静态产物 + 本地代理 | ✅ Nginx ✅ 临时 Node 代理（Vite `server.proxy` 等） |
| 生产 | 静态导出上传 OSS | ❌ 任何 Node / 前端代理进程；✅ OSS + CDN / 反向代理 |

本地用 Nginx 或临时 Node 代理**没问题**，是工具不是架构。底线只有一条：**生产不能依赖它们**。写功能时如果发现「只有开了本地代理才行」，要么改成同源直连，要么让部署侧的 CDN / 反向代理补上。

本地推荐：

1. `npm run build`（改前端时可 `npx vite build --watch`）产出 `dist/console/`。
2. 让 `/api/*` 到 Go，三选一：本地 Nginx、Vite `server.proxy`、或让 Go 直接托管（`static_dir: dist`，访问 `http://127.0.0.1:8080/`）。
3. Workers 日常开发用 **local-first**：控制面、Postgres、builder、Vite 都在开发机；对象存储可用本地 MinIO 或主配置中的 S3 兼容服务；集群只保留 serving runtime、独立 validator（均为 workerd）+ kgateway。见 [docs/local-dev.md](docs/local-dev.md) 和 `scripts/dev-up.sh`。`deploy/workers-kind` 的完整栈只用于独立 e2e，不要和 local-first 同时运行。

本地这些进程只在开发机上存在，不进生产、不进发布产物。

## 5. 提交前检查清单

- [ ] `npm run build` 成功，产出 `dist/console/index.html` 与 `dist/console/assets/`。
- [ ] 新增接口都注册在 `/api/` 下，没有新增根级前缀。
- [ ] 全部网络请求都是同源相对路径。
- [ ] 没有新增对本地代理的**运行期**依赖。
- [ ] 发布前确认部署目标支持「OSS 静态 + 同域转发 `/api/*` 到 Go」；不支持就**不要发布**。
- [ ] 验证功能时打**同源入口**，不要只 curl Vite / 本地代理端口。
- [ ] `make check-secrets` 通过；没有 token、密码、私钥、真实内网 IP/主机名或 SSO client id 进入 tracked 文件。
- [ ] 新增配置项进了主 `Config` / `WorkersConfig`，没有新增独立的 dev 配置文件；`.data/` 没有被 `git add -f`。

静态化发布前用发布拦截脚本自查：

    python3 $STATIC_EXPORT_GUARD/check_static_export.py .  # 本机工具路径，按需调整

（脚本会对 `scripts/`、`deploy/` 下的 Node 工具报 blocker；那些是开发工具，不是发布产物。真正要保证 `publish_allowed` 为 `true` 的是 `web/` 的构建产物。）

## 6. 当前基线

项目尚未上线，以当前代码、主配置与 `internal/storage/schema.sql` 为唯一基线。
不保留旧路由、旧制品格式、旧库导入、补列或回填兼容。数据库初始化保存 schema
fingerprint；已有库与基线不一致时拒绝启动，由开发者显式重建专用开发/测试库。
禁止启动时自动删库、删表或重写旧数据。后续模型修改直接更新完整建表定义，正式上线
后的升级策略另行制定。

文档只描述已实现接口、运行约束和复现步骤；不保留评审稿、实施阶段或未实现方案。
当前架构入口见 [docs/README.md](docs/README.md)。

## 7. 当前状态（已核实）

- 后端接口统一在 `/api/` 下，前端 `base` 为 `/`。
- 日常开发使用 **local-first** 拓扑，见 [docs/local-dev.md](docs/local-dev.md)：控制面、Postgres、builder、Vite 在开发机；对象存储可用本地 MinIO 或主配置中的 S3 兼容服务；集群只保留 serving runtime、独立 validator（均为 workerd）+ kgateway。
- `deploy/workers-kind` 是完整独立 e2e 环境（backend/Postgres/MinIO 都在集群里），已标注不要和 local-first 同时运行。
- 生产 OSS + CDN 的 `/api/*` 同域转发规则仍需部署侧落地（第 2 节）。

## 8. 配置与密钥收口（硬约束）

1. **唯一应用配置**：本地开发只允许 `.data/dev.yaml`（已被 `.gitignore` 忽略）。禁止新增 `config/development.yaml`、`.data/development-*.yaml`、`.data/sso-local.yaml` 或按组件拆分的 YAML。新增配置项必须加到主 `Config` / `WorkersConfig`，并同步更新 `config/example.yaml`、`config/development.example.yaml` 以及加载测试。
2. **唯一 shell/基础设施变量文件**：`.data/dev.env`（gitignored），只放远端主机、控制面/MinIO IP、kind 集群名等脚本变量。`scripts/dev-*.sh` 自动读取，但命令行已设置的变量优先；tracked 脚本里不得写内网默认值。
3. **tracked 文件只放占位符或环境变量引用**：token、密码、私钥、数据库/S3 凭据、真实内网 IP/主机名、SSO client id 一律不得出现在 `git ls-files` 能看到的内容里。示例用 `${KRUN_*}` 或 `replace-me`。
4. **`.data/` 永远不提交**：不要 `git add -f .data/...`；不要为了“方便”把 secret 复制到 `config/` 或文档里。
5. **提交前必须**：`make check-secrets` 通过。它会用 `.data/dev.yaml` / `.data/dev.env` 里的真实值扫描 tracked 文件，并检查私钥 / AWS key 模式；`make test` 也会执行。
6. **配置加载只有一处**：所有运行期配置从 `LoadConfig` 进入；不要新增运行期读取非公开环境变量的开关。地址、profile、运行模式都必须显式出现在主 YAML。Workers 配置也在同一份 YAML 的 `workers:` 段，不允许独立文件或目录。
7. **配置示例必须可加载**：`config/development.example.yaml` 和 `config/example.yaml` 由测试覆盖；示例只能使用占位符，不能写真实凭据。
8. **dev 拓扑保持一致**：local-first；控制面/Postgres/builder/Vite 在开发机；对象存储使用本地 MinIO 或主配置中声明的外部 S3/TOS。集群只保留 serving runtime、独立 validator + kgateway。变更拓扑时同步修改 `docs/local-dev.md` 和 `scripts/dev-*.sh`。

本地真实 TOS/STS 联调凭据保存在本仓库 `.data/dev.yaml`：制品使用 `workers.s3_*` 与
`workers.s3_runtime_credentials`，业务存储使用 `workers.object_storage`。后续调试直接读取
这份配置，不把凭据复制到脚本、命令行参数、文档或记忆中；记忆只记录本段路径与用途。

## 9. 持久化设计约束（硬约束）

方案与表结构见 [docs/storage.md](docs/storage.md)。

1. **一实体一行，禁止整文档快照**。不要再新增 `jsonb` 大文档承载多个实体。`State` 仅为请求工作副本，不作为数据库持久化格式。
2. **不使用外键**。`REFERENCES` / `FOREIGN KEY` / `ON DELETE CASCADE` 一律不写。引用完整性靠三件事：
   - 写入路径先校验被引用对象存在；
   - 删除路径在同一事务里显式删关联行；
   - 定期对账查询发现孤儿行（孤儿行不等于数据损坏，按垃圾清理）。
   理由：外键会引入跨表锁与删除顺序约束，和「尽力而为」的事务目标冲突，也让拆表 / 归档 / 批量清理变难。
3. **事务只做尽力而为的保证**。目标不是交易系统，只保证「元数据自身对得上」：
   - 默认单语句，不加事务块；
   - 只有「半个写会让元数据自相矛盾」时才用有界小事务；按职责拆成清晰步骤，允许多条简单 SQL，以批量发送控制往返，不得含外部网络调用；
   - 跨实体（尤其 IAM × Workers）不共享事务；
   - 需要不变量时优先用 `CHECK` / `UNIQUE` / 条件 `UPDATE`（CAS），不要用 `SELECT ... FOR UPDATE` 全局串行化。
4. **构建 / 发布状态只要求最终一致**。操作行（operation / build）就是持久化工作队列：
   - 状态迁移用条件更新认领，例如
     `UPDATE ... SET state='validating', claimed_by=$1 WHERE operation_id=$2 AND state='queued'`，
     影响行数为 0 即表示别人先认领；
   - 用 `claimed_at` 租约超时接管崩溃副本，不用 advisory lock 选主；
   - 允许同一操作被重试多次，所有副作用必须幂等；
   - 边缘侧靠 config revision + snapshot + ack 收敛，不要求提交即生效。
5. **不留无界数据**。新增任何「每请求 / 每次构建 / 每次发布产生一行」的表，必须同时给出保留期与清理语句，并在同一提交里接上后台清理。禁止无上限 `append`。
6. **热路径不写**。认证、列表、健康检查等读路径不得产生写。需要记录 `last_seen` 之类的字段时用节流写（例如 30 秒一次），不要每请求都写。

## 10. 代码分层边界（硬约束）

- `internal/control` 负责 HTTP、Cookie/CSRF、路由权限门禁、解码、响应和依赖装配；认证、IAM、Workers、仓库、Runtime 配置及审计调用对应独立模块。不得包含 SQL、导入数据库驱动或访问连接池。
- `internal/iam` 负责组织、成员、用户组、角色、资源目录、API Key、邀请及初始化的业务校验和状态修改；接收类型化参数，不接收 HTTP 请求，不依赖 `control`、`storage` 或外部服务客户端。调用方先解析和授权目标，业务返回错误后丢弃工作副本。
- `internal/authorization` 负责 Cedar、权限交集、策略校验及资源解析；只依赖共享模型和 Cedar。授权缓存仅供单次只读请求使用，不进入 `State` 或存储层。
- `internal/storage` 集中 SQL、行映射、事务、CAS 和数据库清理，按认证、IAM、Workers、配置分发等领域组织。对外只提供有业务含义的类型化存储方法，不暴露 `pgx.Tx`、Rows 或连接池。
- `internal/domain` 保存共享数据类型，只依赖标准库。待提交命令必须显式 `json:"-"`，不得进入序列化快照。通用随机凭据、ID 与摘要实现集中在 `internal/securetoken`。
- 完整 DDL 唯一来源是 `internal/storage/schema.sql`，由同目录 `schema.go` 嵌入并校验 fingerprint；不增加自动迁移或启动回填。
- 改表时集中检查 DDL、domain 类型、对应 storage 文件和数据库测试。分层边界由 `TestStorageLayerBoundaries` 和 `TestIAMHandlersDoNotMutateEntities` 自动检查。当前实现边界见 [docs/architecture.md](docs/architecture.md)。

- 业务模块为 `auth`、`iam`、`authorization`、`workers`、`repository`、`runtimeconfig` 和 `audit`；不得导入 HTTP、数据库驱动或基础设施 SDK，不得接收 `http.Request`。源码与制品校验在 `artifact`，配置在 `config`。
- 身份提供商、Git/仓库 OAuth、Kubernetes、S3、邮件、本地 builder、Runtime 管理和控制台调用实现统一放在 `internal/adapters/*`；适配器不得反向导入 HTTP 入口或包含数据库语句。
- HTTP handler 不得直接修改 `State` 实体或 revision。读取所需范围、传递类型化输入、连接权限/审计和映射业务结果属于入口职责。

- ORM 使用 Bun，仅允许在 `internal/storage` 及其子包中导入。数据库行模型在 `internal/storage/models`，由 `schema.sql` 生成；改表后运行 `go generate ./internal/storage/models`，提交前 `make check-storage-models` 必须通过。
- 不使用 ORM 自动建表、迁移、关联保存、软删除或级联。更新显式指定列，CAS 显式校验 revision 和影响行数；标准 CRUD 用模型，批量投影、租约、队列及清理允许保留专用 SQL。ORM 与原生 pgx 复用同一连接池。

## 11. 可读性与格式

- 手写代码按人的阅读顺序组织：一个语句一行，用空行分开校验、准备、执行和返回。
- 条件和循环体使用大括号，多个变量分别声明；避免把多个动作挤进一行。
- 长条件、调用参数、结构体和嵌套配置要换行；不要为了少占行数压缩逻辑。
- 过长的流程按职责拆成有意义的小函数，命名说明实际用途；不要为了格式拆出无意义的包装。
- 测试中的 Worker 源码也保持可读的多行格式，不写成一长串字符串。
- Go 使用 `gofmt`；Workers JavaScript 使用仓库 Prettier 配置，运行
  `npm run format:workers` 和 `npm run check:workers-format`。格式化不替代人工检查逻辑分段。
- 前端 `web/src/` 的 TypeScript、TSX、JavaScript 测试、CSS 和 JSON 使用仓库安装的 Prettier。
  `npm run format:web` 统一格式化，`npm run check:web-format` 检查格式；后者接入 CI 和 `make test`。
- JSX 多个属性按 `singleAttributePerLine` 配置分行，格式以 Prettier 输出为准；`printWidth`
  是排版目标，长文案和字符串不强行拆分。VS Code 使用推荐的 Prettier 扩展，保存时自动格式化。
- 修改前端后，先用 `npx prettier --write <本次修改的文件>` 格式化，再运行
  `npm run check:web-format`、`npm run check` 和 `npm --prefix web test`。不得通过
  `prettier-ignore`、缩小检查范围或关闭检查来规避格式要求。
- 组件按职责组织；复杂状态计算和业务流程提取为具名函数或业务 Hook，重复的界面抽成组件。
  简单事件处理、翻译调用和 Tailwind 类名可以留在 JSX 中，不为拆文件而拆分。

## 12. 使用 Git worktree 隔离功能开发

- 开发项目时先创建独立 worktree，并拉取最新代码后再开始开发。
- 开发功能时可以使用 `git worktree` 创建独立工作目录和分支，隔离不同任务的改动，减少并行开发冲突。
- 当前工作目录已有未提交改动或有其他任务进行时，优先使用独立 worktree；保留原目录中的已有改动，不要覆盖、重置或清理其他任务的文件。
- 新分支默认使用 `codex/` 前缀；完成后只合并当前任务相关改动，并在确认改动已保留后清理对应 worktree。

## 13. PR 与提交信息语言（硬约束）

- PR 标题和描述必须使用中文。
- Git commit message 的标题和正文必须使用中文，包括合并或 squash 时生成的提交信息。
- 代码标识符、文件路径、命令及必要的技术专有名词可保留原文；若使用 Conventional Commits，类型和作用域可保留英文，说明部分必须使用中文，例如 `docs: 明确 PR 与提交信息使用中文`。

## 14. 中英文 README 同步维护

- 根目录的 [README.md](README.md) 为简体中文版，[README.en.md](README.en.md) 为英文版。每次修改任一版本的内容，都在同一个 PR 中同步更新另一版，保持功能、兼容范围、限制和文档入口一致。
- 调整 Workers binding 的支持范围时，同步更新两版 README 的概览与[兼容测试表](docs/verification.md#workers-bindings-兼容测试)中对应的覆盖范围。
