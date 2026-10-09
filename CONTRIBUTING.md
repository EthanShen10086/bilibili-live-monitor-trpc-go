# 协作与开发规范

本项目以 [Google Go 风格原则](https://google.github.io/styleguide/go/guide)、[风格决策](https://google.github.io/styleguide/go/decisions) 和 Go 官方惯例为基线。明确、简单、可维护优先。仓库的格式工具、lint 配置和检查入口是实际执行规则；规则变更与业务改动分开提交。

## 首次开发

需要 Go（版本以 go.mod 为准）、Python 3、Git、Make；本地 hooks 面向 macOS/Linux。运行：

```sh
make tools       # 工具固定版本，装在 .cache/tools；不修改项目依赖
make hooks       # 安装项目 dispatcher，保留已有全局 hooks
make verify
```

工具版本统一存放 scripts/tool-versions.json，禁止安装 @latest 作为团队门禁。更新版本必须单独 PR，检查 Go 兼容性并全量回归。检查不会联网安装缺失工具，也不会偷偷格式化、暂存或生成业务配置。

推荐 VS Code/Cursor 使用官方 `golang.go`（gopls、测试、Delve）与 EditorConfig；仓库已提供 recommendations 和 workspace settings。GoLand 可直接使用 go.mod 和 EditorConfig，在 External Tools 中配置 `make fmt` / `make lint`。IDE 保存格式化是辅助，固定版本的 `make fmt-check` 才是验收规则。不要求额外安装自动生成代码、自动提交或自动改依赖的插件。

## 代码风格与设计

- 使用 gofumpt 与 goimports：标准库、第三方、本模块 import 统一分组；Go 文件 tab 缩进、LF、文件末尾换行。避免手动对齐和强制断开每条长表达式；长 SQL/配置用命名常量或模板提升可读性。
- 短局部变量可用 `err`、`ctx`、`tx`；跨度较长的业务状态使用能表达目的的名称。缩写保持 ID/HTTP/URL，避免无意义的 Helper/Manager/Utils 类型名。
- context 是第一个参数；取消、超时和 goroutine 退出责任必须明确。通过消费者侧接口注入外部调用；不要引入只有一层转发、没有隔离作用的 interface 或 facade。
- monitor 负责业务协调和适配，trpchost 负责框架/HTTP/监督，observability 负责指标/追踪，resource 只记录 best-effort 清理失败。不要在业务层依赖 tRPC 的全局 server 或用 transport 错误污染业务端口。
- 外部输入错误向上传递，使用 errors.Is/As 判断包装错误；避免重复包装或同时在每层打印同一错误。业务永久/临时错误显式分类。
- 写入、事务提交、RowsAffected、状态写入和配置解析失败必须处理。关闭读资源/已完成事务回滚等清理不能覆盖原错误，可通过 resource 包记录安全错误类型；写文件的 Close 错误必须返回。不得把业务失败交给 best-effort 日志吞掉。
- jsonBody 只接受内部 JSON-compatible 请求，失败属于程序错误并 panic；外部响应/用户配置使用正常错误返回。HTTP 内容、URL、token、DSN 不进入日志/trace。
- 每个 package 提供用途和边界说明；公共端口注明调用/生命周期约束。注释解释决定和约束，避免逐行复述代码。新功能按职责命名文件，不机械要求每个函数一个文件或统一大 types.go。
- 不修改已应用 migration；追加版本、保持校验和。去重键、租约、TTL、停机 drain 与 outbox 是必须保留的契约。
- nolint 必须指定规则并解释原因；不允许整包/整目录屏蔽安全或正确性检查。当前唯一测试例外是 errcheck 的历史 setup/cleanup；行为结果必须有断言，其他分析器仍检查测试。

## 检查与 Git 门禁

| 入口 | 内容 |
| --- | --- |
| make fmt | 显式格式化、整理 import；改动须自行审阅暂存 |
| make lint | 验证配置；govet、staticcheck、unused、ineffassign、errcheck、errorlint、bodyclose、durationcheck、nolintlint |
| make test | 无外部服务的完整单元/组件与质量脚本测试，race 与两分钟超时 |
| make test-integration | integration tag，要求隔离 PostgreSQL/Redis，缺配置直接失败 |
| make verify | 格式、lint、race 测试、mod tidy 差异、govulncheck、发布文件检查 |
| make quality-test | 验证 hooks 快照、部分暂存、消息格式与既有 hook 保护 |
| make smoke | 编译后执行实际二进制、探针、备份与退出检查 |

pre-commit 检查 Git index 快照；未暂存修改不参与验收、不被修改。commit-msg 要求 `type(scope): description`，标题不超过 100 字符；类型支持 feat/fix/refactor/test/docs/style/perf/build/ci/revert/chore，scope 可省略。保留 Git 自动 Merge/Revert 标题。一个提交只完成一个可说明的目的，纯格式/重命名与逻辑调整分开。

pre-push 读取 Git 提供的每个待推送 commit，导出提交快照执行 verify；不会误检工作区的未提交内容，不会发起 push。删除远端 ref 不运行源码检查。当前工作区缓存复用固定版本工具，临时快照结束后删除。

安装器不修改 core.hooksPath，不覆盖公司全局 hooks，也不覆盖已有 repo hooks。默认 Git hooks 或明确委托 .git/hooks 的 dispatcher 可自动接入；未知/优先级覆盖的 hook 链会拒绝安装，必须人工审阅链路。共享 Git metadata 的旧 worktree 没有质量脚本时保留原行为，新分支合入后重新执行 make hooks。hooks 可被 --no-verify 绕过，因此 CI 与仓库保护是最终门禁，不能只依赖开发者本机。

## 测试约定

单元/组件测试与源码同包 *_test.go：表驱动覆盖边界；断言对外行为/持久化结果；优先标准 testing、httptest、临时 SQLite 和注入 fake。不调用真实 B站/飞书、不读取用户 .env、不写用户 var。t.Setenv 或全局日志修改的测试不使用 t.Parallel。异步测试用 channel 协调与有界等待，禁止靠长 sleep 凑结果。

所有真实后端测试使用 `//go:build integration`，由专用 CI job 提供一次性 PostgreSQL/Redis；配置缺失必须失败。不可指向生产 DSN。测试按 subscription_id 隔离，清理自身 scope/stream；不对共享库执行破坏性清理。

行为修复先添加能复现问题的测试；重点覆盖幂等、状态转移、取消、重试、租约丢失、异常恢复和数据写入失败。纯格式/文档不新增镜像实现的测试。覆盖率报告用于定位空白，不以统一 80% 代替正确性；provider mock、后端集成、进程 smoke、真实消息送达和部署证明分别报告。

## PR、所有权与上线

feature 分支开发，小 PR、一项职责；及时同步主分支，语义解决冲突，保留他人的未提交工作。PR 使用模板描述触发问题、行为变化、检查结果、兼容性、回滚与尚未验证的环境。改变端口/配置/状态/错误契约时先列出已有调用者与迁移策略，再实现并补文档。

CODEOWNERS 指定维护者；新增协作者时按领域调整 owner。GitHub main 保护规则：main 禁止直接/强制 push 和删除，必须 PR、至少一次批准、CODEOWNER review、过期 review 失效、讨论解决，要求 `go`、`platform` 两个检查成功（已核对实际 GitHub check 名称）。2026-10-09 已通过 GitHub API 启用以上 main 分支保护，配置保存在 .github/branch-protection.json；保留管理员应急绕过，日常仍应遵循 PR。CODEOWNERS 与新 lint 门禁代码目前在功能分支，需要合入 main 才用于之后的审查/CI；远端保护已经生效。

参考 CloudOP backend 的 service/logic/repo 隔离和明确错误处理；本项目进一步统一工具版本、检查入口、源快照、隔离测试与 CI 证据。CloudOP 的内网插件、patch mock、自动 fmt/tidy build 和全局 types.go 约定不适用于本仓库。不能用工具数量证明架构优雅；审查重点是契约、职责、可读性与恢复路径。
