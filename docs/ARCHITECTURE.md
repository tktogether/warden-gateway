# 架构

## 进程结构

`warden-gateway serve` 在单个进程中启动以下服务：

| 组件 | 默认监听 | 作用 |
| --- | --- | --- |
| SNI 入口 | `0.0.0.0:443` | 读取 TLS ClientHello，按 SNI 分流 |
| HTTPS 面板 | `127.0.0.1:8443` | 面板、Docker 代理、GitHub 代理 |
| HTTP | `127.0.0.1:8080` | 本地健康检查和调试 |
| ACME | `0.0.0.0:80` | 证书 HTTP-01 校验 |
| DNS | `0.0.0.0:53` | 可选域名分流，默认关闭；53 被占用时自动回退到 5353、5300、9053 |

sing-box 不由本进程托管：主配置 `/etc/sing-box/config.json` 引用 `/etc/sing-box/conf/*.json` 节点，内核在 `/etc/sing-box/bin/sing-box`，单元为 `sing-box.service`。面板与命令行通过 `sing-box check` 校验配置、通过 `systemctl` 重载服务；内核安装、备份与回滚由 `internal/singbox` 完成。

## 请求路径

### 面板

```text
客户端 -> 443 SNI(panel.example.com) -> 8443 TLS -> /warden/
```

### Docker 镜像

```text
docker pull -> https://panel.example.com/v2/... -> SNI -> 8443 -> DockerHandler -> crproxy -> registry-1.docker.io
```

`DockerHandler` 在 `/v2/<repo>/referrers/<digest>` 返回空 OCI index，其余请求交给 crproxy。

DNS 服务启动时按候选列表逐个尝试绑定 UDP 和 TCP，绑定失败只记录 `status.error` 并继续运行其余组件；绑定成功后把实际地址写入状态供面板展示。

### GitHub

```text
GET https://panel.example.com/https://github.com/owner/repo/... -> SNI -> 8443 -> GitHub 代理 -> github.com
```

代理只接受 `/http://` 和 `/https://` 前缀，并校验目标主机在允许列表中。

### 透传

未命中面板域名且命中透传列表的 SNI 会被原样转发到 `host:443`。TLS 不终止，客户端仍校验原站证书。

## 配置与数据

| 路径 | 内容 |
| --- | --- |
| `/etc/warden-gateway/config.json` | 主配置，权限 0600 |
| `/var/lib/warden-gateway/acme/` | ACME 证书缓存 |
| `/var/lib/warden-gateway/backups/` | 备份归档和校验和 |
| `/var/lib/warden-gateway/initial-access-key` | 首次安装生成的密钥副本，仅 root 可读 |
| `/var/lib/warden-gateway/gateway.log` | 运行日志 |
| `/var/lib/warden-gateway/audit.log` | 审计日志 |
| `/var/lib/warden-gateway/history.db` | 指标历史 |
| `/etc/sing-box/` | sing-box 主配置、节点配置、内核与证书 |
| `/var/lib/warden-gateway/singbox-core/` | 内核安装前快照与卸载归档 |

## 认证

- 访问密钥保存为 SHA-256 十六进制摘要。
- 登录成功后签发 `wg_session` Cookie，内容为 HMAC-SHA256 签名载荷，包含签发时间、过期时间、CSRF 令牌和密钥标识。
- 修改密钥后密钥标识变化，旧 Cookie 立即失效。
- 所有写请求必须携带 `X-CSRF-Token`，并校验 Origin。
- 登录失败按来源 IP 限流，默认 5 分钟 8 次。

## 监控与审计

面板每 15 秒采样一次主机指标，保存在内存环形缓冲区，并提供 1 小时、6 小时、24 小时视图。采样包含 CPU、负载 1/5/15、内存与交换分区、分网卡收发速率、磁盘 IO、TCP 连接状态分布、进程数与连接跟踪计数。

代理统计由 `internal/telemetry` 维护：累计计数、在途请求、状态码分类、延迟直方图、近六十分钟的十秒桶序列、按目标聚合的流量与上游响应耗时。四个通道分别是 Docker 镜像代理、GitHub 下载代理、DNS 分流和 SNI 入口；DNS 额外记录命中与转发计数。

面板「承载」视图只读取上述统计，不再主动探测上游：按四个通道给出请求量、成功率、平均与 P90 耗时、上游等待、网关转发耗时、入出流量、速率与热点目标，并附每分钟请求与流量序列。

审计日志记录登录、修改密钥、服务操作、备份、证书操作与 DNS 配置，单个文件超过 5 MiB 时轮转。

## 备份

备份为 tar.gz，包含 `config/config.json`、`data/acme/**` 和 `systemd/warden-gateway.service`。每个归档生成同名 JSON 清单，记录大小、SHA-256 和文件列表。

恢复流程：

1. 解压到临时目录并校验配置。
2. 对当前状态创建预备份。
3. 覆盖配置、证书缓存和 systemd 单元。
4. 重启 `warden-gateway.service`。

## 目录

```text
internal/app/          组件装配与生命周期
internal/config/       配置模型、校验、密钥生成
internal/auth/         会话、CSRF、登录限流
internal/gateway/      SNI、TLS、Docker 代理
internal/githubproxy/  GitHub 代理
internal/dnsproxy/     DNS 服务
internal/panel/        管理面板、指标、审计
internal/backup/       备份与恢复
internal/telemetry/    代理和请求统计
internal/ui/           终端输出
```
