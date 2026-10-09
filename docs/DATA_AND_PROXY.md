# 数据层与云端代理选型

## 当前选择

单房间、单活实例、每周通常数场通知，继续采用进程内缓存 + SQLite 持久化发送队列。Node / Go / tRPC-Go 使用相同规则。默认不启动独立 MQ 或缓存；云端可选 PostgreSQL/Redis/Streams，参见生产就绪手册。

现有优化：观测状态相同不重复更新；队列统计和下次到期时间在进程内缓存，只有脏标记或外部连接的 data_version 变化时重新扫描；飞书私聊 token 在有效期内复用；任务先入库再发送，场次主键负责去重。内存缓存可重建，数据库仍是业务依据。发送成功与外部飞书之间不是跨系统事务，成功响应丢失时群消息仍可能重复。

新增 jobs_pending_next、jobs_pending_expires 两个 SQLite 部分索引，只包含 pending 行，服务启动时通过 CREATE INDEX IF NOT EXISTS 兼容现有数据库。前者用于按 next 找待发送任务，后者用于标记到期任务；历史 sent/failed/expired 不参与这两个索引。测试混入 2000 条历史记录，验证 EXPLAIN QUERY PLAN 命中索引、到期处理和发送顺序保持正确，重复打开不丢记录。索引有少量存储和状态变更维护成本；对当前很小的数据量不宣称明显加速。

SQLite 仍使用 DELETE journal，保持既有停机迁移协议。不关闭持久化保证，不新增周期 VACUUM 或改用内存数据库。WAL 适合更高读写并发，但需兼顾 checkpoint、额外文件及切换时一致性，本规模暂不启用。

参考：[SQLite 部分索引](https://www.sqlite.org/partialindex.html)、[WAL 权衡](https://www.sqlite.org/wal.html)。

## Redis / MQ / Kafka 什么时候值得用

- 多订阅者共享热点查询、可测量的重复访问压力：评估 Redis 缓存；直播状态仍需要新鲜度约束，不能靠缓存跳过实际观测。
- 多发送工作进程、明显积压或多个下游：评估独立消息队列及确认、幂等和死信策略。
- 大量事件、长期回放及多个独立消费者：评估 Kafka。

这些是重新评估的条件，不是当前部署依赖；多节点使用现有 PostgreSQL 单活租约和任务领取；外部通知仍需要平台幂等或消费者去重，不能只换队列解决重复通知。

## Nginx

当前链路是 systemd 管理的 worker 主动出站请求 B站和飞书，没有公网 HTTP 服务。Node / 原生 Go 不需要 Nginx；tRPC-Go 状态接口监听本机回环，通过 SSH 隧道访问。Nginx 不在查询和通知链路上，没有本项目可执行的代理配置调优。

现有服务器若承载其他网站，不停用或修改它的 Nginx。以后需要 HTTPS 状态面板时再增加反向代理，并单独验证访问控制、请求限制、超时和日志轮转。直播状态、通知发送和管理命令默认不做代理缓存，发送接口不设置代理自动重试，避免过期状态和重复副作用。

参考：[Nginx 代理模块](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)。

## 验收边界

测试使用临时数据库和模拟通知，不修改真实任务或发送真实消息。Linux 二进制构建和云端代码不代表真实云主机已部署；真实 SSH / systemd / 重启恢复需资源就绪后验收。本机必须重启已有后台服务才能应用启动时索引与上一批资源保护。

## 后续实施状态（2026-10-09）

上面的“暂不增加组件”适用于轻量模式。tRPC-Go 已增加配置可选的 PostgreSQL / Redis / Streams 适配器和 Nginx 部署包；本地默认仍不启用。实现与真实验收边界参见 [CLOUD_PLATFORM.md](CLOUD_PLATFORM.md)。
