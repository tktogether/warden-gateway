# API

面板 API 位于 `<public_url>/api/`。除 `/api/health`、`/api/session`、`/api/login` 外，所有接口需要登录 Cookie。写操作需要请求头 `X-CSRF-Token`。

## 认证

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 健康检查，无需登录 |
| GET | `/api/session` | 当前会话状态 |
| POST | `/api/login` | 登录，body `{"key":"..."}` |
| POST | `/api/logout` | 退出登录 |
| POST | `/api/key/change` | 修改访问密钥，body `{"current_key":"...","new_key":"..."}` |

## 状态

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/overview` | 主机、证书、服务、代理、审计摘要，含 `refresh_seconds` |
| GET | `/api/metrics?range=1h\|6h\|24h` | 主机指标序列与代理流量序列，见下 |
| GET | `/api/proxies` | 代理统计，含速率、延迟分布、状态码、热点目标与近六十分钟序列 |
| POST | `/api/proxies/test` | 测试代理，body `{"name":"docker\|github\|panel\|sni\|dns"}` |
| GET | `/api/effect?minutes=60` | 最近承载统计：总量、上游等待、DNS 命中与四通道明细 |
| GET | `/api/services` | 服务列表 |
| POST | `/api/services/{id}/action` | 服务操作，body `{"action":"start\|stop\|restart"}` |
| GET | `/api/cert` | 证书状态 |
| POST | `/api/cert/renew` | 触发证书续期检查 |
| GET | `/api/config` | 配置和设置 |
| GET | `/api/audit?limit=100&q=` | 审计记录 |
| GET | `/api/diagnostics` | 一键诊断（仅网关自身，含 sing-box 段） |

### /api/metrics

返回字段：

| 字段 | 说明 |
| --- | --- |
| `range` | `1h`、`6h` 或 `24h` |
| `points` | 主机采样序列，按范围降采样，最多 360 点 |
| `sample_seconds`、`refresh_seconds` | 主机采样间隔，两者均为 5 秒，供面板定时刷新 |
| `latest` | 最近一次采样 |
| `host` | 主机名、系统、内核、架构、核数、运行时间、磁盘用量 |
| `ifaces` | 各网卡当前收发速率 |
| `counters` | 计数器，例如 `dns_hit`、`dns_miss` |
| `traffic`、`traffic_total` | 全部代理的请求与流量序列 |
| `proxy_series` | 按代理名分组的序列 |
| `step_seconds` | 序列步长，按范围自动选择 |

每个采样点的字段：`at`、`cpu_percent`、`load1`、`load5`、`load15`、`mem_total`、`mem_used`、`mem_available`、`swap_total`、`swap_used`、`net_rx_rate`、`net_tx_rate`、`disk_read_rate`、`disk_write_rate`、`tcp_connections`、`established`、`time_wait`、`listen`、`processes`、`conntrack`。

### /api/proxies

`proxies` 固定四行：`docker`、`github`、`dns`、`sni`。每行包含：

| 字段 | 说明 |
| --- | --- |
| `name`、`label`、`path`、`enabled` | 通道标识与状态 |
| `requests`、`failures`、`bytes_in`、`bytes_out` | 累计值，4xx 不计失败 |
| `success_rate` | 成功率百分比 |
| `in_flight` | 进行中的请求数 |
| `status_classes` | `2xx`、`3xx`、`4xx`、`5xx` 计数 |
| `latency` | `count`、`avg_millis`、`p50`、`p90`、`p99`、`max`、`histogram`（12 档）；分位取自直方图档位且不超过 `max` |
| `rates` | 近六十分钟的 `requests_per_min`、`failures_per_min`、`bytes_in_per_sec`、`bytes_out_per_sec`、`requests_per_sec`，保留三位小数 |
| `last_minute` | 最近一分钟的 `requests`、`failures`、`bytes_in`、`bytes_out`、`in_per_sec`、`out_per_sec`、`requests_per_min`、`failures_per_min` |
| `window` | 近六十分钟的请求、失败、出入流量与延迟汇总 |
| `series` | 近六十分钟、步长 10 秒的序列；无流量通道返回零值时间轴 |
| `targets` | 热点目标，最多 8 个，按流量排序 |
| `upstream` | 上游响应耗时，`count` 与 `avg_millis` |

`recent` 为最近 100 条请求记录。

DNS 通道没有 HTTP 状态码，返回码按语义映射：NOERROR 记 200、FORMERR 记 400、SERVFAIL 与上游不可达记 502、NXDOMAIN 记 404、NOTIMP 记 501、REFUSED 记 403。因此正常解析计入 2xx，上游故障才计入失败。

### /api/effect

GET 请求，返回最近 `minutes`（5 到 60，默认 60）的承载统计。数据来自真实请求，不主动探测上游：

| 字段 | 说明 |
| --- | --- |
| `window_minutes` | 统计窗口，单位分钟 |
| `totals` | 请求数、失败数、成功率、进行中请求、出入字节、出入速率、平均/P90/最大耗时、延迟样本数 |
| `overhead` | 平均总耗时、上游等待、网关转发耗时（总耗时减上游等待）、上游样本数与占比 |
| `dns` | `dns_hit`、`dns_miss`、命中率与总查询数 |
| `channels[]` | 四个通道：`name`、`label`、`path`、`enabled`、请求与失败、`success_rate`、`avg_latency_ms`、`p90_ms`、`max_latency_ms`、`upstream_ms`、`gateway_ms`、`in_flight`、`bytes_in`、`bytes_out`、`in_per_sec`、`out_per_sec`、`requests_per_min`、`status_classes`、`targets` |
| `series[]` | 每分钟的请求、失败、入站与出站序列 |
| `explain` | 统计口径说明 |

4xx 不计为失败；失败指 5xx、无状态码或传输错误。

### /api/dns

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/dns` | DNS 分流配置、运行状态、域名列表与建议网关地址 |
| POST | `/api/dns/config` | 启用或停用网关 DNS 服务，body 至少 `{"enabled":true}`，可带 `gateway_ip`、`listen`、`upstream`、`domains`；保存后服务自动重启 |

`dns` 对象字段：`enabled`、`listen`（配置值）、`gateway_ip`、`domains`、`upstream`、`running`、`active_listen`、`active_port`、`fallback`、`port53`、`error`。

启用时自动选择监听地址：优先配置值（默认 `0.0.0.0:53`），被占用时依次尝试 `5353`、`5300`、`9053`，成功后退回响应 `note` 说明原因，并把实际地址写入配置。没有可用地址时返回 400。

## sing-box

`singbox.enabled` 为 false 时以下接口均返回 503。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/singbox` | 内核、节点、校验结果、端口一致性、备份与流量总览 |
| POST | `/api/singbox` | 新建节点，body `{"type":"hysteria2","name":"手机"}`，可带 `port`、`server`、`method` |
| POST | `/api/singbox/action` | 节点操作，body `{"action":"reset","id":"Hysteria2-12538"}`；动作 `reset`、`enable`、`disable`、`delete`、`restart`、`check`、`stats-enable`、`stats-disable`；凡需要重启的动作都会先清除服务的失败状态、重试至多三次，并在每次启动后观察两秒，确认服务仍处于运行状态且 systemd 未记录新的自动重启；仍失败时返回 400 并附服务日志最后一条错误 |
| GET | `/api/singbox/config?id=ID` | 读取节点配置文本 |
| POST | `/api/singbox/config` | 保存节点配置，body `{"id":"...","text":"..."}`；校验失败返回 400 并附 `check`，原文件不变 |
| GET | `/api/singbox/qr?id=ID&user=0&size=320` | 分享链接二维码，返回 PNG |
| GET | `/api/singbox/connections` | 活动连接，需要先开启实时统计（主配置含 `experimental.clash_api`） |
| GET | `/api/singbox/log?lines=120` | 服务日志尾部 |
| GET | `/api/singbox/core` | 内核状态 |
| POST | `/api/singbox/core` | 内核操作，body `{"action":"install","version":"1.14.4"}`；动作 `install`、`update`、`rollback-previous`、`rollback-preinstall`、`restart`、`uninstall`。安装或更新会启用开机自启，卸载会停用服务并归档内核与单元 |

`stats` 对象字段：`enabled`、`controller`、`secret`。`stats-enable` 把 `experimental.clash_api` 写入主配置（默认 `127.0.0.1:19090`，自动生成 32 位密钥），`stats-disable` 移除该块；两者都会备份主配置、执行 `sing-box check` 并重启 sing-box，失败时恢复原文件。

`drift` 对象字段：`supported`、`running`、`configured`、`listening`、`missing`、`pending`、`changed_at`、`started_at`、`error`。它把启用节点配置的端口与 `ss -lntu` 的监听表对照：`listening` 只保留配置里也确实在监听的端口，`missing` 是配置了但没有任何监听者的端口（重启 sing-box 生效）。`pending` 表示节点文件或主配置在服务启动之后被修改过，`changed_at` 与 `started_at` 给出对应时间。服务单元只保留 `CAP_NET_BIND_SERVICE`，无法按进程归属套接字，因此不区分监听者是谁、也不统计预期之外的监听端口。sing-box 未运行时只返回配置端口，不计算差异；读取失败时 `supported` 为 false。Clash API 端口不参与比对。

`core` 对象字段：`installed`、`version`、`latest`、`latest_error`、`update_available`、`platform`、`binary`、`unit_installed`、`unit_managed`、`backups`、`note`。`unit_managed` 表示 systemd 单元由本项目写入。

`install` 与 `update` 从 GitHub 发布页下载对应架构压缩包，解压后执行 `sing-box version` 核对版本再原子替换；`rollback-previous` 恢复上一份内核备份；`rollback-preinstall` 精确恢复首次安装前的内核、单元与服务状态；`uninstall` 把内核与单元归档到数据目录，节点配置保留。

## 国内机接入与 agent

v1.6.0 起推荐流程是 **agent 模式**：面板签发一次性 enroll token，国内机执行 `cn-setup --enroll-token`，随后由 `warden-cn-agent` 常驻接管分流并每 10 秒心跳上报；面板可查看状态、下发分流域名、升级或回退 agent。`/api/cn` 保留的 `command` / `commands` 是 **legacy 模式**（无 agent、hosts 托管块 + 本地 dnsmasq + 看门狗）的兼容命令，已弃用。

### agent 端点（无需会话，token 认证）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/agent/enroll` | 一次性 enroll token 换发 machine token；按 hostname 去重（同机重装取代旧记录） |
| POST | `/api/agent/heartbeat` | Bearer machine token；上报状态、领取命令、按 hash 增量下发分流域名 |
| GET | `/api/agent/binary/{version}/{arch}[.sha256]` | 自托管的 agent 缓存构建（`warden-cn-agent-linux-{amd64,arm64,arm}`）；未缓存版本 404 |

`/api/agent/binary/...` 同时接受 `{arch}` 与 `warden-cn-agent-linux-{arch}` 两种文件名形态，版本段为裸版本（`1.6.0`，无 `v`）。这些路径在面板前缀下和根路径都可访问；网关安装/升级时把三种架构预取到 `$SCRIPT_DIR/agent/<version>/`（保留最近 3 个版本）。

### 管理端点（需要会话与 CSRF）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/cnmachines` | 机器列表、在线状态、版本/目标版本、探测与资源摘要、事件；附 `cached_versions`、`gateway_version` |
| POST | `/api/cnmachines/enroll-token` | 签发一次性注册令牌（15 分钟） |
| POST | `/api/cnmachines/{id}/command` | `sync-domains` / `self-check` / `self-upgrade`（可带 `version` 指定已缓存版本，低于网关版本即回退；未缓存版本 400） |
| POST | `/api/cnmachines/{id}/auto-follow` | 开关自动跟随网关版本（打开会清除操作员钉选） |
| POST | `/api/cnmachines/{id}/pin` | 钉选/清除机器目标版本（`{"version":""}` 清除） |
| POST | `/api/cnmachines/{id}/remove` | 注销（`force=false` 下发 deregister，`force=true` 立即移除并吊销） |

### `/api/cn`（历史接口）

`/api/cn` 只生成给国内机使用的命令，网关本身不执行国内机设置。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/cn` | 返回 legacy 命令（auto/none）、自检/恢复命令、网关地址、分流域名、agent 二进制地址与 TLS 模式 |

响应字段：`as_of`、`origin`、`gateway_ip`、`panel_host`、`domains`、`script_url`、`command`、`commands`（`auto`、`none`）、`restore_command`、`health_url`、`check_command`、`agent_binary_url`、`agent_binary_version`、`cn_mode`、`tls_mode`、`dns`、`dns_ready`。

`command` 恒等于 `commands.auto`，`cn_mode` 恒为 `auto`；这些都是 legacy 模式命令。`tls_mode` 为 `self` 时说明面板使用自签名证书，agent 注册命令需要 `--insecure-tls`（面板生成的命令会自动带上）。

`health_url` 指向网关 80 端口的 `/healthz`，`check_command` 在 `origin` 非空时重新下载脚本执行 `cn-check`，`origin` 为空时退化为 `sudo warden-cn cn-check`。

`restore_command` 重新下载脚本；国内机执行过 `cn-setup` 后脚本保存为 `/usr/local/bin/warden-cn`，也可直接运行 `sudo warden-cn cn-restore`。

`dns_ready` 为 true 表示网关自身的 DNS 服务正在 53 端口运行。它与国内机分流方式无关，只用于面板展示；国内机是否需要 DNS 由 `cn-check` 在本机判断。

## 脚本下载

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/bootstrap.sh` | 下载安装脚本，无需登录 |
| GET | `/toolbox.sh` | 下载工具箱脚本，无需登录 |

两个脚本由安装时复制到 `/opt/warden-gateway`，可在任意机器上通过面板地址获取，不依赖 GitHub。

## 备份

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/backups` | 备份列表 |
| POST | `/api/backups` | 创建备份 |
| GET | `/api/backups/{id}/download` | 下载备份归档 |
| POST | `/api/backups/{id}/restore` | 恢复备份 |
| DELETE | `/api/backups/{id}` | 删除备份 |

备份 id 格式为 `backup-YYYYMMDDTHHMMSSZ`，同一秒内重复创建会追加序号。

## 响应格式

成功响应为 JSON 对象，错误响应包含 `error` 字段。认证失败返回 401，权限或 CSRF 校验失败返回 403，请求体过大返回 413。
