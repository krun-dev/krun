# Workers 静态资产

Worker 代码与资产属于同一个 Artifact。Version 固定 Artifact、变量和 Secret 版本；
发布与回滚通过同一条环境路由切换代码和资产。

## 构建与格式

Cloudflare 兼容入口支持官方 Wrangler 的 asset upload-session、分批上传和 completion
token：先上传资产，再将资产与代码打包为同一个不可变 Artifact；也支持纯静态 Worker。
绑定、路由选项和限制见 [Cloudflare API](cloudflare-api.md)。上传会话和逐文件元数据保留
一小时；临时对象最长保留 24 小时，由现有 upload GC 清理，失败可以重试。

源码上传或仓库构建可以配置 `assets.directory`、`binding`、`run_worker_first`、
`not_found_handling` 和 `html_handling`。Wrangler/custom build 先生成产物，再由可信
packer 收集资产。所有制品使用 `workerd-bundle-v2` profile、manifest schema 2；
没有静态资产时省略 `assets` 字段。

归档包含 `manifest.json`、`modules/` 和可选 `assets/`。平台和 Runtime 都验证模块
类型、路径、大小、SHA-256 和未声明条目。资产最多 500 个文件、总计 20 MiB、单文件
5 MiB；Content-Type 按扩展名白名单校验。SPA 回退要求存在 `index.html`。

finalize 生成独立的不可变 manifest 对象，按 Artifact 的 manifest digest 校验。
资产对象键在上传前写入 `worker_asset_objects`，按 Artifact 索引，供重试和 GC 定向处理。

## 请求处理

入口 Runtime 根据 Version 的资产配置决定先访问资产还是业务 Worker：

1. 命中 `run_worker_first` 的路径先交给 Worker；否定模式按规则排除。
2. 其他路径先按 HTML handling 和路径索引匹配资产。
3. HTML 规范化可以返回重定向；导航请求可按配置回退到 SPA 入口。
4. 未命中且没有回退时交给业务 Worker。

设置 `assets.binding` 后，业务通过例如 `env.ASSETS.fetch(request)` 访问本 Version
资产。binding 不持平台 token；变量重名或平台保留名称会被拒绝。

manifest 使用 single-flight 和有界 LRU，路径索引只构建一次；资产字节按文件按需下载，
校验 digest 后放入有界 LRU。发布只切换 Version，不清空其他站点的资产缓存。

## 存储与缓存

| 前缀 | 内容 | 访问 |
| --- | --- | --- |
| `v1/<org>/sha256/<digest>/` | 代码归档与 manifest | 私有，按需签名下载 |
| `pub/<org>/<service>/<digest>/` | 公网静态资产副本 | 通过绑定的公网静态域访问 |
| `int/<org>/<service>/<digest>/` | 内网静态资产副本 | 通过绑定的内网静态域访问 |

HTML 使用可重新验证的缓存策略；带内容 hash 的静态文件允许 immutable 缓存。
Runtime 支持 ETag / `If-None-Match`。静态域独立于 Worker 业务域，可接 CDN；
业务域的同路径 API 与 Worker 行为不依赖 CDN 代理运行时。

正式 Artifact 不可覆盖。GC 在确认没有活动引用并取得删除租约后，按对象归属索引
分批 multi-delete，成功后再删除清单与 Artifact 元数据；失败保留清单重试。
保留期与后台预算见 [存储](storage.md)。

配置示例见 [主配置](../config/example.yaml)，真实全栈夹具见
[workers-fullstack](../examples/workers-fullstack/README.md)，验证入口见 [验证说明](verification.md)。
