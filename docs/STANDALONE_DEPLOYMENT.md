# 独立云服务与 Mac 到云端交接

部署源是 `EthanShen10086/bilibili-live-monitor-trpc-go` 的确定 main 提交。
本仓库自己的 Go 模块、源码、配置、容器和测试即可构建运行；不依赖 Node 仓库、
Mac 目录、相邻 Go 仓库或本地 replace。编译产物无需安装 Node/Go/SQLite 动态库。

## 选择入口与数据库

| 使用方式 | 入口/部署配置 | 持久化 | 其他基础设施 |
| --- | --- | --- | --- |
| 单订阅 SQLite | `cmd/monitor`，`manage.sh light` | 持久化卷中的 SQLite | 无，网关可选 |
| 单订阅 PostgreSQL | `cmd/monitor`，`manage.sh postgres` | PostgreSQL | 无 Redis/Kafka，网关可选 |
| 原 PostgreSQL/Redis 配置 | `cmd/monitor`，`manage.sh platform` | PostgreSQL | 可选缓存/Streams，保留兼容 |
| 多租户事件平台 | `cmd/event-platform`，`deploy/event-platform` | PostgreSQL | Kafka、Keycloak、APISIX 或 Nginx |

前两种都可以独立部署云服务器，不需要先在 Mac 运行 Go 服务。
完整事件平台目前不提供 SQLite 适配器；不能仅替换 DSN 就使用 SQLite。
SQLite 适合单实例、本机持久化磁盘，不支持多个主机共同写一个文件。
PostgreSQL 模式支持调度与任务租约；多租户和共享房间检测属于事件平台入口。
原单订阅模块仍服务于 SQLite/PostgreSQL 和 CLI，属于受支持代码，不能当作废弃代码删除。

### Redis、Kafka 与性能

SQLite 默认没有 Redis，`cache: memory` 用于状态接口的 2 秒内存缓存。
PostgreSQL-only 同样有状态接口内存缓存，并非完全无缓存。
原 `platform` 配置已经使用 Redis 状态缓存与 Streams；Redis 检测观察缓存最多
5 秒，按订阅隔离，不是跨房间/跨租户的数据库查询缓存。
状态、场次、通知去重与任务终态始终以数据库为准；运行中 Redis 缓存故障回源，
Streams 唤醒丢失由 PostgreSQL 扫描恢复。显式选择 Redis 后启动连接失败仍报错，
不悄悄更换部署模式。

Kafka 平台提高多消费者解耦、积压恢复和回放能力，不能直接加速 SQL，也不能保证
单次通知延迟更低。Redis 优化重复读取，无法加速每次都必须执行的事务写入。
单房间的默认检测间隔为一分钟，增加中间件不自动提高发现直播的速度。
多租户业务首先采用事件平台的按房间租约共享检测；管理 API 统计查询是否需要
Redis，要依据重复查询比例、SQL p95 和数据库负载测量，再明确缓存失效策略。
当前未宣称实现事件平台的统计结果缓存，也没有同等负载的 SQLite/PG/Redis/Kafka
对比压测结果，不能根据组件数量宣称哪种模式更快。

Linux Docker Engine + Compose v2，在仓库根目录的 `deploy/cloud` 下选择一种：

```sh
./manage.sh light prepare
# 填 .env，审核 config.yaml 后：
./manage.sh light check
./manage.sh light up
```

或者在新的安装目录选择 PostgreSQL-only：

```sh
./manage.sh postgres prepare
# 填飞书变量、POSTGRES_PASSWORD、MONITOR_POSTGRES_DSN；不需要 Redis 变量
./manage.sh postgres check
./manage.sh postgres migrate
./manage.sh postgres up
```

`prepare` 不覆盖现有配置；改数据库要先停止、备份并迁移状态，不能用 prepare 原地
切换。`down` 保留卷。不要同时用 systemd 和 Docker 管理同一订阅。
宿主机仅需出站 B站/通知服务；状态与指标默认回环监听，管理端口不公开。
systemd/二进制交付见 [CLOUD.md](CLOUD.md)；完整事件平台见
[部署包](../deploy/event-platform/README.md)。

## 当前 Mac 的已核实运行来源

2026-10-09 只读核实：LaunchAgent `com.bilibili.live-monitor`，Node PID `63023`，
实际根目录为
`/Users/ethanshen/Documents/Codex/2026-10-04/https-live-bilibili-com-1616-broadcast/outputs/live-monitor`。
启动参数是 `dist/src/cli.js --root <该目录> run --managed local`，使用
`var/state.sqlite`。15 个 TypeScript 源文件与独立 `bilibili-live-monitor-node` 源码一致，
但运行目录本身没有 `.git`，因此不能称作直接运行该仓库 checkout。
PID 和进程状态是当时快照，上线时重新核实；本次审查未停止或重启它。

## 云端验收与正式交接顺序

1. 在云端部署确定 main SHA，用独立测试房间/接收对象和独立状态验证容器或
   systemd、探针、指标、日志、重启、出站网络和备份恢复。用生产接收对象测试通知
   需要明确接受该次真实发送；不能让测试实例同时消费正式订阅状态。
2. 停止云端测试发送端，保留正式发送端未启动。备份 Mac 配置、凭证和运行状态。
3. 在 Mac **实际运行目录**用原 Node 管理命令停服务，命令同时禁用 launchd 自动启动：

   ```sh
   ./bin/monitor service stop --side local
   ./bin/monitor service assert-stopped --side local
   launchctl print-disabled gui/$(id -u)
   ```

   核对该 label 已禁用、原进程已退出，SQLite 实例锁不再被持有。仅 kill PID 不够，
   `KeepAlive` 会再次拉起。无法证明停止时，禁止启动正式云端发送端。
4. 源停止后生成最终一致性 SQLite 快照。可以使用本仓库的 Mac Go 二进制执行
   `monitor --root SOURCE_ROOT backup /absolute/new-snapshot.sqlite`；备份命令不发送消息，
   包含已提交数据、sent/pending 和去重键，拒绝覆盖。不要复制正在写入的文件/WAL。
5. SQLite 云端：停止目标，将快照恢复到空目标 `var/state.sqlite`；核对 config 的房间、
   时间窗口、通知对象和时区，设置 `deployment.active: cloud`。PostgreSQL 单订阅云端：
   先 `platform-migrate`，再用 `platform-import-sqlite` 导入完整停止源根目录，目标 scope
   必须为空；源配置和状态安全传输，凭证各端独立准备。完整事件平台按
   [受控导入说明](event-platform/MIGRATION.md) 执行，先导入禁用的订阅。
6. 核对源/目标场次、sent/pending、TTL、尝试次数和通知对象，再启动一个正式云端
   发送端。验收真实通知后保留 Mac 服务禁用状态。禁止用空数据库开始同一场直播，
   否则可能再次通知。
7. 回滚先停并证明云端发送端退出，再对账迁移云端新增发送记录，最后恢复 Mac。
   不能直接恢复旧快照并开启两个发送端；外部 API 接收成功但本地记录失败仍可能重发。

Node 的 `switch cloud` 不能代替本次跨实现、跨存储交接的验收。
本流程是未来操作手册，当前尚未取得服务器部署和真实收件证明，不提前停 Mac。

## 质量、性能与证明边界

`make verify` 执行固定格式/lint、竞态测试、模块一致性、可达漏洞扫描和上传文件检查；
pre-commit/pre-push 用 Git 快照，main 要求 `go/platform/contracts` 三项 CI。
云 CI 覆盖实际 SQLite 与 PostgreSQL-only 容器、真实数据库/Redis、探针和退出；
事件 CI 覆盖双网关、OIDC、Kafka 故障恢复、多渠道、回放及日志/trace/告警。

`make benchmark` 测量 SQLite 不变观察的去重路径、10,000 条队列统计的延迟和分配。
在同一机器比较基线；结果不等于云端容量保证。正式机器还要记录容器 RSS/CPU、
磁盘增长、FD、API p95、探针失败、队列最老年龄和通知延迟；依据实际负载设容量阈值。
已有索引、空闲节流、响应/队列限制、超时、退避、任务租约、日志轮转和进程看门狗。
不公开 pprof；需要诊断时用受控环境和 SSH 隧道，并避免收集凭证。

测试和监控能发现已覆盖的故障，不能保证零故障或外部通知绝对一次送达。
窗口间短直播可能被轮询遗漏；生产恢复、告警收件人和用户手机提醒均需独立验收。
