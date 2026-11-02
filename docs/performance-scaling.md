# 百万站点规模改造与验证

本次目标是让常用请求、发布和后台协调的成本由目标实体、增量大小或有界工作集决定。
百万站点元数据测试与 kind 功能测试分别验证数据规模和真实链路；不将它们等同于生产 QPS 承诺。

## 七项改动

| 范围 | 实现 | 边界 |
| --- | --- | --- |
| 控制面读取 | 站点 API 按组织/资源过滤 SQL；列表用资源 ID 游标；Runtime 按 Release/Artifact 主键查询；登录只读当前 identity，启动只注册来源；导航只加载当前用户的授权上下文 | 管理员的批量 IAM 管理操作仍保留独立的管理查询语义；没有给它们伪造授权缓存 |
| 网关协调 | 共享路由按 pool × domain 建通配 HTTPRoute；不加载全站点拓扑；命名空间清单一次分页读取并复用；已就绪路由每 30 秒修复，未就绪时每秒重试 | `shared_routing: false` 保留旧站点路由模式，适用于现有小规模开发环境 |
| 发布 | 数据库触发器将变更合并成一条 service dirty 行；每批最多 256 站点；仅对变化路由/Release 写增量；CAS 事务为 4 条数据库语句，无网络调用；操作优先发布自己的站点 | catalogue 变化会一次性入队重建，按有界批次推进；revision 仍是全局单调游标 |
| Runtime 配置 | 紧凑 head 启动；冷 host 定向查询；仅缓存最多 4096 条 route 和其 Release；增量原地同步提交、按引用计数回收，不遍历全表；对象存储有 256 个 host hash shard | 每个 shard 的大小随所属站点数增长；百万条路由均匀分布时约 3906 条/shard；不要求每 Pod 下载全池 |
| 资产 | finalize 独立保存有 digest 的 manifest；控制面用 single-flight + LRU；Runtime manifest 使用 LRU、合并并发回源、预建路径索引；资产字节缓存维持有界 LRU | 冷请求仍会依赖控制面签名与对象存储；独立静态域可直达对象存储/CDN |
| 队列 | 可认领状态/租约筛选，`reconcile_after` 轮转；有界并行协调；`started_at` 区分执行时间与排队时间；认领后才做候选归档 I/O | 不引入额外消息队列；操作行仍是持久化工作队列 |
| 清理 | dirty service retention scope；历史排名仅针对发生变化的站点；过期删除每批最多 1000 行；孤儿 input 有持久游标；资产对象归属索引 + S3 multi-delete；对象扫描有游标和批量上限；GC 独立于发布循环 | 对象 GC 每 10 秒最多执行 8 秒，数据库 retention 每分钟最多执行 20 秒；积压跨批次推进 |

写请求保留一次回滚前像；删除重复的 handler checkpoint。拒绝写入仍由 Store 丢弃业务变更并单独提交拒绝审计。
授权策略仅在单次只读请求内复用，跨请求重新读取权限，不改变权限撤销语义。

## 配置与域名

主 `WorkersConfig` 新增：

```yaml
workers:
  shared_routing: true
  reconcile_concurrency: 8
```

示例配置和独立 kind 环境启用共享路由。`reconcile_concurrency` 默认 8，允许 1–64。
不新增运行期私有环境开关，也不增加独立配置文件。

共享路由下，业务 host 格式为：

```text
w-<service-hash>-<environment>.p-<pool-hash>.<domain-suffix>
```

每个 pool/domain 只需一条 `*.p-<pool-hash>.<domain-suffix>` 路由。主机名从已有 durable
placement 推导，增加 pool 不会重新散列已有服务的地址。Hash 标签不表达权限或网络能力；
Profile、domain 选择、placement 和 Runtime pool 认证仍共同约束可访问对象。

**启用共享路由会改变旧站点地址。** 部署侧必须为这些后缀配置 DNS 和 TLS；一个旧的
`*.example.com` TLS 证书不能直接覆盖多一层标签的 host。项目尚未上线，不保留旧地址别名。
未来实现跨池迁移时，需要处理入口标签/别名连续性，不能仅替换 placement 就承诺地址不变。

控制台仍是 `/` 静态 SPA，全部控制面接口仍在 `/api/*`；冷路由接口位于内部 listener 的
`/api/internal/workers/config/routes/{host}`。生产 OSS + 同域 `/api/*` 分发契约不变。

## 持久化与恢复

- `worker_config_dirty`：一站点一行，generation CAS 防止旧 publisher 吞掉新变更；处理后删除。
- `worker_config_checkpoints`：一 pool/shard 一行，覆盖写 revision/published revision。
- `worker_asset_objects`：一对象一行，artifact 索引直接定位其文件；对象删除成功后再删行。
- `worker_retention_scopes`：一变更站点一行，裁剪/孤儿清理完成后按 generation 条件删除。
- `worker_config_ops` 和 revision 继续保留 512 个 revision；旧不可变 shard 对象按 floor 清理，保留仍被当前 shard pointer 引用的对象。

没有外键、全局选主或数据库内网络调用。IAM 与 Workers 的实体变更保持分开。
触发器只产生一个站点 ID 的协调意图，不在 IAM 事务中修改 Workers 实体。

Shard 在 repeatable-read 视图中读取，并用该视图的真实 head 命名；因此并发发布者不会
给同一个不可变对象键写入不同内容。Pointer 使用 ETag CAS，旧 revision 不能覆盖新 revision。
控制面不可用时，冷 host 可从对应 shard 恢复其最后持久配置；未缓存代码/资产/Secret 的加载仍需其依赖可用。

## 性能验证

2026-11-02 最终验证结果：

| 验证 | 结果 |
| --- | --- |
| `make test`（启用真实 PostgreSQL） | 通过；包含 Go race/vet、前端检查、builder、真实 workerd 和 Wrangler 制品 smoke |
| kind 完整 E2E | 31/31 通过；包含重启恢复、回滚、权限/网络隔离、静态资产、增量配置与 GC |
| Playwright 真实 UI | 7/7 通过；桌面和移动端，无 mock，浏览器 JavaScript 异常检查通过 |
| 百万站点 PostgreSQL 元数据 | 通过；测试结束后删除专用 schema |
| 静态产物与密钥检查 | `npm run build`、`make check-static-build`、`make check-secrets` 通过 |

扩展 E2E 在一个组织内创建 108 个站点，以每页 37 条读完 3 页，无遗漏或重复；
共享 Gateway HTTPRoute 保持 4 条（两个 domain 的业务路由与静态路由）。GC 实际删除
废弃 Artifact 的两份资产对象和清单行，同时验证在用资产仍返回 HTTP 200。

本机 Node v24.3.0，实际 Runtime 函数，百万 routes + 百万个最简 Release 描述，每次仅修改一条路由：

| 实现 | 3 次测量范围 |
| --- | --- |
| 改造前全量复制/遍历 | 955.84–2170.65 ms |
| 改造后按增量更新 | 0.051–0.174 ms |

这是同步配置应用函数的微基准，不包含网络、workerd 调度、业务代码或端到端请求延迟。
回归测试通过禁止 Map 全量迭代来约束复杂度，不依赖机器速度断言。

PostgreSQL 测试在独立 schema 内插入 **100 万站点、100 万 placement 和 100 万 production 环境**。
本次样本每站点尚未发布 Release；25 次单站点批量读取均只加载一个站点，p50 约 2.05 ms，
p95 约 3.25 ms，最大值 56.21 ms。组织导航和登录均加载 0 个站点资源；身份来源注册约 11.25 ms
（仅控制面身份来源注册，不包含 Workers 启动与配置恢复）。首个冷样本也计入最大值。
该测试不覆盖百万活跃 Release 的历史数据量、并发发布速率或百万业务 QPS。

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
# 将测试 Go 同源入口转发到本机；与 control-config 的 public_url 保持一致
KRUN_UI_ORIGIN=http://127.0.0.1:4380 npm run test:ui
```

本地默认端口被占用时，部署和 E2E 脚本可使用同一个 `KRUN_E2E_ORIGIN`，浏览器测试
使用对应的 `KRUN_UI_ORIGIN`。这些变量只属于测试工具，最终地址仍显式写入唯一的主 YAML。

UI 覆盖登录/语言/退出、106 站点跨页、页外深链接、仓库绑定、真实构建/发布/调用、
配置发布/回滚、创建 Worker/preview 和移动端导航。UI 联调还修复了默认 `HEAD` 被错误当作
名为 `HEAD` 的分支查找的问题，并增加真实 Git 仓库回归。

生产容量还需按活跃比例、站点资产大小、总 QPS、发布突发和可用性目标压测 Gateway、
控制面、PostgreSQL、对象存储/CDN 及 runtime 工作集。保留全局 revision CAS 是当前明确的边界；
若发布吞吐达到这个小事务的上限，再按独立 pool/shard 拆分 head。
