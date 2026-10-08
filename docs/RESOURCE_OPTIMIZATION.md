# 后台资源优化与复现

轮询继续使用 `detector.polling.interval_minutes: 1`。以下优化同时适用于 Node、Go、tRPC-Go，以及 macOS / Linux；不需要添加配置或重新填写凭证。

## 已实现的优化

- 状态文件在业务状态变化时立即写入，状态稳定时每 10 秒写一次保活。健康检查仍以 20 秒内的状态为有效，进程退出时强制写入停止状态。
- 队列计数和下一任务时间缓存于内存。每 10 秒只读取 SQLite `PRAGMA data_version`，只有本进程改变任务或其他连接提交修改时才重新扫描队列。通过管理命令手动重试，空闲 worker 正常运行时在约 10 秒内发现；网络调用进行中还需等待调用结束。
- 待发送任务按最早的发送或过期时间唤醒，临时失败退避、30 分钟 TTL 和窗口外继续重试保持原有行为。
- 相同直播状态、场次标识和开播时间不再反复更新 SQLite。标题变化不会生成新任务；下一场直播仍能再次通知。
- 主循环按状态保活、检测时间窗口边界、轮询、队列任务与官方心跳的最近时间等待。停止信号和官方事件会提前唤醒；官方连接的心跳要求继续遵守。

单实例锁仍需要独立保活，以便及时发现失效实例。官方长连接和 tRPC HTTP 服务也有各自的开销，因此不能将主循环等待时间当作整个进程的唤醒频率。

## 资源收益的边界

稳定空闲期间，状态文件保活从每秒一次变成每 10 秒一次，理论上这一项写入次数减少约 90%；队列全量查询不再随空闲循环重复发生。这不是总 CPU 或 SSD 写入减少 90%，也不能据此计算 Mac 寿命延长。尚未取得真实 CPU、内存、SSD 写入及耗电对比数据。

从 1 分钟改成 5 分钟会减少 B站请求，但内存常驻量不会按比例下降，开播检测延迟则会增大。本次保留 1 分钟，不引入阻止 Mac 休眠的操作。

## 本机更新

1. 拉取所选仓库并停止/重新启动实例前，确认部署目录和原 SQLite 状态位置；保持 `.env` 权限 600，不删除数据库。
2. Node 执行 `npm ci && npm run build`；Go 或 tRPC-Go 使用对应仓库的 `bin/setup` 构建。不要同时运行三个版本。
3. 在普通 Mac 终端通过既有服务管理命令重启，或执行 `launchctl kickstart -k "gui/$(id -u)/com.bilibili.live-monitor"`。只重启已经注册的服务，不重新授权本次开机。
4. 运行 `bin/monitor status`、`bin/monitor service health --side local`，确认新 PID、`polling_interval_seconds: 60`、`runtime_optimization_version: 1` 和 `heartbeat_interval_seconds: 10`。tRPC-Go 还可读取本机 `/status`。
5. 窗口外且无待发送任务时观察 `var/status.json`：更新时间约每 10 秒变化，`queue_refreshes` 不随空闲保活递增；业务变化可提前写入。

当前工作区提供 `outputs/update-resource-optimization.command`（位于各仓库之外）：检查公开文件清单、重启既有 Node Mac 服务、核验上述标记，随后推送并核对四个 GitHub 仓库。只应在已有部署的工作区使用，普通克隆按上述步骤更新。

## 云服务器更新

遵循同目录的 CLOUD.md，先停止原实例，再同步所选实现和构建产物；凭证保留于服务器，不随仓库上传。保持 `deployment.active: cloud`、原服务账户、工作目录和数据库路径。

使用既有 systemd 服务执行 `sudo systemctl restart bilibili-live-monitor.service`，再检查 `systemctl status`、服务日志和状态文件中的优化标记、60 秒轮询间隔。若实际安装的 unit 名称不同，使用安装时的 unit 名称。无需改变 systemd 的崩溃恢复配置。

本次没有真实云主机运行、跨 Mac 重启确认、实际管理进程崩溃恢复或手机提醒的新验收证据；这些仍按 MAC.md / CLOUD.md 的步骤单独验收。

## 自动测试

Node：`npm test`。Go / tRPC-Go：`go test -race ./...`、`go vet ./...`；构建后执行仓库的进程冒烟脚本。合集为 `python3 scripts/smoke-go.py`，独立 Go 仓库为 `python3 scripts/smoke.py`。

新增测试覆盖稳定状态写入节流、SQLite 外部提交/手动重试、TTL 早于重试的唤醒、相同场次零更新、空闲真实进程停止和锁释放。Node 还覆盖停止信号提前唤醒、分钟轮询间隔、失败重试和跨进程重启去重。测试使用临时数据库和模拟网络，不发送真实飞书消息。

## 本场发送成功后的检测频率

2026-10-08 新增了按当前场次发送状态调整频率，前一版只有状态写入、数据库与空闲调度优化。

```yaml
detector:
  mode: polling
  polling:
    interval_minutes: 1
    notified_live_interval_minutes: 5
    timeout_seconds: 5
```

- 未直播：每 1 分钟检测，发现开播立即持久化并发送通知。
- 直播中、当前场次通知未成功：继续每 1 分钟检测，发送队列按自己的退避时间重试。业务错误、永久失败和过期任务都不算发送成功。
- 飞书接口确认成功且 SQLite 记录本场 `sent`：改为每 5 分钟确认直播状态，持续直播和改标题不会重复发消息。
- 观测到下播：恢复 1 分钟，等待下一场；即使漏掉下播，只要平台返回新的稳定开播时间，也会识别新场次并再次通知。
- 重启：先立即确认一次房间状态，再按 SQLite 中当前场次的发送记录恢复频率，不靠内存中的“已发送”标志。
- 窗口结束：仍停止 B站查询，保留未过期任务的发送重试。官方事件模式继续使用事件与心跳，不应用轮询降频。

`notified_live_interval_minutes` 可配置 1–60，省略时默认为 5；实际低频间隔取它与正常间隔中的较大值。两个值相同即关闭降频。兼容原有 `interval_seconds`，它与 `interval_minutes` 仍互斥。

状态文件新增 `adaptive_polling_version: 1`、`polling_phase`、`effective_polling_interval_seconds`、`notified_live_interval_seconds`。原有 `polling_interval_seconds` 保留正常间隔，不因降频变成 300。`next_poll_at` 显示实际下一次查询时间。下播后的阶段是 `awaiting_start`，通知未成功是 `awaiting_notification`，成功后持续直播为 `notified_live`；窗口外为 `outside_window`。

降频阶段的 B站查询次数约减少 80%，不代表整周请求或机器总耗电减少 80%。低频确认可能让下播或下播后快速重开的识别延迟达到约 5 分钟；缺少稳定场次时间时，两次查询之间完整发生的下播重开仍可能漏掉。飞书 API 成功不等于手机已经弹出提醒；服务没有手机送达回执。GitHub 仓库保存代码，常驻监测仍由 Mac 或云服务器实例承担。

Mac 更新后必须重启服务。当前工作区提供 `outputs/update-adaptive-polling.command`，核验新 PID、正常 60 秒、通知成功后 300 秒及自适应版本标记，并核对四个远程仓库。云端沿用前文的构建、systemd 重启和状态核验步骤。

资源边界补强见 [RESOURCE_SAFETY.md](RESOURCE_SAFETY.md)。
