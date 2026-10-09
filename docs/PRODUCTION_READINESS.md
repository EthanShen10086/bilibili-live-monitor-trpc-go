# 上线改造与运维约定

## 组件选择

当前一个房间、一分钟轮询、每场一次通知，推荐 `config.light.yaml`：SQLite 持久化 + 数据库任务队列 + 进程内短缓存。数据库必需，用于保存观察、场次去重、任务、重试和失败记录；缓存用于减小只读状态查询成本，不缓存健康探针。

需要多个云端副本、独立发送进程或多机故障接管时，选择 PostgreSQL。业务通过 Repository、Detector、Notifier、TaskQueue、Clock、Observer 接口注入，适配器与 worker 协调分开。默认数据库队列已经提供持久化、原子领取、幂等键、重试、TTL、失败终态。Redis Streams 是可选唤醒通道，PostgreSQL 才是通知记录的依据；即使丢失唤醒，数据库扫描仍能恢复。Redis 缓存每次最多等待 250ms，失败后绕过五秒，启动时所选依赖不可用仍明确报错。

Kafka 暂不增加。它适合需要多消费者、长期事件回放、多个下游系统或高吞吐的事件平台。单房间监控增加 Kafka 会多出 broker、磁盘、分区和消费组运维成本，并不能解决外部飞书 API 与数据库之间的原子提交。以后出现这些需求时，通过 TaskQueue 接入，并继续保留事务 outbox、稳定事件键和消费者去重。

参考：[Kafka 使用场景](https://kafka.apache.org/22/getting-started/uses/)、[Prometheus 埋点](https://prometheus.io/docs/practices/instrumentation/)、[告警实践](https://prometheus.io/docs/practices/alerting/)。不照搬公司内网插件；采用公开 tRPC 框架的插件初始化、过滤器恢复和生命周期。

## 运行行为

- 检测和单个发送协程并行，协调循环独占持久化结果处理。慢通知不阻塞下一次检测或心跳；不并发发送同一进程的任务。
- SIGTERM 停止新任务，当前发送最多 40s，完成后提交数据库。worker 最多等待 42s 收尾，官方会话清理最多 12s，外层 supervisor 留 80s。退出超时仍可能造成重发，不能承诺 exactly-once。
- HTTP 429/503 遵守 `Retry-After`（最多 300s），指数退避加正向抖动；永久业务失败进入 failed，避免无意义重试。配置未知字段和多 YAML 文档直接拒绝，降低拼写错误静默生效的风险。
- 云端 JSON 日志输出到标准流，可由 systemd、Docker 或日志平台收集；本地事件日志有大小限制。日志、指标、trace 不记录请求 URL、凭证、响应正文或消息内容。
- 进程看门狗 30s 启动宽限，每 10s 检查：状态心跳超过 20s 或业务进度超过 90s 时终止 host。飞书永久错误保持可观测，不触发重启循环。Docker 的 unhealthy 本身不会重启容器，进程看门狗与 restart policy 配合才负责恢复。

## 探针与可观测

默认状态服务 `127.0.0.1:19029`；19028 是框架管理端口。

| 路径/命令 | 用途 |
| --- | --- |
| `/livez` / `healthcheck live` | 进程心跳和调度推进，供 supervisor 使用 |
| `/readyz` / `healthcheck ready` | 初始化完成且没有阻断性配置/权限错误 |
| `/healthz` / `healthcheck business` | 检测、队列和通知业务健康，供告警使用 |
| `/status` | 独立的检测/发送状态、错误、队列数量和最早积压时间 |
| `/metrics` | Prometheus 固定标签指标、延迟、队列、健康、进度与 Go 进程指标 |

健康探针均不缓存，窗口外和正常 standby 仍健康。通知失败不会伪装成成功。failed 历史记录在修复/人工重试或保留期清理之前持续告警，值班者必须处理。

将 `deploy/observability/prometheus.yaml` 和 `alerts.yaml` 接入现有 Prometheus，再通过现有 Alertmanager 配置真实收件人。示例覆盖无法抓取、调度卡住、业务失败、死信和积压。指标只走回环/私网；Nginx 默认没有公开 `/metrics`、`/livez` 或 admin。宿主端口或 sender 数量改变时同步修改抓取目标。独立 sender 的状态尚未聚合成全局订阅面板。

可选追踪：

```yaml
observability:
  tracing: true
  sample_ratio: 0.1
  service_name: bilibili-live-monitor
```

配置标准环境变量 `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` 指向现有 OTLP HTTP Collector 的 `/v1/traces`；需要鉴权时通过标准 `OTEL_EXPORTER_OTLP_TRACES_HEADERS` 注入，不写进 YAML/仓库。默认关闭 trace；队列最多 256 spans，导出超时 3s，退出 flush 最多 5s。记录 detector/notification/http 嵌套 span，失败日志携带 trace/span ID，仅保留安全的错误类型；并非所有官方 WebSocket 事件都有独立 span。

## 迁移与备份恢复

PostgreSQL 使用全局迁移账本 `lm_schema_migrations`，保存版本、源码校验和与时间。现有无账本数据库通过幂等 migration 001 接管；未知版本或修改过的已应用 migration 拒绝启动。新迁移必须新增文件/版本，不能改旧文件。账本不代替数据库完整性检查。

生产模板 `auto_migrate: false`，运行前显式迁移：

```sh
cd deploy/cloud
./manage.sh platform prepare
# 填 .env，审核 config.yaml
./manage.sh platform check
./manage.sh platform migrate
./manage.sh platform up
```

管理命令 `monitor --root ROOT platform-migrate` 只迁移并校验订阅绑定，不发通知；自动迁移默认 true 用于兼容旧配置，新生产部署显式关闭。当前只有 additive migration 001，不支持自动破坏性降级。

SQLite 可以在线生成一致性快照，不直接复制正在写入的文件：

```sh
monitor --root /opt/live-monitor backup /secure/backups/monitor-20261009.sqlite
# 停止源和目标实例；新目标先配置 config.yaml/.env，var/state.sqlite 必须不存在
monitor --root /opt/live-monitor-restored restore /secure/backups/monitor-20261009.sqlite
```

输出权限 600，拒绝覆盖既有备份/目标。恢复会验证完整性与业务表并获取实例锁，保留去重和任务。恢复旧快照可能再次发送快照后已成功的通知，恢复前应对账。

PostgreSQL 使用 `pg_dump -Fc`，把备份保存在应用卷以外并定期同步到异机；先验证工具版本和权限，避免在命令行打印 DSN：

```sh
umask 077
docker compose --profile platform -f compose.yaml -f platform.compose.yaml exec -T postgres pg_dump -U monitor -d monitor -Fc > /secure/backups/monitor.dump
# 在空的隔离数据库演练恢复，停止全部 detector/sender，再进行生产恢复
# pg_restore --exit-on-error --single-transaction --no-owner -d EMPTY_DATABASE monitor.dump
```

恢复包括迁移账本、场次和队列；Redis 可从 PostgreSQL outbox 重建，不作为唯一备份。备份保留、异机副本和定时调度由部署平台设置，这次没有配置用户的实际备份账户或通知收件人。

## 上线验收

CI 执行 race、vet、gofmt、govulncheck、真实 PostgreSQL/Redis 集成、coverage artifact、二进制进程和 TLS 代理冒烟，以及容器运行/探针验证。覆盖率是可检查报告，不把总行覆盖率当作业务验收。

上线前仍需用真实账户验证通知可见性、手机提醒、正式服务器的退出/重启恢复、告警投递和备份恢复。检查时区/NTP、磁盘容量与 inode、OOM、证书续期、上游配额、Secret 轮换和出站网络。当前低频轮询可能漏掉两次查询之间的短直播；通知后五分钟确认会扩大下播观测延迟，如需更短时延应缩短配置或接入授权事件。没有订阅 CRUD、共享房间探测和自动凭证轮换，规模扩展前再实现，不能把增加进程数视作无限扩容。
