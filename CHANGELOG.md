# 更新记录

## v1.6.1

### 修复

- 面板「设置」页与「诊断」页的国内机卡片改为 agent 流程：主推「国内机」页生成一次性注册命令，v1.5.x 的 legacy 命令（hosts 托管块 + dnsmasq + 看门狗）收进折叠的「旧版模式命令（已弃用）」，不再作为默认操作展示。
- 面板生成的注册命令在自签名/IP 面板上自动追加 `--insecure-tls`：面板通过 `/api/cn` 新增的 `tls_mode` 字段告知自身证书方式，避免 agent 首次心跳因 x509 失败而回滚。

### 文档

- `docs/API.md` 补充 agent 端点（enroll/heartbeat/binary）与管理端点（cnmachines/pin/auto-follow/remove），`/api/cn` 标注为历史接口并说明新增字段。
- `docs/TOOLBOX.md` 的国内机示例改为 `cn-setup --origin ... --gateway-ip ... --enroll-token ...`，legacy 模式标注为已弃用。
- `README.md` 补充 `--insecure-tls` 说明与 systemd-resolved 守卫（`resolved.conf.d` drop-in）。

## v1.6.0

### 新增

- 国内机常驻 agent `warden-cn-agent`（新 `cmd/warden-cn-agent`，`internal/cnagent`）：单二进制 systemd 常驻服务，接管国内机分流。内置 DNS 监听 `198.18.0.1:53`（复用 `internal/dnsproxy`，地址与 v1.5.x 的 dnsmasq 一致）：分流域名答网关 IP，其余转发上游；健康探测判定网关不可达时逐查询降级为直连解析，不再有"60 秒一次摘 hosts 块"的状态机延迟。健康探测沿用 `OK` / `LINK_443_BLOCKED` / `LINK_PORTS_BLOCKED` / `C_DOWN` / `LOCAL_BROKEN` 分类，先确认本机外网正常再判定网关故障。通过 docker socket 自动发现容器（名称、镜像、状态、启动时间），带 Coolify 标签的容器归组为应用上报，只采集元数据。
- agent 心跳：默认每 10 秒（±20% 抖动避免多台机器同拍）`POST /api/agent/heartbeat`，Bearer machine token 认证。一个心跳往返完成三件事：上报状态、健康探测结果与容器/资源摘要，领命令（`sync-domains`、`self-check`、`self-upgrade`、`deregister`），拿最新分流域名列表——网关侧带版本 hash，hash 没变不下发全量。网关连续 30 秒（3 个周期）无心跳判定离线；每个 token 按 8 秒间隔限流（心跳间隔最低 10 秒）。心跳失败时本地维持现状运行，绝不因远端异常清空本地配置。
- agent 自升级：经网关**自托管端点** `{origin}/api/agent/binary/{version}/{arch}` 下载目标版本二进制，同源拉取 `.sha256` 侧车校验，通过后临时文件写入、原子 rename 替换自身、退出交 systemd 重启；国内机全程不访问 GitHub。下载或校验失败本次不动，下个心跳重试。新版本连续崩溃时 systemd 自动回退到备份的旧二进制。systemd 单元为 `Restart=always` + `WatchdogSec=60`（agent 周期性 sd_notify，卡死强杀重启）加 `MemoryMax` 资源约束。
- 网关 `/api/agent/*`：`POST /api/agent/enroll` 用一次性 enroll token 换发 machine token（随机 32 字节，网关只存 SHA-256 摘要，agent 端 0600）；`POST /api/agent/heartbeat` 返回命令队列、域名列表与目标版本。注册表、token 摘要、待执行命令、机器快照与事件 journal 持久化到 `<DataDir>/cn-agents.json`（0600），网关重启或升级不丢已注册机器，纳入现有备份/恢复；所有 agent 管理操作进审计日志。
- 面板新增「国内机」页：每台机器的在线/离线状态、健康分类、版本、磁盘/内存/负载、容器与应用摘要、最后心跳时间；操作按钮为立即同步、自检、升级、注销——没有远程重启或整机 reboot 入口。注册流程：面板生成一次性 enroll token 与安装命令，国内机执行后自动出现在列表。
- 分流域名在线编辑：面板「设置」页新增分流域名编辑器，保存后写入 `cfg.DNS.Domains` 成为运行时唯一事实源，自动补进 SNI 透传列表保证每个分流域名都被覆盖，随服务重启生效并在心跳周期内下发到所有在线 agent，全程无需发版。
- CI：GitHub Release 在网关二进制之外同时发布 `warden-cn-agent-linux-amd64` / `-arm64` / `-arm` 二进制、`.sha256` 侧车与 tar.gz。网关安装/升级时把这些构建预取到 `$SCRIPT_DIR/agent/<version>/`（保留最近 3 个版本），由 `GET /api/agent/binary/{version}/{arch}` **自托管**对外分发，国内机安装与升级都只连网关。
- 多版本与回退：面板「升级」可选择任意已缓存版本，低于网关版本即回退；目标版本优先级为「操作员 pin > 自动跟随 > 保持现状」，未缓存版本直接拒绝。agent 替换二进制前记录待验证版本，systemd 崩溃回退守卫把它转成「已阻止版本」，恢复后的旧 agent 不再自动重试同一坏包（避免升级回滚循环），只有面板显式升级才清除阻止。

### 改进

- `bootstrap.sh cn-setup` 新增 agent 模式（`--enroll-token` 触发），按固定顺序迁移：快照 → resolv.conf 先写公网兜底 → 停止旧 dnsmasq 与看门狗 → 经网关短路径下载 agent 并启动 → enroll 并等待首次心跳成功 → `getent` 抽检分流域名解析到网关 IP → resolv.conf 定型为 agent DNS 在前 + 公网 DNS 兜底在后 → 摘除 hosts 托管块 → 清理旧脚本、dnsmasq 配置与 watchdog 状态。任何一步失败回滚快照，机器回到迁移前状态。hosts 托管块机制在 agent 模式下整体废弃。
- `cn-check` / `cn-status` / `cn-restore` 命令名与语义保留：agent 模式下内部改为读 agent 本地状态、驱动 agent；`cn-restore` 停用 agent、还原 resolv.conf、清除 `agent.json`。
- `/api/cn` 保留全部现有字段，新增 `agent_binary_url` 等 agent 相关字段；`/api/cn` 与 `/api/dns/status` 的域名列表改为 live 配置，`DefaultSplitDomains()` 与 bootstrap.sh 的 `CN_DEFAULT_DOMAINS` 只保留为全新安装的初始种子。

### 兼容性

- 升级顺序：先代理机（普通 `warden upgrade` 到 v1.6.0），后国内机，两步互相独立，中间可停留任意久。升级网关后 `/api/agent/*` 可用但注册表为空，老国内机行为完全不变。
- 已执行过 `cn-setup` 的国内机重跑一次 `cn-setup`（带面板「国内机」页生成的 enroll token）即完成迁移；每台独立迁移，无全局切换开关。迁移是唯一需要登录国内机的动作，完成后分流域名的增删全部在面板远程进行。
- 无 `--enroll-token` 的旧 `cn-setup` 命令回退 legacy 模式：按 v1.5.x 行为安装 dnsmasq + 看门狗 + hosts 托管块，打印弃用提示；已发出去的旧命令不会失败。`--cn-mode` 语义不变（`auto` / `none`），`none` 模式不安装 agent。
- agent 不提供远程重启或整机 reboot：升级逐台手动触发（面板「升级」按钮设置该机的目标版本），每台机器可单独打开「自动跟随网关版本」（默认关）。
- 注销命令在 agent 下次心跳领取时生效并移除注册；长期离线的机器收不到注销命令，会以 401 心跳噪音留在列表，此时在国内机执行 `cn-restore` 即可退出。
- 心跳请求带 protocol 版本号，网关接受当前与上一版；agent 的自升级目标版本永远来自网关，二进制与协议同步演进，不会出现升到不兼容协议的中间态。心跳被拒（token 吊销或机器注销）时本地照旧运行，不自毁配置，机器永远可以手动恢复。
- 自签名或仅 IP 访问的面板（`tls.mode=self`）在 `cn-setup` 安装时自动识别（证书校验失败但 `-k` 可达），在 `agent.json` 写入 `insecure_tls: true` 并打印告警，使心跳与自升级不再因 x509 失败；使用 ACME 证书的部署保持完整证书校验。
- resolv.conf 最终形态为 agent DNS（198.18.0.1）在前 + 公网 DNS 兜底在后：agent 进程整体死亡时解析落到公网 DNS，退化为无分流但绝不断网。

## v1.5.3

### 修复

- 国内机分流域名补上 `pkg-containers.githubusercontent.com`（GHCR 的层下载主机）。GHCR 对每个 blob 请求返回 307，跳到这台 CDN；此前只分流 `ghcr.io`，manifest 能取到，层数据却直连该主机，`docker pull ghcr.io/...` 停在 `Pulling from ...` 不动。现在 hosts 托管块与本地 DNS 一并指向网关，经 443 SNI 透传取回。
- 新增测试锁定 `DefaultSplitDomains()` 与 `bootstrap.sh` 的 `CN_DEFAULT_DOMAINS` 一致，避免两处列表再次分叉。

### 兼容性

- 已执行过 `cn-setup` 的国内机需要重跑一次 `cn-setup` 才会拿到新的分流域名；升级网关本身不会改写国内机的 hosts。

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
