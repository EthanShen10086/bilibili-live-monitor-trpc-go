# GitHub 发布

目标公开仓库 EthanShen10086/bilibili-live-monitor-trpc-go，与其他版本分别管理。

在已登录 gh 的正常 Mac 终端执行 `./scripts/publish.sh`。脚本检查账号、干净工作区、实际上传清单和密钥格式，创建新公开仓库并推送，不 force push。若上次已创建 origin 但上传失败，只允许相同目标公开仓库续传。工具无法读取钥匙串时不发送 token，也不反复登录。

Node 不提交 node_modules、.runtime、构建缓存；Go 不提交模块/构建缓存、dist。所有版本不提交 .env、SQLite、日志和验证截图。示例配置、源码和锁文件保留；Node vendor 为构建所需的带许可证源码。

三个仓库有独立版本和 CI，不会自动同步。公共业务修复需在 Go 与 tRPC-Go 两边分别提交并验证。合集继续保留，拆分不会覆盖原仓库。
