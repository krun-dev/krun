# Workers 控制台

Workers 页面属于主 React/Vite 静态控制台，入口是 `/?page=workers`。开发启动与
同源代理配置见 [本地开发](local-dev.md)；生产静态托管见 [部署](deployment.md)。

Workers 和对象存储列表支持按名称或 ID 进行不区分大小写的包含搜索，搜索范围覆盖整个
组织。每页可选 20、50 或 100 条，提供上一页和下一页；修改搜索条件或每页条数时返回
第一页。列表与详情展示创建时间，详情通过查询参数中的稳定 ID 单独加载，刷新不依赖
该资源是否在当前列表页。

Workers 列表接口 `GET /api/v1/orgs/{org}/workers` 接受 `q`、`limit`（1–200，
默认 100）和 `after`，返回 `items` 与可选的 `next_after`。游标按资源 ID 推进，搜索先于
分页；`worker_id` 用于独立读取一个可访问的Worker 摘要，不受搜索和游标影响。CF 兼容
接口保持原有协议与响应结构，不使用控制台分页参数。

## 页面流程

1. 从组织 Workers 列表进入 Worker，选择上传源码或配置好的 GitHub / GitLab 仓库。
2. 查看持久化 Build 状态、编译日志和对象存储 Artifact。
3. 选择成功构建并发布到目标环境；Operation 经认领、验证、提交和 runtime ack
   最终收敛到 available。
4. 用 CAS revision 修改环境变量、Worker 域名或执行回滚，避免覆盖并发更新；预览环境
   的创建和删除也通过Worker 行 CAS 仲裁，多个控制面副本不会并发突破数量上限。
5. 在环境配置中新增、轮换或删除 Secret。值只在提交时出现，之后只能看到名称；每次
   修改都会复用当前 Artifact 自动发布一个新 Version，不重新构建代码。
6. 从历史中查看 Version，在“调用测试”中经配置好的 kgateway 验证业务响应。

构建与发布队列支持控制面重启和多副本抢占：认领使用条件更新及租约，副作用必须
幂等。Version 提交不保证边缘立即生效，runtime 通过 config revision、snapshot 和
ack 最终收敛。

调用接口只接受相对 URI，目标 Gateway 与 Host 来自平台配置。平台凭据不会转发给
业务；不跟随重定向，响应正文有大小限制并以转义文本显示。

## 网络配置

组织侧选择平台允许的 Network Profile 和域名，不提供资源池选择或迁移入口。
基础设施目录来自主 YAML，匹配关系见 [Workers 资源池](workers-pools.md)。

仓库内的 [`examples/workers-fullstack`](../examples/workers-fullstack/README.md) 是完整
验证夹具：同一个 Version 同时包含 Worker API 和纯静态前端，并由 kind E2E 实际构建、
发布和调用，不是只靠 mock 或单元测试证明。

自动化测试与浏览器验收入口见 [验证说明](verification.md)。

「对象存储」中点击桶名称可以浏览目录、上传下载、查看元数据和删除文件。对象权限
独立于桶管理权限，文件内容直接传输到对象存储；配置、API 和验收入口见
[文件与分发](bucket-objects.md)。
