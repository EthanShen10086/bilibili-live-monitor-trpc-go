# tRPC-Go 可扩展云端版：配置、部署与复现

本文范围是 `cmd/monitor` 的单订阅云服务，数据库选择 SQLite/PostgreSQL，缓存独立
选择 memory/Redis；原 Redis Streams 配置继续兼容。这些组件不是互斥架构。原 Redis
兼容模式。多租户 Kafka、OIDC、共享检测和回放已经由 `cmd/event-platform` 实现，
见 [事件平台部署包](../deploy/event-platform/README.md)。两种入口不要混用配置/迁移命令。
Mac Node 到云端的正式切换见 [交接手册](STANDALONE_DEPLOYMENT.md)。

## 1. 部署方式

新部署只需 `manage.sh sqlite|postgres prepare [memory|redis]`。不传缓存选项默认
memory；后续 `up/check` 自动读取配置并组合服务。Kafka 属于事件平台入口，其
PostgreSQL、Redis、Kafka 可以同时启用。下面的 light/platform 名称仅为已有部署别名。

- 本地轻量：默认 config.yaml，SQLite + 进程内队列统计缓存，role=both。不连接 PostgreSQL / Redis，不启动额外服务。Node 和独立原生 Go 的本地代码不受本扩展影响。
- 云端轻量：deploy/cloud/config.light.yaml，Linux 后台运行相同 SQLite 逻辑，Nginx 可选。
- 云端 PostgreSQL-only：deploy/cloud/config.postgres.yaml，PostgreSQL 持久化、memory 缓存、database 队列，不启动 Redis 或 Kafka。
- 云端平台：deploy/cloud/config.platform.yaml，PostgreSQL 持久化 + Redis 状态缓存 / Streams 唤醒，支持 both / detector / sender。Redis 缓存和队列均可单独关闭（cache: memory、queue: database）。

平台模式目前支持轮询；官方模式长连接的分布式会话与授权管理尚未实现，配置将明确拒绝，不暗中切回轮询。当前仍是一个配置一个订阅，可部署多份配置扩展房间和接收对象；没有新增订阅 CRUD 或多租户网页。

## 2. 配置与依赖

在现有业务配置后添加（房间、窗口、飞书模式照旧）：

```yaml
platform:
  storage: postgres           # sqlite | postgres
  cache: redis                # memory | redis
  queue: redis_streams        # database | redis_streams
  role: both                  # both | detector | sender
  subscription_id: room-1616-main
  postgres:
    dsn_env: MONITOR_POSTGRES_DSN
    auto_migrate: false
  redis:
    url_env: MONITOR_REDIS_URL
```

deployment.active 必须为 cloud。subscription_id 只允许 1–64 个字母、数字、-、_；同一订阅所有副本保持相同 ID、房间和通知对象。PostgreSQL 存储通知对象的摘要进行绑定校验，不保存凭证。不同接收对象用不同 subscription_id；相同 ID 不能改房间或 Webhook 地址。轮换签名密钥不改变绑定。多个订阅监控同房间时尚未共享一次上游查询；不要用无限增加订阅数量的方式加大 B站压力。

凭证只放权限 600 的 .env 或平台 Secret：

```text
MONITOR_POSTGRES_DSN=postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full
MONITOR_REDIS_URL=rediss://:PASSWORD@HOST:6379/0
```

替换占位符；密码含特殊字符时需要 URL 编码。宿主机 loopback 的 Compose 示例可用 PostgreSQL sslmode=disable 和 redis://；访问远程托管服务应校验证书与 TLS。应用只校验所选组件的凭证，轻量模式不要求这些变量。

Go 驱动使用 pgx、go-redis，版本在 go.mod/go.sum 锁定。框架继续真实使用 tRPC-Go；外部组件通过驱动与适配接口接入，不通过 trpc_go.yaml 的 plugins 字段。

## 3. 数据与并发规则

- PostgreSQL 中 observation 与 notification job 在同一事务产生；job 同时作为待发布 outbox，不存在“先发 Redis 再落数据库”的丢消息窗口。
- 相同订阅只有一个有效调度租约，数据库时钟控制 60 秒租约，活动循环续约。候选副本 standby，不轮询 B站。旧持有者的观察写入校验 owner 和到期时间；租约失效后拒绝写入。
- 任务原子领取，SQL 使用 FOR UPDATE SKIP LOCKED，事务统一先锁 scope 再锁 job，避免与清理事务锁顺序相反。发送时不持有数据库事务锁。完成 / 失败更新必须匹配领取 token 和未到期租约。
- 任务租约 60 秒，完整飞书发送含取 token / 刷新过程最多 40 秒。暂停进程超过租约后可能重新领取，旧 token 不能提交结果；群 Webhook 已收但响应丢失等情况仍无法严格 exactly-once。
- Redis Streams 只传 job key。消息可能重复，PostgreSQL 状态决定能否发送；确认在数据库提交成功或持久化重试状态之后执行。未确认消息可回收。
- 进程退出后任务租约到期可回收；启动时 Redis 不可达则明确失败。运行中 Redis 故障在状态 queue_error 显示，PostgreSQL 扫描仍能发送和重试；恢复后再次发布未确认 outbox。
- Streams 近似限长 10000，确认唤醒消息后删除；Redis 使用 noeviction，避免把 Streams 当缓存随机淘汰。任务丢失唤醒仍由数据库恢复，Redis 不作为发送记录依据。
- 管理接口使用 Redis 缓存 /status 的短期只读结果（2 秒），缓存失败读原状态文件；/healthz 不缓存。状态缓存 key 按服务启动实例隔离，不混淆不同副本。
- 明确选择 `cache: redis` 后，检测 worker 还缓存成功的房间观察，最多 5 秒且不超过检测间隔；拒绝过期、未来时间、错误房间及损坏数据，运行中缓存故障回源。默认 memory 模式只缓存 `/status`，检测仍直接回源，保持原轮询行为。观察 key 按订阅隔离，不能代替事件平台跨订阅共享检测；短号与规范房间 ID 不一致时不缓存观察。数据库负责去重和发送状态，不能从缓存判断是否已通知。
- 保留现有 1 分钟 / 5 分钟降频、TTL、失败退避、90 天限量清理、日志轮转。PostgreSQL version 驱动队列统计刷新；空闲时仍约 10 秒检查一次，Redis 模式额外约 5 秒脉冲，最多每批 10 条 outbox。

租约解决正常副本争抢和恢复，不能让外部飞书 API 与数据库形成跨系统事务。机器时钟应同步；发生 PostgreSQL 网络中断时实例退出，由进程管理器退避重启，禁止盲目继续发送。

## 4. Linux 单机：Docker Compose

目录 deploy/cloud，要求 Linux + Docker Engine + Compose v2。采用 host network 保持 tRPC status/admin 仅监听 127.0.0.1；这套容器网络示例不是 Docker Desktop Mac 部署方案。不要同时用 Docker 与 systemd 管理同一订阅。

轻量版：

```sh
cd deploy/cloud
./manage.sh light prepare
# 编辑 .env 飞书变量；检查 config.yaml，权限由 prepare 设为 600
./manage.sh light check
./manage.sh light up
```

PostgreSQL-only（新的安装目录，先不启用正式发送端）：

```sh
cd deploy/cloud
./manage.sh postgres prepare
# 设置飞书变量、POSTGRES_PASSWORD 和 MONITOR_POSTGRES_DSN
./manage.sh postgres check
./manage.sh postgres migrate
# 核对迁移状态、确认原发送端已停止后：
./manage.sh postgres up
```

PostgreSQL/Redis 兼容版（新的安装目录或先停旧版本再人工审阅配置）：

```sh
cd deploy/cloud
./manage.sh platform prepare
# 设置 .env 中飞书变量、POSTGRES_PASSWORD、REDIS_PASSWORD，及匹配的两个连接 URL
# 本机地址：PostgreSQL 127.0.0.1:5432；Redis 127.0.0.1:6379
./manage.sh platform check
./manage.sh platform migrate
./manage.sh platform up
```

prepare 拒绝覆盖已有 config.yaml，不自动更换用户凭证。容器镜像标签固定在示例中，正式上线应锁定镜像 digest 并维护安全更新。

PostgreSQL、Redis 使用独立持久化卷；端口仅映射宿主 127.0.0.1。平台主应用等依赖健康后启动。Redis AOF everysec 的短暂丢失由 PostgreSQL 恢复；请备份 PostgreSQL，不依赖 Redis AOF 作为通知记录。

```sh
# 以下平台版多文件参数也用于日志、exec、备份等操作
DC='docker compose --profile platform -f compose.yaml -f platform.compose.yaml'
$DC exec monitor /app/monitor --root /app platform-check
$DC exec monitor /app/monitor --root /app status --local-only
./manage.sh platform logs
# 只有明确希望发送真实消息时才运行：
$DC exec monitor /app/monitor --root /app test-notification
```

资源限制是容器保护边界，不是性能或寿命保证。平台示例从约 2 GB 内存云主机评估，额外副本需要测量；默认应用 256 MiB、PostgreSQL 512 MiB、Redis 192 MiB、Nginx 128 MiB。达到上限可能被 OOM 杀死，由重启与租约恢复接管；不能依赖反复 OOM 作为正常工作方式。

### 多发送进程

```sh
./manage.sh platform workers-up
```

额外 sender 使用独立 var/ 卷、19030 admin / 19031 status，不复用主进程目录。主进程可继续 both；也可停机后将主 config.yaml role 改 detector。复制更多 sender 时必须分配独立目录、状态卷和回环端口。相同 subscription_id 共享发送记录和领取规则；不同房间 / 接收对象创建新配置与 ID。本版本不把多个 sender 的状态聚合成全局面板。

### 更新、备份与停止

```sh
./manage.sh platform down  # 保留卷；没有 -v
# 审阅代码/config、备份数据库后
./manage.sh platform up
# 备份（存放到仓库以外，包含标题等业务数据，权限需限制）
$DC exec -T postgres pg_dump -U monitor -d monitor > /安全目录/monitor-backup.sql
```

PostgreSQL 使用版本/校验和迁移账本；生产配置关闭启动自动迁移，先运行 platform-migrate。迁移账号需建表权限，运行账号需要业务读写权限。本示例不提供自动破坏性 schema 降级。回滚应用版本前检查 schema / 字段兼容性；切勿为回滚删除卷。多云主机使用共享托管 PostgreSQL/Redis 或私网服务，并独立配置同一订阅，各机不共享本地 var/。

## 5. SQLite 去重状态迁移

平台模式明确拒绝旧 switch local|cloud 文件复制命令，防止把共享数据库当作 SQLite 搬运。Node / 原生 Go / SQLite tRPC 的原切换命令仍保留。

首次切换现有订阅时，先停止并关闭旧实例自动启动，保存 .env 与 SQLite 备份，确认目标通知对象一致。在源数据所在机器上使用带 PostgreSQL 平台配置的管理命令：

```sh
./bin/monitor platform-import-sqlite /绝对路径/已经停止的旧服务根目录
```

管理命令从源 config.yaml 验证房间，检查源停止并持有实例锁，SQLite 只读；目标 subscription_id 必须没有 observation / job，调度租约不活跃。迁移整个 observation / job（含 sent 和 pending）到 PostgreSQL 的一个事务，最多 10000 条，失败回滚，不覆盖源 SQLite，也不发送消息。必须先停止所有目标副本，导入完成后再启动平台服务。原生 PostgreSQL 导入在取得服务器权限后用真实备份另行验收。私聊成功响应丢失的未确认旧任务仍有外部副作用不确定性。

从平台回切 SQLite 尚未提供自动导出合并；需要停平台并迁移完整状态后再开旧版本，不能直接启用旧 SQLite 快照。

## 6. 可选 Nginx HTTPS

Nginx 只代理 /status 与 /healthz，HTTP GET，TLS 1.2/1.3、Basic Auth、按 IP 限速、短超时、无代理缓存和自动上游重试。其他路径 404。19028/19030 管理端口不代理、不开放安全组。

先准备域名 / 证书与账号文件（都不提交 Git）：

```sh
cd deploy/cloud
# 修改 nginx/nginx.conf 的 server_name 为实际域名
# 放证书 tls/fullchain.pem、私钥 tls/privkey.pem
chmod 600 tls/privkey.pem
# openssl 交互输入密码，输出为密码摘要
printf 'status:%s\n' "$(openssl passwd -apr1)" > auth/status.htpasswd
chmod 644 auth/status.htpasswd
./manage.sh platform nginx-check
./manage.sh platform nginx-up
```

轻量版改 manage.sh light。仅需要公网访问时开放 443，SSH 保持既有策略；如果只用 SSH 隧道，可以不启用 Nginx。证书续期由部署方处理，更新后重新校验并 reload / 重启 Nginx。账号文件是密码摘要但仍应限制获取与 Git 提交。Linux 原生 systemd 部署也可复用该 nginx.conf，把证书和账号路径换成实际绝对路径。

这不是用于 B站出站查询的正向代理，也不会提升直播检测速度。若云主机已有 Nginx 承载其他站点，需要将 server 块合并到既有配置并 nginx -t，不能直接覆盖宿主全局配置或占用同一 443。

## 7. 测试与证明边界

```sh
go test -race ./...
go vet ./...
# 临时/测试数据库与 Redis，测试只删除随机 test-* scope
MONITOR_TEST_POSTGRES='测试 PostgreSQL 连接 URL' MONITOR_TEST_REDIS='测试 Redis URL' make test-integration
# 构建后，全部使用临时目录和假飞书凭证
python3 scripts/smoke.py
NGINX_BIN=/你的/nginx python3 scripts/smoke-nginx.py
```

GitHub Actions cloud-platform.yml 使用原生 PostgreSQL + Redis、Go 竞态检查、真实框架进程、Nginx HTTPS 冒烟、Compose 校验及容器构建。另分别运行 SQLite 和 PostgreSQL-only 只读非 root 容器，后者不连接 Redis。集成用例使用 `integration` tag；缺少隔离后端配置明确失败，普通单元测试不会误连生产后端。

本次本机原生 PostgreSQL 初始化被共享内存权限拒绝，改用 PGlite 的 PostgreSQL wire / SQL 语义测试；PGlite 的连接多路复用不能作为原生 PostgreSQL 的真实多进程锁证明。Redis 为真实临时 Redis 进程。Nginx 编译成功，但 macOS 工具禁止读取网络 sysctl，故本机 -t / 启动被拒绝；真实 Linux CI 和云服务器结果分别报告，不能用文件存在代替运行验收。

## 8. 后续扩展

本单订阅入口的 Repository / Cache / TaskQueue 为独立接口，支持数据库队列与 Redis Streams；不能将 `queue` 写成 kafka。Kafka、多租户 API、鉴权、共享探测已在事件平台入口实现，两者无需同时使用 Redis Streams 和 Kafka。PostgreSQL/事件平台到 SQLite 的自动导出合并仍未提供，不能无状态回切。

### 已验证的 Linux CI（2026-10-09）

实现提交 `4066da24543ef039ea9f8db4516af91475949615` 的 [云平台集成运行](https://github.com/EthanShen10086/bilibili-live-monitor-trpc-go/actions/runs/37878794619) 已成功：原生 PostgreSQL / Redis 集成与竞态检查、Go vet、真实框架进程、真实 Nginx HTTPS / Basic Auth / GET 限制 / 不缓存状态、Compose 模型和云容器构建均通过；同提交原有 tests 工作流也成功。它补足了本机原生 PostgreSQL 和 Nginx 工具权限受限的验证；生产云主机 SSH、systemd/Docker 自启动恢复、实际飞书群/手机送达仍未执行。

探针、指标、trace、迁移账本及恢复流程详见 [上线手册](PRODUCTION_READINESS.md)。
