# B站开播提醒：tRPC-Go 独立仓库

本仓库独立管理 tRPC-Go 实现，有自己的 .git、go.mod/go.sum、测试、CI、部署脚本和运行状态。无需 clone Node 或另一个 Go 仓库。独立仓库作为部署入口，合集保留同步的实现副本。

默认房间 1616，北京时间周三、周五、周六、周日 18:00–24:00 每约 1 分钟轮询，飞书签名群提醒，每场去重，不设每周通知次数上限。支持配置切换官方授权事件、飞书应用私聊、本机和 Linux 云端。

```sh
./bin/setup
cp .env.example .env
chmod 600 .env
# 填入 Webhook 和签名密钥后：
./bin/monitor check-config --probe
./bin/monitor test-notification
```

Go 1.26.3 或兼容更高版本。编译后只需 dist/monitor、config.yaml、.env、bin/monitor；tRPC-Go 另需 trpc_go.yaml。凭证各机器独立配置。缓存、二进制、数据库与日志不提交；go.mod/go.sum 提交。首次下载慢可临时设置 GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct，保留 checksum 校验。

- [Mac 运行和迁移](docs/MAC.md)
- [Linux 云端部署](docs/CLOUD.md)
- [原理与依赖](docs/ARCHITECTURE.md)
- [测试与验收](docs/VALIDATION.md)
- [GitHub 发布](docs/PUBLISH.md)

三个实现的管理服务名相同；选择一个作为后台实例，切换版本前停止旧服务并迁移 SQLite 状态。各仓库的 var/ 不自动共享，也不自动同步代码。原生 Go 入口只运行后台检测；tRPC-Go 入口实际启动框架并提供仅监听回环地址的状态和健康接口。

轮询间隔设置：[分钟级配置和重启步骤](docs/POLLING_INTERVAL.md)。

后台资源优化：[实现说明、Mac 与云端更新步骤](docs/RESOURCE_OPTIMIZATION.md)。保持每 1 分钟轮询。

开播通知成功后自动改为每 5 分钟确认直播状态，观测到下播恢复每 1 分钟；可用 `notified_live_interval_minutes` 调整。详见资源优化手册。

资源保护与复现：[响应上限、日志轮转、历史保留和等待确认节流](docs/RESOURCE_SAFETY.md)。

可选云端平台扩展已实现：PostgreSQL、Redis 缓存、Redis Streams 唤醒、调度与任务租约，以及 Linux Docker Compose / 可选 Nginx HTTPS。默认仍为本机 SQLite，不依赖外部服务。详见 [平台部署与迁移手册](docs/CLOUD_PLATFORM.md)。
