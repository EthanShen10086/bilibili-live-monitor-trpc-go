# 分钟级轮询配置

2026-10-08：默认从 10 秒改为 1 分钟。查询间隔原本可以按秒配置，现在优先按整分钟配置。

```yaml
detector:
  mode: polling
  polling:
    interval_minutes: 1
    timeout_seconds: 5
```

interval_minutes 支持 1–60 的整数，可改为 2、5 等。旧 interval_seconds 仍兼容；分钟、秒字段必须且只能填一个，不同时保留。timeout_seconds 仍是单次 HTTP 超时，不是轮询间隔。

每次查询结束后再等待配置间隔，避免请求重叠。窗口进入或服务恢复仍先查询一次；窗口外停止 B站查询。失败退避不会比正常配置更频繁，退避上限是 5 分钟与正常间隔的较大值。通知队列、重试和心跳继续工作，不会因分钟间隔而停止处理已生成的消息。

status 的 polling_interval_seconds 为换算后的生效值（默认 60），next_poll_at 为计划的下次查询时间；窗口外没有下次轮询时间。Go 输出为毫秒时间戳，Node 窗口外为 null。

修改配置后重启当前服务：

```sh
./bin/monitor check-config
./bin/monitor service stop --side local
./bin/monitor service start --side local
./bin/monitor service health --side local
./bin/monitor status
```

云端对应 --side cloud；配置切换过程继续使用 switch local/cloud。Mac 同次开机的批准会复用，不要求再次授权当前开机；跨电脑重启仍需确认。

默认时段健康查询约 1,440 次/周/房间，是原来的约六分之一，实际包含网络耗时和失败退避。查询可见后的检测延迟约 0–1 分钟再加请求耗时；完整发生在两次查询之间的短直播可能漏掉。

回归覆盖单位换算、旧秒配置、缺失/冲突/非法分钟、分钟退避，以及真实 worker 调度。没有修改飞书凭证、SQLite 去重状态或服务开机确认规则。
