# Workers 后端验证记录 · 2026-10-13

最终结果：**20 / 20 项 kind 端到端检查通过**。另执行 `go test -race ./...`、`go vet ./...`、Builder / Runtime / E2E JavaScript 语法检查、部署 shell 和 Python 语法检查，均通过。新增 8 个 Go Workers 单元测试覆盖权限、幂等、源码边界、不可变配置、队列执行者撤权、归档完整性、runtime 认证、已提交操作回滚及环境范围集合查询。

普通 Go 测试没有提供专用 PostgreSQL 测试连接串，因此原有可选 PG 集成测试按约定跳过；下面的 20 项实际经过 kind 中的 PostgreSQL、MinIO、构建 Job、kgateway 和 workerd。原始通过记录见 [脱敏证据 JSON](workers-kind-evidence-20261013.json)，复现入口见 [部署及验收](../deploy/workers-kind/README.md)。本轮未修改前端。

## 环境

| 项目 | 实际值 |
| --- | --- |
| 远端 | `<user>@<kind-host>` |
| 集群 | kind `krun-workers-20261010`，Kubernetes 1.33.1 |
| 隔离命名空间 | `workers-mvp` |
| CNI / 网关 | Calico 3.30.3 / kgateway 2.3.6 |
| Backend | Go 控制服务单容器，双监听：9000 公开（`/api/*`）、9001 仅 runtime（`/api/internal/workers/*`）；无 Node sidecar |
| Runtime | 2 个 Pod，每个只有 1 个 workerd 容器和进程；`workerd 1.20260928.1` |
| Builder | 独立 Kubernetes Job，Node 24 / esbuild 0.28.2 |
| 存储 | PostgreSQL 18；MinIO `RELEASE.2025-09-07T16-13-09Z`；各自 1 GiB PVC |
| 业务入口 | 私网 Gateway listener 8080；精确环境 Host |

原 `workers-lab` 的 control、PostgreSQL、Redis、runtime-default、runtime-isolated 等部署仍 Ready，未参与本轮部署或测试。

## 通过的检查

| # | 检查 | 观测结果 |
| --- | --- | --- |
| 1 | 真实源码构建并登记制品 | Job 编译成功，S3 正式归档有 hash、location、bucket 和 object key |
| 2 | 发布 test | 候选和同步步骤完成；经 Gateway 返回正确版本、环境、path / query |
| 3 | 相同 Artifact 发布 production | 同一制品使用生产环境自己的变量 |
| 4 | 构建并发布 v2 | production 更新，test 仍为 v1 |
| 5 | 旧 Release revision | 返回 409，拒绝覆盖 |
| 6 | 历史 Release 回滚 | 恢复历史代码和变量，成功历史正确 |
| 7 | 语法错误 | Build failed、无 Artifact；原 production 仍可调用 |
| 8 | 访问源码目录以外文件 | 编译器拒绝 `/input/input.json` 导入 |
| 9 | 模块初始化抛异常 | 候选失败；Release revision 与旧版本保持 |
| 10 | 环境范围 API Key | test Key 无法发布 production；集合只返回可见环境 |
| 11 | staging 开关 | 关闭时拒绝发布；开启后独立 Release / 变量生效 |
| 12 | 业务网络与凭证边界 | HTTP fetch、TCP connect 均被拒绝；无 PLATFORM / LOADER / token binding |
| 13 | 幂等和制品去重 | 同 key 同请求返回同 Build；同内容复用 Artifact；改内容返回 409 |
| 14 | 路径及服务范围 | 越界源码路径 422；其他服务读 Artifact 404 |
| 15 | 无 runtime 滚动更新发布 | 业务发布前后 Pod UID / restartCount 一致 |
| 16 | 完成后的控制服务重启 | 发布历史保留，业务可恢复调用 |
| 17 | 构建执行时重启 | 同一个持久化 Build / Job 被继续处理 |
| 18 | 发布执行时重启 | 原 Operation 完成 available；后续历史回滚成功 |
| 19 | 已提交但配置同步受阻 | 保持 committed；接受历史回滚；恢复连接后回滚 available |
| 20 | 替换 runtime Pod | 新 Pod 从对象存储 bootstrap 配置视图与对象制品，readiness 后经 Gateway 正常响应 |

第 19 项在 backend Pod 的独立 network namespace 内临时拒绝到达 internal listener 9001 的入站请求（覆盖配置增量流与 ack），随后删除规则。原操作在阻断期间没有被错误标记 available；解除阻断后，runtime 追上配置 revision，所以最终状态是 `available`，之后历史版本回滚也完成。这是允许的时序：只有在新回滚提交时仍未完成确认的原操作才会成为 `superseded`。

## 一份实际制品

```json
{
  "storage_location_id": "primary",
  "bucket": "krun-artifacts",
  "object_key": "v1/org_EDcGm1XfTA9lv5fm/sha256/40b7c896a680e77f65dafb02fd228a6887e37ab9e0feebe5d6d0cb0a4665444c/bundle.tar.gz",
  "archive_sha256": "40b7c896a680e77f65dafb02fd228a6887e37ab9e0feebe5d6d0cb0a4665444c",
  "module_sha256": "be07594fecb0e077f4361dfebf6c3824909be2041a03de871dbb9b567d96eb4b",
  "archive_size_bytes": 878,
  "state": "finalized"
}
```

Go 二进制 SHA-256：`00064db3d0700d902e621d7873cac549622adaac2a60994e46adae9141a69764`。

三个实际加载的镜像 ID，顺序为 backend / builder / runtime：

```text
sha256:7b1a11f636770aaf006be326f6bde87d73cd9e28581401f7c1d4b01ecc4cc664
sha256:494b975f21f2cf5cfbc91e4e31cabb9a7b1ad9ecd8947ed62566279d36961453
sha256:de3c073924ac1d8d89e75b4868385ca686717928227b946fa874b11eb40d6559
```

## 实验环境问题及范围

排查 port-forward / exec 中断时，kind 节点日志显示 containerd 2.1.1 在 `RunPodSandbox` 附近多次以 `status=11/SEGV` 退出，随后被 systemd 自动重启。业务 Pod 没有因此重启，旧实验部署仍正常；kind 容器的 `OOMKilled` 为 false，containerd 日志中的异常退出类型为 SEGV。未对共享 kind 节点升级或更换 containerd。

最终验收采用 tester Pod 直接请求 API。读取请求遇到传输错误或代理的空 503 响应时最多重试 3 次；写请求不自动重试。最终一轮记录了 3 次读取传输重试，均发生在同一个失败源码构建的状态查询上，之后正常得到 Build failed。这些重试计入环境问题，不代表请求全程无错误。

测试证明本版构建、发布、代码加载、权限与恢复链路可在该 kind 环境工作。没有验证生产 SSO、公网 DNS / TLS、阿里云 OSS 专用接口、Git / npm 构建、多 pool、DB / Service / Secret bindings、持续流量下零中断、V8 原生逃逸防护或规模性能。
