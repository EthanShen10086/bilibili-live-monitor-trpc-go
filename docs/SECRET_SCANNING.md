# 密钥扫描与 2026-10-10 告警审查

GitGuardian 告警定位到提交 e945bebb23c5015a6e7eebb41a65297a29ce0375 的
deploy/event-platform/render.py 第 16 行。其内容是 AUTH_HOST 与 auth.localhost
默认域名配置，不是账号密码；当前源码改为字典，保持生成结果与 DNS 校验不变。
参考 GitGuardian Authentication Tuple 检测说明：
https://docs.gitguardian.com/secrets-detection/secrets-detection-engine/detectors/generics/authentication_tuple

本次对 fetch 后本地全部 Git refs/history、当前可发布源码执行 Gitleaks 8.30.1。
默认规则额外命中 Compose JDBC 地址中的 keycloak-db:5432/keycloak，属于主机、
端口与库名，不含用户名或密码。配置只对这一文件中的完全相同非凭证 match
提供例外；不排除整个文件、历史提交、测试目录或 generic-api-key 规则。
回归测试确认同一文件加入临时合成 token 仍被阻断，失败输出也不泄露 token。

pre-commit 扫描 index 快照，pre-push 的 verify 扫描待推送提交快照；CI 还拉取完整
历史执行 secrets-history。make tools 安装固定版本。独立运行：

```sh
python3 scripts/quality.py secrets
python3 scripts/quality.py secrets-history
```

扫描默认使用原始标准规则，强制输出 100% 脱敏。可发布文件检查继续独立禁止
运行状态、.env、数据库、日志及其他凭证文件进入 Git。开发机忽略的 .env 不复制
到扫描临时目录、不上传；源码扫描不是用户电脑的全盘审计。

没有发现需要轮换的真实已泄露凭证，因此不重写公开 Git 历史、不强推、不更换
正在运行服务的凭证。历史提交仍含原域名元组，代码修复不会关闭 GitGuardian
历史事件；维护者应在该平台将该事件标记为 false positive，并记录上述证据。
本次未使用 GitGuardian API key，未声称已关闭其告警。发现真实泄露时先撤销/
轮换凭证，再根据泄露范围清理历史，不能只删除当前文件。

GitHub 原生 secret-scanning 告警列表查询为空；这与 GitGuardian 的第三方告警
不是同一个状态。扫描没有命中不等于所有业务漏洞不存在，部署和真实服务不变。
