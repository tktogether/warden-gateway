# 设计文档：国内机 Agent 化（warden-cn-agent）

日期：2026-11-17
状态：待批准
目标版本：v1.6.0
决策人：橙先生

## 1. 意图与背景

现状（v1.5.3）：

- 国内机通过 `bootstrap.sh cn-setup` 一次性生成三样东西：`/etc/hosts` 托管块、本地 dnsmasq（198.18.0.1:53）、看门狗 systemd timer。分流域名烤进本地 `/etc/warden-gateway-cn/cn.env`。
- 新增一个分流域名需要：改 Go 侧 `DefaultSplitDomains()` + 改 bootstrap.sh 的 `CN_DEFAULT_DOMAINS`（测试锁定一致）→ 发版 → 代理机升级 → 国内机重跑 cn-setup。v1.5.3 补 `pkg-containers.githubusercontent.com` 走完了全程。
- 代理机对国内机零感知：没有任何上报，面板只能生成一次性命令文本。

要解决的问题（用户原始诉求）：

1. 在线新增 SNI 支持域名（面板操作，不发版）
2. 国内机域名列表从代理机获取并自动更新（不重跑 cn-setup）
3. 远程管理：设计上报什么信息、远程做什么功能
4. 国内机运行常驻二进制（agent），收编 dnsmasq + 看门狗
5. 国内机自升级
6. auto-discover：自动注册 + 自动发现 workload
7. 数据安全必须有保证

## 2. Goals / Non-goals / Anti-goals

Goals：

- 面板增删分流域名，国内机在 ≤ 2 个心跳周期（约 60–120 秒）内生效，全程无需发版、无需登录国内机。
- 面板能看到每台国内机：在线状态、健康分类、版本、磁盘/内存/负载、容器与应用列表、最后心跳时间。
- 面板可远程触发：立即同步、自检、升级、重启、注销。
- agent 自升级：从网关下载新版本二进制，原子替换，失败回滚。
- 存量 dnsmasq+watchdog 部署可通过新 cn-setup 平滑迁移到 agent。

Non-goals：

- 网关主动推送（公网→国内不可达，一切走 agent 拉取，拓扑决定）。
- 远程执行任意命令（安全面太大，v1 不做）。
- 多台网关 / 高可用集群。（一台网关管多台国内机正是 v1 的目标形态，机器注册表就是为此设计的；不做的是同时存在多台网关。）
- 改动面板的 Docker / GitHub 代理功能本身。

Anti-goals（出现即视为失败）：

- agent 故障导致国内机全机 DNS 不可用，哪怕短暂。
- 分流域名出现第二份硬编码事实源（`DefaultSplitDomains()` 只保留为初始种子，运行时一律以配置为准）。
- 任何明文 token 落盘到网关侧。

## 3. 约束

- 国内机是 Coolify 生产机，agent 必须低资源占用（< 50 MB 常驻内存目标），systemd 约束资源上限。
- 国内机上容器不读宿主 `/etc/hosts`，容器流量只能靠本地 DNS 分流（这是 v1.5.0 引入 dnsmasq 的原因，agent 必须接管同一职责）。
- 现有 `cn-restore` / `cn-check` / `cn-status` 语义需要保留。
- 代理机已有 HTTPS 与证书体系（ACME），agent 流量复用。

## 4. 方案取舍（已决策）

**方案 A（选定）：轻量 Go agent，收编 DNS + 看门狗。**
agent 自己监听 198.18.0.1:53，分流域名答网关 IP、其余转发上游；健康探测内置主循环；网关不可达时降级为直连解析——降级从"60 秒一次的状态机 + 摘除 hosts 块"变成逐查询、连续的内部行为。复用 `internal/dnsproxy`。hosts 托管块机制整体废弃。

**方案 B（否决）：agent 只管理 dnsmasq + watchdog，不做 DNS。**
失败域更小，但两套机制并存、hosts 块的写入/摘除/抢优先级脆弱性保留、降级不实时，远程管理能力封顶。

用户对方案 A 的附加硬性要求：**最大程度防意外**，落地为第 8 节安全网，全部为验收标准。

## 5. 总体架构

```
国内机                                          代理机
┌──────────────────────────┐    HTTPS 443     ┌────────────────────────────┐
│ warden-cn-agent          │ ───────────────> │ warden-gateway             │
│  内置 DNS 198.18.0.1:53  │  POST heartbeat  │  /api/agent/*              │
│  健康探测循环（本地对照） │ <─────────────── │  分流域名事实源（cfg）       │
│  workload 自动发现       │  命令+域名列表    │  agent 二进制托管 + 校验和   │
│  命令执行 / 自升级       │                  │  面板「国内机」页            │
└──────────────────────────┘                  └────────────────────────────┘
```

一个心跳往返完成三件事：上报状态、领命令、拿最新域名列表（仅版本变化时带全量）。不需要网关到国内的任何主动连接。

## 6. 组件设计

### 6.1 warden-cn-agent（新 `cmd/warden-cn-agent`）

- 单二进制，静态编译，systemd 常驻。
- **内置 DNS 服务器**：复用 `internal/dnsproxy.Proxy`（同一 Go module），监听 `198.18.0.1:53`（地址与现网一致，resolv.conf 语义不变）。分流域名 → 网关 IP；其余 → 上游。降级时（探测判定网关不可达）分流域名也直接走上游返回——降级逐查询生效，无状态机延迟。
- **健康探测循环**：沿用现有分类 `OK` / `LINK_443_BLOCKED` / `LINK_PORTS_BLOCKED` / `C_DOWN` / `LOCAL_BROKEN`；保留"本地对照组先确认外网正常，再判定网关故障"的逻辑，避免把本机断网误判成网关故障。
- **workload 自动发现**：通过 docker socket 枚举容器（名称、镜像、状态、启动时间）；带 Coolify 标签的容器归组为应用上报。只采集元数据（见 8.2）。
- **心跳**：默认每 30 秒 `POST /api/agent/heartbeat`（Bearer machine token）。响应携带：待执行命令、域名列表（含版本 hash，agent 本地 hash 一致则忽略）、升级意图——网关为每台机器持久化「目标版本」，开启「自动跟随网关版本」的机器随网关发版自动更新目标版本；agent 仅在目标版本与本地不一致时执行自升级。
- **命令执行**：`sync-domains`（立即重新拉取并应用）、`self-check`（执行等价 cn-check 的检查，报告随下次心跳回传）、`self-upgrade`、`deregister`（清除本机分流配置、恢复直连 resolv.conf、停用自身单元）。**没有远程重启命令**：升级与配置变更本身会重启 agent，卡死由 systemd 看门狗处理；面板不做整机 reboot 能力（已决策，2026-11-17）。
- **自升级**：`GET /api/agent/binary` + 校验和验证 → 临时文件写入 → 原子 rename 替换自身 → 退出交由 systemd 重启。新版本启动失败（连续 2 次崩溃）时 systemd 自动回退到备份的旧二进制（unit 里用 `ExecStartPre` 恢复机制）。
- **本地状态文件**：`/etc/warden-gateway-cn/agent.json`（0600：machine token、网关 origin、上游 DNS、当前列表 hash）；所有改动前快照（沿用现有 `cn_snapshot_file` 模式）。

### 6.2 网关侧 `/api/agent/*`（新）

- `POST /api/agent/enroll`：一次性 enroll token + 机器信息 → 颁发 machine token（随机 32 字节，网关只存 SHA-256 摘要）。enroll token 用后即焚。
- `POST /api/agent/heartbeat`：认证后接收上报，返回命令队列 + 域名列表 + agent 版本。30 秒无心跳判定离线。每 token 限流。
- `GET /api/agent/binary`、`GET /api/agent/binary/checksum`：托管 agent 二进制（**多版本**版本化路径，模式同现有 `/bootstrap.sh` 静态托管，支持回退与灰度）。
- **升级策略（已决策，2026-11-17）**：默认手动——面板逐台点「升级」才设置该机器的目标版本；每台机器可单独打开「自动跟随网关版本」开关（默认关）。生产机上不会出现无人触发的二进制替换。
- **持久化**：注册表、machine token 摘要、待执行命令队列、每台机器的最新快照与事件 journal 落盘到 DataDir 独立状态文件（0600），并纳入现有备份/恢复流程；内存仅作运行缓存。网关重启或升级不得丢失已注册机器——否则国内机就得登机重跑 enroll，直接违背"全程无需登录国内机"。所有 agent 管理操作进 audit log。

### 6.3 面板

- 新页「国内机」：机器卡片（在线/离线、版本、健康分类、磁盘/内存/负载、容器与应用摘要、最后心跳时间）+ 操作按钮（立即同步、自检、升级、注销；**无远程重启/重启整机入口**）。轻量刷新模式同现有面板指标页。
- **分流域名管理**（原诉求 1）：设置页新增「分流域名」编辑器（增删改查）。保存后：
  1. 写入 `cfg.DNS.Domains` —— 这成为运行时唯一事实源；
  2. 校验每个域名被 `sni.passthrough` 某个模式覆盖，未覆盖的自动补进 passthrough（用户显式管理过的 passthrough 不被覆盖时给出提示）；
  3. 保存即生效——沿用现有 `scheduleRestart` 机制，即 `systemctl restart` 整个网关服务：秒级中断，所有 443 在途连接断一次。域名编辑低频、中断可接受，v1 就这么做；零中断热替换列为后续优化，不进 v1 验收；
  4. 心跳周期内自动下发到所有在线 agent。
- `/api/cn` 与 `/api/dns/status` 改为返回 live config 的域名列表，不再返回 `DefaultSplitDomains()`（修掉现存断层）。
- 注册：面板生成一次性 enroll token + 安装命令（`curl bootstrap.sh` + `cn-setup --enroll-token XXX`），机器跑完命令自动出现在列表。

### 6.4 bootstrap.sh `cn-setup`（agent 模式）

- 新流程：下载 agent 二进制（走网关托管路径）→ 写 `agent.json` → 装 systemd unit（含资源约束与看门狗配置）→ enroll → 等待首次心跳成功 → 写 resolv.conf（agent DNS + 公网 DNS 兜底双 nameserver）→ NetworkManager `dns=none` 守卫保留。
- **迁移**：检测旧部署（dnsmasq 单元、watchdog timer、hosts 托管块）→ 停用并清理 → 装 agent。老机器重跑 cn-setup 即完成迁移。
- `cn-restore` / `cn-check` / `cn-status` 保留命令名与语义，内部改为驱动 agent（restore：停用 agent、还原 resolv.conf、清 agent.json；check/status：读 agent 本地状态）。

## 7. 域名列表的生命周期（原诉求 1+2+5 的合并视图)

```
面板编辑「分流域名」
  → cfg.DNS.Domains（事实源，版本 hash 随之变化）
  → 443 SNI / DNS 监听热生效
  → agent 心跳拿到新列表（hash 变化才下发全量）
  → agent 重载内置 DNS（逐查询，无断流）
```

`DefaultSplitDomains()` 与 bootstrap.sh `CN_DEFAULT_DOMAINS` 保留为全新安装的初始种子，现有一致性测试保留。

## 8. 安全网（防意外，验收标准）

1. **resolv.conf 双 nameserver**：agent DNS 在前 + 公网 DNS（默认 223.5.5.5）在后。agent 进程整体死亡时解析落到公网 DNS——退化为无分流，但绝不断网。
2. **DNS 永不黑洞**：任何查询都有答案——网关可达→分流应答；不可达→直连上游；上游也失败→返回缓存的上次已知结果或明确 DNS 错误，不悬挂到客户端超时。
3. **systemd 双保险**：`Restart=always` + `WatchdogSec`（agent 周期性 sd_notify，卡死强杀重启，秒级恢复）；`MemoryMax` 等资源约束。
4. **配置变更先快照后应用，写坏自动还原**。
5. **自升级回滚**：新二进制连续崩溃自动回退旧版本。
6. **心跳失败不产生本地动作**：拿不到命令/列表时维持现状运行，绝不因远端异常清空本地配置。

## 8.2 数据安全（硬性要求，用户明确提出）

- 全链路 HTTPS（复用面板 ACME 证书）；agent 端校验证书。
- machine token 随机 32 字节，网关只存 SHA-256（模式同访问密钥），agent 端 0600。
- 上报最小化：只有元数据（容器名/镜像/状态/启动时间、系统资源数字），不含容器环境变量、不含业务数据内容。
- 网关侧快照与 journal 权限 0600，面板访问走现有会话 + CSRF 体系。
- agent 端点按 token 限流。防重放**不引入**严格单调时间戳校验：单调时钟在机器重启后归零，照字面实现会把每台重启过的国内机永久判定为"时间回退"而失联。心跳携带 boot 时间与 uptime 仅作展示用途，安全边界由 HTTPS + per-machine token + 限流构成，不过度设计。

## 9. 兼容性

- 老版部署（dnsmasq + watchdog）在升级网关后继续可用；迁移由用户择机重跑 cn-setup 完成。
- `/api/cn` 保留全部现有字段，新增 agent 相关字段（`agent_binary_url` 等）。
- `--cn-mode` 参数语义不变（`auto` / `none`），`none` 模式不安装 agent。

## 10. 留给 builder 的决定

- `internal/dnsproxy.Proxy` 复用所需的接口改造（当前构造耦合 config.Config 类型，agent 需构造等效输入；降级逻辑在哪一层注入）。
- docker 枚举实现（直接 HTTP over socket vs 引入库）与 Coolify 标签的具体匹配规则。
- 心跳 payload 的精确 JSON schema 与版本 hash 算法。
- sd_notify 在 Go 内的实现方式（unix socket 直写，避免 cgo）。
- 面板 UI 组件的具体布局与文案。
