# Warden Gateway

Warden Gateway 是面向个人和小团队的自托管入口程序。它在一个进程内提供统一 HTTPS 入口、管理面板、Docker 镜像代理、GitHub 下载代理、证书签发、DNS 分流和配置备份。

## 功能

- 统一入口：443 端口按 SNI 路由。面板域名终止 TLS；GitHub、Docker 等域名原样转发到原站。
- 管理面板：`/warden/` 提供资源指标、服务状态、代理统计、证书状态和配置管理。
- Docker 镜像代理：`/v2/` 提供 registry 兼容接口，并修复上游 referrers 接口的兼容问题。
- GitHub 下载代理：`/https://github.com/...` 支持发布包、raw 文件、codeload 和 git 请求。
- 证书：使用 ACME 自动申请和续期，也支持自有证书或自签名证书。
- 备份恢复：配置、证书缓存和 systemd 单元打包为带校验和的归档；恢复后自动重启服务。
- 一键诊断：检查网关自身的配置校验、服务状态、监听端口、证书与上游连通性。
- 承载统计：按 Docker、GitHub、DNS、SNI 四个通道展示网关实际承载的请求、成功率、耗时与流量。
- 维护命令：安装时注册 warden 命令，日常执行状态、升级、诊断、日志、备份与恢复。
- 访问控制：访问密钥只保存 SHA-256 摘要；会话使用 HMAC 签名 Cookie 和 CSRF 校验。

## 架构

```
客户端
  | 443/TCP (TLS ClientHello)
  v
SNI 路由
  |-- 面板域名 ------> 127.0.0.1:8443 -> /warden/、/v2/、/https://...
  |-- 其他白名单域名 -> 原站 443/TCP（TLS 透传）
```

面板域名同时承载 Docker 镜像代理和 GitHub 代理。客户端只需要访问一个域名。

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| 80 | TCP | ACME HTTP-01 校验 |
| 443 | TCP | SNI 统一入口 |
| 8443 | TCP | 面板与路径代理，默认只监听 127.0.0.1 |
| 8080 | TCP | 本地 HTTP，默认只监听 127.0.0.1 |
| 53 | UDP/TCP | 可选 DNS 分流；被占用时自动回退到 5353、5300、9053 |

## 环境要求

- Linux，systemd。
- root 权限。
- 80 和 443 端口可用。使用域名时，域名 A/AAAA 记录必须指向本机。
- 建议至少 1 核 CPU、512 MB 内存。

没有域名时，安装器使用服务器 IP 和自签名证书。浏览器会提示证书不受信任，这是预期行为。

## 快速安装

推荐先下载再执行。下载后运行会进入交互向导，依次确认面板域名、安装方式、Docker 与 GitHub 代理、SNI 入口、DNS 分流、证书方式与监听地址：

```bash
curl -fL https://raw.githubusercontent.com/tktogether/warden-gateway/main/bootstrap.sh -o bootstrap.sh
sudo bash bootstrap.sh
```

网络无法直接访问 GitHub 时，可在下载命令中改用代理前缀：

```bash
curl -fL https://gh-proxy.com/https://raw.githubusercontent.com/tktogether/warden-gateway/main/bootstrap.sh -o bootstrap.sh
sudo NW_GITHUB_PROXY=https://gh-proxy.com bash bootstrap.sh
```

不要使用 `sudo bash <(curl ...)` 写法：该写法依赖 `/dev/fd`，部分系统不可用，会报 `No such file or directory`。

非交互安装示例：

```bash
sudo bash bootstrap.sh install --domain panel.example.com --email admin@example.com --yes
```

安装完成后会输出一次访问密钥。密钥只显示一次，请立即保存。使用 `--access-key` 指定密钥时不会回显。

### 安装方式

默认下载 GitHub Release 中的 Linux 二进制。也可指定本地二进制或 Docker 镜像：

```bash
sudo bash bootstrap.sh install --binary /root/warden-gateway --domain panel.example.com
sudo bash bootstrap.sh install --docker --domain panel.example.com
```

镜像地址为 `ghcr.io/tktogether/warden-gateway`。

### 常用安装选项

| 选项 | 说明 |
| --- | --- |
| `--domain DOMAIN` | 面板域名，自动申请证书 |
| `--public-url URL` | 完整面板地址，例如 `https://panel.example.com/warden` |
| `--email EMAIL` | ACME 账户邮箱，可选 |
| `--binary PATH` | 使用本地二进制安装 |
| `--version VERSION` | 指定发布版本，默认 `latest` |
| `--access-key KEY` | 指定访问密钥，至少 10 个字符；不指定则自动生成 |
| `--docker` | 使用 Docker 镜像运行 |
| `--no-docker` | 不启用 Docker 镜像代理 |
| `--no-github` | 不启用 GitHub 下载代理 |
| `--no-sni` | 不启用 443 SNI 统一入口 |
| `--panel-path PATH` | 面板路径，默认 `/warden` |
| `--tls auto\|self\|files` | 证书方式：ACME、自签名、已有证书 |
| `--cert-file PATH` | `--tls files` 时的证书路径 |
| `--key-file PATH` | `--tls files` 时的私钥路径 |
| `--listen-https ADDR` | 面板 HTTPS 监听，默认 `127.0.0.1:8443` |
| `--listen-http ADDR` | 本地 HTTP 监听，默认 `127.0.0.1:8080` |
| `--listen-entry ADDR` | SNI 入口监听，默认 `0.0.0.0:443` |
| `--listen-acme ADDR` | ACME HTTP-01 监听，默认 `0.0.0.0:80` |
| `--dns` | 启用 DNS 分流 |
| `--dns-domains LIST` | DNS 分流域名，逗号分隔 |
| `--dns-gateway IP` | DNS 分流返回的网关地址 |
| `--dns-upstream ADDR` | DNS 上游，默认 `1.1.1.1:53` |
| `--yes` | 自动确认 |
| `--non-interactive` | 不进行交互 |
| `--dry-run` | 只打印安装计划 |

## 维护命令

安装和升级会把维护命令注册到 `/usr/local/bin/warden`，它调用 `/opt/warden-gateway/bootstrap.sh`，行为与直接运行脚本一致：

```bash
sudo warden status            # 服务、端口与配置检查
sudo warden doctor            # 一键诊断（仅网关自身）
sudo warden logs              # 最近 100 行日志
sudo warden version           # 已安装版本
sudo warden backup            # 立即备份
sudo warden restore --id latest
sudo warden update            # 升级程序
sudo warden uninstall
```

国内机设置不在维护命令中：这些操作用在国内机上，命令由面板或诊断页生成后复制执行。

## 安装后配置客户端

### Docker

把 Docker 镜像源指向 Warden Gateway。编辑 `/etc/docker/daemon.json`：

```json
{
  "registry-mirrors": ["https://panel.example.com"]
}
```

然后重新加载 Docker：

```bash
sudo systemctl reload docker
```

验证：

```bash
docker pull alpine:3.20
docker info | grep -A2 "Registry Mirrors"
```

### Git

把 GitHub 地址改写为代理地址：

```bash
sudo git config --system url."https://panel.example.com/https://github.com/".insteadOf "https://github.com/"
sudo git config --system --add url."https://panel.example.com/https://github.com/".insteadOf "git@github.com:"
```

SSH 地址会被改写为 HTTPS。访问私有仓库需要配置 HTTPS 凭据。

### GitHub 发布包

```bash
curl -fL https://panel.example.com/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz -o file.tar.gz
```

## 管理面板

安装完成后访问 `https://panel.example.com/warden/`，输入安装时输出的访问密钥。

面板提供：

- 总览：主机负载、内存、磁盘、证书、服务与代理状态，代理卡片带成功率和请求速率。
- 指标：CPU、负载 1/5/15、内存与交换分区、网络吞吐（含分网卡明细）、磁盘 IO、TCP 连接状态分布、连接跟踪、DNS 分流命中率，以及全部代理通道的请求量与流量曲线。
- 代理：每个通道的请求总数、成功率、进行中请求、近一分钟请求与流量、状态码分布、延迟平均/P50/P90/P99/最大与直方图、热点目标、上游响应耗时、近六十分钟曲线和连通性测试。
- 承载：网关最近实际承载的流量。按 Docker、GitHub、DNS、SNI 四个通道给出请求量、成功率、平均与 P90 耗时、上游等待、网关转发耗时、入出流量与热点目标，并附请求与流量趋势。
- 服务：Warden Gateway 的启动、停止和重启。
- 备份：创建、下载、恢复和删除备份。恢复后自动重启服务。
- 审计：登录、配置修改和操作记录。
- 设置：修改访问密钥、查看证书和配置。

访问密钥至少 10 个字符。修改后所有旧会话立即失效。

## 备份与恢复

备份包含 `/etc/warden-gateway/config.json`、ACME 证书缓存和 systemd 单元。备份文件带 SHA-256 校验和，默认保留 10 份。

命令行：

```bash
sudo bash bootstrap.sh backup
sudo bash bootstrap.sh restore --id latest
```

恢复会先自动创建当前状态的预备份，然后覆盖配置和证书缓存，最后重启服务。

## 命令行

```text
warden-gateway serve --config /etc/warden-gateway/config.json
warden-gateway init --public-url https://panel.example.com/warden --domain panel.example.com
warden-gateway check --config /etc/warden-gateway/config.json
warden-gateway key --config /etc/warden-gateway/config.json
warden-gateway backup --config /etc/warden-gateway/config.json
warden-gateway backups --config /etc/warden-gateway/config.json
warden-gateway restore --config /etc/warden-gateway/config.json --id latest
warden-gateway config show --config /etc/warden-gateway/config.json
warden-gateway version
```

`init` 生成访问密钥和会话密钥。`key` 生成新的访问密钥并写入配置。

## 国内机设置

国内服务器访问 GitHub 和 Docker Hub 不稳定时，把分流与镜像设置应用到这些机器上。网关本身不执行国内机设置，只在面板「诊断」「设置」页按所选分流方式生成命令；脚本通过面板地址下载，不依赖 GitHub。

```bash
curl -fsSL https://panel.example.com/bootstrap.sh -o /tmp/wg-bootstrap.sh
sudo bash /tmp/wg-bootstrap.sh cn-setup --gateway-ip 192.0.2.10 \
  --docker-mirror https://panel.example.com --git-mirror https://panel.example.com --yes
```

设置内容：

| 项目 | 说明 |
| --- | --- |
| hosts 分流 | 把 GitHub、Docker 相关域名指向网关，使用 443 入口转发 |
| DNS 分流 | 把 `/etc/resolv.conf` 或 systemd-resolved 指向网关；需要目标机器能访问网关 53 端口 |
| APT 镜像 | 把 `/etc/apt/sources.list` 和 `sources.list.d` 中的源地址替换为指定镜像 |
| BBR | 写入 `/etc/sysctl.d/99-warden-cn-bbr.conf`，使用 `fq` 与 `bbr` |
| Docker 镜像 | 写入 `/etc/docker/daemon.json` 的 `registry-mirrors`，并 reload Docker |
| Git 镜像 | 写入 `/etc/gitconfig`，改写 `https`、`http`、`git`、`git@github.com:` 和 `ssh://git@github.com/` 地址 |

分流方式由 `--cn-mode` 选择：`hosts`（默认，只改 hosts）、`dns`（只改 resolver）、`both`（两者互为兜底）、`none`（都不改）。参与分流的域名用 `--cn-domains` 指定。面板「设置」页选择分流方式后给出对应命令，「诊断」页同时列出 hosts、DNS、both 三种命令。

每次修改前自动备份到 `/var/backups/warden-gateway-cn`，最多保留 10 个。恢复：

```bash
curl -fsSL https://panel.example.com/bootstrap.sh -o /tmp/wg-bootstrap.sh
sudo bash /tmp/wg-bootstrap.sh cn-restore --id latest
```

不带 `--id` 时使用最新备份。

## 一键诊断

```bash
sudo warden doctor
```

检查项：配置校验、服务状态、面板监听、ACME 监听、SNI 入口、Docker 代理、GitHub 代理、DNS 分流（含回退状态）、数据目录。面板「诊断」页展示同一份结果，并附国内机设置命令。

## 可选 DNS 分流

DNS 模式让客户端把 DNS 指向网关，由网关把指定域名解析到本机。默认关闭，可在面板「设置」页启用，也可在配置中开启：

```json
"dns": {
  "enabled": true,
  "listen": "0.0.0.0:53",
  "gateway_ip": "192.0.2.10",
  "domains": ["docker.io", "registry-1.docker.io", "github.com"],
  "upstream": "1.1.1.1:53"
}
```

启用时自动选择监听地址：优先 `0.0.0.0:53`；53 被占用（例如 systemd-resolved）时依次尝试 5353、5300、9053，并把实际监听地址写回配置。面板显示实际监听与回退提示。国内机 resolver 无法指定端口，回退后 DNS 分流不可用，建议关闭 systemd-resolved 的 53 监听后重新启用。DNS 监听失败只记录日志并显示在面板，不会影响面板、443 入口和其他代理。

## 工具箱

仓库根目录的 `toolbox.sh` 集成安装、升级、维护命令、常用网络工具和 BBR 配置：

```bash
sudo bash toolbox.sh
```

远程运行：

```bash
curl -fL https://raw.githubusercontent.com/tktogether/warden-gateway/main/toolbox.sh -o /tmp/warden-toolbox.sh
sudo bash /tmp/warden-toolbox.sh
```

工具箱菜单包含安装、升级、状态、日志、版本、备份、恢复、一键诊断、卸载和远程命令。国内机设置不在工具箱中：按面板生成的命令到国内机执行 `bootstrap.sh cn-setup`。

安装完成后，`bootstrap.sh` 与 `toolbox.sh` 会复制到 `/opt/warden-gateway`，同时复制工具箱源码目录，因此本机可直接运行 `sudo bash /opt/warden-gateway/toolbox.sh`，不需要联网下载。

工具箱也可以直接调用网关命令：

```bash
sudo bash toolbox.sh gateway status
sudo bash toolbox.sh gateway logs
sudo bash toolbox.sh gateway doctor
```

## 安全说明

- 服务以 root 运行，因为需要绑定 80 和 443。
- 面板默认只监听 127.0.0.1，外部访问必须经过 443 入口。
- 访问密钥只保存 SHA-256 摘要。首次安装会在数据目录写入仅 root 可读的初始密钥副本。
- 所有写操作需要 CSRF 令牌，并校验 Origin。
- 登录失败有限流。
- GitHub 代理只允许配置中的域名，拒绝请求体超过限制的请求。
- Docker 代理和 GitHub 代理默认对所有来源开放。可通过防火墙限制来源。

## 开发

```bash
go build ./...
go test ./...
go vet ./...
bash -n bootstrap.sh
bash toolbox.sh self-test
```

## 目录

```text
cmd/warden-gateway/      命令行入口
internal/config/         配置加载与校验
internal/auth/           访问密钥、会话与限流
internal/gateway/        SNI 路由、TLS、Docker 代理
internal/githubproxy/    GitHub 代理
internal/dnsproxy/       可选 DNS 分流
internal/panel/          管理面板与指标
internal/backup/         备份与恢复
internal/app/            组件装配
deploy/                  systemd 与辅助脚本
examples/                配置示例
toolbox/                 运维工具箱
```

## 常见问题

### 面板打不开

1. 确认域名解析到本机：`getent ahostsv4 panel.example.com`。
2. 确认 80 和 443 未被其他程序占用：`ss -lntp | grep -E ':(80|443)'`。
3. 查看服务日志：`journalctl -u warden-gateway -n 100 --no-pager`。

### 证书申请失败

ACME HTTP-01 需要 80 端口可从公网访问，且域名指向本机。Cloudflare 代理会改变解析结果，申请证书时应关闭代理。

### Docker 拉取仍很慢

确认 Docker 已加载镜像源：`docker info | grep -A2 "Registry Mirrors"`。确认客户端到网关的 443 链路正常。

### 忘记访问密钥

以 root 读取 `/var/lib/warden-gateway/initial-access-key`。该文件只在首次安装时写入，修改密钥后不会更新。

## 许可

MIT
