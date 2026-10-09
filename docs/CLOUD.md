# Linux 云服务器部署、恢复与切换

## 准备资源

支持 systemd 的 Linux，建议 1 vCPU / 1 GB 起步；专用普通账号 live-monitor、SSH 密钥、出站能访问 B站及飞书。业务不需要公网 HTTP 端口。管理员首次准备：

```sh
sudo useradd -m -s /bin/bash live-monitor
sudo mkdir -p /opt/live-monitor
sudo chown live-monitor:live-monitor /opt/live-monitor
sudo loginctl enable-linger live-monitor
```

配置该账号 authorized_keys。Mac ~/.ssh/config 增加 Host live-monitor、HostName 服务器地址、User live-monitor、IdentityFile 密钥路径。先验证 ssh live-monitor true。服务不用 root 运行，linger 保证退出 SSH 后及重启后用户服务仍运行。

## 构建、传输和文件布局

Mac 在本仓库根目录交叉编译，避免小服务器编译消耗内存：

```sh
./bin/setup
mkdir -p dist/linux-amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/linux-amd64/monitor ./cmd/monitor
ssh live-monitor 'mkdir -p /opt/live-monitor/dist /opt/live-monitor/bin /opt/live-monitor/var'
scp dist/linux-amd64/monitor live-monitor:/opt/live-monitor/dist/monitor
scp bin/monitor live-monitor:/opt/live-monitor/bin/monitor
scp config.yaml .env.example live-monitor:/opt/live-monitor/
scp trpc_go.yaml live-monitor:/opt/live-monitor/
```

ARM 服务器改 GOARCH=arm64。也可服务器 clone 本仓库并 setup，install_dir 指向实际 clone 根目录。二进制无 CGO，不需要服务器安装 Node、Go 或 SQLite 库（使用 Mac 编译产物时）。

## 首次配置，先不启用

以 live-monitor 账号登录；在 /opt/live-monitor 独立创建权限 600 的 .env。确认 install_dir 和 Mac 配置一致，chmod 755 bin/monitor dist/monitor，chmod 700 var。

```sh
cd /opt/live-monitor
cp .env.example .env
chmod 600 .env
# 编辑真实凭证
./bin/monitor set-active cloud
./bin/monitor check-config --probe
./bin/monitor test-notification
./bin/monitor service install --side cloud
./bin/monitor service doctor --side cloud
```

此时不启动云端，保持 Mac 唯一运行。set-active 仅用于停止后的首次准备；后续 switch 管理两端 active。只复制 YAML 和 SQLite，凭证各端独立准备。

## Mac 管理切换

Mac 配置 deployment.cloud.ssh_host=live-monitor、install_dir=/opt/live-monitor，本地已安装本版本 launchd 并批准本次开机。在 Mac 根目录：

```sh
./bin/monitor switch cloud
./bin/monitor status
# 切回本机
./bin/monitor confirm-start
./bin/monitor switch local
```

预检、停止并禁用两端、证明源退出、同步配置和状态、目标启动验证健康。源停止不确定时拒绝另一端启动。目标失败先证明目标停止，再带回最新队列并恢复源；不能证明停止则拒绝回滚。两个仓库间版本迁移参考 Mac 手册，不以双机锁自动接管。

## 自动启动和恢复验收

systemd --user unit 为 live-monitor.service：Restart=always、RestartSec=20、TimeoutStopSec=45、UMask0077，enable + linger 实现云服务器重启后无人值守启动。云端不用 Mac 图形确认。

```sh
./bin/monitor service health --side cloud
# 会终止受管理进程并检查新 PID，仅在接受短暂停顿时执行
./bin/monitor service verify-recovery --side cloud
systemctl --user status live-monitor.service
journalctl --user -u live-monitor.service -n 100
```

管理员 reboot 后检查状态、群消息及唯一实例。当前只有交叉编译和模拟切换证明，没有服务器资源则未完成真实 systemd、SSH 切换及重启验收。升级先 stop、备份状态、替换二进制、start/health，保留 .env 与数据库。

tRPC-Go 额外带 trpc_go.yaml，19028/19029 只监听回环，不开放安全组；需要时用 SSH 端口转发。

扩展云端部署（PostgreSQL / Redis / 可选 Nginx）见 [CLOUD_PLATFORM.md](CLOUD_PLATFORM.md)。
