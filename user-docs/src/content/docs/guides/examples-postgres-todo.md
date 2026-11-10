---
title: Workers + PG
description: 用 neon.new 创建临时 PostgreSQL，建表并通过 Hyperdrive 将全栈 TODO 示例部署到 krun。
---

「留白」由静态前端、Worker JSON API 和 PostgreSQL 组成。浏览器调用同源的 `/api/todos`，
Worker 用 `pg` 和 `env.DB.connectionString` 保存待办。 [示例源码](https://github.com/krun-dev/krun-examples/tree/main/examples/postgres-todo)

:::caution[警告 · 临时数据库]
本教程创建的是演示用的临时 PostgreSQL。Neon 标注未认领的数据库会在 **72 小时**后到期，
具体时间以 Neon 返回的到期时间为准。到期后应用将无法连接，测试数据可能无法取回。只放测试数据，
正式使用前请向团队或平台管理员申请正式 PostgreSQL，再按本文最后一节切换绑定。
此示例没有应用登录，演示时请把 Worker 的访问范围限制在可信测试网络。
:::

## 1. 申请临时 PostgreSQL

在 Bash 或 Zsh 中执行下面的命令，需要安装 `curl` 和 `jq`。命令调用
[neon.new 的临时库 API](https://neon.new/)，将直连连接串保存到 `ORIGIN_DSN`，终端只显示到期时间。

~~~sh
unset ORIGIN_DSN
if NEON_RESPONSE="$(curl -fsS --max-time 30 https://neon.new/api/v1/database \
  -H 'Content-Type: application/json' \
  --data '{"ref":"krun-postgres-todo"}')" &&
  ORIGIN_DSN="$(printf '%s' "$NEON_RESPONSE" | jq -er '.connection_string | strings | select(length > 0)')"; then
  printf '临时数据库到期时间（UTC）%s\n' "$(printf '%s' "$NEON_RESPONSE" | jq -r '.expires_at')"
else
  printf '%s\n' '未能创建临时数据库，请检查上方错误。' >&2
fi
unset NEON_RESPONSE
~~~

申请成功后，在**同一个终端**继续建表。连接串含有源库密码，避免打印它或将它写进仓库。

Neon 正逐步停用 `neon.new`。如果此接口已下线，可以在 [Claimable Neon](https://neon.com/claimable-neon)
创建临时 PostgreSQL，复制直连连接串。回到同一个终端，运行 `read -r -s ORIGIN_DSN`，
粘贴后按回车，再继续下一步。

## 2. 初始化表结构

准备 Git 和 PostgreSQL 的 `psql` 客户端。在上一步的终端执行下列命令，使用已保存的连接串。

~~~sh
git clone --branch main --single-branch https://github.com/krun-dev/krun-examples.git
cd krun-examples/examples/postgres-todo

psql "$ORIGIN_DSN" -v ON_ERROR_STOP=1 -f schema.sql
psql "$ORIGIN_DSN" -v ON_ERROR_STOP=1 -c 'SELECT count(*) FROM todos;'
~~~

第一条 `psql` 命令创建 `todos` 表。第二条能查出行数，就可以继续配置和部署。
直接使用 Neon 提供的 TLS 连接串，无需修改其中的查询参数。

## 3. 绑定并发布

控制台与 Wrangler 任选一种。Neon 是公网源库，目标 Worker 所在的运行池需要允许公网出网。
Worker 本身的访问地址可以继续限制在测试网络。

### 使用控制台

1. 在目标组织打开「数据库 → Hyperdrive」，点击「创建 Hyperdrive 配置」。
2. 名称可填 `postgres-todo-demo`。从 Neon 直连连接串中取出主机、端口、库名、用户名和密码，
   分别填入对应字段。连接串省略端口时填 `5432`。字段填写解码后的值，例如 URI 密码中的
   `%40` 在表单里填 `@`。
3. 在「Workers」创建 Worker，进入「代码源」绑定 GitHub 仓库。
   - 仓库地址填 `https://github.com/krun-dev/krun-examples`
   - 分支填 `main`
   - 项目目录填 `examples/postgres-todo`
4. 选择目标环境，打开「配置版本 → 管理环境草稿 → Hyperdrive 绑定」。选择
   `postgres-todo-demo`，将绑定名填为 `DB` 并保存。
5. 进入「构建与发布」，点击「构建新版本」。构建成功后发布到同一环境，等待发布生效，
   再打开环境显示的访问地址。

### 使用原版 Wrangler

准备 Node.js 和 npm。按 [Wrangler 部署指南](/docs/guides/cloudflare-workers.html) 配置 krun 的
API 地址、组织和 API Token。在第 2 步的同一个终端、示例目录中执行：

~~~sh
npm ci
npx wrangler hyperdrive create postgres-todo-demo --connection-string "$ORIGIN_DSN"
~~~

把命令输出的配置 ID 填到 `wrangler.jsonc` 的 `hyperdrive[0].id`，保留绑定名 `DB`，
然后发布。

~~~sh
npx wrangler deploy
unset ORIGIN_DSN
~~~

使用 CLI 输出的 Worker 地址验收。项目配置中只保存绑定名和配置 ID。

## 4. 验证读写

1. 打开 Worker 地址，输入一条测试待办并添加。
2. 勾选完成，刷新页面，确认待办及完成状态都还在。
3. 删除测试待办，再刷新一次，确认已删除。也可以访问 Worker 地址下的 `/api/todos`，
   查看接口返回的列表。

如果页面能打开但读取待办失败，检查临时库是否到期、`schema.sql` 是否执行在同一个数据库、
目标环境是否已发布带 `DB` 绑定的版本，以及运行池是否允许访问公网源库。

## 切换到正式 PostgreSQL

正式使用前，请向团队或平台管理员申请正式 PostgreSQL。拿到源库后执行 `schema.sql`，
按需迁移需要保留的数据，为正式库新建 Hyperdrive 配置。将正式环境的 `DB` 绑定切到新配置，
重新发布并重复读写验收。如果需要保留临时库里的数据，请在其到期前导出。
