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
