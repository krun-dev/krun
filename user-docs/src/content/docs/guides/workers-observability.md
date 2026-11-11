---
title: Workers 观测
description: 在 Worker 详情中查看请求数、HTTP 5xx 比例和响应延迟，并按环境与版本对比。
---

本指南可以直接阅读。在[控制台的 Workers 列表](/?page=workers)中打开你有权查看的 Worker，点击「指标」。看板默认使用详情页选中的环境，显示最近 1 小时的数据。

下方截图使用样例数据。打开「指标」页后，可以看到同一生产环境中两个版本的请求数、5xx 比例和响应延迟。

<figure class="krun-metrics-figure krun-metrics-overview">
  <div class="krun-metrics-screenshot">
    <img src="/docs/screenshots/workers-observability/workers-metrics-production.png" alt="Worker 详情的指标页，展示两个生产版本的三项指标" width="2220" height="2085" loading="lazy" />
    <span class="krun-metrics-tab" aria-hidden="true">1</span>
    <span class="krun-metrics-filters" aria-hidden="true">2</span>
    <span class="krun-metrics-time" aria-hidden="true">3</span>
  </div>
  <figcaption>
    <ol>
      <li>「指标」页签</li>
      <li>环境与版本筛选</li>
      <li>时间范围</li>
    </ol>
    <a href="/docs/screenshots/workers-observability/workers-metrics-production.png">查看原图</a>
  </figcaption>
</figure>

1. 在 Grafana 顶部用 **Environment** 选择生产或预览环境，用 **Version** 选择一个或多个版本。曲线按环境和实际处理请求的版本分组，可用于比较发布或回滚前后的流量。
2. 用右上角的时间选择器覆盖要比较的部署时段。版本列表来自所选环境和时间范围内采集到的数据。如果找不到旧版本，先扩大时间范围。
3. 如果看板要求登录，点击「在 Grafana 中打开」，用有该数据源访问权限的 Grafana 账号登录，再回到「指标」页刷新。

在 **Environment** 中选择 **All**，可以把生产环境和预览环境的版本放在同一张图里比较。

<figure class="krun-metrics-figure">
  <div class="krun-metrics-screenshot">
    <img src="/docs/screenshots/workers-observability/workers-metrics-environments.png" alt="指标看板同时展示两个生产版本和一个预览版本" width="1764" height="1026" loading="lazy" />
    <span class="krun-metrics-requests" aria-hidden="true">1</span>
    <span class="krun-metrics-errors" aria-hidden="true">2</span>
    <span class="krun-metrics-latency" aria-hidden="true">3</span>
  </div>
  <figcaption>
    <ol>
      <li>请求数</li>
      <li>HTTP 5xx 比例</li>
      <li>P50、P95 和 P99 响应延迟</li>
    </ol>
    <a href="/docs/screenshots/workers-observability/workers-metrics-environments.png">查看原图</a>
  </figcaption>
</figure>

## 指标口径

| 面板 | 含义 |
| --- | --- |
| Requests | 每个图表时间区间内转发到 Worker 执行并结束的 HTTP 请求数。静态资产直出走独立路径。 |
| HTTP 5xx ratio | 上述请求中返回 HTTP 5xx，或转发后在拿到响应前失败的比例。无请求时该时间区间留空。 |
| Response latency | 从转发开始到拿到 Worker 响应的 P50、P95 和 P99，包含调用过程中发生的冷加载。 |

版本级数据从平台升级 serving runtime 后开始积累。如果三个面板都显示 `No data`，先选中有流量的环境和时段，再请求一个已知会执行 Worker 的 HTTP 路径。采集周期默认为 30 秒。发送请求后等待至少两个采集周期，再刷新看板。

## 看板排查

| 现象 | 原因和处理 |
| --- | --- |
| 「尚未配置 Grafana 指标看板。」 | 控制面未配置 Grafana。联系平台管理员配置 `worker_metrics.grafana_url`。 |
| 直开 Grafana 后仍提示无权访问 | 当前 Grafana 账号缺少看板或数据源的访问权限。联系 Grafana 管理员为账号授予对应范围的访问权限。 |
| 直开 Grafana 能看到数据，嵌入区域为空或浏览器显示拒绝连接 | 嵌入设置或浏览器会话可能阻止加载。先在 Grafana 登录并刷新指标页。若仍失败，请让平台管理员检查两端的 CSP 与 Grafana 嵌入配置。 |

[Markdown 原文](/docs/guides/workers-observability.md.txt)
