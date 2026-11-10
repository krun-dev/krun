---
title: Wrangler 配置兼容范围
description: krun 支持的 Wrangler 配置文件、构建字段、静态资产及部署配置边界。
---

krun 支持项目中的 `wrangler.jsonc`、`wrangler.json` 和 `wrangler.toml`，但只对本页列出的能力提供支持。配置能被 Wrangler 读取、项目能构建成功，都不代表其中的 Cloudflare 产品配置会在 krun 生效。当前构建器和部署验证使用 Wrangler **4.155.0**。

## 配置文件与项目目录

仓库构建在控制台选择的「项目目录」根部查找配置，顺序为 `wrangler.jsonc` → `wrangler.json` → `wrangler.toml`，只使用第一个找到的文件，不合并。建议只保留一份配置。JSONC 支持注释和尾逗号，TOML 支持相应表与数组写法。

`main` 和静态资产路径必须留在项目目录内；符号链接和越界路径会被拒绝。monorepo 应把项目目录指向包含 Wrangler 配置和构建输入的子目录，不能依赖该目录外的源码或 workspace 包。

支持已经配置好的 Cloudflare Vite plugin 或框架 adapter。构建时生成的 `.wrangler/deploy/config.json` 可以指向项目目录内的最终配置，但不支持其中的额外 `auxiliaryWorkers`。仓库中已有的 `.wrangler`、`node_modules` 会被排除并重新生成。

## 构建配置

以下字段用于生成 Worker 模块和静态资产，适用于仓库构建；使用 Wrangler 直接部署时也由 Wrangler 处理构建。

| 字段 | 支持范围 |
| --- | --- |
| `main` | 模块格式的 JavaScript、JSX、TypeScript、TSX 入口；入口文件可由构建生成。最终主模块必须是 ESM，提供 `fetch` handler |
| `compatibility_date` | 当前接受 `2021-11-02` 至 `2026-09-28`（含首尾）；省略时平台默认使用 `2026-09-28` |
| `compatibility_flags` | 传入固定的 workerd 运行时，支持 `nodejs_compat`；最多 32 个、不重复。具体 flag 必须由运行时支持，不能据此假设完整 Node.js 或任意 npm 包可用 |
| `build.command` | 支持自定义构建；仓库构建有此命令时由 Wrangler 执行，否则有 `package.json` 的 `scripts.build` 时先执行该脚本 |
| `rules` | 支持 `type`、`globs`、`fallthrough`；类型为 `ESModule`、`CommonJS`、`Text`、`Data`、`CompiledWasm`。CommonJS 可作为依赖模块，不能作为主入口 |
| `find_additional_modules`、`base_dir`、`preserve_file_names` | 交给 Wrangler 收集额外模块和处理模块路径，产物仍须通过平台模块类型、路径和大小校验 |
| `no_bundle` | 支持已准备好的模块树，依赖必须能随产物加载 |
| `upload_source_maps` | 为 `true` 时收集输出中的 source map；不代表接入 Cloudflare 的调试或观测服务 |
| `assets` | 支持下表中的静态资产配置，也支持只有资产、没有 `main` 的项目 |

仓库依赖通过 npm、pnpm、Yarn 或 Bun 锁文件安装；有依赖或需要执行包构建脚本时，应提交唯一包管理器的锁文件。构建器使用平台固定的工具版本。构建脚本应只生成产物，不执行登录、部署或资源创建命令；构建环境不提供 Cloudflare 凭据。

仓库构建的兼容日期和 flags 优先使用显式构建参数，否则读取初始 Wrangler 配置，再使用平台默认值。Vite 或 adapter 生成的最终配置不能覆盖已经固定的构建参数。

## 静态资产配置

| 字段或文件 | 支持范围 |
| --- | --- |
| `assets.directory` | 项目目录内的相对路径；支持构建时生成。Vite 项目初始配置可只声明 binding，但最终必须生成资产目录 |
| `assets.binding` | 注入指定名称的资产 binding，例如 `env.ASSETS.fetch(request)`；不能使用平台保留名称或 `KRUN_` 前缀 |
| `assets.run_worker_first` | 支持 `true`、`false` 或路径模式数组。数组最多 16 项，每项最多 128 字符；支持末尾 `*` 和 `!` 排除模式，不支持任意 glob |
| `assets.not_found_handling` | `none`、`single-page-application`、`404-page` |
| `assets.html_handling` | `none`、`auto-trailing-slash`、`force-trailing-slash`、`drop-trailing-slash` |
| `assets.base_path` | 支持静态资产访问前缀 |
| `.assetsignore` | 放在资产目录内，构建打包时排除匹配文件 |
| `_headers`、`_redirects` | 放在资产目录内，随版本冻结，仅作用于静态响应。支持响应头规则、301/302/303/307/308 重定向和站内 200 重写，不代理外部站点 |

## 仓库构建与 Wrangler 部署的区别

控制台仓库构建只生成代码、模块、兼容参数和静态资产。它不会把配置中的变量、R2 桶或 Cloudflare 资源声明自动导入 krun 环境。使用哪种发布方式，就按对应列配置：

| 配置 | 控制台仓库构建 | Wrangler 直接部署到 krun |
| --- | --- | --- |
| `name` | 不据此创建或切换 krun Worker；目标是控制台选择的 Worker | 作为组织内的 Script 名称，首次发布创建对应 Worker，同名后续发布更新它 |
| `account_id` | 不据此选择 krun 组织；使用当前控制台组织 | 使用 krun 组织 ID，也可通过 `CLOUDFLARE_ACCOUNT_ID` 设置 |
| `vars` | 不自动注入运行环境；在 krun 目标环境中配置变量 | 支持字符串和 JSON 值，随 Version 冻结 |
| `r2_buckets` | 不自动创建桶或绑定；在 krun 创建逻辑桶并配置目标环境的 R2 binding | 支持 `binding`、`bucket_name`，引用同组织已启用的逻辑桶；不支持非 `default` 的 jurisdiction |
| `hyperdrive` | 在 krun 的目标环境中选择 PostgreSQL 托管配置并添加绑定 | 支持 `binding` 和 `id`，引用同组织的 PostgreSQL 托管配置。见 [PostgreSQL 托管接入](/docs/guides/workers-hyperdrive.html) |
| `version_metadata.binding` | 在 krun 目标环境中配置版本元数据 binding | 支持注入 `id`、`tag`、`timestamp` |
| `env.<name>` | 选择 krun 构建或发布环境不会自动启用 Wrangler 的同名配置段；仓库构建使用顶层配置，不传 `--env` | `--env` 由 Wrangler 解析；生成的不同 Script 名称对应不同 krun Worker，不映射到同一 Worker 的 krun 环境 |
| `keep_vars` / `--keep-vars` | 不作为仓库产物配置导入 | 支持保留未提交的普通变量，同名新值仍覆盖；普通上传默认替换变量 |

Secret 通过 krun 控制台或 `wrangler secret put` 设置，不写入配置或源码。仓库构建会拒绝 `.dev.vars`、`.env` 等本地凭据文件，它们不能作为生产 Secret 的来源。

## 不支持的配置能力

| 配置或能力 | 当前边界与替代方式 |
| --- | --- |
| `kv_namespaces`、`d1_databases`、`durable_objects`、`queues`、`services` | 不支持相应 binding；构建能保留类导出不代表提供 Durable Objects 服务 |
| `triggers.crons` | 不支持定时触发器，Worker 当前以 HTTP 请求入口运行 |
| `route`、`routes`、Cloudflare Zone / 自定义域名 | 不调用 Cloudflare 路由或域名接口；通过 krun 管理 Worker 域名 |
| `workers_dev`、`preview_urls` | 不作为 krun 托管地址的开关；使用 krun 返回的部署或预览地址，当前不支持通过兼容接口关闭托管 URL |
| `observability`、`tail_consumers` | 不提供 Cloudflare 日志、tail 或观测服务 |
| Python、Rust 构建、legacy Service Worker、Containers | 不在当前模块 Worker 构建与运行范围内 |

未列出的字段不构成支持承诺：部分字段会被 Wrangler 读取，但不会进入 krun 产物或运行配置；不支持的 binding 在兼容上传接口会返回错误。不要仅凭配置未报错判断能力已经生效。

## 仓库构建示例

下面的配置可用于 TypeScript Worker 加静态资产项目；需提供 `src/index.ts` 和非空的 `public/` 目录。需要 npm 依赖时同时提交锁文件。

```jsonc
{
  "name": "hello-worker",
  "main": "src/index.ts",
  "compatibility_date": "2026-09-28",
  "assets": {
    "directory": "./public",
    "binding": "ASSETS",
    "run_worker_first": ["/api/*"],
    "not_found_handling": "single-page-application"
  }
}
```

这里的 `/api/*` 是业务 Worker 内的请求匹配规则。仓库构建的目标 Worker、发布环境、变量、Secret 和 R2 binding 仍在 krun 中选择或配置。

只发布静态资源时可以省略 `main`，仅配置 `assets.directory`。完整的 HTML / CSS / SVG 项目见 [静态页面示例](/docs/guides/examples-static-site.html)，无需编写 Worker 入口或配置 binding。

部署步骤见 [使用 Wrangler 部署 Workers](/docs/guides/cloudflare-workers.html)，运行 API 及网络限制见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html)和[对象存储](/docs/guides/component-object-storage.html)。
