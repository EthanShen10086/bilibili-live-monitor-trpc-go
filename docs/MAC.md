# Mac 配置、常驻和迁移

## 构建与真实消息

在本仓库根目录操作，先安装 Go 1.26.3 或兼容版本：

```sh
./bin/setup
cp .env.example .env
chmod 600 .env
# 编辑 .env 中的 FEISHU_WEBHOOK、FEISHU_WEBHOOK_SECRET
./bin/monitor check-config --probe
./bin/monitor test-notification
```

飞书群设置添加自定义机器人，开启签名校验，Webhook 和签名密钥只保存在本地 .env。test-notification 真实发送；需要分别确认群内可见和手机通知。

## 常驻与开机确认

先停止此前的 Node/Go/tRPC-Go 实例。三个仓库使用相同服务名，install 不代表可以同时运行多个版本。本仓库未接管现有 Node 后台服务。

```sh
./bin/monitor confirm-start
./bin/monitor service install --side local
./bin/monitor service start --side local
./bin/monitor service health --side local
./bin/monitor status
```

launchd 登录后启动；confirm_each_boot=true 时每次电脑重启后的首次启动先确认，批准记录只对应本次内核启动身份。同次启动再次登录或崩溃恢复复用批准。暂不启用保持 waiting_confirmation，不开始检测，可 confirm-start 后启用。读取启动身份失败时拒绝猜测。保持 Mac 在检测时段唤醒及联网。

```sh
./bin/monitor service doctor --side local
# 明确允许短暂停顿后执行，主动终止受管理进程并验证新 PID：
./bin/monitor service verify-recovery --side local
```

实际重启电脑，登录、确认、查看状态，是跨重启验收；单元测试不替代这一步。工具无法发信号时会报错，不能宣称已恢复。

## 从其他仓库迁移

先在源仓库停止并导出，在目标仓库导入（安全路径不要提交 Git）：

```sh
# 源仓库根目录
./bin/monitor service stop --side local
./bin/monitor state-export > /安全路径/state.base64
# 切换到本仓库，分别配置自己的 .env 后
./bin/monitor state-import < /安全路径/state.base64
./bin/monitor confirm-start
./bin/monitor service install --side local
./bin/monitor service start --side local
./bin/monitor service health --side local
```

各仓库有独立 var/，不会共享去重状态。SQLite 表结构和队列 JSON 兼容，但必须先停止后导出/导入；只复制代码会失去已发送去重记录。三种版本之间切换用此流程；switch local/cloud 用于同一实现的两端迁移。失败时先确认目标停止，再恢复源入口和源最新状态。

## tRPC-Go 状态接口

本实现实际使用 tRPC-Go 框架。trpc_go.yaml 默认只绑定 127.0.0.1：状态 19029，管理 19028。run 同时启动业务 worker 和框架。

```sh
curl -f http://127.0.0.1:19029/healthz
curl http://127.0.0.1:19029/status
```

接口只读；healthy/outside_window 且心跳新鲜返回 200，其他情况 healthz 为 503。停止时协调 worker、框架和实例锁退出。占用端口时启动失败，请改两个不同的回环端口。
