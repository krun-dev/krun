---
title: Workers 观测
description: 在 Worker 详情中按环境和版本对比请求数、HTTP 5xx 比例、响应延迟和 CPU 时间。
---

本指南可以直接阅读。在[控制台的 Workers 列表](/?page=workers)中打开你有权查看的 Worker，点击「指标」。看板默认使用详情页选中的环境，显示最近 1 小时的数据。

下方截图使用样例数据。打开「指标」页签 ①，可以看到同一生产环境中两个版本的请求数、5xx 比例和响应延迟。

[![① 指标页签，② 环境与版本筛选，③ 时间范围](/docs/screenshots/workers-observability/workers-metrics-production.png)](/docs/screenshots/workers-observability/workers-metrics-production.png)

1. 在 Grafana 顶部的环境与版本筛选区 ②，用 **Environment** 选择生产或预览环境，用 **Version** 选择一个或多个版本。曲线按环境和实际处理请求的版本分组，可用于比较发布或回滚前后的流量。
2. 用右上角的时间选择器 ③ 覆盖要比较的部署时段。版本列表来自所选环境和时间范围内采集到的数据。如果找不到旧版本，先扩大时间范围。
3. 如果看板要求登录，点击「在 Grafana 中打开」，用有该数据源访问权限的 Grafana 账号登录，再回到「指标」页刷新。

在 **Environment** 中选择 **All**，可以同时比较生产和预览版本的请求数 ①、HTTP 5xx 比例 ②，以及 P50、P95 和 P99 响应延迟 ③。

[![① 请求数，② HTTP 5xx 比例，③ 响应延迟](/docs/screenshots/workers-observability/workers-metrics-environments.png)](/docs/screenshots/workers-observability/workers-metrics-environments.png)

向下滚动到 **Worker CPU time** 面板 ④，按选定环境和版本对比 CPU 消耗速率。数据汇总自各 serving Pod。面板仅显示使用支持 CPU 计量的 Linux AMD64 runtime 收集的数据。下方截图使用样例数据。

[![④ Worker CPU time 面板](/docs/screenshots/workers-observability/workers-cpu-time.png)](/docs/screenshots/workers-observability/workers-cpu-time.png)

也可以用 CLI 获取同一看板地址。先按 [CLI 指南](/docs/guides/krun-cli/)安装并登录，或设置 `KRUN_ENDPOINT` 和有 Worker 读取权限的 `KRUN_TOKEN`。用 `krun org list` 查组织 ID，并用 `krun workers list --org ORG_ID` 查 Worker ID。

```bash
krun workers metrics --org ORG_ID --worker WORKER_ID
```

## 指标口径

| 面板 | 含义 |
| --- | --- |
| Requests | 每个图表时间区间内转发到 Worker 执行并结束的 HTTP 请求数。静态资产直出走独立路径。 |
| HTTP 5xx ratio | 上述请求中返回 HTTP 5xx，或转发后在拿到响应前失败的比例。无请求时该时间区间留空。 |
| Response latency | 从转发开始到拿到 Worker 响应的 P50、P95 和 P99，包含调用过程中发生的冷加载。 |
| Worker CPU time | 每秒消耗的请求执行线程 CPU 秒数。I/O 等待、Worker 启动和其他线程的 CPU 时间不计入。异常退出或采集间隙可能导致少记。 |

版本级数据从平台升级 serving runtime 后开始积累。如果三个面板都显示 `No data`，先选中有流量的环境和时段，再请求一个已知会执行 Worker 的 HTTP 路径。采集周期默认为 30 秒。发送请求后等待至少两个采集周期，再刷新看板。

## 看板排查

| 现象 | 原因和处理 |
| --- | --- |
| 「尚未配置 Grafana 指标看板。」 | 控制面未配置 Grafana。联系平台管理员配置 `worker_metrics.grafana_url`。 |
| 直开 Grafana 后仍提示无权访问 | 当前 Grafana 账号缺少看板或数据源的访问权限。联系 Grafana 管理员为账号授予对应范围的访问权限。 |
| 直开 Grafana 能看到数据，嵌入区域为空或浏览器显示拒绝连接 | 嵌入设置或浏览器会话可能阻止加载。先在 Grafana 登录并刷新指标页。若仍失败，请让平台管理员检查两端的 CSP 与 Grafana 嵌入配置。 |

[Markdown 原文](/docs/guides/workers-observability.md.txt)
