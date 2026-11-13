# 更新记录

## v1.5.2

### 新增

- 80 端口现在也承担 GitHub 短路径反代：`http://<面板域名>/<host>/<path>` 直接由 GitHub 代理处理，不再 301 到 HTTPS。适用于到 443 建连不稳定的线路（实测某条线路 443 约 5% 的 SYN 丢失，而 80 端口 150/150 全部成功）。ACME 校验路径 `/.well-known/acme-challenge/` 与其他路径行为不变，仍交给 TLS 管理器。

## v1.5.1

### 修复

- `cn-setup` 的 APT 镜像替换会把**所有** http(s) 源地址换成新主机，包括 Docker CE 这类第三方仓库。第三方仓库的路径与主机是绑定的（`https://download.docker.com/linux/ubuntu`），换主机后 `apt-get update` 直接 404。现在只改写路径以 `/ubuntu` 结尾的发行版仓库，其余源原样保留并打印提示。

## v1.5.0

### 新增

- GitHub 代理支持短路径 `/<host>/<path>`，例如 `https://panel.example.com/github.com/owner/repo`，与原有的 `/https://github.com/owner/repo` 等价。首段主机名必须命中 `github.allow_hosts`，不接受端口与 userinfo。Coolify 等只接受完整 URL 的构建工具可以直接填这种地址。
- `/healthz` 在 443 入口与 80 端口（ACME 监听）上公开返回 `{"ok":true,"version":"...","started":"..."}`，不需要登录，供国内机与看门狗探测代理机是否可用。
- 面板「设置」页的国内机设置改为两段命令：`cn-setup --cn-mode auto` 与自检 `cn-check`，并展示 `health_url`。
- bootstrap.sh 新增 `cn-check`（只读体检）与 `cn-status`（配置快照）两个子命令。

### 改进

- 国内机分流合并为 `auto` 一种：前置 hosts 托管块 + 本地 `dnsmasq`（`198.18.0.1:53`，由 `wg-dns0` dummy 网卡承载）+ `/etc/resolv.conf` 指向本地 DNS + NetworkManager `dns=none` 守卫。容器不读宿主 `/etc/hosts`，只有本地 DNS 能让 Coolify 构建容器里的 `git clone`、`docker pull` 走网关。
- hosts 托管块改为写入文件最前，避免被第三方 hosts 块抢占优先级；写入后用 `getent` 自检。
- 新增看门狗 `warden-gateway-cn-watchdog.timer`：默认 60 秒一次，先用本机对照组确认外网正常，再区分 `OK`、`LINK_443_BLOCKED`、`LINK_PORTS_BLOCKED`、`C_DOWN`、`LOCAL_BROKEN`；连续 3 次故障降级摘除 hosts 块，连续 2 次恢复后写回。
- `cn-restore` 除还原文件外，还会停用并禁用本地 DNS 与看门狗单元，清理 `watchdog.state` 与日志。
- `/usr/local/bin/warden-cn` 按调用名自我限制，只提供 `cn-setup`、`cn-check`、`cn-status`、`cn-restore`、`version`、`help`，不再执行代理机的安装与升级命令。
- `/api/cn` 返回 14 个字段，新增 `health_url` 与 `check_command`；`commands` 改为 `auto`、`none`，`cn_mode` 恒为 `auto`，不再根据网关自身 DNS 状态推断分流方式。

### 兼容性

- `--cn-mode` 的旧值 `hosts`、`dns`、`both` 仍被接受，打印弃用提示后按 `auto` 处理，已发出去的旧命令不会失败。
- 老版本网关生成的命令仍可执行；老版本部署的 warden-cn 需用新版脚本重新执行一次 `cn-setup` 才会带上本地 DNS 与看门狗。
