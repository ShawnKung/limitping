# limitping

[![CI](https://github.com/ShawnKung/limitping/actions/workflows/ci.yml/badge.svg)](https://github.com/ShawnKung/limitping/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`limitping` 是一个面向 Codex 订阅的轻量命令行工具：查询 5 小时/每周用量与重置券，
并可使用 Codex CLI 当前可见的最低优先级模型发送一个最小 `ping`。它也可以把用量和
ping 状态推送到 Prometheus Pushgateway。

> [!IMPORTANT]
> 这是非官方项目，与 OpenAI 无隶属或背书关系。用量接口属于未公开的 Codex 后端实现，
> 未来可能变化。`codex exec` 会消耗 token，也不保证一定启动新的订阅窗口。

## 致谢与项目定位

本项目大量代码、实现思路和产品灵感源于
[wavever/CCLimitPing](https://github.com/wavever/CCLimitPing)。特别感谢
[wavever](https://github.com/wavever) 开源了完整项目，并清晰梳理了 Codex 用量读取、
OAuth 刷新和窗口触发机制；没有这些工作，`limitping` 不会以现在的形式存在。

这个仓库不是 CCLimitPing 的完整替代品，而是针对个人自动化场景做的轻量、Codex-only
实现：

| | CCLimitPing | 本项目 `limitping` |
| --- | --- | --- |
| Provider | Claude Code、Codex、Spark | 仅 Codex |
| 运行方式 | 手动、`watch`、`schedule`、后台常驻 | 只有 `status` / `ping`；配合 cron、LaunchAgent 或其他调度器运行 |
| 后台能力 | 内置 `bg start/status/logs/stop`、会话钩子 | 不启动后台进程，不管理守护服务 |
| 配置与功能面 | 配置文件、通知、续跑、兑换、升级等完整能力 | 尽量少的 flags，无独立配置文件 |
| ping 方式 | 交互式官方 CLI | `codex exec` headless 调用 |
| 可观测性 | 本地状态与日志 | 可选 Prometheus Pushgateway metrics，并记录最近成功 ping 时间 |

如果需要多 Provider、内置 watch/background、活跃会话检测或自动续跑，应优先使用
CCLimitPing；如果只需要一个可嵌入现有 cron/plist/监控体系的 Codex 小工具，本项目更合适。

## 功能

- 显示 Codex 5 小时与每周剩余额度、重置时间和重置券。
- `--json` 输出机器可读快照。
- 从 `codex debug models` 动态选择最低优先级的可见模型。
- `--dry-run` 在执行模型调用或推送指标前预览操作。
- `--if-5h-full` 只在 5 小时额度恢复到 100% 时执行 ping；指标仍正常上报。
- 将用量、重置券数量和最近成功 ping 时间推送到 Pushgateway。
- 成功 ping 时间持久化，后续上报不会被 `ping_completed=0` 覆盖。

## 环境要求

- Go 1.22 或更高版本（从源码安装时）。
- 已安装并登录的官方 `codex` CLI。
- macOS 或 Linux。Windows 尚未测试。

`limitping` 复用 `~/.codex/auth.json`；设置 `CODEX_HOME` 时则读取
`$CODEX_HOME/auth.json`。

## 安装

安装最新 GitHub Release 到 `~/.local/bin`：

```sh
curl -fsSL https://raw.githubusercontent.com/ShawnKung/limitping/main/install.sh | sh
```

脚本支持 macOS/Linux 的 amd64 与 arm64，会校验 SHA-256，并以 `0755` 权限安装
`~/.local/bin/limitping`。建议执行前先打开并检查脚本内容。

使用 Go：

```sh
go install github.com/ShawnKung/limitping/cmd/limitping@latest
```

从源码安装到 `~/.local/bin`：

```sh
git clone https://github.com/ShawnKung/limitping.git
cd limitping
make install
```

可以覆盖安装目录：

```sh
make install BINDIR=/custom/bin
```

## 使用

```console
$ limitping status
正在查询 codex 用量...
codex (plus)
  5h     [█████████░]  剩余  92.0%         30分 后重置 (周六 22:28 UTC+8)
  周     [████████░░]  剩余  80.0%  5天 2时28分 后重置 (周五 00:27 UTC+8)
  重置券 1 张可用
```

常用命令：

```sh
limitping status
limitping status --json
limitping version
limitping ping --dry-run
limitping ping
limitping ping --if-5h-full
limitping ping --push-metric https://pushgateway.example.com
limitping status --push-metric https://pushgateway.example.com
```

`ping` 会忽略 `visibility != "list"` 的内部模型，选择 `priority` 数值最大的可见模型，
然后执行：

```text
codex exec -m <model> -c model_reasoning_effort=low ping
```

`--if-5h-full` 要求用量响应包含 5 小时窗口，并且 `used_percent == 0`。不满足时命令正常
退出；如果同时设置了 `--push-metric`，仍会推送最新用量，并将本次
`limitping_ping_completed` 记为 `0`。

## 预触发 5h 窗口能少等多久？

下面给出一个简化模型，用来量化“让 5h 窗口尽量连续启动”的收益。它不是 Codex
计费系统的精确仿真，而是便于理解的等待时间模型：

- 窗口长度为 $T=5$ 小时；
- 真实使用会话按强度为 $\lambda$（次/小时）的泊松过程到达；
- 每个预先启动的窗口内，第一次真实使用到来后会很快耗尽额度；
- 只统计该窗口内至少发生一次真实使用的情况，不计网络延迟和 `ping` 自身消耗。

令 $X$ 为窗口启动后第一次真实使用到来的时间。条件于 $X<T$，它服从截断指数分布：

```math
f_{X\mid X<T}(x)=\frac{\lambda e^{-\lambda x}}{1-e^{-\lambda T}},\qquad 0\le x<T
```

不预触发时，第一次真实使用才启动窗口；假设额度随即耗尽，下一次重置仍需等待约
$T$。预触发时，这次使用到达时窗口已经运行了 $X$，只需再等待 $T-X$。因此平均减少
的等待时间为：

```math
\mathbb E[\Delta]
=\mathbb E[X\mid X<T]
=\frac{1}{\lambda}-\frac{T}{e^{\lambda T}-1}
```

预触发后的平均等待时间为：

```math
\mathbb E[W_{\mathrm{ping}}]
=T-\mathbb E[\Delta]
=T-\frac{1}{\lambda}+\frac{T}{e^{\lambda T}-1}
```

代入 $T=5$ 小时：

| 平均真实使用频率 | $\lambda$ | 平均少等 | 预触发后平均等待 | 相对 5h 减少 |
| --- | ---: | ---: | ---: | ---: |
| 每 10 小时 1 次 | 0.1/h | 2时18分 | 2时42分 | 45.9% |
| 每 5 小时 1 次 | 0.2/h | 2时05分 | 2时55分 | 41.8% |
| 每 2 小时 1 次 | 0.5/h | 1时33分 | 3时27分 | 31.1% |
| 每小时 1 次 | 1/h | 58分 | 4时02分 | 19.3% |
| 每 30 分钟 1 次 | 2/h | 30分 | 4时30分 | 10.0% |

当真实使用非常稀疏时，条件到达时刻趋近于在 5 小时窗口内均匀分布，此时平均少等
$T/2=2.5$ 小时，也就是约 **50%**。这也是“随机时刻到达”直觉下的简单答案。使用越
频繁，第一次真实使用通常越靠近窗口起点，本来就会很快自然启动窗口，所以预触发的
边际收益越小。

这个模型只回答“第一次真实使用快速耗尽额度后，距离下一次重置还有多久”。如果要
计算额度耗尽后陆续到来的其他会话的总排队时间，还需要额外给出单个窗口的额度、每次
会话消耗量和会话持续时间。

## Prometheus Pushgateway

`--push-metric <endpoint>` 是全局选项，可用于 `status` 和 `ping`。endpoint 必须是
不含认证信息、查询参数或 fragment 的 `http://` 或 `https://` URL。指标通过 HTTP
`PUT` 推送到：

```text
<endpoint>/metrics/job/limitping/instance/<hostname>/collector/limitping
```

主要指标：

| 指标 | 含义 |
| --- | --- |
| `limitping_window_used_ratio` | 5h/周窗口已用比例 |
| `limitping_window_remaining_ratio` | 5h/周窗口剩余比例 |
| `limitping_window_remaining_seconds` | 距窗口重置的秒数 |
| `limitping_window_reset_timestamp_seconds` | 窗口重置时间戳 |
| `limitping_reset_credits_available` | 可用重置券数量 |
| `limitping_reset_credit_expiration_timestamp_seconds` | 最早到期的可用重置券时间戳 |
| `limitping_ping_completed` | 本次是否完成 ping（0/1） |
| `limitping_last_successful_ping_timestamp_seconds` | 最近一次成功 ping 的时间戳 |
| `metrics_pusher_collector_success` | 本次采集是否成功 |
| `metrics_pusher_last_run_timestamp_seconds` | 最近采集开始时间 |

最近成功 ping 保存在：

```text
${XDG_STATE_HOME:-$HOME/.local/state}/limitping/last-successful-ping
```

### Grafana 示例看板

[`contrib/grafana/limitping-dashboard.json`](contrib/grafana/limitping-dashboard.json) 是从实际
部署中导出并脱敏的示例看板，包含推送健康度、5h/周余量、重置时间、重置券、最近成功
ping 和历史趋势等 15 个 panels。

在 Grafana 中选择 **Dashboards → New → Import**，上传 JSON，并为 `DS_PROMETHEUS`
选择抓取 Pushgateway 的 Prometheus datasource。看板查询按 Prometheus 抓取
Pushgateway 的默认 `honor_labels: false` 行为编写，因此使用 `exported_job` 和
`exported_instance` 标签。例如：

```yaml
scrape_configs:
  - job_name: push_gateway
    static_configs:
      - targets: ["pushgateway:9091"]
```

如果启用了 `honor_labels: true`，需要把看板查询中的 `exported_job` /
`exported_instance` 改为 `job` / `instance`。

> [!CAUTION]
> Pushgateway 会收到主机名、订阅计划、用量比例、重置时间和可用重置券数量。请使用可信
> endpoint；跨不可信网络时应使用 HTTPS，不要在 URL 中放用户名、密码或 token。

## macOS LaunchAgent 示例

仓库提供了不含个人地址的模板：
[`contrib/launchd/io.github.shawnkung.limitping.plist.example`](contrib/launchd/io.github.shawnkung.limitping.plist.example)。

将模板中的 `__HOME__` 和 `__PUSHGATEWAY_URL__` 替换为实际值，创建日志目录后复制到
`~/Library/LaunchAgents/io.github.shawnkung.limitping.plist`，再加载：

```sh
mkdir -p ~/.local/state/limitping
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/io.github.shawnkung.limitping.plist
```

模板默认登录后立即运行，并每 120 秒执行一次。代理环境、日志路径和周期应按自己的环境
调整。

## 安全与隐私

- `limitping` 不输出或上传 Codex access token、refresh token 或 account ID。
- 用量接口返回 401 时，工具可能使用 refresh token 刷新登录，并以 `0600` 权限原子更新
  `auth.json`。
- `--dry-run` 不调用 `codex exec`，也不向 Pushgateway 发送请求。
- 报告安全问题时，请勿在公开 issue 中粘贴 `auth.json`、token 或完整调试日志。

## 开发

```sh
make check   # gofmt 检查、go vet、单元测试
make build
```

## 发布

推送形如 `v0.1.0` 的 tag 会触发 GitHub Actions 创建 Release：

```sh
git tag -a v0.1.0 -m "limitping v0.1.0"
git push origin v0.1.0
```

Release workflow 会把 tag 注入版本号、把 tag 对应的 Git commit 注入构建信息，并生成
以下四种发布包及 `checksums.txt`：

- macOS amd64
- macOS arm64
- Linux amd64
- Linux arm64

可以用下面的命令核对安装包来源：

```console
$ limitping version
limitping 0.1.0
commit: 0123456789abcdef0123456789abcdef01234567
```

提交规范与安全报告方式见 [CONTRIBUTING.md](CONTRIBUTING.md) 和
[SECURITY.md](SECURITY.md)。

## 许可证与归属

本项目采用 [MIT License](LICENSE)。部分用量查询与 OAuth 刷新实现改编自 MIT 许可的
[wavever/CCLimitPing](https://github.com/wavever/CCLimitPing)，完整归属见
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
