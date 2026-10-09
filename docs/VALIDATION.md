# 拆分后的验证记录

本仓库是独立 tRPC-Go 模块，go.mod 指向自己的 GitHub 仓库，没有本地 replace 或对其他版本的导入。原生版已去掉框架依赖；tRPC-Go 版包含自己的框架与业务源码。后续分别维护。

2026-10-04 拆分后执行 go mod tidy、go test -race ./...（22 项通过）、go vet ./...、本机二进制构建与临时目录进程冒烟，均通过。Linux amd64 和 arm64 也完成无 CGO 交叉编译；未进行真实 Linux 运行验收。

```sh
./bin/setup
go test -race ./...
go vet ./...
python3 scripts/smoke.py
```

smoke 用假凭证和临时目录，排除当天检测窗口，验证启动、实例排他锁和 SIGTERM 退出；tRPC-Go 另外验证实际框架 healthz/status。不会注册系统服务或发真实消息。

拆分前共用逻辑已完成真实 B站查询和两个入口各一次飞书 API 成功发送；不把这些历史结果当作此目录已部署。当前 Node 服务仍运行原版本。真实 Go/tRPC-Go LaunchAgent、SIGKILL 恢复、主机重启、私聊、官方授权与云端部署仍待资源验收。

上传排除模块/构建缓存、二进制、.env、SQLite、日志，保留 go.mod/go.sum 和示例配置。

## 2026-10-08 分钟级轮询更新

默认 interval_minutes: 1，旧秒配置仍支持且单位互斥。新增分钟转换、非法配置与长间隔退避测试；新增真实 worker 调度测试。当前 24 项测试通过（go test -race），go vet 通过。 不把单元测试当作真实开播、手机提醒或云端重启的验收。详见 POLLING_INTERVAL.md。

## 2026-10-08 后台资源优化

Node 各副本 41 项通过、1 项真实平台测试跳过；原生 Go 26 项、tRPC-Go 与合集 Go 各 28 项竞态测试通过，vet 通过。覆盖 10 秒状态保活、空闲队列缓存、外部重试检测、TTL 唤醒、相同观测不写入、空闲退出。轮询继续为 1 分钟。重启 Mac 常驻实例和推送 GitHub 的结果需单独核验；自动测试不是实际部署或手机送达证明。

## 2026-10-08 当前场次通知成功后降频

Node 各副本 42 项通过、1 项真实平台测试跳过；原生 Go 28 项、tRPC-Go 与合集 Go 各 30 项竞态测试通过。新增当前场次的发送状态判断，扩展真实 worker 测试以核验发送失败保持正常间隔、发送成功后低频确认、进程重启恢复低频、下播恢复正常间隔和新场次去重记录。Go 全量本地 WebSocket 测试首次因网络沙箱拒绝监听失败，取得会话网络权限后完整重跑通过。没有发送新的真实飞书消息。Mac 服务重启、真实云服务器和手机提醒须单独核验。

## 2026-10-08 资源保护补强

Node 各副本 48 项通过、1 项真实平台测试跳过；Go 34 项、tRPC-Go / 合集 Go 各 36 项竞态测试通过，vet 通过。新增等待确认低频保活与停止、响应体大小/取消/关闭、超大日志和备份上限、缓存标准输出捕获、历史批次/日频/保留当前及 pending 的测试。Go managed 进程冒烟与真实 tRPC HTTP 检查通过；Node 真实 managed CLI 临时目录冒烟通过。没有发送新真实通知、没有执行实际管理进程崩溃恢复或跨 Mac 重启。常驻实例加载新代码须另行核验。

## 2026-10-09 数据层增量验证

新增 pending 部分索引（next、expires）。Node 主部署全量 49 项通过、1 项真实平台测试跳过；独立 Node 与合集分别构建并通过 3 项数据层测试。三个 Go 模块完整 go test -race ./... 通过。测试用 2000 条历史通知验证查询计划命中索引、过期处理、下一任务选择及重复打开保留数据。数据/代理选型说明见 docs/DATA_AND_PROXY.md（从 docs/ 目录阅读时为 DATA_AND_PROXY.md）。未发送真实通知，Mac launchctl 重启仍被系统拒绝，真实云端未部署。

## 2026-10-09 可选云端平台

平台增量的本机完整竞态测试 45 项通过（5 项数据库/Redis 集成用例使用 PGlite SQL/wire 与真实临时 Redis）；Go vet、Mac 构建通过，原轻量 tRPC 进程冒烟通过。Compose v2.39.4 实际校验 light / platform + workers + nginx 模型通过。原生 PostgreSQL 初始化被当前工具共享内存权限拒绝；Nginx 编译通过，实际 -t 被网络 sysctl 权限拒绝。已提供 GitHub Actions 原生 PostgreSQL/Redis、Nginx HTTPS、容器构建验收；CI 与真实云端结果需独立核对。未接触真实飞书凭证，未发送真实消息，未启动用户 Mac 的新服务。详见 CLOUD_PLATFORM.md。
