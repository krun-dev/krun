# 对象存储凭据适配器

`internal/adapters/storagecredentials` 提供独立的凭据获取与缓存模块。
它不传输对象内容。R2 runtime 通过只在内部 listener 注册的
`POST /api/internal/workers/versions/{version}/bindings/{binding}/credentials` 获取凭据。
完整使用方式及兼容范围见 [R2 binding](workers-r2.md)。

## 接口与实现

业务调用接口在 `internal/runtimeconfig/credential_port.go`，厂商接口在
`internal/adapters/storagecredentials/provider/provider.go`。
runtimeconfig 先以当前部署、pool、组织和启用状态解析 binding，再调用缓存模块。
validator 使用独立凭据，不能调用签发接口。缓存命中也不能绕过服务端的资源检查。

共享的 scope 和凭据类型在 `internal/domain/storage_credentials.go`，不依赖 SDK。

| 凭据类型 | 行为 |
| --- | --- |
| `tos_assume_role` | 调用火山 STS AssumeRole，按 bucket、前缀、动作生成会话 Policy |
| `static` | 返回显式配置的长期凭据，不具有云端会话限权能力 |

`Registry` 按 `StoreID` 选择配置好的 provider，并检查 bucket 和根前缀。
provider 的角色和 STS 地址在构造时固定，调用者不能传任意角色、地址或 Policy JSON。
TOS OpenAPI 使用自己的 HMAC-SHA256 签名，与对象请求的 AWS SigV4 分开。
不依赖火山 SDK，不使用环境凭据自动发现，也不在 STS 失败时降级到 static。

调用方必须在构造 `StorageCredentialScope` 之前授权 runtime 身份、Version、binding 和
当前资源权限。scope 中包含组织、逻辑 bucket ID、store ID、物理 bucket、固定前缀、
允许动作和授权 revision。缓存不代替这些校验，也不接收业务自行指定的云端权限策略。

`StorageCredentials` 的全部字段禁止默认 JSON 序列化，普通格式化输出也脱敏。
provider 错误只保留分类和经过过滤的 request ID，不保留上游错误原文、签名 URL 或响应体。

## 主配置

使用唯一主配置的 `workers.object_storage` 段，与制品存储的 `workers.s3_*` 分开。
完整可加载模板见 `config/example.yaml` 的注释段；本地 static 模板见
`config/development.example.yaml`。真实值只放 `.data/dev.yaml` 或该文件的环境变量引用。

```yaml
workers:
  # 其余必需 Workers 配置保持在同一映射中。
  object_storage:
    default_store: primary
    cursor_key: ${KRUN_OBJECT_STORAGE_CURSOR_KEY}
    request_timeout_seconds: 600
    credential_ttl_seconds: 3600
    refresh_before_seconds: 300
    credential_cache_entries: 1024
    credential_concurrency: 8
    credential_timeout_seconds: 20
    stores:
      - id: primary
        endpoint: https://tos-s3-cn-beijing.volces.com
        region: cn-beijing
        bucket: example-worker-data
        root_prefix: worker-data/v1
        addressing_style: virtual
        credentials:
          type: tos_assume_role
          access_key_id: ${KRUN_OBJECT_STORAGE_ACCESS}
          secret_access_key: ${KRUN_OBJECT_STORAGE_SECRET}
          role_trn: ${KRUN_OBJECT_STORAGE_ROLE}
          sts_endpoint: https://sts.volcengineapi.com
```

角色必须拥有目标 bucket 权限，调用者必须可以扮演该角色。
对象读写、删除权限限制在 `bucket/prefix/*`；列举使用 bucket 资源和
`tos:prefix` 条件。角色权限是上限，会话 Policy 进一步收窄。
根前缀和 scope 前缀拒绝通配符及路径跳转；scope 必须以 `/` 结尾，避免相邻前缀混淆。

static 可连接配置了 `path` 或 `virtual` 寻址的 S3 服务，只填
`type: static`、`access_key_id`、`secret_access_key`，不能混入 role 或 STS 地址。
static 返回的密钥拥有原有权限，不应提供给不可信业务代码。

## 制品与静态资产的 Runtime 读取

主配置 `workers.s3_runtime_credentials` 可选择同一个 `tos_assume_role` provider。
它使用 `workers.s3_runtime_endpoint`、`s3_region`、`s3_bucket`、`s3_prefix` 和寻址设置，
不与业务 R2 的 bucket 或读写权限混用。完整注释示例及环境变量引用见 `config/example.yaml`。
TOS 需要区域 HTTPS S3 端点和 virtual 寻址。STS 失败不会降级到静态凭据。
省略该段时使用 `s3_access_key` / `s3_secret_key`，供本地 MinIO 等环境使用；
也可显式配置 `type: static`。static 不具备云端会话限权，不应当作 STS 隔离。

Go 在签发前校验 Runtime 身份、pool、Version 或静态域可见性；临时凭据仅允许
`GetObject`，范围为实际 `s3_prefix` 下的一个不可变制品目录或站点资产目录。
不授予上传、删除、列举权限。签发复用有界 Manager，缓存最多 1024 个 scope，
并发最多 8，超时 20 秒，会话请求期限 1 小时，提前 5 分钟刷新并附加抖动。

内部响应使用 `Cache-Control: no-store`，凭据不进入公开资源、配置快照、持久化状态或业务 env。
可信入口使用与 R2 相同的 SigV4 实现生成 60 秒有效的读取签名，并直接访问对象存储。
本地授权租约为 5 分钟；回源时距会话过期不足 2 分钟或租约不足 5 秒则重新授权。
授权缓存最多 1024 项，资产清单和字节缓存独立，续期失败不使用过期授权回源。
已缓存的文件不要求续期；部署状态仍由路由配置控制。STS 云端会话直到到期仍可能有效，
本地租约不能撤销已经签发的云端权限。

独立 validator 只从候选验证管理请求获得对应制品的只读授权，不申请 serving 资产凭据，
也不获得业务 R2 授权。配置快照的签名读取保持既有实现；构建上传、控制台对象下载和
日志下载仍按各自 API 返回预签名 URL。

## 缓存与失效

每个 store 有独立的有界 LRU 缓存，完整 scope 是缓存键。
组织、bucket、前缀、动作或授权 revision 不同，都不能共用缓存项。
重新构造 Registry 会创建全新缓存，适用于主配置和签发密钥轮换后的进程重启。

缓存命中且未进入刷新窗口时不调用 STS。进入刷新窗口后，同 scope 的并发请求
合并为一次刷新；不同 scope 的请求受并发上限约束。刷新使用有界独立 context，
单个请求取消不会取消其他等待者的刷新。

刷新提前量附加按 scope 计算的 0–30 秒抖动。限流和暂时不可用时退避 5 秒，
只有旧凭据仍满足调用者要求的最小剩余有效期时才允许使用。认证或权限拒绝会移除旧缓存。
签发返回的实际期限为准；不返回已过期或有效期不足的凭据。
旧 revision 的缓存不会给新 revision 使用，但缓存本身不能撤销已签发的云端会话。

## 验证

默认测试不访问真实云服务：

```sh
go test -race ./internal/adapters/storagecredentials/... ./internal/config
```

以下命令从仓库根目录运行，显式加载唯一主配置，对其中所有业务存储执行真实测试：

```sh
go test -race ./internal/adapters/storagecredentials \
  -run '^TestConfiguredStorage$' -v \
  -args -storage-config=../../../.data/dev.yaml
```

测试只在配置的 `root_prefix/integration-tests/<随机ID>/` 下创建小文件，验证上传、
读取内容、HEAD、列举、删除及缓存复用，然后清理并确认对象不存在。
有会话限权能力的 provider 还验证跨前缀读取、上传、删除、列举及整桶列举均返回
`403 AccessDenied`。static 仅验证对象操作，不声称具备云端前缀隔离。

真实 workerd + 配置的对象存储测试：

```sh
go test ./internal/adapters/storagecredentials \
  -run '^TestConfiguredR2Runtime$' -v \
  -args -storage-config=../../../.data/dev.yaml
```

测试使用短期凭据文件（0600、测试结束删除）启动固定版本 workerd，
验证真实 facade、原始压缩字节、条件操作、Range、空对象和分页；对象完成后清理。

真实云测试会生成临时凭据并产生少量请求和对象存储费用；未传 `-storage-config` 时跳过。
不输出凭据和对象内容，不创建 bucket、不修改 IAM 或桶策略。

TOS 凭据和前缀隔离通过，不等于 R2 API、流、分片或 Cloudflare 返回语义已经通过验收。

制品与静态资产的真实 STS / workerd 验证：

```sh
KRUN_TEST_POSTGRES_URL="$TEST_DATABASE_URL" \
  go test ./internal/control -run '^TestConfiguredRuntimeReads$' -v -count=1 \
  -args -runtime-storage-config=../../.data/dev.yaml
```

这项测试要求主配置显式选择 `s3_runtime_credentials.type: tos_assume_role`，不会回退到
静态凭据。元数据写入专用 PostgreSQL 测试库的独立 schema。权限 handler、生产模板生成的 serving / validator、
STS 和对象存储均使用真实实现。测试仅在 `s3_prefix/integration-tests/<随机ID>/` 下创建
配置头、制品、清单和资产，验证配置签名读取、静态响应与缓存、代码执行、凭据不进入
业务 env，以及云端对写入、删除、列举、相邻目录读取的拒绝，结束后删除对象并确认不存在。
