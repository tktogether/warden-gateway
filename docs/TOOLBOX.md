# 工具箱

工具箱已独立为 [tktogether/warden-toolbox](https://github.com/tktogether/warden-toolbox)，包含常用网络工具、BBR 配置和本项目的安装、维护入口。

## 运行

```bash
curl -fsSL https://raw.githubusercontent.com/tktogether/warden-toolbox/main/toolbox.sh -o /tmp/warden-toolbox.sh
sudo bash /tmp/warden-toolbox.sh
```

不带参数进入交互菜单；也可直接使用子命令：

```bash
sudo bash /tmp/warden-toolbox.sh list
sudo bash /tmp/warden-toolbox.sh gateway install
sudo bash /tmp/warden-toolbox.sh gateway status
sudo bash /tmp/warden-toolbox.sh common net-bench 1.1.1.1
sudo bash /tmp/warden-toolbox.sh bbr status
```

不要使用 `bash <(curl ...)` 形式：进程替换依赖 `/dev/fd`，部分系统不可用。

## 转发脚本

本仓库根目录的 `toolbox.sh` 是转发脚本，保留 `sudo bash toolbox.sh <操作>` 的旧入口。执行时按 `WARDEN_TOOLBOX_LOCAL` → `WARDEN_TOOLBOX_URL` → 直连 raw → `gh-proxy.com` → `ghfast.top` → `ghproxy.net` 获取独立工具箱，透传参数和退出码。

安装后脚本位于 `/opt/warden-gateway/toolbox.sh`，面板 `/toolbox.sh` 提供同一份转发脚本。

## 模块

| 模块 | 说明 |
| --- | --- |
| `gateway` | 调用本项目 `bootstrap.sh`：install、update、status、logs、version、backup、restore、doctor、uninstall |
| `common` | system-info、net-bench、docker-mirror-speedtest、dns-check、port-check、github-speedtest、cert-check、disk-usage、docker-info |
| `bbr` | status、install、rollback、benchmark、schemes |

### gateway

工具箱按以下顺序定位本项目脚本：`WARDEN_GATEWAY_BOOTSTRAP` → `/opt/warden-gateway/bootstrap.sh` → 同仓检出 → 从 GitHub 下载。`where` 显示解析结果。

```bash
sudo bash toolbox.sh gateway where
sudo bash toolbox.sh gateway status
sudo bash toolbox.sh gateway doctor
sudo bash toolbox.sh gateway upgrade
```

`status`、`logs`、`version` 只读，不要求 root。

### BBR

```bash
sudo bash toolbox.sh bbr status
sudo bash toolbox.sh bbr install
sudo bash toolbox.sh bbr rollback
sudo bash toolbox.sh bbr benchmark --peer 203.0.113.10 --seconds 10 --schemes bbr,cubic
```

`install` 写入 `/etc/sysctl.d/99-bbr.conf` 并立即生效；`rollback` 恢复启用前的配置。`benchmark` 需要目标机运行 iperf3 服务端，测试后自动恢复原拥塞控制算法。

## 国内机设置

国内机设置不在工具箱和维护命令中执行。网关只生成命令，需在国内机上运行 `bootstrap.sh cn-setup`；执行后脚本保存为 `warden-cn`，之后用 `sudo warden-cn cn-restore` 恢复：

```bash
curl -fsSL https://panel.example.com/bootstrap.sh -o /tmp/wg-bootstrap.sh
sudo bash /tmp/wg-bootstrap.sh cn-setup --gateway-ip 192.0.2.10 --cn-mode auto --yes
```

`--cn-mode` 只有 `auto`（默认，前置 hosts 托管块 + 本地 dnsmasq + resolver 指向本机 DNS + NetworkManager 守卫）和 `none`（关闭分流，只做 APT/BBR/Docker/Git 镜像）。旧值 `hosts`、`dns`、`both` 会提示弃用并按 `auto` 处理。

`warden-cn` 只提供 `cn-setup`、`cn-check`、`cn-status`、`cn-restore`、`version`、`help`；安装、升级、卸载网关等代理机操作会以退出码 2 拒绝。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `WARDEN_TOOLBOX_LOCAL` | 本地工具箱脚本路径，优先使用 |
| `WARDEN_TOOLBOX_URL` | 工具箱下载地址 |
| `WARDEN_GATEWAY_BOOTSTRAP` | 本项目 bootstrap.sh 路径 |
| `TOOLBOX_REPO` | 仓库，默认 `tktogether/warden-toolbox` |
| `TOOLBOX_BRANCH` | 分支，默认 `main` |
