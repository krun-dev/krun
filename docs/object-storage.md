# S3 兼容对象存储

本页描述 `workers.s3_*` 的制品、配置与静态资产存储。业务逻辑桶、R2 binding 与文件
浏览器使用独立的 `workers.object_storage`，见 [R2 binding](workers-r2.md) 和
[文件与分发](bucket-objects.md)。

制品存储使用通用 S3 适配器，不按厂商切换代码。控制面、builder 和 runtime 可以使用
不同 endpoint，但必须指向同一个桶和前缀。全部配置放在主 YAML 的 `workers:` 下。

## 寻址

| `s3_addressing_style` | 对象地址 | 用法 |
| --- | --- | --- |
| `path` | `https://endpoint/bucket/key` | MinIO、支持 path-style 的兼容服务 |
| `virtual` | `https://bucket.endpoint/key` | 支持桶域名的兼容服务；endpoint 必须是 DNS 名 |
| `auto` | 根据 endpoint 选择 | AWS 域名用 virtual；其他 endpoint 用 path；HTTPS 下带点的桶名用 path |

SDK 请求、上传/下载预签名 URL、Runtime 配置 URL 共用寻址规则。AWS S3 使用与桶
region 对应的区域 endpoint，SDK 不隐式开启 dual-stack；需要时在 endpoint 中显式指定。
寻址规则参考 [AWS S3 地址说明](https://docs.aws.amazon.com/AmazonS3/latest/userguide/VirtualHosting.html)。

| 服务 | 常用寻址 | `s3_region` |
| --- | --- | --- |
| AWS S3 | `auto` 或显式 `path` / `virtual` | 桶所在的 AWS region |
| MinIO / 自建 S3 | `path`；配置了桶域名时可用 `virtual` | 服务端配置的签名 region，通常为 `us-east-1` |
| 火山 TOS 的 S3 接口 | `virtual` | endpoint 对应的 region |
| 其他 S3 兼容服务 | 显式选择服务支持的方式 | 使用服务要求的签名 region，例如 `auto` |

配置不包含厂商域名默认值。自定义端口、HTTP/HTTPS、对象前缀都来自主 YAML；
运行时不通过额外环境变量选择厂商。

## 桶管理和读取方式

两项配置互相独立：

| 配置 | 行为 |
| --- | --- |
| `s3_bucket_mode: managed` | 控制面检查并在需要时创建专用桶 |
| `s3_bucket_mode: external` | 使用已存在的桶，不创建桶、不修改桶策略 |
| `s3_access_mode: signed` | 配置使用 SigV4，制品和资产使用短期预签名 URL；静态域名转到 Runtime |
| `s3_access_mode: direct` | 配置和静态资产直接读取；静态域名转到配置的静态源站 |

只有 `managed + direct` 会配置 `pub/`、`int/`、`config/` 前缀的匿名读取策略。
`managed + signed` 创建桶后保持私有；`external + direct` 的源站访问由部署方配置。
省略 access mode 时，managed 默认 direct，external 默认 signed；主配置示例显式选择 signed。
业务 Worker 不接收存储凭据，也不获得可信 entry worker 的出站网络能力。

```yaml
s3_endpoint: https://storage.example.com
s3_builder_endpoint: https://storage.example.com
s3_runtime_endpoint: https://storage.example.com
s3_region: us-east-1
s3_addressing_style: path
s3_bucket: replace-me
s3_bucket_mode: external
s3_access_mode: signed
s3_prefix: dev/run
s3_location_id: primary
s3_access_key: ${KRUN_S3_ACCESS}
s3_secret_key: ${KRUN_S3_SECRET}
```

省略 `config_base_url` 时自动生成 Runtime 配置地址。`s3_prefix` 可留空；有值时不带
首尾斜杠。列表、单个/批量删除及预签名 URL 都经过统一前缀适配器。更换桶或前缀
时使用新的 storage location id，避免把已有逻辑 key 解释为另一个存储位置。

这里的 `workers.s3_*` 鉴权支持显式 Access Key / Secret Key 和 SigV4，不含 IAM Role 自动发现、STS
临时凭据自动刷新或厂商私有对象存储 API。local-first 网络渲染的地址限制与操作步骤
见 [本地开发](local-dev.md#s3-兼容对象存储)。
