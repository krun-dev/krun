# Workers 真实控制台 · 2026-10-13

直接使用本项目的 React / Vite 控制台，在现有登录、组织、服务与权限模型中新增 Workers 页面。没有修改 `krun/workers/index.html` 原型。

## 启动和体验

在 `github/krun` 项目根目录运行：

```sh
npm ci
npm run dev:kind
```

入口为 `http://127.0.0.1:4380/console/?page=workers`。当前连接 `<user>@<kind-host>` 上已部署的 `workers-mvp` 后端；首次登录选择“本地开发登录”。此模式是 kind 开发身份，不是生产 SSO。

脚本自动启动远端 backend 的 loopback port-forward、本机 SSH tunnel 4381，以及本机 Vite 4380。前后端对浏览器为同一个 origin，复用 Session / CSRF，前端没有内置 API Key。连接断开会重连；HTTP 请求设 20 秒上限，写请求不自动重试。直接在终端启动时 Ctrl-C 停止本机项目与 SSH tunnel。

已经保留一个可实际操作的工作空间 **Workers 体验**、服务 **hello-worker**：

```text
organization: org_-mEYnYcWmV2YjjSM
service: service_AaktH7zQtdE7uUIw
test Host: w-2a0c96dc845f2d15-test.workers.test
production Host: w-2a0c96dc845f2d15-production.workers.test
```

固定入口：

```text
http://127.0.0.1:4380/console/?page=workers&workspace=org_-mEYnYcWmV2YjjSM&worker=service_AaktH7zQtdE7uUIw&environment=production
```

## 可操作流程

1. 从服务列表进入或创建 Worker，关联代码仓库，填写 ref、项目目录和入口文件，选择构建环境，提交真实 Kubernetes Job。
2. 查看后端 Build 状态、实际编译日志，以及 MinIO 归档的 bucket / object key / hash。
3. 在构建列表选择成功版本，选择目标环境、检查配置，确认发布。
4. 页面轮询真实 Operation 的制品检查、候选验证、Release 提交、运行副本确认，区分 `committed` 和 `available`。
5. 修改环境变量或 staging 开关。配置采用 CAS revision，保存变量不改变当前运行版本；原始 Release 变量另行展示。
6. 在环境发布历史中确认回滚，恢复该 Release 的代码及变量。
7. 在“调用测试”中填写相对路径和 HTTP 方法，通过真实 kgateway 调用 workerd，查看 HTTP status、正文、Release ID 和 Pod UID。

构建列表的当前 / 历史 / 未发布 / 生效中状态由真实 Release、Artifact 和发布历史计算。发布接口返回的 queued Operation 立即显示，后续用服务器状态更新；页面刷新后仍可读取持久化历史与正在进行的操作。

调用 API 先检查当前用户 / Key 的目标环境 `service.invoke` 和 Session CSRF。Gateway 地址及 Host 来自平台配置与环境记录；用户只能选择相对 URI，不能指定外部 URL。请求不转发平台身份凭证，响应最多 64 KiB，不跟随重定向，不把业务 HTML / Cookie 注入控制台 origin。业务正文以转义文本展示。

调用实现将参数校验、目标环境鉴权和网关请求拆开，使用明确的请求 / 响应类型与服务级 HTTP Client。认证和 CSRF 校验与其他控制 API 共用；网络请求在状态事务结束后执行，避免慢 Worker 占住 IAM 状态锁。新增六组回归测试覆盖网关 / Host 路由、凭证和响应隔离、重定向、响应大小边界、非法输入、Session CSRF、取消及上游故障。

kind 的 backend NetworkPolicy 仅新增到自身 Gateway Pod TCP 8080 的出口。runtime 的数据库与对象存储权限没有因此变化。

## 本轮真实浏览器验证

通过浏览器 UI 完成了登录、创建组织与 Worker、提交源码、读日志及制品、发布 test、配置生产变量、同制品发布 production、构建 / 发布 v2、从历史回滚 v1、开启 / 关闭 staging。

实测响应：

```json
{
  "message": "Hello production from kind",
  "version": "v2",
  "environment": "production",
  "path": "/"
}
```

v2 实际响应头：

```text
X-Krun-Release: rel_edbe9540bdff8c8b998210d41528b2e0e2970d612d99ebdcd1acb74a00d23c63
X-Krun-Pod: c29a1931-d532-4322-8243-d2db66765f36
```

回滚后 production Release 为 r3，HTTP 200 返回 `version: v1` 和同一生产 GREETING。test 保持原 v1，staging 恢复默认关闭。页面中保留两个成功构建及生产发布 / 回滚历史，用户可以继续发布 v2 或上传自己的代码。

前端 TypeScript 检查、生产构建、国际化 4 项、权限 5 项检查通过。后端 `go test -race ./...` 与 `go vet ./...` 通过；新增调用入口及读取接口的权限 / 路径测试通过。此前 20 项后端 kind 验收另见 [后端报告](workers-kind-verification.md)，初始浏览器构建、发布和回滚没有触发 runtime Release 更新。验收收尾观察到一个旧 runtime 发生 OOMKilled（原 limit 512 MiB），实验池随后调整至每 Pod 1 GiB 并滚动更新。该容量调整与业务版本发布是独立动作。

当前源码构建范围、平台限制及实验环境问题仍见 [后端接口与流程](workers-backend.md)。该启动方式服务于本机体验；不是公网生产部署。

## 仓库构建更新

Workers 入口已调整为服务列表；点击服务后进入详情，构建表单改用可配置的 GitHub / GitLab / 自建 GitLab 来源。配置、凭证边界、API 及当前构建范围见 [仓库构建说明](workers-repositories.md)。
