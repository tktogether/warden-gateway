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
| GET | `/api/overview` | 主机、证书、服务、代理、审计摘要 |
| GET | `/api/metrics?range=1h\|6h\|24h` | 主机指标序列与代理流量序列，见下 |
| GET | `/api/proxies` | 代理统计，含速率、延迟分布、状态码、热点目标与近六十分钟序列 |
| POST | `/api/proxies/test` | 测试代理，body `{"name":"docker\|github\|panel\|sni\|dns"}` |
| POST | `/api/effect` | 直连上游与经代理转发的耗时对比 |
| GET | `/api/services` | 服务列表 |
| POST | `/api/services/{id}/action` | 服务操作，body `{"action":"start\|stop\|restart"}` |
| GET | `/api/cert` | 证书状态 |
| POST | `/api/cert/renew` | 触发证书续期检查 |
| GET | `/api/config` | 配置和设置 |
| GET | `/api/audit?limit=100&q=` | 审计记录 |
| GET | `/api/diagnostics` | 一键诊断：项目自身与国内机配置 |

### /api/metrics

返回字段：

| 字段 | 说明 |
| --- | --- |
| `range` | `1h`、`6h` 或 `24h` |
| `points` | 主机采样序列，最多 360 点 |
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
| `latency` | `count`、`avg_millis`、`p50`、`p90`、`p99`、`max`、`histogram`（12 档） |
| `rates` | `requests_per_min`、`failures_per_min`、`bytes_in_per_sec`、`bytes_out_per_sec`、`requests_per_sec` |
| `window` | 近六十分钟的请求、失败、出入流量与延迟汇总 |
| `targets` | 热点目标，最多 8 个，按流量排序 |
| `upstream` | 上游响应耗时，`count` 与 `avg_millis` |

`recent` 为最近 100 条请求记录。

### /api/effect

POST 请求，四路并发测量同一上游的直连耗时与经代理耗时：

| 字段 | 说明 |
| --- | --- |
| `rows[].name` | `docker`、`github`、`dns`、`sni` |
| `rows[].direct_ms`、`proxied_ms` | 直连与经代理耗时，单位毫秒 |
| `rows[].delta_ms` | `proxied_ms - direct_ms`，正值表示代理更慢 |
| `rows[].direct_error`、`proxied_error` | 失败原因，成功时为空 |
| `rows[].ok` | 两侧均成功时为 true |
| `explain` | 测量口径说明 |

HTTP 通道测到响应头为止；DNS 通道测单次解析；SNI 通道测 TLS 握手。

### /api/dns

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/dns` | DNS 分流状态、域名列表、建议网关地址与本机分流方式 |
| POST | `/api/dns/config` | 启用或停用网关 DNS 服务，body `{"enabled":true}`，可带 `gateway_ip`、`listen`、`upstream`、`domains`；保存后服务自动重启 |

## 国内机设置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/cn` | 国内机设置状态、命令与备份列表 |
| POST | `/api/cn/apply` | 在本机应用国内机设置，body 见下 |
| POST | `/api/cn/restore` | 恢复国内机设置，body `{"id":"latest"}` |

`/api/cn/apply` 的 body：

```json
{
  "gateway_ip": "192.0.2.10",
  "apt_mirror": "https://mirrors.aliyun.com",
  "docker_mirror": "https://panel.example.com",
  "git_mirror": "https://panel.example.com",
  "domains": ["github.com", "registry-1.docker.io"],
  "mode": "hosts",
  "hosts": true,
  "dns": false,
  "apt": true,
  "bbr": true,
  "docker": true,
  "git": true,
  "apt_update": false
}
```

`mode` 取 `hosts`、`dns`、`both` 或 `none`，一旦指定就覆盖 `hosts` 与 `dns` 两个布尔值；留空时按布尔值执行。`steps` 中 `detail` 可能缺省。

返回 `{"as_of":"...","backup_id":"...","steps":[{"name":"hosts","ok":true,"detail":"..."}],"warnings":[]}`。每次应用前自动备份，最多保留 10 个。

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
