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
| GET | `/api/metrics?range=1h\|6h\|24h` | 主机指标序列 |
| GET | `/api/proxies` | 代理统计 |
| POST | `/api/proxies/test` | 测试代理，body `{"name":"docker\|github\|panel\|sni\|dns"}` |
| GET | `/api/services` | 服务列表 |
| POST | `/api/services/{id}/action` | 服务操作，body `{"action":"start\|stop\|restart"}` |
| GET | `/api/cert` | 证书状态 |
| POST | `/api/cert/renew` | 触发证书续期检查 |
| GET | `/api/config` | 配置和设置 |
| GET | `/api/audit?limit=100&q=` | 审计记录 |
| GET | `/api/diagnostics` | 一键诊断：项目自身与国内机配置 |

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
  "hosts": true,
  "dns": false,
  "apt": true,
  "bbr": true,
  "docker": true,
  "git": true,
  "apt_update": false
}
```

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
