---
title: Workers + R2
description: 部署拾光照片墙和拾物文件工作台，通过 Workers 的 R2 binding 读写对象存储。
---

这类示例由静态前端、Worker API 和 R2 binding 组成。浏览器调用同源业务 API，Worker 通过已绑定的逻辑桶读写文件；浏览器和源码不接触对象存储的长期凭据。

## 选择项目

| 示例源码 | 用途 | 项目目录 | Binding 名称 |
| --- | --- | --- | --- |
| [拾光 · Photo Wall](https://github.com/krun-dev/krun-examples/tree/main/examples/photo-wall) | 上传、浏览照片和打开原图 | `examples/photo-wall` | `PHOTOS` |
| [拾物 · Object Studio](https://github.com/krun-dev/krun-examples/tree/main/examples/object-storage) | 上传、预览、下载、搜索和删除文件 | `examples/object-storage` | `FILES` |

按[通用部署步骤](/docs/guides/worker-examples.html#在控制台构建并发布)连接示例仓库，项目目录选择上表对应路径。Object Studio 会在构建时生成前端资产；运行时两者都使用 krun Workers，无需 Node.js。

## 配置对象存储

在组织的对象存储页面创建独立逻辑桶。建议使用自己的 LDAP 用户名作为前缀，避免与其他人的示例重名。将下表中的 `your-ldap` 替换为自己的 LDAP 用户名，在 Worker **目标环境**的 R2 绑定中配置：

| 示例 | 建议桶名 | Binding 名称 |
| --- | --- | --- |
| Photo Wall | `your-ldap-photo-wall` | `PHOTOS` |
| Object Studio | `your-ldap-object-studio-files` | `FILES` |

例如 LDAP 用户名为 `zhangsan` 时，创建 `zhangsan-photo-wall`，再将 `PHOTOS` 绑定到该桶；Worker 通过 `env.PHOTOS` 访问它。binding 名称必须与源码一致，桶名选择自己实际创建的资源。桶名使用 3–63 个小写字母、数字或连字符，首尾必须是字母或数字。

保存绑定后再构建并发布。控制台仓库构建不会自动根据 `wrangler.jsonc` 创建桶或导入环境绑定。支持的方法及限制见[对象存储](/docs/guides/component-object-storage.html)。

## 验证部署

- **Photo Wall**：打开环境地址，上传一张支持格式的图片，确认可打开原图；刷新页面，照片仍应可见。
- **Object Studio**：打开环境地址，上传一个测试文件，确认可以预览或下载；刷新后仍可见，验证结束后在页面删除测试文件。

两个示例没有应用层登录、访问控制或配额，能够访问 Worker 地址的人可以操作其文件。用于可信环境演示；面向不可信用户开放前，应增加认证、访问控制、配额和清理机制。更多本地运行与验证命令见各项目 README。
