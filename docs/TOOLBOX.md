# 工具箱

`toolbox.sh` 是统一运维入口，同时提供交互菜单和子命令。

## 安装与维护

```bash
sudo bash toolbox.sh install
sudo bash toolbox.sh update
sudo bash toolbox.sh status
sudo bash toolbox.sh backup
sudo bash toolbox.sh restore --id latest
sudo bash toolbox.sh doctor
sudo bash toolbox.sh cn-setup
sudo bash toolbox.sh cn-restore --id latest
sudo bash toolbox.sh uninstall
```

## 常用工具

```bash
sudo bash toolbox.sh common system-info
sudo bash toolbox.sh common net-bench 1.1.1.1
sudo bash toolbox.sh common docker-mirror-speedtest
sudo bash toolbox.sh common dns-check github.com
sudo bash toolbox.sh common port-check 443
sudo bash toolbox.sh common github-speedtest
sudo bash toolbox.sh common cert-check panel.example.com
sudo bash toolbox.sh common disk-usage
sudo bash toolbox.sh common docker-info
```

## BBR

```bash
sudo bash toolbox.sh bbr status
sudo bash toolbox.sh bbr install
sudo bash toolbox.sh bbr rollback
sudo bash toolbox.sh bbr benchmark --peer 203.0.113.10 --seconds 10 --schemes bbr,cubic
```

`install` 写入 `/etc/sysctl.d/99-bbr.conf` 并立即生效；`rollback` 恢复启用前的配置。`benchmark` 需要目标机运行 iperf3 服务端。

## 远程自举

工具箱在缺少本地源码时自动下载仓库压缩包。下载顺序：自定义地址、GitHub archive、codeload、代理前缀。

```bash
curl -fL https://raw.githubusercontent.com/tktogether/warden-gateway/main/toolbox.sh -o /tmp/warden-toolbox.sh
sudo bash /tmp/warden-toolbox.sh
```

不要使用 `bash <(curl ...)` 形式：进程替换依赖 `/dev/fd`，部分系统不可用。

## 诊断与国内机设置

```bash
sudo bash toolbox.sh doctor        # 项目自身与国内机配置两段诊断
sudo bash toolbox.sh cn-setup      # 应用 hosts/DNS 分流、APT、Docker、Git 镜像和 BBR
sudo bash toolbox.sh cn-restore    # 恢复最近一次国内机设置备份
```

`cn-setup` 和 `cn-restore` 都是 `bootstrap.sh` 的子命令，工具箱直接透传参数。

可用环境变量：

| 变量 | 说明 |
| --- | --- |
| `NW_ARCHIVE_URL` | 单个源码压缩包地址 |
| `NW_ARCHIVE_URLS` | 空格分隔的多个地址 |
| `NW_GITHUB_PROXY` | GitHub 代理前缀，例如 `https://gh-proxy.com` |
| `NW_REPO` | 仓库，默认 `tktogether/warden-gateway` |
| `NW_BRANCH` | 分支，默认 `main` |
