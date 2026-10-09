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

### 已验证的 Linux CI（2026-10-09）

实现提交 `4066da24543ef039ea9f8db4516af91475949615` 的 [云平台集成运行](https://github.com/EthanShen10086/bilibili-live-monitor-trpc-go/actions/runs/37878794619) 已成功：原生 PostgreSQL / Redis 集成与竞态检查、Go vet、真实框架进程、真实 Nginx HTTPS / Basic Auth / GET 限制 / 不缓存状态、Compose 模型和云容器构建均通过；同提交原有 tests 工作流也成功。它补足了本机原生 PostgreSQL 和 Nginx 工具权限受限的验证；生产云主机 SSH、systemd/Docker 自启动恢复、实际飞书群/手机送达仍未执行。

## 2026-10-09 上线前加固（feat/cloud-readiness）

本地隔离 worktree 完成依赖安全、标准流日志、接口注入、异步发送收尾、三类探针、Prometheus/OTLP、限流抖动、缓存故障降级、迁移账本、在线备份恢复和进度看门狗。当前常驻 Node 服务未重启或改动。

- 全仓 `go test -race -json -coverprofile=... ./...`：58 项通过、0 跳过；使用隔离的原生 PostgreSQL 和 Redis，覆盖租约、去重、并发领取、Streams、迁移版本校验。测试调用假通知适配器，没有发送真实消息。
- 覆盖率：monitor 56.5%、trpchost 50.0%、observability 94.4%，全仓 56.7%。CLI/main、系统服务及 host 生命周期部分由真实二进制 smoke 检验，不包含在 Go 测试覆盖率中；不宣称全业务路径覆盖完成。
- `go vet ./...`、`go mod tidy -diff`、gofmt/diff 检查通过。Go 1.26.9，官方 govulncheck 扫描 0 个可达漏洞；另有 1 个导入包漏洞未被当前调用图命中，不表示依赖中所有公告都不存在。
- 实际 tRPC 二进制 smoke：临时目录与端口、排除当天窗口，验证 `/livez`、`/readyz`、`/healthz`、`/metrics`、`/status`，CLI 探针、在线备份、独占锁、SIGTERM 正常退出与状态落盘。
- OTLP HTTP 本机模拟 Collector 验证实际导出与退出 flush；日志/指标/span 的敏感信息排除与 trace 关联测试通过。备份恢复测试验证通知去重与 pending 状态。
- Linux amd64/arm64 无 CGO 交叉编译通过；这证明构建兼容，不证明生产 Linux 运行成功。

新增 CI coverage artifact、非 root/只读容器运行与健康检查、Prometheus 配置检查。本机无 Docker，本次没有运行新容器 smoke 或 promtool；新分支尚未推送，因此不能沿用上面的历史 CI 成功作为新改造的验收。

正式服务器部署、自启动与崩溃恢复、真实告警收件人、生产备份演练、飞书群可见性/手机提醒和官方资质仍需对应资源上的独立验收。运行/组件与恢复说明见 [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md)。

## 2026-10-09 开发规范与协作门禁

已对照 CloudOP backend 的本地 Makefile、CI 和 AGENTS 约定，以及 Google Go 风格原则；完成纯格式单独提交、严格错误/资源处理修复、固定版本开发工具、单元/集成分层、Git source snapshot hooks、PR 模板、CODEOWNERS 与贡献约定。

- 固定 golangci-lint v2.14.0 全仓检查（含 integration tag）为 0 issues；gofumpt/goimports 与本地/CI 共享 quality.py。make verify、真实隔离 PostgreSQL/Redis 测试和实际进程 smoke 通过。
- 新增不合法窗口/损坏启动批准必须关闭的行为测试，以及清理日志不泄露凭证、已结束事务不产生误告警测试；质量脚本 4 项回归覆盖部分暂存、提交快照、消息格式与既有 hooks 保护。
- 项目 hooks 已安装，并保留全局 core.hooksPath。故意构造的格式错误暂存快照会被拒绝，真实 index 与工作区不变。实际 git commit 已运行格式/lint/race/manifest 门禁。
- 实际 hook 验证曾发现测试子进程继承 Git 变量，影响暂存区和本地配置；已通过 reflog 恢复功能分支、暂存区、正常身份与全局 hooks，隔离所有 repository-local Git 环境变量并加入回归断言。源码与常驻服务未受影响。
- GitHub main 原无保护；已核实 check 名称 go/platform 并通过 API 启用 PR、至少一次审批、codeowner review、过期审批失效、最后推送审批、同步最新主分支、解决讨论、禁止强推/删除。管理员保留应急权限。新 CI/CODEOWNERS 内容仍需功能分支合入 main；不把 settings 写入成功当作新分支 CI 通过。

本轮没有实际 push、部署或真实通知发送；开发门禁不替代正式云端的部署恢复和真实交付验收。
