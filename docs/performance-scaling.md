# 规模边界与验证

常用请求的实现要求是让常用请求、发布和后台协调的成本由目标实体、增量大小或有界工作集决定。
百万站点元数据测试与 kind 功能测试分别验证数据规模和真实链路；不将它们等同于生产 QPS 承诺。

## 当前实现

| 范围 | 实现 | 边界 |
| --- | --- | --- |
| 控制面读取 | 站点 API 按组织/资源过滤 SQL；列表用资源 ID 游标；Runtime 按 Version/Artifact 主键查询；登录只读当前 identity，启动只注册来源；导航只加载当前用户的授权上下文 | 管理员的批量 IAM 管理操作仍保留独立的管理查询语义；没有给它们伪造授权缓存 |
| 网关协调 | 共享路由按 pool × domain 建通配 HTTPRoute；不加载全站点拓扑；命名空间清单一次分页读取并复用；已就绪路由每 30 秒修复，未就绪时每秒重试 | 共享路由是唯一入口模型 |
| 发布 | 稳定 host/path 绑定与 deployment/Version 分离；逐资源代次；归一化合并通知；每轮最多处理 128 个待重建 Worker | 普通切版本不重写绑定；域名/路径目录变化仍需有界重建；没有全局 head 提交 |
| Runtime 配置 | Pod 级 Rust agent、一条 Protobuf WebSocket、按需资源图、当前状态恢复、主库代次复核、受保护的有界缓存 | 每个副本都要为其订阅工作集复核版本；不同 Pod 的活跃集合可能重叠；冷加载和依赖授权仍有成本 |
| 资产 | finalize 独立保存有 digest 的 manifest；控制面用 single-flight + LRU；Runtime manifest 使用 LRU、合并并发回源、预建路径索引，已加载的 Worker 在其生命周期内保留该 manifest；资产字节缓存维持有界 LRU | 冷请求仍会依赖控制面授权与对象存储；同一资产目录在授权租约内由 workerd 本地签名，无逐文件控制面调用；独立静态域可直达对象存储/CDN |
| 队列 | 可认领状态/租约筛选，`reconcile_after` 轮转；有界并行协调；`started_at` 区分执行时间与排队时间；认领后才做候选归档 I/O | 不引入额外消息队列；操作行仍是持久化工作队列 |
| 清理 | dirty Worker retention scope；历史排名仅针对发生变化的站点；过期删除每批最多 1000 行；孤儿 input 有持久游标；资产对象归属索引 + S3 multi-delete；对象扫描有游标和批量上限；GC 独立于发布循环 | 对象 GC 每 10 秒最多执行 8 秒，数据库 retention 每分钟最多执行 20 秒；积压跨批次推进 |

写请求保留一次回滚前像；删除重复的 handler checkpoint。拒绝写入仍由 Store 丢弃业务变更并单独提交拒绝审计。
授权策略仅在单次只读请求内复用，跨请求重新读取权限，不改变权限撤销语义。

## 配置与域名

主 `WorkersConfig` 的协调并发与资源分发配置（完整上限见 `config/example.yaml`）：

```yaml
workers:
  reconcile_concurrency: 8
  runtime_config:
    protocol: 2
    verify_millis: 5000
    read_concurrency: 32
    pod_cold_concurrency: 4
```

业务入口统一使用共享路由。`reconcile_concurrency` 默认 8，允许 1–64。
不新增运行期私有环境开关，也不增加独立配置文件。

共享路由下，业务 host 格式为：

```text
<worker-name>.<domain-suffix>
```

每个 domain 只需一条 `*.<domain-suffix>` 路由，且必须经 Profile 唯一选中一个池。
服务主机名与 pool 无关；同池增加副本不改变地址。Hash 标签不表达权限或网络能力；
Profile、domain 选择、placement 和 Runtime pool 认证仍共同约束可访问对象。

部署侧为每个 `*.<domain-suffix>` 配置 DNS 和 TLS，即可覆盖单层业务主机名。

控制台是 `/` 静态 SPA，全部控制面接口仍在 `/api/*`；冷路由接口位于内部 listener 的
`/api/internal/workers/config/v2/watch`（WebSocket）。生产 OSS + 同域 `/api/*` 分发契约不变。

## 持久化与恢复

v2 使用 `worker_runtime_bindings` 保存一条路径规则一行，`worker_runtime_generations` 保存逐资源代次，
`worker_runtime_notifications` 合并未发通知，`worker_runtime_route_dirty` 合并绑定重建。
`worker_runtime_sessions` 和 `worker_runtime_resource_acks` 保存带 session 的有限观测状态。
通知通过主库 LISTEN/NOTIFY 传播，丢失后通过主库版本复核修复，不把通知作为持久事件日志。
删除代次保留 24 小时，过期 session/ACK 有界清理；源实体缺失始终返回删除状态。

v1 的 `worker_config_*` 与 `worker_runtime_acks` 只在过渡模式中使用，原保留窗口和 checkpoint 清理仍适用。
制品和资产仍存对象存储；v2 配置不写 S3 checkpoint，不依赖恢复全池路由快照。
断连保留已加载资源并暂停主动回收，原授权租约不延长，冷加载和离线重启不保证成功。
详见[边缘配置分发](workers-backend.md#边缘配置分发)。

## 真实活跃版本负载

`TestPostgresRuntimeResourcesTenThousandActive` 在隔离 PostgreSQL schema 中创建 100,000 个站点、
10,000 个真实 Version、环境与路由绑定，以四个并行读取者加载活跃资源图，再检查 10,000 次热读取不回源。
fixture 导入后执行 ANALYZE，测量元数据批量读取，不包含代码下载、Secret、资产或业务执行。
批量大小、耗时和缓存占用由测试日志记录；这些数据不是生产 QPS/SLO 承诺。

```bash
KRUN_RESOURCE_SCALE=1 KRUN_TEST_POSTGRES_URL="$TEST_DATABASE_URL" \
  go test ./internal/control -run '^TestPostgresRuntimeResourcesTenThousandActive$' -count=1 -v -timeout 15m
```

## 复现与测量范围

测试分为真实 PostgreSQL 规模回归、实际 Runtime 函数复杂度测试、真实 workerd / Wrangler
制品 smoke、独立 kind E2E 和 Playwright UI。百万站点 fixture 包含站点、placement 与
production 环境，未包含百万个活跃 Version。单路由更新微基准分别通过冷查询缓存 64、512
和 4096 条路由，再测量单条增量更新。测量不包含网络、业务代码和端到端延迟。这些结果用于
约束算法与元数据读取范围，不是生产 QPS 或发布吞吐承诺。

复现命令（数据库必须是专用、可写的测试数据库）：

```bash
KRUN_TEST_POSTGRES_URL="$TEST_DATABASE_URL" make test
KRUN_SCALE_SITES=1000000 KRUN_TEST_POSTGRES_URL="$TEST_DATABASE_URL" \
  go test ./internal/control -run '^TestPostgresMillionSiteReadBound$' -v -timeout 20m
node --expose-gc workers/runtime/test/scaling-benchmark.mjs
npm run build
make check-static-build
```

功能验证使用独立 kind 栈，前端静态产物由测试 Go server 同源提供；生产前端仍上传 OSS。
完整 E2E 和 UI 验证入口：

```bash
# 先按 deploy/workers-kind/README.md 部署专用 kind 环境
KUBECONFIG="$TEST_KUBECONFIG" KIND_CLUSTER_NAME="$TEST_CLUSTER" \
  node deploy/workers-kind/e2e.mjs
# 在单独终端转发同源入口，端口与 control-config 的 public_url 一致
kubectl --kubeconfig "$TEST_KUBECONFIG" -n workers-mvp \
  port-forward deployment/backend 4380:9000 --address=127.0.0.1
KRUN_UI_ORIGIN=http://127.0.0.1:4380 npm run test:ui
```

本地默认端口被占用时，部署和 E2E 脚本可使用同一个 `KRUN_E2E_ORIGIN`，浏览器测试
使用对应的 `KRUN_UI_ORIGIN`。这些变量只属于测试工具，最终地址仍显式写入唯一的主 YAML。

UI 覆盖登录/语言/退出、106 站点跨页、页外深链接、仓库绑定、真实构建/发布/调用、
配置发布/回滚、创建 Worker/preview 和移动端导航；授权测试还覆盖 krun CLI 和官方 Wrangler
的浏览器登录、设备码、自动刷新及退出撤销。测试自动编译本机 CLI；也可用 `KRUN_CLI_BINARY`
指定已编译的路径。默认 `HEAD` 使用仓库实际默认分支，并有真实 Git 仓库回归。

生产容量还需按活跃比例、站点资产大小、总 QPS、发布突发和可用性目标压测 Gateway、
控制面、PostgreSQL、对象存储/CDN 及 Runtime 工作集。v2 已移除全局 revision 提交与 checkpoint 重写，
后续扩展依据资源复核、冷加载预算及主库查询的实际瓶颈决定。
