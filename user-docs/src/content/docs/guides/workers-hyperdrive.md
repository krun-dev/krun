---
title: PostgreSQL 托管接入
description: 用控制台或原版 Wrangler 配置 Hyperdrive，让 Worker 通过标准驱动访问 PostgreSQL。
---

Hyperdrive 由 krun 加密保存源库凭据。绑定了数据库的 Worker 获取源库连接串，通过 workerd 按池的出网规则直接连接 PostgreSQL。请为 Worker 使用最小权限的数据库账号。
公网和内网数据库使用同一套流程。**当前只支持 PostgreSQL**，创建配置时须填写源库域名。

| 用途 | 怎么填 |
| --- | --- |
| 在控制台登记源库 | 分别填写域名、端口、库名、用户名和密码 |
| 在 Wrangler 登记源库 | 传 `--connection-string`，也可以逐项传参 |
| 在 Worker 中连接数据库 | 把 `env.DB.connectionString` 交给 `postgres.js` 或 `pg` |

源库连接串含有源库用户名和密码。Wrangler 在创建配置时解析它，再把源库字段提交给 krun。
`env.DB.connectionString` 由平台生成，包含源库地址和凭据。Worker 项目保存绑定名和配置 ID。
源库须支持 TLS，并使用与源库域名匹配、由 workerd 信任的 CA 签发的证书。

## 申请临时 PostgreSQL

如果手头没有源库，可以用 `curl` 申请演示库。Neon 是公网源库，目标 Worker 所在的运行池需要允许公网出网。

:::caution[警告 · 临时数据库]
未认领的 Neon 临时库会在 **72 小时**后到期，以接口返回的时间为准。到期后应用将无法连接，
请只存放测试数据。正式使用前请申请正式 PostgreSQL，为正式库新建 Hyperdrive 配置，
更新 Worker 绑定并重新发布。
:::

在 Bash 或 Zsh 中执行下面的命令，需要安装 `curl` 和 `jq`。命令通过
[neon.new 的临时库 API](https://neon.new/) 创建数据库，将直连连接串保存到 `ORIGIN_DSN`，
终端只显示到期时间。

~~~sh
unset ORIGIN_DSN
if NEON_RESPONSE="$(curl -fsS --max-time 30 https://neon.new/api/v1/database \
  -H 'Content-Type: application/json' \
  --data '{"ref":"krun-hyperdrive-demo"}')" &&
  ORIGIN_DSN="$(printf '%s' "$NEON_RESPONSE" | jq -er '.connection_string | strings | select(length > 0)')"; then
  printf '临时数据库到期时间（UTC）%s\n' "$(printf '%s' "$NEON_RESPONSE" | jq -r '.expires_at')"
else
  printf '%s\n' '未能创建临时数据库，请检查上方错误。' >&2
fi
unset NEON_RESPONSE
~~~

连接串含有源库密码，请妥善保存。使用 Wrangler 时，在同一个终端继续执行下文命令。
使用控制台时，将同一连接串中的域名、端口、库名、用户名和密码分别填入对应字段。

Neon 正逐步停用 `neon.new`。如果接口已下线，可在 [Claimable Neon](https://neon.com/claimable-neon)
创建临时 PostgreSQL，复制直连连接串后继续配置。需要建表和发布一个完整应用时，见
[Workers + PG](/docs/guides/examples-postgres-todo.html)。

## 使用控制台

1. 进入组织的「数据库 → Hyperdrive」，点击「创建 Hyperdrive 配置」。
2. 填写配置名称、源库域名、端口、库名和登录凭据。
3. 打开目标 Worker，选择环境，在「配置版本 → 管理环境草稿 → Hyperdrive 绑定」选择刚创建的配置。
   填写绑定名 `DB`，保存后发布新版本。
4. 需要更换源库密码时，在 Hyperdrive 列表点击「轮换源库密码」。先在源库修改密码，再更新 Hyperdrive 配置并发布新版本，使 Worker 获取新连接串。

配置属于当前组织。删除配置会停止发放连接串。撤销已发放的凭据和已建连接需要在源库操作。需要更换源库地址、库名或用户名时，
创建一个新配置，更新 Worker 绑定并发布。

## 使用 Wrangler

按 [Wrangler 部署指南](/docs/guides/cloudflare-workers.html) 配置 krun API 地址和登录。
使用 `wrangler@4.155.0`。如果已按上文申请临时库，直接复用 `ORIGIN_DSN`。使用已有源库时，
将其连接串从安全渠道存入同一个变量。

~~~sh
npx wrangler@4.155.0 hyperdrive create orders-db \
  --connection-string "$ORIGIN_DSN" \
  --binding DB \
  --update-config

npx wrangler@4.155.0 deploy
~~~

源库连接串接受 `postgres://` 和 `postgresql://`，例如
`postgresql://app:replace-me@orders.example.test:5432/orders`。主机填写域名，端口默认
`5432`。如果密码含有 `@`、`:` 或 `/` 等 URI 保留字符，先对密码做百分号编码，或改用
`--host`、`--port`、`--database`、`--user` 和 `--password` 分字段传入。

Wrangler 可自动更新 JSON 或 JSONC 配置。使用 TOML 时，把 CLI 输出的绑定复制到项目配置。

~~~toml
[[hyperdrive]]
binding = "DB"
id = "<hyperdrive-config-id>"
~~~

配置可以用 `wrangler hyperdrive list`、`get`、`update` 和 `delete` 管理。使用 `update` 轮换密码时，
传 `--password`。请通过 Hyperdrive 配置接口管理源库凭据。连接串仅交付给运行相应版本的 Worker。

## 使用数据库驱动

安装 `postgres@3.4.8`（postgres.js），在 Wrangler 配置的 `compatibility_flags` 中添加
`nodejs_compat`。将绑定的连接串交给驱动。

~~~ts
import postgres from "postgres";

export default {
  async fetch(_request: Request, env: { DB: { connectionString: string } }) {
    const sql = postgres(env.DB.connectionString, { max: 1 });
    try {
      const [row] = await sql`select 1 as value`;
      return Response.json(row);
    } finally {
      await sql.end();
    }
  },
};
~~~

一个客户端会话对应一条源库连接。应用用完连接后应及时关闭。krun 的 Wrangler 兼容范围覆盖配置管理
和 Worker 绑定。Worker 直连源库，查询缓存与平台连接池超出兼容范围。
