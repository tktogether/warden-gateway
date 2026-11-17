#!/usr/bin/env bash
# Warden Gateway one-click installer.
set -eo pipefail

SERVICE="warden-gateway"
BIN_PATH="/usr/local/bin/warden-gateway"
CONFIG_DIR="/etc/warden-gateway"
CONFIG_FILE="$CONFIG_DIR/config.json"
DATA_DIR="/var/lib/warden-gateway"
SCRIPT_DIR="/opt/warden-gateway"
UNIT_PATH="/etc/systemd/system/$SERVICE.service"
LOGROTATE_PATH="/etc/logrotate.d/$SERVICE"
CLI_PATH="/usr/local/bin/warden"
CN_CLI_PATH="/usr/local/bin/warden-cn"
PANEL_BASE_PATH="/warden"
HTTP_LISTEN="127.0.0.1:8080"
REPO="tktogether/warden-gateway"
DEFAULT_VERSION="latest"
GITHUB_PROXY="${NW_GITHUB_PROXY:-}"

COMMAND="install"
ASSUME_YES=0
NON_INTERACTIVE=0
DRY_RUN=0
DOMAIN=""
PUBLIC_URL=""
EMAIL=""
BINARY=""
VERSION="$DEFAULT_VERSION"
MODE="binary"
NO_DOCKER=0
NO_GITHUB=0
NO_SNI=0
PURGE=0
RESTORE_ID="latest"
SINGBOX_ARGS=()
NEW_ACCESS_KEY=""
ACCESS_KEY=""
IMAGE=""
CONFIG_ARGS=""

DNS_DOMAINS=""
DNS_GATEWAY=""
DNS_LISTEN="0.0.0.0:53"
DNS_UPSTREAM="1.1.1.1:53"
DNS_ENABLE=0
NO_DNS=0
GATEWAY_IP=""

EXPLICIT_MODE=0
EXPLICIT_TLS=0
EXPLICIT_LISTEN=0
EXPLICIT_DOCKER=0
EXPLICIT_GITHUB=0
EXPLICIT_SNI=0
EXPLICIT_DNS=0
EXPLICIT_CN=0
INTERACTIVE_SETUP=1
CN_BACKUP_DIR=""
CN_CREATED_FILES=""
CN_CHANGED=0

CN_ORIGIN=""
TLS_MODE="auto"
ENTRY_LISTEN="0.0.0.0:443"
ACME_LISTEN="0.0.0.0:80"
HTTPS_LISTEN="127.0.0.1:8443"
CERT_FILE=""
KEY_FILE=""

CN_APT_MIRROR="https://mirrors.aliyun.com"
CN_DOCKER_MIRROR=""
CN_GIT_MIRROR=""
CN_DOMAINS=""
CN_SPLIT_MODE="auto"
CN_LOCAL_DNS=1
CN_LOCAL_DNS_ADDR="198.18.0.1"
CN_LOCAL_DNS_UPSTREAM=""
CN_WATCHDOG=1
CN_WATCH_INTERVAL=60
CN_NM_GUARD=1
CN_SPLIT_DIR="/etc/warden-gateway-cn"
CN_DNSMASQ_CONF="$CN_SPLIT_DIR/dnsmasq.conf"
CN_DNS_UNIT="/etc/systemd/system/warden-gateway-cn-dns.service"
CN_DNS_UNIT_NAME="warden-gateway-cn-dns"
CN_NM_CONF="/etc/NetworkManager/conf.d/90-warden-gateway-cn.conf"
CN_WATCH_SCRIPT="/usr/local/lib/warden-gateway-cn/watchdog.sh"
CN_WATCH_ENV="$CN_SPLIT_DIR/cn.env"
CN_WATCH_UNIT="/etc/systemd/system/warden-gateway-cn-watchdog.service"
CN_WATCH_TIMER="/etc/systemd/system/warden-gateway-cn-watchdog.timer"
CN_WATCH_UNIT_NAME="warden-gateway-cn-watchdog"
CN_WATCH_LOG="/var/log/warden-gateway-cn-watchdog.log"
CN_WATCH_STATE="$CN_SPLIT_DIR/watchdog.state"
CN_ONLY=0
CN_OPTIONS_CONFIGURED=0
CN_MANIFEST=""
CN_RECORDED=""
CN_DO_HOSTS=1
CN_DO_APT=1
CN_DO_BBR=1
CN_DO_DOCKER=1
CN_DO_GIT=1
CN_DO_APT_UPDATE=1
CN_BACKUP_ROOT="/var/backups/warden-gateway-cn"
CN_RESTORE_ID="latest"
CN_HOSTS_BEGIN="# >>> warden-gateway cn >>>"
CN_HOSTS_END="# <<< warden-gateway cn <<<"

# agent 模式（--enroll-token）：本机由 warden-cn-agent 接管分流，见 cn_setup_agent。
# agent 固定监听 198.18.0.1:53（internal/cnagent DefaultDNSListen），不提供改监听的选项。
CN_AGENT_UNIT="/etc/systemd/system/warden-cn-agent.service"
CN_AGENT_UNIT_NAME="warden-cn-agent"
CN_AGENT_BIN="/usr/local/lib/warden-gateway-cn/warden-cn-agent"
CN_AGENT_CONFIG="$CN_SPLIT_DIR/agent.json"
CN_AGENT_STATE_DIR="/var/lib/warden-gateway-cn"
CN_AGENT_ROLLBACK_GUARD="/usr/local/lib/warden-gateway-cn/agent-rollback-guard.sh"
CN_AGENT_GUARD_LOG="/var/log/warden-gateway-cn-agent-guard.log"
CN_AGENT_UPSTREAM="223.5.5.5:53"
CN_AGENT_FALLBACK_DNS="119.29.29.29"
CN_AGENT_HEARTBEAT=10
CN_AGENT_ENROLL_TOKEN=""
CN_AGENT_NO_AGENT=0
CN_AGENT_ORIGIN=""
CN_AGENT_MACHINE_ID=""
CN_AGENT_TOKEN=""
CN_AGENT_INSECURE_TLS=0
CN_DEFAULT_DOMAINS="github.com,api.github.com,codeload.github.com,raw.githubusercontent.com,objects.githubusercontent.com,gist.githubusercontent.com,ghcr.io,pkg-containers.githubusercontent.com,registry-1.docker.io,auth.docker.io,production.cloudfront.docker.com"

C_RESET=""
C_BOLD=""
C_DIM=""
C_GREEN=""
C_YELLOW=""
C_RED=""
if [ -t 1 ] && [ -z "$NO_COLOR" ]; then
  C_RESET=$'\033[0m'
  C_BOLD=$'\033[1m'
  C_DIM=$'\033[2m'
  C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'
  C_RED=$'\033[31m'
fi

say() { printf '%s\n' "$*"; }
info() { printf '%s\n' "$*"; }
ok() { printf '%s%s%s\n' "$C_GREEN" "$*" "$C_RESET"; }
warn() { printf '%s%s%s\n' "$C_YELLOW" "$*" "$C_RESET" >&2; }
die() { printf '%s错误：%s%s\n' "$C_RED" "$*" "$C_RESET" >&2; exit 1; }
title() { printf '\n%s%s%s\n' "$C_BOLD" "$*" "$C_RESET"; }
step() { printf '%s[%s]%s %s\n' "$C_DIM" "$1" "$C_RESET" "$2"; }
display_width() {
  local s="$1" bytes
  bytes="$(LC_ALL=C; printf '%s' "${#s}")"
  printf '%s' "$(( (bytes + ${#s}) / 2 ))"
}
pad_to() {
  local s="$1" target="$2" width pad
  width="$(display_width "$s")"
  pad=$(( target - width ))
  [ "$pad" -lt 0 ] && pad=0
  printf '%s' "$s"
  printf '%*s' "$pad" ''
}
kv() { printf '  %s %s\n' "$(pad_to "$1" 18)" "$2"; }
is_tty() { [ -t 0 ] && [ -t 1 ]; }
have() { command -v "$1" >/dev/null 2>&1; }
is_ipv4() {
  case "$1" in
    *[!0-9.]*|"") return 1;;
  esac
  [ "$(printf '%s' "$1" | awk -F. 'NF==4 && $1<=255 && $2<=255 && $3<=255 && $4<=255 {print "ok"}')" = "ok" ]
}
need_root() { [ "$DRY_RUN" -eq 1 ] && return 0; [ "$(id -u)" -eq 0 ] || die "请使用 root 运行"; }
confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  [ "$NON_INTERACTIVE" -eq 1 ] && return 0
  [ ! -t 0 ] && return 0
  local answer
  printf '%s [Y/n] ' "$1" >&2
  read -r answer || true
  case "$answer" in n|N|no|NO) return 1;; *) return 0;; esac
}
ask() {
  local prompt="$1" default="$2" answer
  if [ "$NON_INTERACTIVE" -eq 1 ] || [ ! -t 0 ]; then
    printf '%s\n' "$default"
    return 0
  fi
  if [ -n "$default" ]; then
    printf '%s [%s]: ' "$prompt" "$default" >&2
  else
    printf '%s: ' "$prompt" >&2
  fi
  read -r answer || true
  if [ -z "$answer" ]; then answer="$default"; fi
  printf '%s\n' "$answer"
}
ask_yes() {
  local prompt="$1" default="$2" answer suffix
  if [ "$NON_INTERACTIVE" -eq 1 ] || [ ! -t 0 ]; then
    [ "$default" = "1" ]
    return $?
  fi
  if [ "$default" = "1" ]; then suffix="[Y/n]"; else suffix="[y/N]"; fi
  printf '%s %s ' "$prompt" "$suffix" >&2
  read -r answer || true
  answer="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"
  if [ -z "$answer" ]; then [ "$default" = "1" ]; return $?; fi
  case "$answer" in y|yes) return 0;; *) return 1;; esac
}
run() {
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + %s\n' "$*"; return 0; fi
  "$@"
}

usage() {
  if [ "${CN_ONLY:-0}" -eq 1 ]; then
    usage_cn
    return 0
  fi
  cat <<'USAGE'
Warden Gateway 一键安装脚本

用法：
  bootstrap.sh [install] [选项]     安装或升级
  bootstrap.sh update [选项]        升级程序（保留配置和数据）
  bootstrap.sh uninstall [--purge]  卸载
  bootstrap.sh status               查看状态
  bootstrap.sh logs                 查看最近日志
  bootstrap.sh version              查看版本
  bootstrap.sh backup               立即备份
  bootstrap.sh restore [--id ID]    恢复备份（默认最新）
  bootstrap.sh doctor               一键诊断（仅网关自身）
  bootstrap.sh self-test            自检
  bootstrap.sh singbox [命令]       管理 sing-box 内核（status/install/rollback-previous/rollback-preinstall/restart/uninstall）
  bootstrap.sh cn-setup             在需要走网关的国内机上应用设置
  bootstrap.sh cn-check             在国内机上探测网关连通性并给出判定（不改文件）
  bootstrap.sh cn-status            在国内机上查看当前分流配置状态
  bootstrap.sh cn-restore [--id ID] 恢复国内机设置（默认最新备份）

安装完成后会注册维护命令 warden，常用操作：
  sudo warden status | update | doctor | backup | restore | logs | version | singbox
  国内机设置不在维护命令中，请在国内机上执行面板设置页生成的命令。
  国内机执行过 cn-setup 后，脚本同时保存为 warden-cn，可直接运行：
  sudo warden-cn cn-restore

安装选项：
  --domain DOMAIN        面板域名（留空则使用 IP 自签名证书）
  --public-url URL       面板完整地址，例如 https://panel.example.com/warden
  --email EMAIL          ACME 账户邮箱（可选）
  --access-key KEY       指定访问密钥（默认自动生成）
  --binary PATH          使用本地二进制安装
  --version VERSION      下载指定版本（默认 latest）
  --docker               使用 Docker 镜像运行
  --no-docker            不启用 Docker 镜像代理
  --no-github            不启用 GitHub 下载代理
  --no-sni               不启用 443 SNI 统一入口
  --panel-path PATH      面板路径，默认 /warden
  --tls MODE             auto（ACME）、self（自签名）或 files（已有证书）
  --cert-file PATH       TLS 证书文件（--tls files）
  --key-file PATH        TLS 私钥文件（--tls files）
  --listen-https ADDR    面板 HTTPS 监听，默认按域名/IP 自动选择
  --listen-http ADDR     本地 HTTP 监听，默认 127.0.0.1:8080
  --listen-entry ADDR    SNI 入口监听，默认 0.0.0.0:443
  --listen-acme ADDR     ACME HTTP 监听，默认 0.0.0.0:80
  --dns                  启用 DNS 分流
  --no-dns               不启用 DNS 分流
  --dns-domains LIST     参与 DNS 分流的域名，逗号分隔
  --dns-gateway IP       DNS 分流返回的网关地址（默认本机公网 IP）
  --dns-upstream ADDR    上游 DNS，默认 1.1.1.1:53
  --yes                  自动确认
  --non-interactive      不进行交互
  --dry-run              只打印步骤
  --purge                卸载时同时删除配置、数据和备份
  --id ID                恢复指定备份，默认 latest

国内机设置选项（仅 cn-setup / cn-check / cn-status / cn-restore 使用）：
  --enroll-token TOKEN   用面板签发的一次性注册令牌执行 agent 模式迁移（推荐）
  --insecure-tls         面板为自签名证书或仅 IP 访问时关闭 agent 的证书校验
                         （默认自动探测：仅当校验失败而 -k 可访问时启用）
  --no-agent             强制使用 legacy 模式（dnsmasq + 看门狗，已弃用）
  --origin URL           面板源地址，例如 https://panel.example.com（agent 模式必填，
                         或用 --public-url / 交互输入）
  --gateway-ip IP        国内机指向的网关地址
  --upstream ADDR        agent 上游 DNS，host:port，默认 223.5.5.5:53
  --no-hosts             不写 /etc/hosts 托管块（仅 legacy 模式）
  --no-local-dns         不安装本地 dnsmasq（容器将拿不到网关地址，不建议；仅 legacy）
  --local-dns-addr IP    本地 dnsmasq 监听地址，默认 198.18.0.1（仅 legacy 模式）
  --local-dns-upstream A 本地 dnsmasq 上游，逗号分隔，默认沿用本机现有 DNS 与 223.5.5.5
  --no-nm-guard          不写 NetworkManager dns=none（resolv.conf 可能被重写）
  --no-watchdog          不安装看门狗定时器（仅 legacy 模式）
  --watch-interval SEC   看门狗探测周期，默认 60
  --no-apt               不修改 APT 镜像
  --no-bbr               不启用 BBR
  --no-docker-mirror     不修改 Docker 镜像
  --no-git               不修改 Git 镜像
  --no-apt-update        不执行 apt-get update
  --apt-mirror URL       APT 镜像地址，默认 https://mirrors.aliyun.com
  --docker-mirror URL    Docker 镜像地址，默认使用网关地址
  --git-mirror URL       Git 镜像地址，默认使用网关地址
  --cn-domains LIST      参与分流的域名（agent 模式由面板下发，此选项仅 legacy 生效）
  --cn-mode MODE         auto（默认，本地 DNS + hosts）或 none（不启用分流；仅 legacy）
  --cn-id ID             恢复的国内机设置备份

不带参数运行且处于终端时进入交互安装向导。
USAGE
}

usage_cn() {
  cat <<'USAGE_CN'
warden-cn - Warden Gateway 国内机维护命令

用法：
  warden-cn cn-setup [选项]      应用国内机分流设置
  warden-cn cn-check             只读体检：探测代理机并检查本机分流状态
  warden-cn cn-status            查看当前分流配置快照
  warden-cn cn-restore [--cn-id ID]  回滚最近一次（或指定）备份
  warden-cn version              查看版本
  warden-cn -h | --help          显示本帮助

cn-setup 选项：
  --enroll-token TOKEN     面板签发的一次性注册令牌，执行 agent 模式迁移（推荐）
  --insecure-tls           面板为自签名证书或仅 IP 访问时关闭 agent 证书校验
  --no-agent               强制 legacy 模式（dnsmasq + 看门狗，已弃用）
  --origin URL             面板源地址（agent 模式必填，或用 --public-url / 交互输入）
  --gateway-ip IP          代理机地址（必填或自动探测本机出口 IP）
  --upstream ADDR          agent 上游 DNS，host:port，默认 223.5.5.5:53
  --cn-domains LIST        参与分流的域名（agent 模式由面板下发，仅 legacy 生效）
  --cn-mode MODE           auto（默认）或 none（仅 legacy）
  --no-hosts               不写 /etc/hosts 托管块（仅 legacy）
  --no-local-dns           不装本地 dnsmasq（容器将无法解析分流域名；仅 legacy）
  --local-dns-addr IP      本地 DNS 监听地址，默认 198.18.0.1（仅 legacy）
  --local-dns-upstream LIST 本地 DNS 上游，逗号分隔，默认沿用本机 resolver
  --no-nm-guard            不写 NetworkManager 的 dns=none 配置
  --no-watchdog            不安装看门狗（仅 legacy）
  --watch-interval SEC     看门狗探测周期，默认 60，最小 15
  --no-apt / --apt-mirror URL
  --no-bbr
  --no-docker-mirror / --docker-mirror URL
  --no-git / --git-mirror URL
  --no-apt-update
  --cn-id ID               cn-restore 指定备份 ID，默认 latest
  --yes | -y / --non-interactive / --dry-run

本命令不提供 install / update / uninstall / status / logs / doctor 等代理机操作，
这些请在代理机上使用 warden 命令。执行过的设置可用 cn-restore 一键回滚。
USAGE_CN
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      install|update|uninstall|status|logs|version|backup|restore|doctor|self-test|cn-setup|cn-check|cn-status|cn-restore)
        COMMAND="$1"; shift;;
      singbox)
        COMMAND="singbox"; shift; SINGBOX_ARGS=("$@"); return 0;;
      --domain) DOMAIN="$2"; shift 2;;
      --public-url) PUBLIC_URL="$2"; shift 2;;
      --email) EMAIL="$2"; shift 2;;
      --access-key) ACCESS_KEY="$2"; shift 2;;
      --binary) BINARY="$2"; shift 2;;
      --version) VERSION="$2"; shift 2;;
      --docker) MODE="docker"; EXPLICIT_MODE=1; shift;;
      --no-docker) NO_DOCKER=1; EXPLICIT_DOCKER=1; shift;;
      --no-github) NO_GITHUB=1; EXPLICIT_GITHUB=1; shift;;
      --no-sni) NO_SNI=1; EXPLICIT_SNI=1; shift;;
      --panel-path) PANEL_BASE_PATH="$2"; shift 2;;
      --tls) TLS_MODE="$2"; EXPLICIT_TLS=1; shift 2;;
      --cert-file) CERT_FILE="$2"; EXPLICIT_TLS=1; shift 2;;
      --key-file) KEY_FILE="$2"; EXPLICIT_TLS=1; shift 2;;
      --listen-https) HTTPS_LISTEN="$2"; EXPLICIT_LISTEN=1; shift 2;;
      --listen-http) HTTP_LISTEN="$2"; EXPLICIT_LISTEN=1; shift 2;;
      --listen-entry) ENTRY_LISTEN="$2"; EXPLICIT_LISTEN=1; shift 2;;
      --listen-acme) ACME_LISTEN="$2"; EXPLICIT_LISTEN=1; shift 2;;
      --dns) DNS_ENABLE=1; EXPLICIT_DNS=1; shift;;
      --no-dns) NO_DNS=1; DNS_ENABLE=0; EXPLICIT_DNS=1; shift;;
      --dns-domains) DNS_DOMAINS="$2"; shift 2;;
      --dns-gateway) DNS_GATEWAY="$2"; shift 2;;
      --dns-upstream) DNS_UPSTREAM="$2"; shift 2;;
      --gateway-ip) GATEWAY_IP="$2"; shift 2;;
      --no-hosts) CN_DO_HOSTS=0; EXPLICIT_CN=1; shift;;
      --no-local-dns) CN_LOCAL_DNS=0; EXPLICIT_CN=1; shift;;
      --local-dns-addr) CN_LOCAL_DNS_ADDR="$2"; EXPLICIT_CN=1; shift 2;;
      --local-dns-upstream) CN_LOCAL_DNS_UPSTREAM="$2"; EXPLICIT_CN=1; shift 2;;
      --no-nm-guard) CN_NM_GUARD=0; EXPLICIT_CN=1; shift;;
      --no-watchdog) CN_WATCHDOG=0; EXPLICIT_CN=1; shift;;
      --watch-interval) CN_WATCH_INTERVAL="$2"; EXPLICIT_CN=1; shift 2;;
      --no-apt) CN_DO_APT=0; EXPLICIT_CN=1; shift;;
      --no-bbr) CN_DO_BBR=0; EXPLICIT_CN=1; shift;;
      --no-docker-mirror) CN_DO_DOCKER=0; EXPLICIT_CN=1; shift;;
      --no-git) CN_DO_GIT=0; EXPLICIT_CN=1; shift;;
      --no-apt-update) CN_DO_APT_UPDATE=0; EXPLICIT_CN=1; shift;;
      --apt-mirror) CN_APT_MIRROR="$2"; shift 2;;
      --docker-mirror) CN_DOCKER_MIRROR="$2"; EXPLICIT_CN=1; shift 2;;
      --git-mirror) CN_GIT_MIRROR="$2"; EXPLICIT_CN=1; shift 2;;
      --cn-domains) CN_DOMAINS="$2"; EXPLICIT_CN=1; shift 2;;
      --cn-mode) CN_SPLIT_MODE="$2"; EXPLICIT_CN=1; shift 2;;
      --cn-id) CN_RESTORE_ID="$2"; shift 2;;
      --enroll-token) CN_AGENT_ENROLL_TOKEN="$2"; shift 2;;
      --insecure-tls) CN_AGENT_INSECURE_TLS=1; shift;;
      --no-agent) CN_AGENT_NO_AGENT=1; shift;;
      --upstream) CN_AGENT_UPSTREAM="$2"; shift 2;;
      --origin) CN_AGENT_ORIGIN="$2"; shift 2;;
      --yes|-y) ASSUME_YES=1; shift;;
      --non-interactive) NON_INTERACTIVE=1; INTERACTIVE_SETUP=0; shift;;
      --dry-run) DRY_RUN=1; shift;;
      --purge) PURGE=1; shift;;
      --id) RESTORE_ID="$2"; shift 2;;
      -h|--help|help) usage; exit 0;;
      *) die "未知参数：$1";;
    esac
  done
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n';;
    aarch64|arm64) printf 'arm64\n';;
    armv7l) printf 'armv7\n';;
    *) die "不支持的架构：$(uname -m)";;
  esac
}

detect_ip() {
  local ip=""
  if have curl; then
    ip="$(curl -4 -fsS --connect-timeout 5 --max-time 8 https://api.ipify.org 2>/dev/null || true)"
    [ -z "$ip" ] && ip="$(curl -4 -fsS --connect-timeout 5 --max-time 8 https://ifconfig.me 2>/dev/null || true)"
  fi
  printf '%s\n' "$ip"
}

download() {
  local url="$1" out="$2"
  if have curl; then
    run curl -fL --connect-timeout 10 --max-time 300 --retry 2 --retry-delay 2 -o "$out" "$url"
  elif have wget; then
    run wget -q --timeout=30 --tries=3 -O "$out" "$url"
  else
    die "需要 curl 或 wget"
  fi
}

asset_name() {
  local arch
  arch="$(detect_arch)"
  printf 'warden-gateway_linux_%s.tar.gz\n' "$arch"
}

release_urls() {
  local asset base
  asset="$(asset_name)"
  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  if [ -n "$GITHUB_PROXY" ]; then
    printf '%s/%s/%s\n' "${GITHUB_PROXY%/}" "$base" "$asset"
  fi
  printf '%s/%s\n' "$base" "$asset"
  printf 'https://gh-proxy.com/%s/%s\n' "$base" "$asset"
  printf 'https://ghfast.top/%s/%s\n' "$base" "$asset"
}

install_binary_from_release() {
  local tmp urls url ok_flag asset
  tmp="$(mktemp -d)"
  urls="$(release_urls)"
  ok_flag=0
  while IFS= read -r url; do
    [ -z "$url" ] && continue
    step "下载" "$url"
    if download "$url" "$tmp/release.tgz"; then
      if tar -tzf "$tmp/release.tgz" >/dev/null 2>&1; then ok_flag=1; break; fi
    fi
    warn "下载失败，尝试下一个来源"
  done <<< "$urls"
  [ "$ok_flag" -eq 1 ] || die "无法下载发布包，请使用 --binary 指定本地二进制"
  tar -xzf "$tmp/release.tgz" -C "$tmp"
  asset="$(find "$tmp" -maxdepth 2 -type f -name warden-gateway | head -n 1)"
  [ -n "$asset" ] || die "发布包中未找到 warden-gateway"
  install -m 0755 "$asset" "$BIN_PATH.new"
  rm -rf "$tmp"
}

install_binary_local() {
  [ -f "$BINARY" ] || die "本地二进制不存在：$BINARY"
  install -m 0755 "$BINARY" "$BIN_PATH.new"
}

install_binary() {
  if [ -n "$BINARY" ]; then
    step "安装" "使用本地二进制 $BINARY"
    install_binary_local
  else
    step "安装" "下载 $VERSION 版本"
    install_binary_from_release
  fi
  mv -f "$BIN_PATH.new" "$BIN_PATH"
}

# cache_agent_version 解析本次要缓存的具体版本（裸版本，无 v 前缀）。
# VERSION=latest 时必须先读已安装二进制的版本（write_version_from_binary 在
# 缓存之后才跑），再退回 $SCRIPT_DIR/VERSION；都拿不到就返回非零，由调用方
# 跳过缓存——绝不能建出 $SCRIPT_DIR/agent/latest/ 这种永远匹配不上的目录。
# Docker 模式例外：宿主机上的 $BIN_PATH 可能是不再运行的旧二进制，脚本版本
# 文件由 cn_docker_cache_agent 从运行中的容器刷新，优先级必须高于它。
cache_agent_version() {
  local ver=""
  if [ "$VERSION" != "latest" ]; then
    ver="${VERSION#v}"
  elif [ "$MODE" = "docker" ] && [ -f "$SCRIPT_DIR/VERSION" ]; then
    ver="$(tr -d ' \r\n' < "$SCRIPT_DIR/VERSION" | sed 's/^v//')"
  else
    if [ -x "$BIN_PATH" ]; then
      ver="$("$BIN_PATH" version 2>/dev/null | awk 'NR == 1 {print $1}' | sed 's/^v//')"
    fi
    if [ -z "$ver" ] || [ "$ver" = "latest" ]; then
      if [ -f "$SCRIPT_DIR/VERSION" ]; then
        ver="$(tr -d ' \r\n' < "$SCRIPT_DIR/VERSION" | sed 's/^v//')"
      fi
    fi
  fi
  case "$ver" in
    ''|latest|*[!0-9A-Za-z._-]*) return 1;;
  esac
  printf '%s\n' "$ver"
}

# cache_agent_binaries 把本版本的 agent 构建（三种架构 + CI 发布的 sha256
# 侧车）预取到 $SCRIPT_DIR/agent/<version>/，由面板
# /api/agent/binary/{version}/{arch} 对外提供。国内机的安装与自升级因此只连
# 本网关，不必访问 GitHub；预取失败不阻塞网关安装（面板会返回 404 并提示重跑
# 安装器），但会在输出里明确告警。
# 侧车必须来自 CI（同一 release 的 $asset.sha256），不能用刚下载的字节现算：
# 现算只能发现损坏，发现不了网关侧被替换；校验不过/缺侧车就跳过该架构。
cache_agent_binaries() {
  local arch asset tmp sidecar expected actual ok_flag=0 ver ver_dir
  if ! ver="$(cache_agent_version)"; then
    warn "无法解析具体 agent 版本（VERSION=$VERSION），跳过 agent 构建预取"
    return 1
  fi
  [ "$DRY_RUN" -eq 1 ] && { printf '  + 预取 agent 构建到 %s/agent/%s/\n' "$SCRIPT_DIR" "$ver"; return 0; }
  # 每个版本一个目录：/agent/<version>/<asset>[.sha256]。多版本才能支持灰度与
  # 回退——把某台机器钉在旧构建上，或回滚到已缓存的版本。
  ver_dir="$SCRIPT_DIR/agent/$ver"
  mkdir -p "$ver_dir" 2>/dev/null || { warn "创建 agent 缓存目录失败：$ver_dir"; return 1; }
  local base
  # 目录名是解析后的裸版本，下载源必须指向同一个具体 tag（v<ver>），不能再用
  # releases/latest/download：--binary 时 ver 读自本地二进制的版本，与 latest
  # 可能不是同一个 release，会让目录和来源不一致。
  base="https://github.com/$REPO/releases/download/v$ver"
  for arch in amd64 arm64 arm; do
    asset="warden-cn-agent-linux-$arch"
    tmp="$ver_dir/.$asset.$$"
    sidecar="$tmp.sha256"
    if ! download "$base/$asset" "$tmp"; then
      rm -f "$tmp" "$sidecar"
      warn "预取 agent 构建失败：$asset（面板将无法分发该架构的 agent）"
      continue
    fi
    if [ ! -s "$tmp" ]; then
      rm -f "$tmp" "$sidecar"
      warn "预取的 agent 构建为空：$asset"
      continue
    fi
    if ! download "$base/$asset.sha256" "$sidecar"; then
      rm -f "$tmp" "$sidecar"
      warn "预取 agent 校验和失败：$asset.sha256（跳过该架构，面板不会分发未经 CI 签发的构建）"
      continue
    fi
    expected="$(awk 'NR == 1 {print $1}' "$sidecar" 2>/dev/null | tr 'A-F' 'a-f')"
    case "$expected" in
      ''|*[!0-9a-f]*)
        rm -f "$tmp" "$sidecar"
        warn "agent 校验和格式无效：$asset.sha256"
        continue;;
    esac
    actual="$(sha256sum "$tmp" 2>/dev/null | awk '{print $1}' | tr 'A-F' 'a-f' || true)"
    if [ "${#expected}" -ne 64 ] || [ "$actual" != "$expected" ]; then
      rm -f "$tmp" "$sidecar"
      warn "agent 构建校验和不匹配：$asset（期望 $expected，实际 ${actual:-空}），已跳过"
      continue
    fi
    mv -f "$tmp" "$ver_dir/$asset"
    # 缓存 CI 发布的侧车（只留裸 hex 一行），而不是从下载字节现算。
    printf '%s\n' "$expected" > "$ver_dir/$asset.sha256"
    rm -f "$sidecar"
    chmod 0755 "$ver_dir/$asset"
    chmod 0644 "$ver_dir/$asset.sha256"
    ok_flag=1
  done
  if [ "$ok_flag" -eq 1 ]; then
    ok "agent 构建已缓存到 $ver_dir（国内机只连本网关下载）"
    cn_prune_agent_cache
  fi
  return 0
}

# cn_prune_agent_cache 只保留最近 3 个 agent 版本目录（含本次刚缓存的）。
# 更旧的删除，避免网关磁盘随发版无限增长；正在被机器钉住的版本若被清掉，
# 该机器的心跳仍会指向它，下载会 404 并保持旧二进制运行（安全网第 6 条）。
cn_prune_agent_cache() {
  local root="$SCRIPT_DIR/agent" keep=3
  [ -d "$root" ] || return 0
  # 按版本号排序（sort -V 可用时用它，否则退化为字典序），删除第 keep 个之后的。
  # 先显式探测 find 是否支持 -printf：把探测结果当 if 条件，BusyBox find 不
  # 支持时会安静地走 else，而不是让 sort 的空输出假装成功。
  local dirs
  if find "$root" -maxdepth 0 -printf '' >/dev/null 2>&1; then
    dirs="$(find "$root" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' 2>/dev/null | sort -V)"
  else
    dirs="$(find "$root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sed 's#.*/##' | sort)"
  fi
  [ -n "$dirs" ] || return 0
  local total idx name
  total="$(printf '%s\n' "$dirs" | wc -l)"
  idx=0
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    idx=$((idx + 1))
    if [ "$idx" -le $((total - keep)) ]; then
      rm -rf "$root/$name" 2>/dev/null || true
    fi
  done <<< "$dirs"
  return 0
}

# cn_docker_cache_agent Docker 安装/升级后的 agent 构建预取。单脚本 Docker
# 安装既没有 $BIN_PATH version，也没有 $SCRIPT_DIR/VERSION，版本只能从正在
# 运行的网关解析：优先本机 HTTP 健康端点（listen.http 来自配置，其次 CLI
# 值），再退到容器内二进制的 version，最后 -k 访问 PUBLIC_URL。解析成功后
# 先写 $SCRIPT_DIR/VERSION（cache_agent_version 在 Docker 模式优先读它），
# 再走统一的 cache_agent_binaries；任何失败只告警，不影响安装/升级。
cn_docker_cache_agent() {
  local v="" http_addr raw
  if [ -f "$CONFIG_FILE" ]; then
    http_addr="$(sed -n '/"listen"[[:space:]]*:[[:space:]]*{/,/}/p' "$CONFIG_FILE" \
      | sed -n 's/.*"http"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  fi
  [ -n "$http_addr" ] || http_addr="$HTTP_LISTEN"
  if [ -n "$http_addr" ]; then
    v="$(curl -fsS --connect-timeout 3 --max-time 8 "http://$http_addr/api/health" 2>/dev/null \
      | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  fi
  if [ -z "$v" ] && have docker; then
    raw="$(docker exec "$SERVICE" /usr/local/bin/warden-gateway version 2>/dev/null | head -n 1 || true)"
    v="$(printf '%s' "$raw" | awk '{print $1}' | sed 's/^v//')"
  fi
  if [ -z "$v" ] && [ -n "$PUBLIC_URL" ]; then
    v="$(curl -kfsS --connect-timeout 3 --max-time 8 "$PUBLIC_URL/api/health" 2>/dev/null \
      | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  fi
  v="$(printf '%s' "$v" | tr -d ' \r\n')"
  v="${v#v}"
  case "$v" in
    ''|latest|*[!0-9.]*)
      warn "无法从运行中的 Docker 网关解析具体版本，跳过 agent 构建预取"
      return 1;;
  esac
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + 从运行中的网关解析版本 %s，并预取 agent 构建\n' "$v"
    return 0
  fi
  if ! printf '%s\n' "$v" > "$SCRIPT_DIR/VERSION"; then
    warn "写入 $SCRIPT_DIR/VERSION 失败，跳过 agent 构建预取"
    return 1
  fi
  cache_agent_binaries || true
  return 0
}

is_cn_mirror() {
  case "$1" in
    *://mirrors.aliyun.com*|*://mirrors.cloud.aliyuncs.com*|*://mirrors.tuna.tsinghua.edu.cn*|*://mirrors.ustc.edu.cn*|*://mirrors.cloud.tencent.com*|*://mirrors.huaweicloud.com*|*://mirrors.163.com*|*://mirrors.bfsu.edu.cn*|*://mirrors.nju.edu.cn*|*cmecloud.cn*|*://mirrors.zju.edu.cn*|*://mirrors.sjtug.sjtu.edu.cn*) return 0;;
    *) return 1;;
  esac
}

is_email() {
  case "$1" in
    *@*.*) return 0;;
    *) return 1;;
  esac
}

validate_options() {
  if [ -n "$PUBLIC_URL" ]; then
    case "$PUBLIC_URL" in
      http://*|https://*) ;;
      *) die "面板地址必须以 http:// 或 https:// 开头：$PUBLIC_URL";;
    esac
  fi
  case "$TLS_MODE" in
    auto|self|files) ;;
    *) die "TLS 模式只能是 auto、self 或 files：$TLS_MODE";;
  esac
  if [ -n "$EMAIL" ]; then
    case "$EMAIL" in
      *@*.*) ;;
      *) die "邮箱格式不正确：$EMAIL";;
    esac
  fi
  if [ -n "$DNS_LISTEN" ]; then
    case "$DNS_LISTEN" in
      *:*) ;;
      *) die "DNS 监听地址需为 host:port：$DNS_LISTEN";;
    esac
  fi
  if [ "$DNS_ENABLE" -eq 1 ]; then
    [ -n "$DNS_UPSTREAM" ] || die "DNS 上游不能为空"
    case "$DNS_UPSTREAM" in
      *:*) ;;
      *) die "DNS 上游需为 host:port：$DNS_UPSTREAM";;
    esac
  fi
  if [ -n "$CN_GATEWAY" ] && ! is_ipv4 "$CN_GATEWAY"; then
    die "网关地址不是 IPv4：$CN_GATEWAY"
  fi
  case "$CN_SPLIT_MODE" in
    auto|none) ;;
    hosts|dns|both)
      warn "分流方式 $CN_SPLIT_MODE 已弃用，按 auto 处理（本地 DNS + hosts 托管块）"
      CN_SPLIT_MODE="auto";;
    *) die "分流方式只能是 auto 或 none：$CN_SPLIT_MODE";;
  esac
  if [ "$CN_LOCAL_DNS" -eq 1 ] && ! is_ipv4 "$CN_LOCAL_DNS_ADDR"; then
    die "本地 DNS 监听地址不是 IPv4：$CN_LOCAL_DNS_ADDR"
  fi
  case "$CN_WATCH_INTERVAL" in
    ''|*[!0-9]*) die "看门狗周期必须是整数秒：$CN_WATCH_INTERVAL";;
  esac
  if [ "$CN_WATCH_INTERVAL" -lt 15 ]; then
    die "看门狗周期不能小于 15 秒：$CN_WATCH_INTERVAL"
  fi
  case "$CN_AGENT_UPSTREAM" in
    *:*) ;;
    *) die "agent 上游 DNS 需为 host:port：$CN_AGENT_UPSTREAM";;
  esac
  if [ -n "$CN_AGENT_ENROLL_TOKEN" ]; then
    case "$CN_AGENT_ENROLL_TOKEN" in
      ""|*[!0-9a-fA-F]*) warn "enroll token 不是 64 位十六进制，面板签发的令牌应为该格式";;
    esac
    if [ "${#CN_AGENT_ENROLL_TOKEN}" -ne 64 ]; then
      warn "enroll token 长度不是 64：${#CN_AGENT_ENROLL_TOKEN}"
    fi
  fi
  if [ -n "$CN_AGENT_ENROLL_TOKEN" ] && [ "$CN_AGENT_NO_AGENT" -eq 0 ] && [ "$CN_LOCAL_DNS_ADDR" != "198.18.0.1" ]; then
    warn "agent 模式固定监听 198.18.0.1:53，--local-dns-addr 仅 legacy 模式有效"
  fi
  if [ "$CN_DO_HOSTS" -eq 0 ] && [ "$CN_LOCAL_DNS" -eq 1 ]; then
    warn "已用 --no-hosts 关闭 hosts 托管块，本地 DNS 将没有分流域名数据，实际不会生效"
  fi
  if [ -n "$CN_APT_MIRROR" ]; then
    case "$CN_APT_MIRROR" in
      http://*|https://*) ;;
      *) die "APT 镜像地址必须以 http:// 或 https:// 开头：$CN_APT_MIRROR";;
    esac
  fi
  local mirror
  for mirror in "$CN_DOCKER_MIRROR" "$CN_GIT_MIRROR"; do
    [ -n "$mirror" ] || continue
    case "$mirror" in
      http://*|https://*) ;;
      *) die "镜像地址必须以 http:// 或 https:// 开头：$mirror";;
    esac
  done
}

resolve_config_args() {
  local host ip default_tls default_entry default_acme default_https
  ip="$(detect_ip)"
  if [ -z "$DOMAIN" ] && [ -n "$PUBLIC_URL" ]; then
    host="$(printf '%s' "$PUBLIC_URL" | sed 's#^[a-zA-Z][a-zA-Z0-9+.-]*://##')"
    host="$(printf '%s' "$host" | cut -d/ -f1)"
    host="${host%%:*}"
    DOMAIN="$host"
  fi
  if [ -z "$DOMAIN" ] && is_tty && [ "$NON_INTERACTIVE" -eq 0 ]; then
    title "面板地址"
    say "域名可直接申请 Let's Encrypt 证书；留空使用服务器 IP 和自签名证书。"
    DOMAIN="$(ask "面板域名" "")"
  fi
  if [ -n "$DOMAIN" ]; then
    case "$DOMAIN" in
      *://*) die "域名不要包含协议：$DOMAIN";;
      */*) die "域名不要包含路径：$DOMAIN";;
    esac
    if is_ipv4 "$DOMAIN"; then
      default_tls="self"; default_entry=""; default_acme=""; default_https="0.0.0.0:443"
    else
      default_tls="auto"; default_entry="0.0.0.0:443"; default_acme="0.0.0.0:80"; default_https="127.0.0.1:8443"
    fi
    [ -n "$PUBLIC_URL" ] || PUBLIC_URL="https://$DOMAIN$PANEL_BASE_PATH"
  else
    [ -n "$ip" ] || ip="$(ask "服务器公网 IP" "")"
    [ -n "$ip" ] || die "无法确定公网 IP，请指定 --domain 或 --public-url"
    DOMAIN="$ip"
    default_tls="self"; default_entry=""; default_acme=""; default_https="0.0.0.0:443"
    [ -n "$PUBLIC_URL" ] || PUBLIC_URL="https://$ip$PANEL_BASE_PATH"
  fi
  if [ "$EXPLICIT_TLS" -eq 0 ]; then TLS_MODE="$default_tls"; fi
  if [ "$EXPLICIT_LISTEN" -eq 0 ]; then
    ENTRY_LISTEN="$default_entry"
    ACME_LISTEN="$default_acme"
    HTTPS_LISTEN="$default_https"
  fi
  if [ "$NO_SNI" -eq 1 ]; then
    ENTRY_LISTEN=""
    [ "$EXPLICIT_LISTEN" -eq 1 ] || HTTPS_LISTEN="0.0.0.0:443"
  fi
  CONFIG_ARGS="init --config $CONFIG_FILE --data-dir $DATA_DIR --public-url $PUBLIC_URL --domain $DOMAIN --panel-path $PANEL_BASE_PATH --tls $TLS_MODE --listen-http $HTTP_LISTEN --listen-https $HTTPS_LISTEN --listen-entry=$ENTRY_LISTEN --listen-acme=$ACME_LISTEN"
  if [ "$TLS_MODE" = "files" ]; then
    [ -n "$CERT_FILE" ] && [ -n "$KEY_FILE" ] || die "--tls files 需要同时指定 --cert-file 与 --key-file"
    CONFIG_ARGS="$CONFIG_ARGS --cert-file $CERT_FILE --key-file $KEY_FILE"
  fi
  if [ -n "$EMAIL" ]; then CONFIG_ARGS="$CONFIG_ARGS --email $EMAIL"; fi
  if [ -n "$ACCESS_KEY" ]; then CONFIG_ARGS="$CONFIG_ARGS --access-key $ACCESS_KEY"; fi
  if [ "$NO_DOCKER" -eq 1 ]; then CONFIG_ARGS="$CONFIG_ARGS --no-docker"; fi
  if [ "$NO_GITHUB" -eq 1 ]; then CONFIG_ARGS="$CONFIG_ARGS --no-github"; fi
  if [ "$DNS_ENABLE" -eq 1 ]; then
    local dns_gw dns_domains
    dns_gw="$DNS_GATEWAY"
    [ -n "$dns_gw" ] || dns_gw="$GATEWAY_IP"
    [ -n "$dns_gw" ] || dns_gw="$ip"
    dns_domains="$DNS_DOMAINS"
    [ -n "$dns_domains" ] || dns_domains="$CN_DEFAULT_DOMAINS"
    CONFIG_ARGS="$CONFIG_ARGS --dns-listen $DNS_LISTEN --dns-upstream $DNS_UPSTREAM"
    [ -n "$dns_gw" ] && CONFIG_ARGS="$CONFIG_ARGS --dns-gateway $dns_gw"
    [ -n "$dns_domains" ] && CONFIG_ARGS="$CONFIG_ARGS --dns-domain $dns_domains"
  fi
}

config_exists() { [ -f "$CONFIG_FILE" ]; }

config_public_url() {
  [ -f "$CONFIG_FILE" ] || return 1
  sed -n 's/.*"public_url": "\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -n 1
}

installed_docker() { have docker && docker inspect "$SERVICE" >/dev/null 2>&1; }

detect_installed_mode() {
  if installed_docker && [ ! -x "$BIN_PATH" ]; then MODE="docker"; fi
}

run_bin() {
  if [ -x "$BIN_PATH" ]; then
    "$BIN_PATH" "$@"
  elif installed_docker; then
    docker exec "$SERVICE" /usr/local/bin/warden-gateway "$@"
  else
    die "未安装"
  fi
}

parse_access_key() {
  printf '%s\n' "$1" | sed -n 's/^access_key=//p'
}

init_config() {
  local out
  if config_exists; then
    step "配置" "保留现有配置 $CONFIG_FILE"
    return 0
  fi
  step "配置" "生成 $CONFIG_FILE"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + %s %s\n' "$BIN_PATH" "$CONFIG_ARGS"
    return 0
  fi
  out="$("$BIN_PATH" $CONFIG_ARGS)" || die "生成配置失败"
  NEW_ACCESS_KEY="$(parse_access_key "$out")"
}

init_docker_config() {
  local out
  if config_exists; then
    step "配置" "保留现有配置 $CONFIG_FILE"
    return 0
  fi
  step "配置" "在容器内生成 $CONFIG_FILE"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + docker run --rm ... %s %s\n' "$IMAGE" "$CONFIG_ARGS"
    return 0
  fi
  out="$(docker run --rm -v "$CONFIG_DIR:$CONFIG_DIR" -v "$DATA_DIR:$DATA_DIR" "$IMAGE" $CONFIG_ARGS)" || die "生成配置失败"
  NEW_ACCESS_KEY="$(parse_access_key "$out")"
}

repo_raw_urls() {
  local path="$1"
  if [ -n "$GITHUB_PROXY" ]; then
    printf '%s/https://raw.githubusercontent.com/%s/main/%s\n' "${GITHUB_PROXY%/}" "$REPO" "$path"
  fi
  printf 'https://raw.githubusercontent.com/%s/main/%s\n' "$REPO" "$path"
  printf 'https://gh-proxy.com/https://raw.githubusercontent.com/%s/main/%s\n' "$REPO" "$path"
  printf 'https://ghfast.top/https://raw.githubusercontent.com/%s/main/%s\n' "$REPO" "$path"
  printf 'https://cdn.jsdelivr.net/gh/%s@main/%s\n' "$REPO" "$path"
}

fetch_repo_file() {
  local path="$1" dest="$2" url tmp
  tmp="$dest.part.$$"
  while IFS= read -r url; do
    [ -z "$url" ] && continue
    if curl -fL --connect-timeout 10 --max-time 60 --retry 1 -o "$tmp" "$url" 2>/dev/null; then
      if [ -s "$tmp" ] && head -c 2 "$tmp" | grep -q '#!'; then
        mv -f "$tmp" "$dest"
        return 0
      fi
    fi
    rm -f "$tmp"
  done <<< "$(repo_raw_urls "$path")"
  return 1
}

# fetch_repo_raw 下载仓库内的任意文本文件（不校验 shebang，用于 VERSION）。
fetch_repo_raw() {
  local path="$1" dest="$2" url tmp
  tmp="$dest.part.$$"
  while IFS= read -r url; do
    [ -z "$url" ] && continue
    if curl -fL --connect-timeout 10 --max-time 60 --retry 1 -o "$tmp" "$url" 2>/dev/null; then
      if [ -s "$tmp" ]; then
        mv -f "$tmp" "$dest"
        return 0
      fi
    fi
    rm -f "$tmp"
  done <<< "$(repo_raw_urls "$path")"
  return 1
}

install_scripts() {
  step "脚本" "安装到 $SCRIPT_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + install -m 0755 bootstrap.sh toolbox.sh %s\n' "$SCRIPT_DIR"
    return 0
  fi
  local self dir dest_dir
  self="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
  dir="$(dirname "$self")"
  mkdir -p "$SCRIPT_DIR"
  chmod 0755 "$SCRIPT_DIR"
  dest_dir="$(cd "$SCRIPT_DIR" && pwd -P)"
  # 从已安装位置执行（如 warden update）时源与目标是同一文件，install 会报
  # “are the same file” 并以非零退出；同目录时跳过复制。
  if [ -f "$self" ] && [ "$self" != "$dest_dir/bootstrap.sh" ]; then
    install -m 0755 "$self" "$dest_dir/bootstrap.sh"
  fi
  if [ -f "$dir/toolbox.sh" ]; then
    if [ "$dir/toolbox.sh" != "$dest_dir/toolbox.sh" ]; then
      install -m 0755 "$dir/toolbox.sh" "$dest_dir/toolbox.sh"
    fi
  elif [ ! -f "$SCRIPT_DIR/toolbox.sh" ]; then
    # 单文件安装时本地没有 toolbox.sh，从仓库下载；失败不影响安装。
    if ! fetch_repo_file toolbox.sh "$SCRIPT_DIR/toolbox.sh"; then
      warn "未能下载 toolbox.sh，可稍后手动获取"
    fi
    [ -f "$SCRIPT_DIR/toolbox.sh" ] && chmod 0755 "$SCRIPT_DIR/toolbox.sh"
  fi
  # 1.4.0 起工具箱独立为 warden-toolbox，toolbox.sh 只是转发脚本；
  # 清理旧版本安装在本地的工具箱源码目录，避免残留过期模块。
  if [ -d "$SCRIPT_DIR/toolbox" ]; then
    rm -rf "$SCRIPT_DIR/toolbox"
  fi
  if [ -f "$dir/VERSION" ] && [ "$dir/VERSION" != "$dest_dir/VERSION" ]; then
    install -m 0644 "$dir/VERSION" "$dest_dir/VERSION"
  fi
  return 0
}

# script_self_path 返回当前脚本的绝对路径。脚本被 source 时 $0 是调用者，
# 优先使用 BASH_SOURCE。
script_self_path() {
  local src dir
  src="${BASH_SOURCE[0]:-$0}"
  dir="$(cd "$(dirname "$src")" 2>/dev/null && pwd -P)" || return 1
  printf '%s/%s\n' "$dir" "$(basename "$src")"
}

# script_public_url 返回网关托管的安装脚本地址；无法确定时输出空。
script_public_url() {
  local origin
  origin="$(printf '%s' "${PUBLIC_URL:-}" | sed -n 's#^\([a-zA-Z][a-zA-Z0-9+.-]*://[^/]*\).*#\1#p')"
  [ -n "$origin" ] || return 0
  printf '%s/bootstrap.sh\n' "$origin"
}

# install_cn_cli 把当前脚本保存为 warden-cn，供国内机恢复设置时直接调用，
# 不再依赖当前目录是否存在 bootstrap.sh。
install_cn_cli() {
  local self dest_dir
  self="$(script_self_path)" || return 1
  [ -n "$self" ] && [ -f "$self" ] || return 1
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + install %s %s\n' "$self" "$CN_CLI_PATH"; return 0; fi
  mkdir -p "$(dirname "$CN_CLI_PATH")" || return 1
  dest_dir="$(cd "$(dirname "$CN_CLI_PATH")" 2>/dev/null && pwd -P)" || dest_dir=""
  if [ -n "$dest_dir" ] && [ "$self" = "$dest_dir/$(basename "$CN_CLI_PATH")" ]; then return 0; fi
  install -m 0755 "$self" "$CN_CLI_PATH" || return 1
  ok "已注册命令 $CN_CLI_PATH"
}

install_cli() {
  step "命令" "注册维护命令 $CLI_PATH"
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + write %s\n' "$CLI_PATH"; return 0; fi
  mkdir -p "$(dirname "$CLI_PATH")"
  cat > "$CLI_PATH" <<CLIEOF
#!/usr/bin/env bash
set -euo pipefail
BOOTSTRAP="$SCRIPT_DIR/bootstrap.sh"
case "\${1:-}" in
  cn-setup|cn-check|cn-status|cn-restore)
    printf '该命令用于国内机，请在国内机上执行面板设置页生成的命令。\n' >&2
    printf '国内机已执行过 cn-setup 时，可直接运行：sudo warden-cn %s\n' "\$1" >&2
    exit 2;;
esac
if [ ! -f "\$BOOTSTRAP" ]; then
  printf '未找到 %s，请重新安装 warden-gateway。\n' "\$BOOTSTRAP" >&2
  exit 1
fi
exec bash "\$BOOTSTRAP" "\$@"
CLIEOF
  chmod 0755 "$CLI_PATH"
}

# prepare_update_scripts 从仓库下载最新 bootstrap.sh/toolbox.sh/VERSION 到临时目录。
# 直接执行已安装脚本时，源目录与安装目录相同，install 无法覆盖自身；改为在新脚本
# 中带 NW_UPDATE_REEXEC=1 重新执行，由新脚本完成脚本与版本文件的更新。
prepare_update_scripts() {
  local self dir path
  self="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
  [ "$self" = "$SCRIPT_DIR/bootstrap.sh" ] || return 1
  find "${TMPDIR:-/tmp}" -maxdepth 1 -type d -name 'warden-update.*' -mmin +180 -exec rm -rf {} + 2>/dev/null || true
  dir="$(mktemp -d "${TMPDIR:-/tmp}/warden-update.XXXXXX")" || return 1
  for path in bootstrap.sh toolbox.sh; do
    if ! fetch_repo_raw "$path" "$dir/$path"; then
      warn "未能获取最新 $path，继续使用当前脚本"
      rm -rf "$dir"
      return 1
    fi
    chmod 0755 "$dir/$path"
  done
  fetch_repo_raw VERSION "$dir/VERSION" >/dev/null 2>&1 || true
  UPDATE_SCRIPT_DIR="$dir"
  return 0
}

# write_version_from_binary 用实际安装的二进制版本刷新脚本版本文件。
write_version_from_binary() {
  [ "$DRY_RUN" -eq 0 ] || return 0
  [ -x "$BIN_PATH" ] || return 0
  local out v
  out="$("$BIN_PATH" version 2>/dev/null | head -n 1)"
  v="$(printf '%s' "$out" | sed -e 's/^v//' -e 's/ .*//')"
  case "$v" in ""|*[!0-9.]*) return 0;; esac
  if [ -f "$SCRIPT_DIR/VERSION" ] && [ "$(cat "$SCRIPT_DIR/VERSION")" = "$v" ]; then
    return 0
  fi
  printf '%s\n' "$v" > "$SCRIPT_DIR/VERSION"
}

install_unit() {
  step "服务" "写入 systemd 单元"
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + write %s\n' "$UNIT_PATH"; return 0; fi
  cat > "$UNIT_PATH" <<EOF
[Unit]
Description=Warden Gateway
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_PATH serve --config $CONFIG_FILE
Restart=always
RestartSec=3
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=false
ReadWritePaths=$CONFIG_DIR $DATA_DIR

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$UNIT_PATH"
}

install_logrotate() {
  step "日志" "写入轮转配置"
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + write %s\n' "$LOGROTATE_PATH"; return 0; fi
  cat > "$LOGROTATE_PATH" <<EOF
$DATA_DIR/gateway.log $DATA_DIR/audit.log {
    daily
    size 5M
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0600 root root
}
EOF
  chmod 0644 "$LOGROTATE_PATH"
}

pull_docker_image() {
  have docker || die "未安装 Docker"
  IMAGE="ghcr.io/$REPO:$VERSION"
  [ "$VERSION" = "latest" ] && IMAGE="ghcr.io/$REPO:latest"
  step "拉取" "$IMAGE"
  run docker pull "$IMAGE"
}

run_docker_container() {
  step "服务" "启动 Docker 容器 $SERVICE"
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + docker run -d --name %s --network host ...\n' "$SERVICE"; return 0; fi
  docker rm -f "$SERVICE" >/dev/null 2>&1 || true
  docker run -d --name "$SERVICE" --network host --restart unless-stopped \
    -v "$CONFIG_DIR:$CONFIG_DIR" -v "$DATA_DIR:$DATA_DIR" -v "$SCRIPT_DIR:$SCRIPT_DIR:ro" \
    "$IMAGE" serve --config "$CONFIG_FILE" >/dev/null
}

install_service() {
  step "服务" "启动 $SERVICE"
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + systemctl daemon-reload; systemctl enable --now %s\n' "$SERVICE"; return 0; fi
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null 2>&1 || true
  systemctl restart "$SERVICE"
  sleep 2
  systemctl is-active --quiet "$SERVICE" || { journalctl -u "$SERVICE" -n 40 --no-pager || true; die "服务启动失败"; }
}

wait_health() {
  local url="$1" allow_local="${2:-0}" host i local_url
  host="$(printf '%s' "$url" | sed 's#^[a-zA-Z][a-zA-Z0-9+.-]*://##' | cut -d/ -f1 | cut -d: -f1)"
  local_url="http://127.0.0.1:8080$PANEL_BASE_PATH/api/health"
  for i in $(seq 1 30); do
    if curl -kfsS --connect-timeout 2 --max-time 3 "$url/api/health" >/dev/null 2>&1; then return 0; fi
    case "$url" in
      https://*)
        if curl -kfsS --connect-timeout 2 --max-time 3 --resolve "$host:443:127.0.0.1" "$url/api/health" >/dev/null 2>&1; then return 0; fi;;
    esac
    if [ "$allow_local" -eq 1 ] && curl -fsS --connect-timeout 2 --max-time 3 "$local_url" >/dev/null 2>&1; then
      warn "公网地址健康检查未通过，本机接口正常"
      return 0
    fi
    sleep 1
  done
  return 1
}

ensure_certificate() {
  local domain="$1" url="$2" i
  [ -z "$domain" ] && return 0
  is_ipv4 "$domain" && return 0
  [ "$domain" = "$(detect_ip)" ] && return 0
  step "证书" "通过 ACME 申请 $domain"
  for i in $(seq 1 30); do
    if curl -fsS --connect-timeout 3 --max-time 8 --resolve "$domain:443:127.0.0.1" "$url/api/health" >/dev/null 2>&1; then
      ok "证书已生效"
      return 0
    fi
    curl -kfsS --connect-timeout 3 --max-time 8 --resolve "$domain:443:127.0.0.1" "$url/api/health" >/dev/null 2>&1 || true
    sleep 2
  done
  warn "证书暂未生效，请检查域名解析和 80/443 端口：journalctl -u $SERVICE -n 50"
  return 1
}

# ---------------------------------------------------------------------------
# 国内机设置（cn-setup / cn-restore）
# ---------------------------------------------------------------------------

cn_backup_root() { printf '%s\n' "${CN_BACKUP_ROOT:-/var/backups/warden-gateway-cn}"; }
cn_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
cn_stamp() { date -u +%Y%m%dT%H%M%SZ; }
cn_path_hash() { printf '%s' "$1" | sha256sum | cut -c1-16; }

cn_backup_count() {
  local root i=0 dir
  root="$(cn_backup_root)"
  [ -d "$root" ] || { printf '0\n'; return 0; }
  for dir in "$root"/cn-*; do
    [ -d "$dir" ] || continue
    [ -f "$dir/manifest.json" ] && i=$((i + 1))
  done
  printf '%s\n' "$i"
}

cn_latest_backup() {
  local root dir latest=""
  root="$(cn_backup_root)"
  [ -d "$root" ] || return 0
  for dir in $(ls -1d "$root"/cn-* 2>/dev/null | sort); do
    [ -f "$dir/manifest.json" ] || continue
    latest="$dir"
  done
  [ -n "$latest" ] && basename "$latest"
}

cn_begin_backup() {
  local root base
  root="$(cn_backup_root)"
  base="cn-$(cn_stamp)"
  CN_BACKUP_DIR="$root/$base"
  if [ -e "$CN_BACKUP_DIR" ]; then
    CN_BACKUP_DIR="$root/$base-$$"
  fi
  CN_RECORDED=""
  CN_CHANGED=1
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + mkdir -p %s/files\n' "$CN_BACKUP_DIR"
    return 0
  fi
  mkdir -p "$CN_BACKUP_DIR/files"
  chmod 0700 "$CN_BACKUP_DIR" "$CN_BACKUP_DIR/files"
  : > "$CN_BACKUP_DIR/records.tsv"
  chmod 0600 "$CN_BACKUP_DIR/records.tsv"
}

cn_snapshot_file() {
  local path="$1" exists=0 size=0 hash stored
  [ "$CN_CHANGED" -eq 1 ] || return 0
  case " $CN_RECORDED " in
    *" $path "*) return 0;;
  esac
  CN_RECORDED="$CN_RECORDED $path"
  hash="$(cn_path_hash "$path")"
  if [ -e "$path" ]; then
    exists=1
    size="$(wc -c < "$path" | tr -d ' ')"
    stored="$CN_BACKUP_DIR/files/$hash"
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  + cp %s %s\n' "$path" "$stored"
    else
      cp -p "$path" "$stored"
    fi
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
    printf '%s\t%s\t%s\n' "$path" "$exists" "$size" >> "$CN_BACKUP_DIR/records.tsv"
  fi
}

cn_finish_backup() {
  local first=1 path exists size flag
  [ "$CN_CHANGED" -eq 1 ] || return 0
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + write %s/manifest.json\n' "$CN_BACKUP_DIR"
    return 0
  fi
  printf '{\n  "id": "%s",\n  "created": "%s",\n  "created_ts": %s,\n  "files": [' \
    "$(basename "$CN_BACKUP_DIR")" "$(cn_now)" "$(date -u +%s)" > "$CN_BACKUP_DIR/manifest.json"
  while IFS="$(printf '\t')" read -r path exists size; do
    [ -n "$path" ] || continue
    if [ "$exists" = "1" ]; then flag="true"; else flag="false"; fi
    if [ "$first" -eq 1 ]; then first=0; else printf ',' >> "$CN_BACKUP_DIR/manifest.json"; fi
    printf '\n    {"path": "%s", "existed": %s, "size": %s}' "$path" "$flag" "$size" >> "$CN_BACKUP_DIR/manifest.json"
  done < "$CN_BACKUP_DIR/records.tsv"
  printf '\n  ]\n}\n' >> "$CN_BACKUP_DIR/manifest.json"
  chmod 0600 "$CN_BACKUP_DIR/manifest.json"
  rm -f "$CN_BACKUP_DIR/records.tsv"
  ok "已备份到 $CN_BACKUP_DIR"
}

cn_prune_backups() {
  local root dir keep=10 count=0
  root="$(cn_backup_root)"
  [ -d "$root" ] || return 0
  for dir in $(ls -1d "$root"/cn-* 2>/dev/null | sort -r); do
    count=$((count + 1))
    if [ "$count" -gt "$keep" ]; then
      if [ "$DRY_RUN" -eq 1 ]; then printf '  + rm -rf %s\n' "$dir"; else rm -rf "$dir"; fi
    fi
  done
}

cn_strip_block() {
  awk -v b="$2" -v e="$3" '
    $0 == b {skip = 1; next}
    $0 == e {skip = 0; next}
    !skip {print}
  ' "$1"
}

cn_write_managed_block() {
  local path="$1" body="$2" tmp
  tmp="$(mktemp)"
  if [ -f "$path" ]; then
    cn_strip_block "$path" "$CN_HOSTS_BEGIN" "$CN_HOSTS_END" > "$tmp"
  fi
  printf '%s\n%s\n%s\n' "$CN_HOSTS_BEGIN" "$body" "$CN_HOSTS_END" >> "$tmp"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + write %s\n' "$path"
    rm -f "$tmp"
    return 0
  fi
  cat "$tmp" > "$path"
  rm -f "$tmp"
  ok "已更新 $path"
}

cn_prepend_managed_block() {
  local path="$1" body="$2" tmp
  tmp="$(mktemp)"
  printf '%s\n%s\n%s\n' "$CN_HOSTS_BEGIN" "$body" "$CN_HOSTS_END" > "$tmp"
  if [ -f "$path" ]; then
    cn_strip_block "$path" "$CN_HOSTS_BEGIN" "$CN_HOSTS_END" >> "$tmp"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + write %s\n' "$path"
    rm -f "$tmp"
    return 0
  fi
  cat "$tmp" > "$path"
  rm -f "$tmp"
  ok "已更新 $path"
}

# ---------------------------------------------------------------------------
# 探测（全部在国内机本机执行，不登录代理机）
# ---------------------------------------------------------------------------

cn_ping_ok() {
  local ip="$1" secs="${2:-1}"
  have ping || return 1
  ping -c 1 -W "$secs" "$ip" >/dev/null 2>&1
}

cn_tcp_open() {
  local host="$1" port="$2" secs="${3:-2}"
  if have timeout; then
    timeout "$secs" bash -c "exec 3<>/dev/tcp/$host/$port" >/dev/null 2>&1
  else
    ( exec 3<>"/dev/tcp/$host/$port" ) >/dev/null 2>&1
  fi
}

cn_http_ok() {
  have curl || return 1
  curl -fsS -o /dev/null --connect-timeout 2 --max-time 4 "$1" >/dev/null 2>&1
}

# cn_internet_ok 用三个外部对照点判断本机是否还能正常上网。
# 对照组不通时任何“代理机故障”的判定都不可信，因此必须先排除本机故障。
cn_internet_ok() {
  local url fails=0
  for url in "https://www.apple.com" "https://mirrors.aliyun.com" "https://pypi.org"; do
    cn_http_ok "$url" || fails=$((fails + 1))
  done
  [ "$fails" -lt 2 ]
}

# cn_probe_gateway 输出一行判定，并设置 CN_PROBE_RESULT：
#   OK                 对照组通，443 通或 80/healthz 通
#   LINK_443_BLOCKED   对照组通，80 通但 443 不通（线路干扰，代理机正常）
#   LINK_PORTS_BLOCKED 对照组通，ICMP 通但端口全不通（线路拦截）
#   C_DOWN             对照组通，ICMP 与 22/80/443 全不通（判定代理机不可用）
#   LOCAL_BROKEN       对照组不通（本机网络故障，不做任何切换）
# 退出码：0 OK / 1 LINK_443_BLOCKED / 2 C_DOWN / 3 LINK_PORTS_BLOCKED / 4 LOCAL_BROKEN
cn_probe_gateway() {
  local ip="$1" icmp=0 p22=0 p80=0 p443=0 health=0
  CN_PROBE_RESULT="UNKNOWN"
  if ! cn_internet_ok; then
    CN_PROBE_RESULT="LOCAL_BROKEN"
    printf 'LOCAL_BROKEN\n'
    return 4
  fi
  if cn_ping_ok "$ip" 1; then icmp=1; fi
  if cn_tcp_open "$ip" 22 2; then p22=1; fi
  if cn_tcp_open "$ip" 80 2; then p80=1; fi
  if cn_tcp_open "$ip" 443 2; then p443=1; fi
  if [ "$p80" -eq 1 ] && cn_http_ok "http://$ip/healthz"; then health=1; fi
  if [ "$health" -eq 1 ] || [ "$p443" -eq 1 ]; then
    CN_PROBE_RESULT="OK"
    printf 'OK\n'
    return 0
  fi
  if [ "$p80" -eq 1 ]; then
    CN_PROBE_RESULT="LINK_443_BLOCKED"
    printf 'LINK_443_BLOCKED\n'
    return 1
  fi
  if [ "$icmp" -eq 0 ] && [ "$p22" -eq 0 ]; then
    CN_PROBE_RESULT="C_DOWN"
    printf 'C_DOWN\n'
    return 2
  fi
  CN_PROBE_RESULT="LINK_PORTS_BLOCKED"
  printf 'LINK_PORTS_BLOCKED\n'
  return 3
}

cn_probe_label() {
  case "$1" in
    OK) printf '正常';
      ;;
    LINK_443_BLOCKED) printf '代理机正常，443 被线路拦截';;
    LINK_PORTS_BLOCKED) printf '代理机可达，端口被线路拦截';;
    C_DOWN) printf '代理机不可达';;
    LOCAL_BROKEN) printf '本机网络异常（不做切换）';;
    *) printf '%s' "$1";;
  esac
}

# cn_detect_upstreams 选出本地 dnsmasq 的上游：沿用本机现有 DNS，补 223.5.5.5 兜底。
cn_detect_upstreams() {
  local out="" ns
  if [ "$CN_LOCAL_DNS_UPSTREAM" != "" ]; then
    printf '%s\n' "$(printf '%s' "$CN_LOCAL_DNS_UPSTREAM" | tr ',' ' ' | tr -s ' ' | sed 's/^ //; s/ $//')"
    return 0
  fi
  if [ -f /etc/resolv.conf ]; then
    while IFS= read -r ns; do
      [ -n "$ns" ] || continue
      case "$ns" in 127.*|0.0.0.0) continue;; esac
      [ "$ns" = "$CN_LOCAL_DNS_ADDR" ] && continue
      is_ipv4 "$ns" || continue
      case " $out " in *" $ns "*) continue;; esac
      out="$out $ns"
    done <<EOF
$(awk '$1 == "nameserver" {print $2}' /etc/resolv.conf 2>/dev/null)
EOF
  fi
  case " $out " in
    *" 223.5.5.5 "*) ;;
    *) out="$out 223.5.5.5";;
  esac
  printf '%s\n' "$(printf '%s' "$out" | sed 's/^ //')"
}

# cn_put_file 从标准输入写入文件（原地覆写，避免 bind mount 上 rename 报 EBUSY）。
cn_put_file() {
  local path="$1" mode="${2:-0644}" tmp
  tmp="$(mktemp)"
  cat > "$tmp"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + write %s\n' "$path"
    rm -f "$tmp"
    return 0
  fi
  mkdir -p "$(dirname "$path")"
  cat "$tmp" > "$path"
  chmod "$mode" "$path"
  rm -f "$tmp"
}

# cn_apply_hosts 把托管块写到 /etc/hosts 最前面。
# 前置的原因：机器上可能还有第三方 hosts 块（例如 GitHub520），
# 它们常用 sed 截断再追加，块放末尾会被挤掉；放最前则首个匹配生效。
cn_apply_hosts() {
  local ip="$1" domains="$2" list got
  domains="${domains//,/ }"
  list="$(printf '%s' "$domains" | tr -s ' ' | sed 's/^ //; s/ $//')"
  [ -n "$list" ] || { warn "分流域名为空，跳过 hosts"; return 1; }
  cn_snapshot_file /etc/hosts
  cn_prepend_managed_block /etc/hosts "$ip $list"
  if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
  got="$(getent ahostsv4 github.com 2>/dev/null | awk 'NR == 1 {print $1}')"
  if [ "$got" != "$ip" ]; then
    warn "hosts 托管块未生效：github.com 解析到 ${got:-空}，请检查 /etc/nsswitch.conf 的 hosts 顺序"
    return 1
  fi
  return 0
}

# cn_remove_hosts 删掉托管块（看门狗降级时使用）。
cn_remove_hosts() {
  cn_snapshot_file /etc/hosts
  cn_strip_block /etc/hosts "$CN_HOSTS_BEGIN" "$CN_HOSTS_END" > /tmp/.wg-hosts.$$
  if [ "$DRY_RUN" -eq 1 ]; then
    rm -f /tmp/.wg-hosts.$$
    return 0
  fi
  cat /tmp/.wg-hosts.$$ > /etc/hosts
  rm -f /tmp/.wg-hosts.$$
}

# cn_install_dnsmasq 保证 dnsmasq 可用；缺失时用当前 APT 源安装 dnsmasq-base。
cn_install_dnsmasq() {
  if [ -x /usr/sbin/dnsmasq ] || have dnsmasq; then return 0; fi
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + apt-get install -y dnsmasq-base\n'
    return 0
  fi
  step "本地 DNS" "安装 dnsmasq-base"
  if apt-get install -y dnsmasq-base >/dev/null 2>&1; then return 0; fi
  warn "安装 dnsmasq-base 失败：容器内将无法解析分流域名"
  return 1
}

# cn_apply_local_dns 装本地 dnsmasq（监听非回环地址），写 unit 并启动。
# 必须用非回环地址：Docker 会把回环地址从容器 resolv.conf 里剔除。
# 数据源是 /etc/hosts（dnsmasq 默认读 hosts），命中分流域名即返回网关地址。
cn_apply_local_dns() {
  local ip="$1" upstreams="$2" u
  [ "$CN_LOCAL_DNS" -eq 1 ] || return 0
  cn_install_dnsmasq || return 1
  cn_snapshot_file "$CN_DNSMASQ_CONF"
  cn_snapshot_file "$CN_DNS_UNIT"
  {
    printf 'port=53\n'
    printf 'listen-address=%s\n' "$ip"
    printf 'bind-interfaces\n'
    printf 'no-resolv\n'
    printf 'no-poll\n'
    printf 'no-dhcp-interface=\n'
    printf 'cache-size=1000\n'
    for u in $upstreams; do
      printf 'server=%s\n' "$u"
    done
  } | cn_put_file "$CN_DNSMASQ_CONF" 0644
  cn_put_file "$CN_DNS_UNIT" 0644 <<UNIT
[Unit]
Description=Warden Gateway CN local DNS (dnsmasq)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=/bin/sh -c 'ip link show wg-dns0 >/dev/null 2>&1 || ip link add wg-dns0 type dummy; ip addr replace ${ip}/32 dev wg-dns0; ip link set wg-dns0 up'
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=$CN_DNSMASQ_CONF
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT
  if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
  if ! have systemctl; then
    warn "本机没有 systemd，无法托管本地 DNS"
    return 1
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if systemctl enable "$CN_DNS_UNIT_NAME" >/dev/null 2>&1 \
    && systemctl restart "$CN_DNS_UNIT_NAME" >/dev/null 2>&1; then
    sleep 1
    if ss -lunH 'sport = :53' 2>/dev/null | grep -q "$ip"; then
      ok "本地 DNS 已监听 $ip:53（数据源 /etc/hosts，上游：$upstreams）"
      return 0
    fi
    warn "本地 DNS 已启动，但 $ip:53 未监听：journalctl -u $CN_DNS_UNIT_NAME -n 30"
    return 1
  fi
  warn "本地 DNS 服务启动失败：journalctl -u $CN_DNS_UNIT_NAME -n 30"
  return 1
}

# cn_apply_resolver 让 resolv.conf 指向本地 dnsmasq，并保留 ISP 上游兜底。
cn_apply_resolver() {
  local addr="$1" upstreams="$2" u body target="/etc/resolv.conf"
  if [ -L "$target" ]; then
    target="$(readlink -f "$target" 2>/dev/null || printf '%s' "$target")"
  fi
  cn_snapshot_file /etc/resolv.conf
  cn_snapshot_file "$target"
  body=""
  if [ "$CN_LOCAL_DNS" -eq 1 ]; then body="nameserver $addr"; fi
  for u in $upstreams; do
    if [ -n "$body" ]; then body="$body"$'\n'"nameserver $u"; else body="nameserver $u"; fi
  done
  body="$body"$'\n'"options timeout:1 attempts:1"
  cn_prepend_managed_block "$target" "$body"
}

# cn_apply_nm_guard 阻止 NetworkManager 重写 /etc/resolv.conf。
cn_apply_nm_guard() {
  [ "$CN_NM_GUARD" -eq 1 ] || return 0
  if [ ! -d /etc/NetworkManager ]; then return 0; fi
  cn_snapshot_file "$CN_NM_CONF"
  cn_put_file "$CN_NM_CONF" 0644 <<'NMEOF'
[main]
dns=none
NMEOF
  if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
  if have systemctl && systemctl is-active NetworkManager >/dev/null 2>&1; then
    systemctl reload NetworkManager >/dev/null 2>&1 || warn "重载 NetworkManager 失败，resolv.conf 可能被改写"
  fi
  return 0
}

cn_apply_apt() {
  local mirror="$1" file changed=0
  for file in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [ -f "$file" ] || continue
    grep -Eq 'https?://[A-Za-z0-9._:-]+' "$file" || continue
    # 只替换发行版仓库（URL 路径以 /ubuntu 开头）。Docker CE、pypi 等第三方仓库的
    # 主机名与路径是一一对应的（download.docker.com/linux/ubuntu），换主机就会 404，
    # 因此原样保留并提示。
    if ! grep -Eq 'https?://[A-Za-z0-9._:-]+/ubuntu([/[:space:]]|$)' "$file"; then
      info "跳过 $file：不是发行版仓库，保持原样"
      continue
    fi
    changed=1
    cn_snapshot_file "$file"
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  + 替换 %s 的源地址为 %s\n' "$file" "$mirror"
    else
      sed -E -i "s#https?://[A-Za-z0-9._:-]+(/ubuntu([/[:space:]]|$))#${mirror}\1#g" "$file"
    fi
  done
  [ "$changed" -eq 1 ] || { warn "未找到可替换的发行版仓库地址"; return 1; }
  ok "APT 发行版源已替换为 $mirror"
}

cn_apply_bbr() {
  cn_snapshot_file /etc/sysctl.d/99-warden-cn-bbr.conf
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + write /etc/sysctl.d/99-warden-cn-bbr.conf 并执行 sysctl --system\n'
    return 0
  fi
  modprobe tcp_bbr 2>/dev/null || true
  printf '# warden-gateway cn\nnet.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n' > /etc/sysctl.d/99-warden-cn-bbr.conf
  chmod 0644 /etc/sysctl.d/99-warden-cn-bbr.conf
  sysctl --system >/dev/null 2>&1 || { warn "应用内核参数失败"; return 1; }
  local cc
  cc="$(cat /proc/sys/net/ipv4/tcp_congestion_control 2>/dev/null || true)"
  if [ "$cc" != "bbr" ]; then
    warn "内核未启用 bbr，当前为 $cc"
    return 1
  fi
  ok "已启用 bbr / fq"
}

cn_json_set_mirror() {
  local path="$1" mirror="$2"
  python3 - "$path" "$mirror" <<'PY'
import json
import os
import sys

path = sys.argv[1]
mirror = sys.argv[2]
doc = {}
if os.path.exists(path):
    with open(path, "r", encoding="utf-8") as handle:
        text = handle.read().strip()
    if text:
        doc = json.loads(text)
doc["registry-mirrors"] = [mirror]
directory = os.path.dirname(path)
if directory:
    os.makedirs(directory, exist_ok=True)
with open(path, "w", encoding="utf-8") as handle:
    json.dump(doc, handle, indent=2)
    handle.write("\n")
os.chmod(path, 0o600)
PY
}

cn_apply_docker_mirror() {
  local mirror="$1"
  if ! have docker && [ ! -S /var/run/docker.sock ]; then
    say "未安装 Docker，跳过镜像设置"
    return 0
  fi
  if ! have python3; then
    warn "缺少 python3，跳过 Docker 镜像设置"
    return 0
  fi
  cn_snapshot_file /etc/docker/daemon.json
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + 写入 /etc/docker/daemon.json registry-mirrors=%s\n' "$mirror"
    return 0
  fi
  mkdir -p /etc/docker
  cn_json_set_mirror /etc/docker/daemon.json "$mirror" || { warn "写入 Docker 配置失败"; return 1; }
  if have systemctl && systemctl list-unit-files docker.service >/dev/null 2>&1; then
    systemctl reload docker >/dev/null 2>&1 || warn "docker 未重载，稍后手动执行 systemctl reload docker"
  fi
  ok "Docker 镜像已设置为 $mirror"
}

cn_apply_git_mirror() {
  local mirror="$1" base prefix old_conf existing
  if ! have git; then
    say "未安装 git，跳过 Git 镜像设置"
    return 0
  fi
  old_conf="${NW_GIT_CONFIG_FILE:-/etc/gitconfig}"
  cn_snapshot_file "$old_conf"
  base="$mirror/https://github.com/"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + git config --system url.%s.insteadOf 写入 5 条前缀\n' "$base"
    return 0
  fi
  if [ -f "$old_conf" ]; then
    while IFS= read -r existing; do
      [ -n "$existing" ] || continue
      git config --system --remove-section "url.$existing" >/dev/null 2>&1 || true
    done <<EOF
$(grep -oE 'url "[^"]*/https://github.com/"' "$old_conf" 2>/dev/null | sed -E 's#^url "##; s#"$##' | sort -u)
EOF
  fi
  git config --system --replace-all "url.$base.insteadOf" "https://github.com/" || return 1
  for prefix in "http://github.com/" "git://github.com/" "git@github.com:" "ssh://git@github.com/"; do
    git config --system --add "url.$base.insteadOf" "$prefix" || return 1
  done
  ok "Git 镜像已设置为 $mirror"
}

cn_mode_label() {
  case "$1" in
    auto) printf '自动（hosts + 本地 DNS）';;
    none) printf '不启用分流';;
    *) printf '%s' "$1";;
  esac
}

# cn_split_plan 把 --cn-mode 与各开关折算成三个动作位。
cn_split_plan() {
  CN_PLAN_HOSTS=0
  CN_PLAN_LOCAL_DNS=0
  CN_PLAN_RESOLVER=0
  if [ "$CN_SPLIT_MODE" = "none" ]; then return 0; fi
  if [ "$CN_DO_HOSTS" -eq 1 ]; then CN_PLAN_HOSTS=1; fi
  if [ "$CN_LOCAL_DNS" -eq 1 ]; then
    CN_PLAN_LOCAL_DNS=1
    CN_PLAN_RESOLVER=1
  fi
  return 0
}

cn_option_summary() {
  local hosts_state dns_state apt_state bbr_state docker_state git_state watch_state
  cn_split_plan
  if [ "$CN_PLAN_HOSTS" -eq 1 ]; then hosts_state="启用（前置托管块）"; else hosts_state="跳过"; fi
  if [ "$CN_PLAN_LOCAL_DNS" -eq 1 ]; then
    dns_state="启用（本地 dnsmasq $CN_LOCAL_DNS_ADDR:53）"
  else
    dns_state="跳过"
  fi
  if [ "$CN_DO_APT" -eq 1 ]; then apt_state="$CN_APT_MIRROR"; else apt_state="跳过"; fi
  if [ "$CN_DO_BBR" -eq 1 ]; then bbr_state="启用"; else bbr_state="跳过"; fi
  if [ "$CN_DO_DOCKER" -eq 1 ]; then docker_state="${CN_DOCKER_MIRROR:-$CN_GATEWAY}"; else docker_state="跳过"; fi
  if [ "$CN_DO_GIT" -eq 1 ]; then git_state="${CN_GIT_MIRROR:-$CN_GATEWAY}"; else git_state="跳过"; fi
  if [ "$CN_WATCHDOG" -eq 1 ]; then watch_state="启用（每 ${CN_WATCH_INTERVAL}s 探测一次）"; else watch_state="跳过"; fi
  kv "网关地址" "$CN_GATEWAY"
  kv "分流方式" "$(cn_mode_label "$CN_SPLIT_MODE")"
  kv "hosts 分流" "$hosts_state"
  kv "本地 DNS" "$dns_state"
  kv "看门狗" "$watch_state"
  kv "APT 镜像" "$apt_state"
  kv "BBR" "$bbr_state"
  kv "Docker 镜像" "$docker_state"
  kv "Git 镜像" "$git_state"
}

interactive_cn_options() {
  [ "$INTERACTIVE_SETUP" -eq 1 ] || return 0
  [ "$CN_OPTIONS_CONFIGURED" -eq 0 ] || return 0
  if [ "$NON_INTERACTIVE" -eq 1 ] || ! is_tty; then
    return 0
  fi
  title "国内机设置"
  say "以下设置会修改本机的 hosts、本地 DNS、APT、Docker、Git 配置。"
  CN_GATEWAY="${CN_GATEWAY:-${GATEWAY_IP:-$(detect_ip)}}"
  CN_GATEWAY="$(ask "网关地址（代理机 IP，即 C 的地址）" "$CN_GATEWAY")"
  CN_DOMAINS="${CN_DOMAINS:-$CN_DEFAULT_DOMAINS}"
  CN_DOMAINS="$(ask "参与分流的域名" "$CN_DOMAINS")"
  local mode_answer
  say "分流方式：1 自动（hosts 前置块 + 本地 DNS，推荐）；2 不启用分流"
  mode_answer="$(ask "分流方式" "auto")"
  case "$mode_answer" in
    1|auto|自动) CN_SPLIT_MODE="auto";;
    2|none|不分流) CN_SPLIT_MODE="none";;
    hosts|dns|both) warn "hosts/dns/both 已合并为自动模式，按自动处理"; CN_SPLIT_MODE="auto";;
    *) warn "未识别的分流方式，按自动处理"; CN_SPLIT_MODE="auto";;
  esac
  say "本地 DNS 让容器也能解析到网关（容器不读宿主 /etc/hosts，只认 resolv.conf）。"
  if ask_yes "启用本地 DNS（dnsmasq，监听 $CN_LOCAL_DNS_ADDR:53）？" 1; then
    CN_LOCAL_DNS=1
  else
    CN_LOCAL_DNS=0
    warn "关闭本地 DNS 后，容器内的 git clone / docker pull 仍会解析失败"
  fi
  if ask_yes "启用看门狗（代理机不可达时自动摘除 hosts 块止损）？" 1; then
    CN_WATCHDOG=1
  else
    CN_WATCHDOG=0
  fi
  if ask_yes "修改 APT 镜像？" 1; then
    CN_DO_APT=1
    CN_APT_MIRROR="$(ask "APT 镜像地址" "$CN_APT_MIRROR")"
  else
    CN_DO_APT=0
  fi
  if ask_yes "启用 BBR？" 1; then CN_DO_BBR=1; else CN_DO_BBR=0; fi
  if ask_yes "配置 Docker 镜像加速？" 1; then
    CN_DO_DOCKER=1
    CN_DOCKER_MIRROR="$(ask "Docker 镜像地址" "${CN_DOCKER_MIRROR:-$CN_GATEWAY}")"
  else
    CN_DO_DOCKER=0
  fi
  if ask_yes "配置 Git 镜像？" 1; then
    CN_DO_GIT=1
    CN_GIT_MIRROR="$(ask "Git 镜像地址" "${CN_GIT_MIRROR:-$CN_GATEWAY}")"
  else
    CN_DO_GIT=0
  fi
  CN_OPTIONS_CONFIGURED=1
  title "国内机设置摘要"
  cn_option_summary
}

# cn_apply_watchdog 生成看门狗脚本与 systemd timer。
# 脚本自带探测逻辑（不依赖本文件），状态机：OK 连续 2 次恢复网关路径；
# 连续 3 次非 OK 则摘除 hosts 托管块止损。
cn_apply_watchdog() {
  local ip="$1" domains="$2"
  [ "$CN_WATCHDOG" -eq 1 ] || return 0
  if ! have systemctl; then
    warn "本机没有 systemd，跳过看门狗"
    return 1
  fi
  cn_snapshot_file "$CN_WATCH_SCRIPT"
  cn_snapshot_file "$CN_WATCH_ENV"
  cn_snapshot_file "$CN_WATCH_UNIT"
  cn_snapshot_file "$CN_WATCH_TIMER"
  cn_put_file "$CN_WATCH_ENV" 0600 <<CNENV
CN_GW_IP=$ip
CN_DOMAINS="$domains"
CN_HOSTS_BEGIN="$CN_HOSTS_BEGIN"
CN_HOSTS_END="$CN_HOSTS_END"
CN_WATCH_LOG="$CN_WATCH_LOG"
CN_WATCH_STATE="$CN_WATCH_STATE"
CN_WATCH_INTERVAL=$CN_WATCH_INTERVAL
CNENV
  cn_put_file "$CN_WATCH_SCRIPT" 0755 <<'WATCHEOF'
#!/bin/bash
# 由 warden-gateway bootstrap 生成，请勿手工编辑。
set -u
ENV_FILE="${CN_WATCH_ENV_PATH:-/etc/warden-gateway-cn/cn.env}"
[ -f "$ENV_FILE" ] || exit 0
. "$ENV_FILE"
log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$CN_WATCH_LOG"; }
internet_ok() {
  local url fails=0
  for url in https://www.apple.com https://mirrors.aliyun.com https://pypi.org; do
    curl -fsS -o /dev/null --connect-timeout 2 --max-time 4 "$url" >/dev/null 2>&1 || fails=$((fails + 1))
  done
  [ "$fails" -lt 2 ]
}
tcp_open() {
  local port="$1"
  timeout 2 bash -c "exec 3<>/dev/tcp/$CN_GW_IP/$port" >/dev/null 2>&1
}
# hosts 改写后必须让本地 dnsmasq 重读，否则它会继续返回缓存里的旧地址。
reload_dns() {
  command -v systemctl >/dev/null 2>&1 || return 0
  systemctl is-active warden-gateway-cn-dns >/dev/null 2>&1 || return 0
  systemctl reload warden-gateway-cn-dns >/dev/null 2>&1 || pkill -HUP dnsmasq >/dev/null 2>&1 || true
}
probe() {
  internet_ok || { printf 'LOCAL_BROKEN'; return 4; }
  local icmp=0 p22=0 p80=0 p443=0 health=0
  ping -c 1 -W 1 "$CN_GW_IP" >/dev/null 2>&1 && icmp=1
  tcp_open 22 && p22=1
  tcp_open 80 && p80=1
  tcp_open 443 && p443=1
  if [ "$p80" -eq 1 ] && curl -fsS -o /dev/null --connect-timeout 2 --max-time 4 "http://$CN_GW_IP/healthz" >/dev/null 2>&1; then health=1; fi
  if [ "$health" -eq 1 ] || [ "$p443" -eq 1 ]; then printf 'OK'; return 0; fi
  if [ "$p80" -eq 1 ]; then printf 'LINK_443_BLOCKED'; return 1; fi
  if [ "$icmp" -eq 0 ] && [ "$p22" -eq 0 ]; then printf 'C_DOWN'; return 2; fi
  printf 'LINK_PORTS_BLOCKED'; return 3
}
gateway_on() {
  local tmp
  tmp="$(mktemp)"
  {
    printf '%s\n' "$CN_HOSTS_BEGIN"
    printf '%s %s\n' "$CN_GW_IP" "$CN_DOMAINS"
    printf '%s\n' "$CN_HOSTS_END"
    awk -v b="$CN_HOSTS_BEGIN" -v e="$CN_HOSTS_END" '$0 == b {skip=1; next} $0 == e {skip=0; next} !skip' /etc/hosts 2>/dev/null
  } > "$tmp"
  cat "$tmp" > /etc/hosts
  rm -f "$tmp"
  reload_dns
  log "已恢复网关 hosts 块 -> $CN_GW_IP"
}
gateway_off() {
  local tmp
  grep -qxF "$CN_HOSTS_BEGIN" /etc/hosts 2>/dev/null || return 0
  tmp="$(mktemp)"
  awk -v b="$CN_HOSTS_BEGIN" -v e="$CN_HOSTS_END" '$0 == b {skip=1; next} $0 == e {skip=0; next} !skip' /etc/hosts > "$tmp"
  cat "$tmp" > /etc/hosts
  rm -f "$tmp"
  reload_dns
  log "已摘除网关 hosts 块（降级：改走直连解析）"
}
state_get() {
  local key="$1" val=""
  if [ -f "$CN_WATCH_STATE" ]; then
    val="$(sed -n "s/^$key=//p" "$CN_WATCH_STATE" | head -n 1)"
  fi
  printf '%s' "${val:-0}"
}
state_put() {
  printf 'mode=%s\nfails=%s\noks=%s\nresult=%s\nupdated=%s\n' "$1" "$2" "$3" "$4" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$CN_WATCH_STATE"
}
main() {
  local mode fails oks result rc
  mode="$(state_get mode)"
  [ "$mode" = "0" ] && mode="gateway"
  fails="$(state_get fails)"
  oks="$(state_get oks)"
  result="$(probe)"
  rc=$?
  case "$result" in
    OK)
      oks=$((oks + 1))
      fails=0
      if [ "$mode" = "degraded" ] && [ "$oks" -ge 2 ]; then
        gateway_on
        mode="gateway"
      fi
      ;;
    LOCAL_BROKEN)
      log "本机网络异常，跳过本次判定"
      ;;
    *)
      fails=$((fails + 1))
      oks=0
      if [ "$mode" = "gateway" ] && [ "$fails" -ge 3 ]; then
        log "探测结果 $result 连续 $fails 次，进入降级"
        gateway_off
        mode="degraded"
      fi
      ;;
  esac
  state_put "$mode" "$fails" "$oks" "$result"
  return 0
}
main
WATCHEOF
  cn_put_file "$CN_WATCH_UNIT" 0644 <<WUNIT
[Unit]
Description=Warden Gateway CN watchdog (one shot)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=CN_WATCH_ENV_PATH=$CN_WATCH_ENV
ExecStart=/bin/bash $CN_WATCH_SCRIPT
WUNIT
  cn_put_file "$CN_WATCH_TIMER" 0644 <<WTIMER
[Unit]
Description=Warden Gateway CN watchdog timer

[Timer]
OnBootSec=45s
OnUnitActiveSec=${CN_WATCH_INTERVAL}s
AccuracySec=5s
Unit=$CN_WATCH_UNIT_NAME.service

[Install]
WantedBy=timers.target
WTIMER
  if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if systemctl enable --now "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1; then
    ok "看门狗已启用：每 ${CN_WATCH_INTERVAL}s 探测一次，日志 $CN_WATCH_LOG"
    return 0
  fi
  warn "看门狗 timer 启动失败：journalctl -u $CN_WATCH_UNIT_NAME -n 30"
  return 1
}

# cn_verify_split 应用后自检：分流域名必须解析到网关。
cn_verify_split() {
  local ip="$1" domains="$2" domain got bad=0
  [ "$DRY_RUN" -eq 0 ] || return 0
  domains="${domains//,/ }"
  for domain in $domains; do
    got="$(getent ahostsv4 "$domain" 2>/dev/null | awk 'NR == 1 {print $1}')"
    if [ "$got" = "$ip" ]; then
      kv "  $domain" "$got"
    else
      kv "  $domain" "${got:-解析失败}（期望 $ip）"
      bad=1
    fi
  done
  return "$bad"
}

# ---------------------------------------------------------------------------
# agent 模式（--enroll-token 触发，spec §9.4 顺序锁死）
# ---------------------------------------------------------------------------

# cn_agent_arch agent 二进制的构建目标（对齐 Task 15 发布的 asset 名）。
# 与 detect_arch 的网关 tar 命名（armv7）不同：agent 是 arm。
cn_agent_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n';;
    aarch64|arm64) printf 'arm64\n';;
    armv7l) printf 'arm\n';;
    *) die "不支持的架构（agent 仅提供 amd64/arm64/arm）：$(uname -m)";;
  esac
}

# cn_agent_origin_ok 探测候选面板源：/api/health 可达即认为可用。
cn_agent_origin_ok() {
  [ -n "$1" ] || return 1
  curl -kfsS --connect-timeout 3 --max-time 8 "$1/api/health" >/dev/null 2>&1
}

# cn_agent_resolve_origin 确定面板源地址（下载 agent、enroll、心跳共用）。
# 候选依次为：--origin → --public-url 的源 → 交互输入 → 网关地址直连
# （仅对无 SNI 的 IP 面板有效）。候选必须通过 /api/health 探测。
cn_agent_resolve_origin() {
  local candidate gateway_host
  if [ -n "$CN_AGENT_ORIGIN" ]; then
    case "$CN_AGENT_ORIGIN" in
      http://*|https://*) ;;
      *) die "--origin 必须以 http:// 或 https:// 开头：$CN_AGENT_ORIGIN";;
    esac
    CN_AGENT_ORIGIN="${CN_AGENT_ORIGIN%/}"
    if cn_agent_origin_ok "$CN_AGENT_ORIGIN"; then return 0; fi
    warn "--origin 健康检查未通过：$CN_AGENT_ORIGIN/api/health"
    return 1
  fi
  candidate="$(printf '%s' "$PUBLIC_URL" | sed -n 's#^\([a-zA-Z][a-zA-Z0-9+.-]*://[^/]*\).*#\1#p')"
  if [ -n "$candidate" ] && cn_agent_origin_ok "$candidate"; then
    CN_AGENT_ORIGIN="$candidate"
    return 0
  fi
  if is_tty && [ "$NON_INTERACTIVE" -eq 0 ]; then
    say "agent 模式需要面板源地址（用于下载 agent 二进制和注册本机）。"
    candidate="$(ask "面板源地址（例如 https://panel.example.com）" "$candidate")"
    [ -n "$candidate" ] || return 1
    case "$candidate" in
      http://*|https://*) ;;
      *) candidate="https://$candidate";;
    esac
    candidate="$(printf '%s' "$candidate" | sed -n 's#^\([a-zA-Z][a-zA-Z0-9+.-]*://[^/]*\).*#\1#p')"
    if [ -n "$candidate" ] && cn_agent_origin_ok "$candidate"; then
      CN_AGENT_ORIGIN="$candidate"
      return 0
    fi
    warn "面板源地址健康检查未通过：$candidate/api/health"
    return 1
  fi
  gateway_host="${CN_GATEWAY:-${GATEWAY_IP:-}}"
  if [ -n "$gateway_host" ]; then
    for candidate in "https://$gateway_host" "http://$gateway_host"; do
      if cn_agent_origin_ok "$candidate"; then
        CN_AGENT_ORIGIN="$candidate"
        return 0
      fi
    done
  fi
  warn "无法确定面板源地址：请加 --origin https://面板域名（或 --public-url）重试"
  return 1
}

# cn_agent_apply_insecure_tls 处理自签名/IP 面板的证书校验（spec §8.2）：
# 安装器的探测一直用 curl -k，但 agent 默认校验证书，tls.mode=self 时每次
# 心跳都会 x509 失败。判据是"普通（校验证书）请求失败、-k 请求成功"——
# 只有这种情况下才认为网关证书不可验证（自签名或仅 IP 访问），为本次机器
# 启用 insecure_tls 并明确告警；ACME 正常路径不写该字段、保持校验。
cn_agent_apply_insecure_tls() {
  if [ "$CN_AGENT_INSECURE_TLS" -eq 1 ]; then
    warn "已启用 --insecure-tls：agent 跳过面板证书校验（仅适用于自签名/IP 面板）"
    return 0
  fi
  if curl -fsS --connect-timeout 3 --max-time 8 "$CN_AGENT_ORIGIN/api/health" >/dev/null 2>&1; then
    return 0
  fi
  if curl -kfsS --connect-timeout 3 --max-time 8 "$CN_AGENT_ORIGIN/api/health" >/dev/null 2>&1; then
    CN_AGENT_INSECURE_TLS=1
    warn "证书校验失败但 -k 可访问：判定为自签名/IP 面板，已为本机 agent 关闭证书校验（insecure_tls）"
    return 0
  fi
  return 0
}

# cn_agent_version 确定要下载的 agent 版本（release tag = v<版本>）。
# 依次取：--version 显式值 → 脚本安装目录的 VERSION 文件 → 面板 /api/health
# 的 version 字段（agent 与网关同版本，对齐面板 agent_binary_url 的语义）。
# VERSION 文件与 /api/health 的 version 都是 release tag 形态（v1.6.0），
# 校验前先剥掉前导 v，否则 release 构建会误判成"无法确定版本"；输出始终裸版本。
cn_agent_version() {
  local v=""
  if [ "$VERSION" != "latest" ]; then
    printf '%s\n' "${VERSION#v}"
    return 0
  fi
  if [ -f "$SCRIPT_DIR/VERSION" ]; then
    v="$(tr -d ' \r\n' < "$SCRIPT_DIR/VERSION")"
    v="${v#v}"
    case "$v" in ""|*[!0-9.]*) v="";; esac
  fi
  if [ -z "$v" ]; then
    v="$(curl -kfsS --connect-timeout 3 --max-time 8 "$CN_AGENT_ORIGIN/api/health" 2>/dev/null \
      | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
    v="${v#v}"
    case "$v" in ""|*[!0-9.]*) v="";; esac
  fi
  [ -n "$v" ] || die "无法确定 agent 版本：请用 --version 指定（例如 --version 1.6.0）"
  printf '%s\n' "${v#v}"
}

# cn_agent_hostname_safe 输出可安全放进 JSON 的主机名。
cn_agent_hostname_safe() {
  local h
  h="$(hostname 2>/dev/null || true)"
  h="$(printf '%s' "$h" | tr -c 'A-Za-z0-9._-' '-' | sed 's/^-*//; s/-*$//' || true)"
  [ -n "$h" ] || h="cn-machine"
  printf '%s\n' "$h"
}

# cn_agent_ensure_machine_id 保证 /etc/machine-id 存在且非空：缺失或空白时
# 先生成（systemd 机器一般自带；bootstrap 起的容器可能没有），否则机器身份
# 每次重装都会漂移（Task 2 注记：cnagent.NewMachineID 的随机回退不稳定）。
cn_agent_ensure_machine_id() {
  local mid=""
  if [ -f /etc/machine-id ]; then
    mid="$(tr -d ' \r\n\t' < /etc/machine-id 2>/dev/null || true)"
  fi
  if [ -n "$mid" ]; then return 0; fi
  cn_snapshot_file /etc/machine-id
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + 生成 /etc/machine-id\n'
    return 0
  fi
  if have systemd-machine-id-setup; then
    systemd-machine-id-setup >/dev/null 2>&1 || true
  fi
  if [ ! -s /etc/machine-id ]; then
    if ! head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n' > /etc/machine-id; then
      warn "生成 /etc/machine-id 失败"
      return 1
    fi
  fi
  if [ ! -s /etc/machine-id ]; then
    warn "无法生成 /etc/machine-id"
    return 1
  fi
  chmod 0444 /etc/machine-id 2>/dev/null || true
  ok "已生成 /etc/machine-id"
  return 0
}

# cn_agent_write_config 写 agent.json（0600）。字段与 internal/cnagent Config
# 的 snake_case JSON 标签一一对应；token 先留空，enroll 成功后由
# cn_setup_agent 用 machine token 重写（ruling：文件创建时 token 为空）。
cn_agent_write_config() {
  local token="$1" machine_id="$2" hostname insecure=""
  hostname="$(cn_agent_hostname_safe)"
  # 正常 ACME 路径不写 insecure_tls（保持 agent.json 最小）；只有自签名/IP
  # 面板才追加该字段，Config.InsecureTLS 据此决定心跳/升级是否校验证书。
  if [ "$CN_AGENT_INSECURE_TLS" -eq 1 ]; then
    insecure=',
  "insecure_tls": true'
  fi
  cn_snapshot_file "$CN_AGENT_CONFIG"
  cn_put_file "$CN_AGENT_CONFIG" 0600 <<EOF
{
  "origin": "$CN_AGENT_ORIGIN",
  "token": "$token",
  "gateway_ip": "$CN_GATEWAY",
  "upstream": "$CN_AGENT_UPSTREAM",
  "repo": "$REPO",
  "machine_id": "$machine_id",
  "hostname": "$hostname",
  "heartbeat_seconds": $CN_AGENT_HEARTBEAT,
  "dns_listen": "198.18.0.1:53"$insecure
}
EOF
}

# cn_agent_download 经代理机的 GitHub 代理短路径下载 agent 二进制
# （spec §9.3：一律不直连 GitHub）。systemd 会以 root 运行这个二进制，
# 安装前必须用 .sha256 侧车校验（与 agent 自升级同严格：侧车只允许裸 hex，
# 两列 sha256sum 输出视为格式错误；缺侧车即失败，不静默跳过），
# 校验通过才原子替换到 $CN_AGENT_BIN。
cn_agent_download() {
  local version="$1" arch="$2" url tmp sidecar expected actual
  # 网关自托管：面板把与自身同版本的 agent 构建缓存在本地，国内机只连网关，
  # 安装与升级都不必再访问 GitHub（agent 自升级走同一条路径）。
  url="$CN_AGENT_ORIGIN/api/agent/binary/${version#v}/warden-cn-agent-linux-$arch"
  step "下载" "$url"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + curl -fsSL %s -o %s\n' "$url" "$CN_AGENT_BIN"
    return 0
  fi
  if ! mkdir -p "$(dirname "$CN_AGENT_BIN")"; then
    warn "创建 agent 安装目录失败：$(dirname "$CN_AGENT_BIN")"
    return 1
  fi
  tmp="$CN_AGENT_BIN.new.$$"
  # 注意不用 --retry：Windows mingw curl 在重试后失败时错误地返回 0，
  # 会让下面的失败分支失效（下载是否成功由非空检查 + sha256 校验兜底）。
  if ! curl -kfsS --connect-timeout 10 --max-time 300 -o "$tmp" "$url"; then
    rm -f "$tmp"
    warn "下载 agent 二进制失败"
    return 1
  fi
  if [ ! -s "$tmp" ]; then
    rm -f "$tmp"
    warn "下载的 agent 二进制为空"
    return 1
  fi
  # .sha256 侧车校验（缺侧车或格式不符都算失败）。
  local sidecar="$CN_AGENT_ORIGIN/api/agent/binary/${version#v}/warden-cn-agent-linux-$arch.sha256"
  if ! curl -kfsS --connect-timeout 10 --max-time 60 \
      -o "$tmp.sha" "$sidecar"; then
    rm -f "$tmp" "$tmp.sha"
    warn "下载 agent sha256 侧车失败：$sidecar"
    return 1
  fi
  expected="$(tr -d ' \r\n\t' < "$tmp.sha")"
  rm -f "$tmp.sha"
  case "$expected" in
    ''|*[!0-9a-fA-F]*)
      rm -f "$tmp"
      warn "agent sha256 侧车不是裸 hex（须与 sha256sum 单列输出一致）"
      return 1;;
  esac
  if [ "${#expected}" -ne 64 ]; then
    rm -f "$tmp"
    warn "agent sha256 侧车长度不是 64：${#expected}"
    return 1
  fi
  actual="$(sha256sum "$tmp" 2>/dev/null | awk '{print $1}' || true)"
  case "$actual" in
    ''|*[!0-9a-fA-F]*) rm -f "$tmp"; warn "无法计算二进制 sha256"; return 1;;
  esac
  if [ "$actual" != "$(printf '%s' "$expected" | tr 'A-F' 'a-f')" ]; then
    rm -f "$tmp"
    warn "agent 二进制 sha256 校验失败：期望 $expected，实际 $actual"
    return 1
  fi
  ok "agent 二进制 sha256 校验通过"
  if ! install -m 0755 "$tmp" "$CN_AGENT_BIN.new"; then
    rm -f "$tmp"
    warn "安装 agent 二进制失败"
    return 1
  fi
  rm -f "$tmp"
  mv -f "$CN_AGENT_BIN.new" "$CN_AGENT_BIN" || return 1
  return 0
}

# cn_agent_write_guard 生成 agent-rollback-guard.sh（unit 的 ExecStartPre）：
# upgrade-pending 存在且当前 NRestarts 比写入基线多 >= 3（升级自身的干净
# 重启占 1 次，之后又连续崩了 2 次）时，若有自升级备份 .prev 就换回旧二进制
# 并清掉失败计数（spec §8 安全网：新版本连续 2 次崩溃自动回退），结果记入
# 日志文件。
# 基线来自 agent 退出前写入的 "<version> <nrestarts>"：升级自身的干净
# exit-0 重启也会 +1，绝不能把这次算成崩溃（旧版按绝对 NRestarts>=2 判断，
# 第二次升级在还没跑起来前就被回滚）。agent 读不到 NRestarts 时基线写 -1，
# 表示"未知"：只有当前计数已 >= 3（足以证明反复启动失败）才回退，避免把升级
# 前就有的重启历史误判成新版本崩溃。没有 pending 标记时一律不动。
cn_agent_write_guard() {
  cn_snapshot_file "$CN_AGENT_ROLLBACK_GUARD"
  cn_put_file "$CN_AGENT_ROLLBACK_GUARD" 0755 <<GUARDEOF
#!/bin/bash
# 由 warden-gateway bootstrap 生成，请勿手工编辑。
set -u
BIN="$CN_AGENT_BIN"
LOG="$CN_AGENT_GUARD_LOG"
STATE_DIR="$CN_AGENT_STATE_DIR"
PENDING="\$STATE_DIR/upgrade-pending"
nrestarts="\$(systemctl show warden-cn-agent -p NRestarts --value 2>/dev/null || printf '0')"
case "\$nrestarts" in ''|*[!0-9]*) nrestarts=0;; esac
[ -f "\$PENDING" ] || exit 0
baseline="\$(awk '{print \$2; exit}' "\$PENDING" 2>/dev/null || printf '%s' '-1')"
# 基线 -1（或缺失/损坏）表示升级前读不到重启计数，不能做差值判断。
unknown_baseline=0
case "\$baseline" in
  -1|''|*[!0-9]*) unknown_baseline=1; baseline=0;;
esac
delta=\$((nrestarts - baseline))
if [ "\$unknown_baseline" -eq 1 ]; then
  # 未知基线：只有当前计数已 >= 3 才足以证明反复启动失败。
  [ "\$nrestarts" -ge 3 ] || exit 0
else
  # 已知基线：干净升级重启占 1 次，delta >= 3 即新版本之后又崩了 2 次。
  [ "\$delta" -ge 3 ] || exit 0
fi
[ -e "\$BIN.prev" ] || exit 0
# 回滚前先把「待验证版本」转成「已阻止版本」：恢复后的旧 agent 下次心跳会
# 看到同一个 target_version，没有这个标记就会再装一次、再崩一次（升级回滚
# 循环）。只有面板显式下发的 self-upgrade 命令才会清掉它。
pending_version="\$(awk '{print \$1; exit}' "\$PENDING" 2>/dev/null || true)"
if [ -f "\$PENDING" ]; then
  if [ -n "\$pending_version" ]; then
    printf '%s\n' "\$pending_version" > "\$STATE_DIR/upgrade-blocked" 2>/dev/null || true
  fi
  rm -f "\$PENDING"
fi
if mv -f "\$BIN.prev" "\$BIN"; then
  printf '%s warden-cn-agent 升级后重启 %s 次未成功，已回退到上一个二进制\n' "\$(date -u +%Y-%m-%dT%H:%M:%SZ)" "\$delta" >> "\$LOG" 2>/dev/null || true
  if [ -s "\$STATE_DIR/upgrade-blocked" ]; then
    printf '%s 已阻止版本 %s 的自动升级（需面板手动升级）\n' "\$(date -u +%Y-%m-%dT%H:%M:%SZ)" "\$(cat "\$STATE_DIR/upgrade-blocked")" >> "\$LOG" 2>/dev/null || true
  fi
  systemctl reset-failed warden-cn-agent >/dev/null 2>&1 || true
fi
exit 0
GUARDEOF
}

# cn_agent_write_unit 写 warden-cn-agent.service。WatchdogSec=60（裁定 R7：
# 最坏探测周期约 39s，30s 会被误杀）；第一个 ExecStartPre 确保 198.18.0.1
# 存在（agent 不自建 dummy 接口，与 legacy dnsmasq 单元同款），第二个跑
# 崩溃回退守卫。
cn_agent_write_unit() {
  cn_snapshot_file "$CN_AGENT_UNIT"
  cn_put_file "$CN_AGENT_UNIT" 0644 <<UNITEOF
[Unit]
Description=Warden Gateway CN agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=/bin/sh -c 'ip link show wg-dns0 >/dev/null 2>&1 || ip link add wg-dns0 type dummy; ip addr replace 198.18.0.1/32 dev wg-dns0; ip link set wg-dns0 up'
ExecStartPre=$CN_AGENT_ROLLBACK_GUARD
ExecStart=$CN_AGENT_BIN run --config $CN_AGENT_CONFIG
Restart=always
RestartSec=2
WatchdogSec=60
MemoryMax=64M
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
UNITEOF
}

# cn_agent_resolv_write 重写本机 resolver。
#   public：纯公网兜底（迁移安全网 = spec §9.4 第 2/3 步形态，也是回滚后
#           保留的形态；与 agent 注销时 restoreResolvConfCmd 同构）。
#   final ：agent DNS 在前 + 公网兜底在后（spec §9.4 第 6 步定型形态）。
# resolv.conf 只写一次原始快照（cn_snapshot_file 自动去重），原始内容始终
# 可由 cn-restore 还原。
cn_agent_resolv_write() {
  local mode="$1" target="/etc/resolv.conf"
  if [ -L "$target" ]; then
    target="$(readlink -f "$target" 2>/dev/null || printf '%s' "$target")"
  fi
  cn_snapshot_file /etc/resolv.conf
  cn_snapshot_file "$target"
  {
    if [ "$mode" = "final" ]; then
      printf 'nameserver 198.18.0.1\n'
    fi
    # 公网兜底取 --upstream 的主机部分（默认 223.5.5.5），再补 119.29.29.29
    # 第二兜底（与 agent 内 fallbackPublicDNS 一致）。
    if [ "${CN_AGENT_UPSTREAM%%:*}" = "223.5.5.5" ]; then
      printf 'nameserver 223.5.5.5\n'
    else
      printf 'nameserver %s\n' "${CN_AGENT_UPSTREAM%%:*}"
    fi
    printf 'nameserver %s\n' "$CN_AGENT_FALLBACK_DNS"
    printf 'options timeout:1 attempts:1\n'
  } | cn_put_file "$target" 0644
}

# cn_agent_stop_legacy 停止并禁用旧 dnsmasq 单元与看门狗 service+timer，
# resolv.conf 暂置纯公网直连（spec §9.4 第 3 步：此刻机器回到装 warden 前
# 的"无分流直连"老状态，业务不中断）。
cn_agent_stop_legacy() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + systemctl disable --now %s %s.timer %s.service\n' \
      "$CN_DNS_UNIT_NAME" "$CN_WATCH_UNIT_NAME" "$CN_WATCH_UNIT_NAME"
    return 0
  fi
  if have systemctl; then
    systemctl disable --now "$CN_DNS_UNIT_NAME" >/dev/null 2>&1 || true
    systemctl disable --now "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1 || true
    systemctl disable --now "$CN_WATCH_UNIT_NAME.service" >/dev/null 2>&1 || true
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  cn_agent_resolv_write public || return 1
  return 0
}

# cn_agent_enroll 用一次性注册令牌换 machine token：
# POST {origin}/api/agent/enroll，body {"enroll_token":...,"hostname":...}，
# 响应 {"machine_id":"...","token":"..."}。jq 不可用时按脚本既有风格用
# sed 提取（同 config_public_url / domain 的解析方式）。
cn_agent_enroll() {
  local resp payload hostname machine token
  hostname="$(cn_agent_hostname_safe)"
  payload="{\"enroll_token\":\"$CN_AGENT_ENROLL_TOKEN\",\"hostname\":\"$hostname\"}"
  step "注册" "$CN_AGENT_ORIGIN/api/agent/enroll"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + curl -d %s %s/api/agent/enroll\n' "$payload" "$CN_AGENT_ORIGIN"
    return 0
  fi
  resp="$(curl -kfsS --connect-timeout 5 --max-time 15 -H 'Content-Type: application/json' \
    -d "$payload" "$CN_AGENT_ORIGIN/api/agent/enroll" 2>/dev/null)" || {
    warn "注册请求失败（enroll token 一次性且 15 分钟有效，需在面板重新生成）"
    return 1
  }
  machine="$(printf '%s' "$resp" | sed -n 's/.*"machine_id" *: *"\([^"]*\)".*/\1/p' | head -n 1)"
  token="$(printf '%s' "$resp" | sed -n 's/.*"token" *: *"\([^"]*\)".*/\1/p' | head -n 1)"
  if [ -z "$machine" ] || [ -z "$token" ]; then
    warn "注册响应缺少 machine_id/token"
    return 1
  fi
  CN_AGENT_MACHINE_ID="$machine"
  CN_AGENT_TOKEN="$token"
  ok "注册成功：machine_id=$machine"
}

# cn_agent_start_and_wait 启动 agent 并等待 state.json 出现且 probe_result=OK。
# 状态文件只在心跳成功后落盘（Task 8 ruling 7），probe=OK 即"接入成功"。
# 启动前先删除旧 state.json：重跑 cn-setup 时旧文件会让等待逻辑把"上次的
# 成功状态"误当成这次启动成功（F8）。先 enable 再 restart，确保对已在运行的
# 单元也真正换成新配置/新二进制。
# 超时 60s 视为迁移失败（spec §9.4：超时 → 回滚）。
cn_agent_start_and_wait() {
  local i
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + systemctl daemon-reload && systemctl enable %s && rm -f %s && systemctl restart %s\n' \
      "$CN_AGENT_UNIT_NAME" "$CN_AGENT_STATE_DIR/state.json" "$CN_AGENT_UNIT_NAME"
    return 0
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if ! systemctl enable "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1; then
    warn "agent 服务启用失败：journalctl -u $CN_AGENT_UNIT_NAME -n 30"
    return 1
  fi
  rm -f "$CN_AGENT_STATE_DIR/state.json"
  if ! systemctl restart "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1; then
    warn "agent 服务启动失败：journalctl -u $CN_AGENT_UNIT_NAME -n 30"
    return 1
  fi
  step "等待" "等待 agent 首次心跳成功（最长 60s）"
  for i in $(seq 1 60); do
    if [ -f "$CN_AGENT_STATE_DIR/state.json" ] \
      && grep -qE '"probe_result" *: *"OK"' "$CN_AGENT_STATE_DIR/state.json" 2>/dev/null; then
      ok "agent 已接入（第 ${i}s 心跳成功）"
      return 0
    fi
    sleep 1
  done
  if [ -f "$CN_AGENT_STATE_DIR/state.json" ]; then
    warn "最新状态：$(tr -d '\n' < "$CN_AGENT_STATE_DIR/state.json" | head -c 300)"
  fi
  warn "等待超时：journalctl -u $CN_AGENT_UNIT_NAME -n 30"
  return 1
}

# cn_agent_cleanup_legacy 删除 legacy 部署的脚本/状态/配置/单元文件
# （全部已在第 1 步快照，cn-restore 可还原）；agent.json 仍在
# $CN_SPLIT_DIR，目录保留。NetworkManager dns=none 守卫按 spec §9.4 第 6 步保留。
cn_agent_cleanup_legacy() {
  local path
  for path in "$CN_WATCH_SCRIPT" "$CN_WATCH_ENV" "$CN_WATCH_STATE" "$CN_WATCH_LOG" \
    "$CN_DNSMASQ_CONF" "$CN_WATCH_UNIT" "$CN_WATCH_TIMER" "$CN_DNS_UNIT"; do
    if [ -e "$path" ]; then
      if [ "$DRY_RUN" -eq 1 ]; then
        printf '  + rm -f %s\n' "$path"
      else
        rm -f "$path"
      fi
    fi
  done
  if [ "$DRY_RUN" -eq 0 ] && have systemctl; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  return 0
}

# cn_agent_rollback 迁移失败回滚（spec §9.4 失败中止语义）：恢复第 1 步
# 快照、重新启用旧单元、resolv.conf 还原为纯公网兜底——第 2 步写入的公网
# DNS 保留（无害且有益）。resolv.conf 的原始内容不在回滚中还原，而是留在
# 快照归档里，供 cn-restore 完整回退到迁移前。
# 例外（重复迁移，裁定于最终评审修复 5）：机器在本次 cn-setup 之前就已是
# agent 模式（快照记录 warden-cn-agent.service existed=1）时，失败重跑不能
# 把此前正常工作的 agent 留在停用状态——按快照还原迁移前的 agent 态
# resolv.conf（198.18.0.1 在前 + 公网兜底），并重新 enable --now
# warden-cn-agent。原本就是 legacy 的机器保持原样：纯公网 + 旧单元。
cn_agent_rollback() {
  local path existed size stored agent_was=0
  warn "agent 迁移失败，回滚到迁移前状态（保留公网兜底 DNS）"
  if [ "$DRY_RUN" -eq 0 ] && have systemctl; then
    systemctl disable --now "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1 || true
  fi
  if [ -f "$CN_BACKUP_DIR/records.tsv" ]; then
    # records.tsv 行格式与 cn_finish_backup 相同：path \t existed \t size，
    # 三个变量都必须接收，否则 existed 会吞进 size 导致误删而非还原。
    while IFS="$(printf '\t')" read -r path existed size; do
      [ -n "$path" ] || continue
      # 重复迁移探测：迁移前 agent 单元已存在（existed=1）→ 本次是在已有
      # agent 的机器上重跑 cn-setup --enroll-token，回滚要恢复 agent 模式。
      if [ "$path" = "$CN_AGENT_UNIT" ] && [ "$existed" = "1" ]; then
        agent_was=1
      fi
      case "$path" in
        *resolv.conf) continue;;
      esac
      if [ "$existed" = "1" ]; then
        stored="$CN_BACKUP_DIR/files/$(cn_path_hash "$path")"
        if [ -f "$stored" ]; then
          mkdir -p "$(dirname "$path")" 2>/dev/null || true
          cp -p "$stored" "$path" || warn "回滚恢复失败：$path"
        fi
      else
        rm -f "$path"
      fi
    done < "$CN_BACKUP_DIR/records.tsv"
  fi
  if [ "$agent_was" = "1" ]; then
    # 迁移前 agent 就在本机运行：resolv.conf 按快照还原（快照里的正是迁移
    # 前的 agent 定型形态），而不是改写为纯公网。
    if [ -f "$CN_BACKUP_DIR/records.tsv" ]; then
      while IFS="$(printf '\t')" read -r path existed size; do
        [ -n "$path" ] || continue
        case "$path" in
          *resolv.conf) ;;
          *) continue;;
        esac
        if [ "$existed" = "1" ]; then
          stored="$CN_BACKUP_DIR/files/$(cn_path_hash "$path")"
          if [ -f "$stored" ]; then
            mkdir -p "$(dirname "$path")" 2>/dev/null || true
            cp -p "$stored" "$path" || warn "回滚恢复失败：$path"
          fi
        fi
      done < "$CN_BACKUP_DIR/records.tsv"
    fi
  else
    cn_agent_resolv_write public || true
  fi
  if [ "$DRY_RUN" -eq 0 ] && have systemctl; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable --now "$CN_DNS_UNIT_NAME" >/dev/null 2>&1 || true
    systemctl enable --now "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1 || true
    if [ "$agent_was" = "1" ]; then
      # 单元文件与二进制已按快照还原，把此前正常工作的 agent 重新拉起。
      if ! systemctl enable --now "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1; then
        warn "warden-cn-agent 重新启动失败：journalctl -u $CN_AGENT_UNIT_NAME -n 30"
      fi
    fi
  elif [ "$DRY_RUN" -eq 1 ] && [ "$agent_was" = "1" ]; then
    printf '  + systemctl enable --now %s\n' "$CN_AGENT_UNIT_NAME"
  fi
  return 0
}

# cn_agent_fail 迁移任一步失败的统一出口：回滚 + 快照归档（供 cn-restore
# 完整回退，含 resolv.conf 原始内容）。总是返回 0，由调用方 return 1。
cn_agent_fail() {
  cn_agent_rollback || true
  cn_finish_backup || true
  cn_prune_backups || true
  return 0
}

# cn_agent_teardown 停用并清理 agent（cn-restore 在检测到 agent 配置时调用）。
cn_agent_teardown() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + systemctl disable --now %s\n' "$CN_AGENT_UNIT_NAME"
    printf '  + rm -f %s %s %s\n' "$CN_AGENT_UNIT" "$CN_AGENT_BIN" "$CN_AGENT_CONFIG"
    return 0
  fi
  if have systemctl; then
    systemctl disable --now "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1 || true
  fi
  rm -f "$CN_AGENT_UNIT" "$CN_AGENT_BIN" "$CN_AGENT_BIN.prev" "$CN_AGENT_ROLLBACK_GUARD" \
    "$CN_AGENT_CONFIG" "$CN_AGENT_STATE_DIR/state.json" "$CN_AGENT_GUARD_LOG" \
    >/dev/null 2>&1 || true
  if have systemctl; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  rmdir "$CN_AGENT_STATE_DIR" >/dev/null 2>&1 || true
  return 0
}

# cn_setup_agent agent 模式迁移，顺序 = spec §9.4 的 8 步（顺序即验收标准）：
#   1 快照全部将改动的文件与单元；
#   2 resolv.conf 写公网兜底（安全网）；
#   3 停止禁用旧 dnsmasq 与看门狗 service+timer，resolv.conf 暂置纯公网；
#   4 下载 agent（经代理机 GitHub 代理短路径）+ machine-id 守卫 + agent.json
#     （0600，token 先空）+ 崩溃回退守卫 + systemd 单元；
#   5 enroll 换 machine token 回填 agent.json → 启动 → 等 state.json
#     probe=OK（60s 超时即失败）；
#   6 resolv.conf 定型（198.18.0.1 在前 + 公网兜底在后）+ NetworkManager
#     dns=none 守卫保留；随后 getent 抽查分流域名解析到网关（第 5 步收尾）；
#   7 摘除 /etc/hosts 托管块（CN_HOSTS_BEGIN/END 段整体删除）；
#   8 清理 watchdog 脚本/env/state/日志、dnsmasq conf 与旧单元文件，快照归档。
# 任何一步失败 → cn_agent_fail（回滚 + 归档）。
cn_setup_agent() {
  local version arch machine_id probe_rc=0 probe_out got
  title "国内机 agent 模式迁移"
  say "本机将由 warden-cn-agent 接管分流：下载 agent、注册到面板、本地 DNS 由 agent 托管。"
  # agent 模式必须有真实网关地址：没有 --gateway-ip 时 detect_ip 给出的是本机
  # 出口 IP，agent 会探测自身，心跳 probe 永远到不了 OK，白白烧掉一次性
  # enroll token。在快照之前快速失败（此刻还没有任何可回滚的改动）。
  if [ -z "$GATEWAY_IP" ]; then
    die "agent 模式必须提供 --gateway-ip（面板生成的命令已包含）；回退到本机出口 IP 会让 agent 探测自身"
  fi
  CN_GATEWAY="$GATEWAY_IP"
  is_ipv4 "$CN_GATEWAY" || die "网关地址不是 IPv4：$CN_GATEWAY"
  cn_agent_resolve_origin || return 1
  cn_agent_apply_insecure_tls
  step "面板" "$CN_AGENT_ORIGIN"
  version="$(cn_agent_version)"
  arch="$(cn_agent_arch)"
  kv "agent 版本" "v$version（$arch）"
  kv "上游 DNS" "$CN_AGENT_UPSTREAM"
  step "探测" "检查本机网络与代理机 $CN_GATEWAY"
  probe_out="$(cn_probe_gateway "$CN_GATEWAY")" || probe_rc=$?
  kv "探测结果" "$(cn_probe_label "$probe_out")"
  if [ "$probe_rc" -eq 4 ]; then
    die "本机无法访问外部网络（对照点全部失败），未做任何修改"
  fi
  if [ "$probe_rc" -ne 0 ]; then
    warn "代理机当前未就绪，迁移仍会继续；探测恢复后 agent 会自动回到网关路径"
  fi
  confirm "确认迁移到 agent 模式？" || { say "已取消"; return 1; }

  CN_CHANGED=0
  cn_begin_backup

  # 第 1 步：快照全部将改动的文件与单元（新文件记录 existed=0，cn-restore 会删除）。
  step "快照" "备份将被改动的文件到 $(cn_backup_root)"
  cn_snapshot_file /etc/resolv.conf
  cn_snapshot_file /etc/hosts
  cn_snapshot_file "$CN_DNSMASQ_CONF"
  cn_snapshot_file "$CN_DNS_UNIT"
  cn_snapshot_file "$CN_WATCH_SCRIPT"
  cn_snapshot_file "$CN_WATCH_ENV"
  cn_snapshot_file "$CN_WATCH_UNIT"
  cn_snapshot_file "$CN_WATCH_TIMER"
  cn_snapshot_file "$CN_WATCH_STATE"
  cn_snapshot_file "$CN_WATCH_LOG"
  cn_snapshot_file "$CN_AGENT_BIN"
  cn_snapshot_file "$CN_AGENT_UNIT"
  cn_snapshot_file "$CN_AGENT_CONFIG"
  cn_snapshot_file "$CN_AGENT_ROLLBACK_GUARD"

  # 第 2 步：resolv.conf 写公网兜底 DNS（先建立安全网再动任何服务）。
  step "安全网" "写入公网兜底 DNS（${CN_AGENT_UPSTREAM%%:*}）"
  if ! cn_agent_resolv_write public; then
    cn_agent_fail
    return 1
  fi

  # 第 3 步：停止禁用旧单元，resolv.conf 暂置纯公网。
  step "停旧" "停止 warden-gateway-cn-dns 与看门狗 service+timer"
  if ! cn_agent_stop_legacy; then
    cn_agent_fail
    return 1
  fi

  # 第 4 步：下载 agent + machine-id 守卫 + agent.json + 守卫脚本 + unit。
  step "装新" "下载 warden-cn-agent 并安装单元"
  if ! cn_agent_download "$version" "$arch"; then
    cn_agent_fail
    return 1
  fi
  if ! cn_agent_ensure_machine_id; then
    cn_agent_fail
    return 1
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    machine_id="(dry-run)"
  else
    machine_id="$(tr -d ' \r\n' < /etc/machine-id)"
  fi
  # 重跑 cn-setup 的机器上旧 agent 可能仍在运行：先停掉再写新配置/装新单元，
  # 否则后面的 restart 还没到、旧进程已经用旧 token 继续心跳（F8）。
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '  + systemctl stop %s（若在运行）\n' "$CN_AGENT_UNIT_NAME"
  elif have systemctl && systemctl is-active --quiet "$CN_AGENT_UNIT_NAME"; then
    systemctl stop "$CN_AGENT_UNIT_NAME" >/dev/null 2>&1 || true
  fi
  if ! cn_agent_write_config "" "$machine_id"; then
    cn_agent_fail
    return 1
  fi
  if ! cn_agent_write_guard; then
    cn_agent_fail
    return 1
  fi
  if ! cn_agent_write_unit; then
    cn_agent_fail
    return 1
  fi

  # 第 5 步：enroll 换 machine token 回填 agent.json，启动并等待 probe=OK。
  if ! cn_agent_enroll; then
    cn_agent_fail
    return 1
  fi
  if ! cn_agent_write_config "$CN_AGENT_TOKEN" "$machine_id"; then
    cn_agent_fail
    return 1
  fi
  if ! cn_agent_start_and_wait; then
    cn_agent_fail
    return 1
  fi

  # 第 6 步：resolv.conf 定型（agent 在前 + 公网兜底在后），NM 守卫保留。
  step "定型" "resolv.conf 指向 agent 本地 DNS（198.18.0.1 在前 + 公网兜底）"
  if ! cn_agent_resolv_write final; then
    cn_agent_fail
    return 1
  fi
  if ! cn_apply_nm_guard; then
    cn_agent_fail
    return 1
  fi
  # 第 5 步收尾：getent 抽查分流域名解析到网关（须在定型之后才能走 agent DNS）。
  # 管道带 || true：getent 解析失败不能让 set -e 直接退出——下面的回滚守卫必须可达。
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(getent ahostsv4 github.com 2>/dev/null | awk 'NR == 1 {print $1}' || true)"
    if [ "$got" != "$CN_GATEWAY" ]; then
      warn "分流域名抽查未通过：github.com 解析到 ${got:-空}，期望 $CN_GATEWAY"
      cn_agent_fail
      return 1
    fi
    kv "github.com" "$got"
  fi

  # 第 7 步：摘除 /etc/hosts 托管块——不摘的话 agent 降级直连时 hosts 仍把
  # 分流域名钉在网关 IP，止损路径失效（spec §9.4 第 7 步）。
  step "摘除" "删除 /etc/hosts 中的 warden-gateway 托管块"
  if ! cn_remove_hosts; then
    cn_agent_fail
    return 1
  fi

  # 第 8 步：清理 legacy 文件与单元，快照归档。
  step "清理" "删除 watchdog 脚本/env/state、dnsmasq conf 与旧单元文件"
  cn_agent_cleanup_legacy
  cn_finish_backup
  cn_prune_backups

  title "agent 模式迁移完成"
  kv "面板" "$CN_AGENT_ORIGIN"
  kv "网关地址" "$CN_GATEWAY"
  kv "agent 服务" "$CN_AGENT_UNIT_NAME（systemd 托管）"
  kv "配置" "$CN_AGENT_CONFIG"
  kv "状态" "$CN_AGENT_STATE_DIR/state.json"
  if [ -n "$CN_BACKUP_DIR" ]; then
    kv "备份" "$CN_BACKUP_DIR"
    if install_cn_cli; then
      kv "体检命令" "sudo $CN_CLI_PATH cn-check"
      kv "恢复命令" "sudo $CN_CLI_PATH cn-restore"
    fi
  fi
  return 0
}

cn_setup() {
  need_root
  title "国内机设置"
  validate_options
  # agent 模式分派：--enroll-token 且未 --no-agent → 按 spec §9.4 迁移。
  if [ -n "$CN_AGENT_ENROLL_TOKEN" ] && [ "$CN_AGENT_NO_AGENT" -eq 0 ]; then
    cn_setup_agent
    return $?
  fi
  if [ -n "$CN_AGENT_ENROLL_TOKEN" ] && [ "$CN_AGENT_NO_AGENT" -eq 1 ]; then
    warn "已指定 --no-agent，忽略 --enroll-token，按 legacy 模式执行"
  fi
  # legacy 弃用提示：legacy 路径保持完全可用，但 agent 模式是未来默认。
  warn "legacy 模式已弃用（dnsmasq + 看门狗）：请在面板「国内机」页生成带 --enroll-token 的新命令，迁移到 agent 模式。"
  interactive_cn_options
  if [ -z "$CN_GATEWAY" ]; then
    CN_GATEWAY="${GATEWAY_IP:-$(detect_ip)}"
  fi
  [ -n "$CN_GATEWAY" ] || die "无法确定网关地址，请指定 --gateway-ip"
  is_ipv4 "$CN_GATEWAY" || die "网关地址不是 IPv4：$CN_GATEWAY"
  if [ -z "$CN_DOMAINS" ]; then CN_DOMAINS="$CN_DEFAULT_DOMAINS"; fi
  title "设置摘要"
  cn_option_summary
  confirm "确认应用国内机设置？" || { say "已取消"; return 1; }
  local probe_rc=0 probe_out upstreams
  step "探测" "检查本机网络与代理机 $CN_GATEWAY"
  probe_out="$(cn_probe_gateway "$CN_GATEWAY")" || probe_rc=$?
  kv "探测结果" "$(cn_probe_label "$probe_out")"
  if [ "$probe_rc" -eq 4 ]; then
    die "本机无法访问外部网络（对照点全部失败），未做任何修改"
  fi
  if [ "$probe_rc" -ne 0 ]; then
    warn "代理机当前未就绪，配置仍会写入，看门狗会在其恢复后自动切回"
  fi
  CN_CHANGED=0
  cn_begin_backup
  local failed=0
  upstreams="$(cn_detect_upstreams)"
  cn_apply_nm_guard || failed=1
  if [ "$CN_PLAN_HOSTS" -eq 1 ]; then
    cn_apply_hosts "$CN_GATEWAY" "$CN_DOMAINS" || failed=1
  fi
  if [ "$CN_PLAN_LOCAL_DNS" -eq 1 ]; then
    cn_apply_local_dns "$CN_LOCAL_DNS_ADDR" "$upstreams" || failed=1
  fi
  if [ "$CN_PLAN_RESOLVER" -eq 1 ]; then
    cn_apply_resolver "$CN_LOCAL_DNS_ADDR" "$upstreams" || failed=1
  fi
  if [ "$CN_DO_APT" -eq 1 ]; then
    cn_apply_apt "$CN_APT_MIRROR" || failed=1
  fi
  if [ "$CN_DO_BBR" -eq 1 ]; then
    cn_apply_bbr || failed=1
  fi
  if [ "$CN_DO_DOCKER" -eq 1 ]; then
    cn_apply_docker_mirror "${CN_DOCKER_MIRROR:-$CN_GATEWAY}" || failed=1
  fi
  if [ "$CN_DO_GIT" -eq 1 ]; then
    cn_apply_git_mirror "${CN_GIT_MIRROR:-$CN_GATEWAY}" || failed=1
  fi
  if [ "$CN_DO_APT" -eq 1 ] && [ "$CN_DO_APT_UPDATE" -eq 1 ]; then
    step "APT" "更新软件包索引"
    if [ "$DRY_RUN" -eq 0 ]; then
      apt-get update >/dev/null 2>&1 || { warn "apt-get update 失败"; failed=1; }
    fi
  fi
  cn_apply_watchdog "$CN_GATEWAY" "$CN_DOMAINS" || failed=1
  cn_finish_backup
  cn_prune_backups
  title "国内机设置完成"
  if [ "$CN_PLAN_HOSTS" -eq 1 ] || [ "$CN_PLAN_RESOLVER" -eq 1 ]; then
    step "自检" "分流域名解析结果"
    cn_verify_split "$CN_GATEWAY" "$CN_DOMAINS" || { warn "部分域名未解析到网关，请查看上方明细"; failed=1; }
  fi
  if [ -n "$CN_BACKUP_DIR" ] && [ "$DRY_RUN" -eq 0 ]; then
    kv "备份" "$CN_BACKUP_DIR"
    if install_cn_cli; then
      kv "恢复命令" "sudo $CN_CLI_PATH cn-restore"
    else
      local self
      self="$(script_self_path || true)"
      warn "未能注册 $CN_CLI_PATH"
      if [ -n "$self" ]; then
        kv "脚本路径" "$self"
        kv "恢复命令" "sudo bash $self cn-restore"
      else
        say "请保留当前脚本，之后执行：bash <脚本路径> cn-restore"
      fi
    fi
  fi
  if [ "$failed" -eq 1 ]; then
    warn "部分设置未成功，请查看上方输出"
    return 1
  fi
  return 0
}

cn_restore_one() {
  local dir="$1" manifest="$1/manifest.json" line path existed stored
  [ -f "$manifest" ] || die "备份缺少 manifest.json：$manifest"
  while IFS= read -r line; do
    case "$line" in
      *'"path"'*) ;;
      *) continue;;
    esac
    path="$(printf '%s' "$line" | sed -n 's/.*"path": "\([^"]*\)".*/\1/p')"
    existed="$(printf '%s' "$line" | sed -n 's/.*"existed": \(true\|false\).*/\1/p')"
    [ -n "$path" ] || continue
    if [ "$existed" = "true" ]; then
      stored="$dir/files/$(cn_path_hash "$path")"
      if [ ! -f "$stored" ]; then
        warn "缺少备份文件：$stored"
        continue
      fi
      if [ "$DRY_RUN" -eq 1 ]; then
        printf '  + 恢复 %s\n' "$path"
      else
        mkdir -p "$(dirname "$path")"
        cp -p "$stored" "$path"
        say "已恢复 $path"
      fi
    else
      if [ "$DRY_RUN" -eq 1 ]; then
        printf '  + 删除 %s\n' "$path"
      else
        rm -f "$path"
        say "已删除 $path"
      fi
    fi
  done < "$manifest"
  if [ "$DRY_RUN" -eq 0 ] && have systemctl; then
    systemctl stop "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1 || true
    systemctl disable "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1 || true
    systemctl stop "$CN_DNS_UNIT_NAME" >/dev/null 2>&1 || true
    systemctl disable "$CN_DNS_UNIT_NAME" >/dev/null 2>&1 || true
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl reload docker >/dev/null 2>&1 || true
    if systemctl is-active NetworkManager >/dev/null 2>&1; then
      systemctl reload NetworkManager >/dev/null 2>&1 || true
    fi
    sysctl --system >/dev/null 2>&1 || true
  fi
  if [ -d "$CN_SPLIT_DIR" ]; then
    rm -f "$CN_WATCH_STATE" "$CN_WATCH_LOG" >/dev/null 2>&1 || true
    rmdir "$CN_SPLIT_DIR" >/dev/null 2>&1 || true
  fi
}

# cn_check_agent agent 模式只读体检：state.json 摘要 + agent 服务状态 +
# getent 抽查（spec §9.5：与 legacy 输出格式一致，shell 只读，不驱动 agent）。
cn_check_agent() {
  local rc=0 got state_file="$CN_AGENT_STATE_DIR/state.json"
  local ip state_updated state_probe state_detail state_count
  title "国内机分流体检（agent 模式）"
  ip="$(sed -n 's/.*"gateway_ip" *: *"\([^"]*\)".*/\1/p' "$CN_AGENT_CONFIG" 2>/dev/null | head -n 1)"
  kv "网关地址" "${ip:-未知}"
  if [ -z "$ip" ]; then
    warn "无法从 $CN_AGENT_CONFIG 读取网关地址"
    rc=1
  fi
  step "agent" "检查 warden-cn-agent 服务与状态文件"
  if have systemctl && systemctl is-active --quiet "$CN_AGENT_UNIT_NAME" 2>/dev/null; then
    kv "$CN_AGENT_UNIT_NAME" "active"
  else
    kv "$CN_AGENT_UNIT_NAME" "未运行"
    rc=1
  fi
  if [ -f "$state_file" ]; then
    state_updated="$(sed -n 's/.*"updated_at" *: *"\([^"]*\)".*/\1/p' "$state_file" | head -n 1)"
    state_probe="$(sed -n 's/.*"probe_result" *: *"\([^"]*\)".*/\1/p' "$state_file" | head -n 1)"
    state_detail="$(sed -n 's/.*"probe_detail" *: *"\([^"]*\)".*/\1/p' "$state_file" | head -n 1)"
    state_count="$(sed -n 's/.*"domains_count" *: *\([0-9][0-9]*\).*/\1/p' "$state_file" | head -n 1)"
    kv "状态更新" "${state_updated:-未知}"
    kv "探测结果" "$(cn_probe_label "${state_probe:-UNKNOWN}")"
    if [ -n "$state_detail" ]; then kv "探测明细" "$state_detail"; fi
    kv "分流域名" "${state_count:-0} 条"
    if [ "${state_probe:-UNKNOWN}" != "OK" ]; then
      rc=1
    fi
  else
    kv "状态文件" "缺失（$state_file）"
    rc=1
  fi
  step "hosts" "检查 /etc/hosts 托管块"
  if grep -qxF "$CN_HOSTS_BEGIN" /etc/hosts 2>/dev/null; then
    kv "托管块" "仍存在（agent 模式应已摘除，会覆盖降级止损）"
  else
    kv "托管块" "不存在（agent 模式正常）"
  fi
  step "本地 DNS" "检查本机 resolver 与解析抽查"
  if [ -f /etc/resolv.conf ]; then
    kv "resolv.conf" "$(awk '$1 == "nameserver" {printf "%s ", $2}' /etc/resolv.conf | sed 's/ $//')"
  fi
  if [ -n "$ip" ] && [ "$DRY_RUN" -eq 0 ]; then
    # || true：cn-check 只读，解析失败应报告"未命中"，不应让 set -e 静默退出。
    got="$(getent ahostsv4 github.com 2>/dev/null | awk 'NR == 1 {print $1}' || true)"
    if [ "$got" = "$ip" ]; then
      kv "github.com" "$got（命中网关）"
    else
      kv "github.com" "${got:-解析失败}（期望 $ip）"
      rc=1
    fi
  fi
  if [ "$rc" -eq 0 ]; then
    ok "agent 分流路径正常"
  else
    warn "agent 分流存在问题，详见上方各项；journalctl -u $CN_AGENT_UNIT_NAME -n 30"
  fi
  return "$rc"
}

# cn_check 只读体检：探测代理机、检查 hosts 块 / 本地 DNS / 看门狗状态，不改任何文件。
cn_check() {
  if [ -f "$CN_AGENT_CONFIG" ]; then
    cn_check_agent
    return $?
  fi
  local ip="$1" domains="$2" rc=0 out got
  if [ -f "$CN_WATCH_ENV" ]; then
    # shellcheck disable=SC1090
    . "$CN_WATCH_ENV" >/dev/null 2>&1 || true
  fi
  ip="${ip:-${CN_GW_IP:-${CN_GATEWAY:-${GATEWAY_IP:-}}}}"
  if [ -z "$ip" ]; then
    ip="$(detect_ip 2>/dev/null || true)"
  fi
  domains="${domains:-${CN_DOMAINS:-${GATEWAY_DOMAINS:-$CN_DEFAULT_DOMAINS}}}"
  [ -n "$ip" ] || die "缺少网关地址，请指定 --gateway-ip"
  is_ipv4 "$ip" || die "网关地址不是 IPv4：$ip"
  title "国内机分流体检"
  step "探测" "检查本机网络与代理机 $ip"
  out="$(cn_probe_gateway "$ip")" || rc=$?
  kv "探测结果" "$(cn_probe_label "$out")"
  kv "本机外网" "$(cn_internet_ok && say_ok_text || say_bad_text)"
  step "hosts" "检查 /etc/hosts 托管块"
  if grep -qxF "$CN_HOSTS_BEGIN" /etc/hosts 2>/dev/null; then
    kv "托管块" "存在"
    got="$(getent ahostsv4 github.com 2>/dev/null | awk 'NR == 1 {print $1}')"
    kv "github.com" "${got:-解析失败}"
    if [ "$got" = "$ip" ]; then kv "判定" "命中网关"; else kv "判定" "未命中网关"; rc=1; fi
  else
    kv "托管块" "不存在（当前走直连解析）"
    rc=1
  fi
  step "本地 DNS" "检查 dnsmasq 与本机 resolver"
  if have systemctl && systemctl is-active "$CN_DNS_UNIT_NAME" >/dev/null 2>&1; then
    kv "$CN_DNS_UNIT_NAME" "active"
  else
    kv "$CN_DNS_UNIT_NAME" "未运行"
  fi
  if [ -f /etc/resolv.conf ]; then
    kv "resolv.conf" "$(awk '$1 == "nameserver" {printf "%s ", $2}' /etc/resolv.conf | sed 's/ $//')"
  fi
  if have systemctl && systemctl is-active "$CN_WATCH_UNIT_NAME.timer" >/dev/null 2>&1; then
    kv "看门狗" "active（每 ${CN_WATCH_INTERVAL}s）"
  else
    kv "看门狗" "未运行"
  fi
  if [ -f "$CN_WATCH_STATE" ]; then
    kv "看门狗状态" "$(tr '\n' ' ' < "$CN_WATCH_STATE" | sed 's/ $//')"
  fi
  if [ "$rc" -eq 0 ]; then
    ok "分流路径正常"
  else
    warn "分流路径存在问题，详见上方各项；容器内可用 docker run --rm alpine:3.20 getent hosts github.com 复核"
  fi
  return "$rc"
}

say_ok_text() { printf '正常'; }
say_bad_text() { printf '异常'; }

# cn_status_agent agent 模式配置快照（只读，输出风格与 legacy 一致）。
cn_status_agent() {
  local origin ip state_file="$CN_AGENT_STATE_DIR/state.json"
  local state_updated state_probe state_count
  title "国内机分流状态（agent 模式）"
  origin="$(sed -n 's/.*"origin" *: *"\([^"]*\)".*/\1/p' "$CN_AGENT_CONFIG" 2>/dev/null | head -n 1)"
  ip="$(sed -n 's/.*"gateway_ip" *: *"\([^"]*\)".*/\1/p' "$CN_AGENT_CONFIG" 2>/dev/null | head -n 1)"
  kv "面板" "${origin:-未知}"
  kv "网关地址" "${ip:-未知}"
  if have systemctl; then
    kv "agent 服务" "$(systemctl is-active "$CN_AGENT_UNIT_NAME" 2>/dev/null || true)"
  fi
  if [ -f "$state_file" ]; then
    state_updated="$(sed -n 's/.*"updated_at" *: *"\([^"]*\)".*/\1/p' "$state_file" | head -n 1)"
    state_probe="$(sed -n 's/.*"probe_result" *: *"\([^"]*\)".*/\1/p' "$state_file" | head -n 1)"
    state_count="$(sed -n 's/.*"domains_count" *: *\([0-9][0-9]*\).*/\1/p' "$state_file" | head -n 1)"
    kv "状态更新" "${state_updated:-未知}"
    kv "探测结果" "$(cn_probe_label "${state_probe:-UNKNOWN}")"
    kv "分流域名" "${state_count:-0} 条"
  else
    kv "状态文件" "缺失"
  fi
  if grep -qxF "$CN_HOSTS_BEGIN" /etc/hosts 2>/dev/null; then
    kv "hosts 托管块" "仍存在（agent 模式应已摘除）"
  else
    kv "hosts 托管块" "不存在（agent 模式已摘除）"
  fi
  if [ -f /etc/resolv.conf ]; then
    kv "resolv.conf" "$(awk '$1 == "nameserver" {printf "%s ", $2}' /etc/resolv.conf | sed 's/ $//')"
  fi
  kv "agent 配置" "$CN_AGENT_CONFIG"
  if [ -d "$(cn_backup_root)" ]; then
    kv "备份数量" "$(cn_backup_count)"
  fi
  kv "恢复命令" "sudo warden-cn cn-restore"
}

# cn_status 打印当前分流配置快照（只读）。
cn_status() {
  if [ -f "$CN_AGENT_CONFIG" ]; then
    cn_status_agent
    return 0
  fi
  local ip domains
  if [ -f "$CN_WATCH_ENV" ]; then
    # shellcheck disable=SC1090
    . "$CN_WATCH_ENV" >/dev/null 2>&1 || true
  fi
  ip="${CN_GW_IP:-$CN_GATEWAY}"
  domains="${CN_DOMAINS:-$CN_DEFAULT_DOMAINS}"
  title "国内机分流状态"
  kv "网关地址" "${ip:-未配置}"
  kv "分流域名" "$domains"
  if grep -qxF "$CN_HOSTS_BEGIN" /etc/hosts 2>/dev/null; then
    kv "hosts 托管块" "存在（$(grep -nF "$CN_HOSTS_BEGIN" /etc/hosts | head -n 1 | cut -d: -f1) 行起）"
  else
    kv "hosts 托管块" "不存在"
  fi
  if have systemctl; then
    kv "本地 DNS" "$(systemctl is-active "$CN_DNS_UNIT_NAME" 2>/dev/null || true)"
    kv "看门狗" "$(systemctl is-active "$CN_WATCH_UNIT_NAME.timer" 2>/dev/null || true)"
  fi
  if [ -f /etc/resolv.conf ]; then
    kv "resolv.conf" "$(awk '$1 == "nameserver" {printf "%s ", $2}' /etc/resolv.conf | sed 's/ $//')"
  fi
  if [ -f "$CN_WATCH_STATE" ]; then
    kv "看门狗状态" "$(tr '\n' ' ' < "$CN_WATCH_STATE" | sed 's/ $//')"
  fi
  if [ -d "$CN_SPLIT_DIR" ]; then
    kv "配置目录" "$CN_SPLIT_DIR"
  fi
  if [ -d "$(cn_backup_root)" ]; then
    kv "备份数量" "$(cn_backup_count)"
  fi
  kv "恢复命令" "sudo warden-cn cn-restore"
}

cn_restore() {
  need_root
  title "恢复国内机设置"
  local count id dir
  count="$(cn_backup_count)"
  if [ "$count" -eq 0 ]; then
    warn "未找到国内机设置备份：$(cn_backup_root)"
    return 1
  fi
  id="$CN_RESTORE_ID"
  if [ -z "$id" ] || [ "$id" = "latest" ]; then
    id="$(cn_latest_backup)"
  fi
  [ -n "$id" ] || die "未找到可恢复的备份"
  dir="$(cn_backup_root)/$id"
  [ -d "$dir" ] || die "备份不存在：$id"
  kv "备份" "$id"
  kv "位置" "$dir"
  confirm "确认恢复该备份？" || { say "已取消"; return 1; }
  # agent 模式先停用并清理 agent（停止本地 DNS、删除 agent 文件），再走
  # 快照还原（含 resolv.conf / hosts 的迁移前内容），完整回到迁移前状态。
  if [ -f "$CN_AGENT_CONFIG" ]; then
    step "agent" "停用并清理 warden-cn-agent"
    cn_agent_teardown
  fi
  cn_restore_one "$dir"
  ok "恢复完成"
  say "如需重启面板服务，请执行 systemctl restart warden-gateway。"
}


# ---------------------------------------------------------------------------
# 交互安装向导
# ---------------------------------------------------------------------------

interactive_network_options() {
  [ "$INTERACTIVE_SETUP" -eq 1 ] || return 0
  if [ "$NON_INTERACTIVE" -eq 1 ] || ! is_tty; then return 0; fi
  local answer default_tls default_entry default_acme default_https
  title "高级选项"
  say "直接回车使用方括号中的默认值。"
  PANEL_BASE_PATH="$(ask "面板路径" "$PANEL_BASE_PATH")"
  if is_ipv4 "$DOMAIN"; then
    default_tls="self"; default_entry=""; default_acme=""; default_https="0.0.0.0:443"
  else
    default_tls="auto"; default_entry="0.0.0.0:443"; default_acme="0.0.0.0:80"; default_https="127.0.0.1:8443"
  fi
  if [ "$EXPLICIT_TLS" -eq 0 ]; then
    say "证书方式：1) 自动申请 2) 自签名 3) 已有证书文件"
    answer="$(ask "选择证书方式" "1")"
    case "$answer" in
      2) TLS_MODE="self";;
      3) TLS_MODE="files"; CERT_FILE="$(ask "证书文件路径" "$CERT_FILE")"; KEY_FILE="$(ask "私钥文件路径" "$KEY_FILE")";;
      *) TLS_MODE="$default_tls";;
    esac
  fi
  if [ "$EXPLICIT_LISTEN" -eq 0 ]; then
    HTTP_LISTEN="$(ask "本地 HTTP 监听" "$HTTP_LISTEN")"
    HTTPS_LISTEN="$(ask "面板 HTTPS 监听" "$default_https")"
    if [ "$NO_SNI" -eq 0 ]; then
      ENTRY_LISTEN="$(ask "SNI 入口监听" "$default_entry")"
    fi
    if [ "$TLS_MODE" = "auto" ]; then
      ACME_LISTEN="$(ask "ACME HTTP 监听" "$default_acme")"
    fi
  fi
}

interactive_install() {
  [ "$INTERACTIVE_SETUP" -eq 1 ] || return 0
  [ "$NON_INTERACTIVE" -eq 0 ] || return 0
  [ "$ASSUME_YES" -eq 0 ] || return 0
  is_tty || return 0
  local mode domain email
  title "安装向导"
  say "直接回车使用方括号中的默认值。"
  if [ "$EXPLICIT_MODE" -eq 0 ]; then
    say "安装方式：1) 二进制 2) Docker"
    mode="$(ask "选择安装方式" "1")"
    case "$mode" in
      2|docker|Docker) MODE="docker";;
      *) MODE="binary";;
    esac
  fi
  if [ -z "$DOMAIN" ] && [ -z "$PUBLIC_URL" ]; then
    title "面板地址"
    say "填写域名可直接申请 Let's Encrypt 证书；留空使用服务器 IP 和自签名证书。"
    domain="$(ask "面板域名" "")"
    [ -n "$domain" ] && DOMAIN="$domain"
  fi
  if [ "$EXPLICIT_TLS" -eq 0 ] || [ -z "$EMAIL" ]; then
    while true; do
      email="$(ask "ACME 邮箱（可留空）" "$EMAIL")"
      if [ -z "$email" ] || is_email "$email"; then break; fi
      warn "邮箱格式不正确，请重新输入。"
    done
    [ -n "$email" ] && EMAIL="$email"
  fi
  if [ "$EXPLICIT_DOCKER" -eq 0 ]; then
    if ask_yes "启用 Docker 镜像代理？" 1; then NO_DOCKER=0; else NO_DOCKER=1; fi
  fi
  if [ "$EXPLICIT_GITHUB" -eq 0 ]; then
    if ask_yes "启用 GitHub 下载代理？" 1; then NO_GITHUB=0; else NO_GITHUB=1; fi
  fi
  if [ "$EXPLICIT_SNI" -eq 0 ]; then
    if ask_yes "启用 SNI 统一入口（443 按域名分流）？" 1; then NO_SNI=0; else NO_SNI=1; fi
  fi
  if [ "$EXPLICIT_DNS" -eq 0 ]; then
    if ask_yes "启用 DNS 分流（监听 53 端口）？" 0; then
      DNS_ENABLE=1
      local gw
      gw="$(ask "DNS 返回的网关 IP" "${DNS_GATEWAY:-${GATEWAY_IP:-}}")"
      [ -n "$gw" ] && DNS_GATEWAY="$gw"
    else
      DNS_ENABLE=0
    fi
  fi
  interactive_network_options
}

install_summary() {
  local docker_state github_state sni_state dns_state
  if [ "$NO_DOCKER" -eq 1 ]; then docker_state="未启用"; else docker_state="启用"; fi
  if [ "$NO_GITHUB" -eq 1 ]; then github_state="未启用"; else github_state="启用"; fi
  if [ "$NO_SNI" -eq 1 ]; then sni_state="未启用"; else sni_state="启用"; fi
  if [ "$DNS_ENABLE" -eq 1 ]; then dns_state="启用 ${DNS_LISTEN}"; else dns_state="未启用"; fi
  kv "安装方式" "$MODE"
  kv "Docker 镜像代理" "$docker_state"
  kv "GitHub 下载代理" "$github_state"
  kv "SNI 入口" "$sni_state"
  kv "DNS 分流" "$dns_state"
  kv "面板路径" "$PANEL_BASE_PATH"
  kv "证书方式" "$TLS_MODE"
  kv "面板监听" "$HTTPS_LISTEN"
  kv "SNI 监听" "${ENTRY_LISTEN:-未启用}"
}

# ---------------------------------------------------------------------------
# 一键诊断
# ---------------------------------------------------------------------------

doctor_ok() { printf '  %s[正常]%s %s %s\n' "$C_GREEN" "$C_RESET" "$(pad_to "$1" 18)" "$2"; }
doctor_warn() { printf '  %s[注意]%s %s %s\n' "$C_YELLOW" "$C_RESET" "$(pad_to "$1" 18)" "$2"; }
doctor_fail() { printf '  %s[异常]%s %s %s\n' "$C_RED" "$C_RESET" "$(pad_to "$1" 18)" "$2"; }
doctor_skip() { printf '  %s[跳过]%s %s %s\n' "$C_DIM" "$C_RESET" "$(pad_to "$1" 18)" "$2"; }

doctor_project() {
  local domain ip url out
  title "项目自身"
  kv "架构" "$(detect_arch)"
  ip="$(detect_ip)"
  if [ -n "$ip" ]; then kv "公网 IP" "$ip"; else kv "公网 IP" "未知"; fi
  if [ -f "$CONFIG_FILE" ]; then
    doctor_ok "配置文件" "$CONFIG_FILE"
  else
    doctor_fail "配置文件" "缺少 $CONFIG_FILE（未安装？）"
    return 1
  fi
  if out="$(run_bin check --config "$CONFIG_FILE" 2>&1)"; then
    doctor_ok "配置校验" "$(printf '%s' "$out" | head -n 1)"
  else
    doctor_fail "配置校验" "$(printf '%s' "$out" | head -n 2 | tr '\n' ' ')"
  fi
  if have systemctl; then
    if systemctl is-active --quiet "$SERVICE"; then
      doctor_ok "服务状态" "active"
    else
      doctor_fail "服务状态" "未运行"
    fi
  fi
  url="$(config_public_url)"
  if [ -n "$url" ]; then
    if curl -kfsS --connect-timeout 3 --max-time 8 "$url/api/health" >/dev/null 2>&1; then
      doctor_ok "面板接口" "$url/api/health"
    else
      doctor_fail "面板接口" "$url/api/health 无响应"
    fi
  fi
  domain="$(sed -n 's/.*"domain": "\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -n 1)"
  if [ -n "$domain" ] && ! is_ipv4 "$domain" && have getent; then
    local resolved
    resolved="$(getent ahostsv4 "$domain" | awk '{print $1}' | head -n 1)"
    if [ -z "$resolved" ]; then
      doctor_fail "域名解析" "$domain 解析失败"
    elif [ "$resolved" = "$ip" ]; then
      doctor_ok "域名解析" "$domain -> $resolved"
    else
      doctor_warn "域名解析" "$domain -> $resolved（本机 $ip）"
    fi
  fi
  if have ss; then
    local ports
    ports="$(ss -lntH 2>/dev/null | awk '{print $4}' | grep -Eo ':(80|443|8080|8443)$' | sort -u | tr '\n' ' ')"
    if [ -n "$ports" ]; then doctor_ok "端口监听" "$ports"; else doctor_warn "端口监听" "未发现 80/443/8080/8443"; fi
  fi
  return 0
}

cmd_doctor() {
  need_root
  title "一键诊断"
  doctor_project || true
  title "诊断完成"
  say "如某项异常，按提示处理后重新执行 warden doctor。"
}


cmd_install() {
  need_root
  if [ "$(uname -s)" != "Linux" ] && [ "$DRY_RUN" -eq 0 ]; then die "仅支持 Linux"; fi
  title "Warden Gateway 安装"
  have curl || have wget || die "需要 curl 或 wget"
  if ! have systemctl; then
    if [ "$DRY_RUN" -eq 1 ]; then warn "未找到 systemd，预览继续"; else die "需要 systemd"; fi
  fi
  interactive_install
  validate_options
  resolve_config_args
  title "安装摘要"
  kv "面板地址" "$PUBLIC_URL"
  kv "域名" "$DOMAIN"
  kv "配置目录" "$CONFIG_DIR"
  kv "数据目录" "$DATA_DIR"
  install_summary
  if [ "$ASSUME_YES" -eq 0 ] && [ "$NON_INTERACTIVE" -eq 0 ] && is_tty; then
    confirm "确认安装？" || { say "已取消"; exit 0; }
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    title "安装计划"
    kv "二进制" "$BIN_PATH"
    kv "服务单元" "$UNIT_PATH"
    if [ "$MODE" = "docker" ]; then kv "镜像" "ghcr.io/$REPO:$( [ "$VERSION" = "latest" ] && printf latest || printf '%s' "$VERSION" )"; fi
    say "预览完成，未修改系统。"
    exit 0
  fi
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  chmod 0700 "$CONFIG_DIR" "$DATA_DIR"
  if [ "$MODE" = "docker" ]; then
    install_scripts
    install_cli
    pull_docker_image
    init_docker_config
    run_docker_container
    # Prefetch after the service is running: three downloads can take minutes
    # and must never extend the install/upgrade downtime window (F7). Docker
    # installs have no host binary/version file, so resolve the running
    # gateway's version first (R2).
    cn_docker_cache_agent || true
  else
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    install_binary
    init_config
    install_scripts
    install_cli
    write_version_from_binary
    install_unit
    install_logrotate
    install_service
    cache_agent_binaries || true
  fi
  if wait_health "$PUBLIC_URL"; then
    ok "健康检查通过"
  else
    journalctl -u "$SERVICE" -n 40 --no-pager || true
    die "健康检查未通过"
  fi
  ensure_certificate "$DOMAIN" "$PUBLIC_URL" || true
  title "安装完成"
  kv "面板地址" "$PUBLIC_URL"
  if [ -n "$NEW_ACCESS_KEY" ]; then
    kv "访问密钥" "$NEW_ACCESS_KEY"
    say "请保存访问密钥；登录后可在设置中修改。"
  elif [ -n "$ACCESS_KEY" ]; then
    say "访问密钥使用 install --access-key 指定的值。"
  else
    say "已保留原有配置和访问密钥。"
  fi
  title "维护命令"
  kv "维护命令" "$CLI_PATH"
  say "常用操作：sudo warden status | doctor | logs | backup | restore | update"
  local cn_url cn_script
  cn_url="$(script_public_url || true)"
  cn_script="$SCRIPT_DIR/bootstrap.sh"
  if [ -n "$cn_url" ]; then
    say "国内机设置：在面板设置页复制生成好的命令，或先执行"
    kv "下载脚本" "curl -fsSL $cn_url -o /tmp/wg-bootstrap.sh"
    kv "再执行" "sudo bash /tmp/wg-bootstrap.sh cn-setup"
  else
    say "国内机设置：把 $cn_script 复制到国内机后执行"
    kv "执行" "sudo bash bootstrap.sh cn-setup"
  fi
}

cmd_update() {
  need_root
  [ -f "$CONFIG_FILE" ] || die "未安装，请先执行 install"
  detect_installed_mode
  if [ "${NW_UPDATE_REEXEC:-0}" != "1" ] && prepare_update_scripts; then
    info "脚本" "使用仓库最新脚本执行升级"
    NW_UPDATE_REEXEC=1 UPDATE_SCRIPT_DIR="$UPDATE_SCRIPT_DIR" exec bash "$UPDATE_SCRIPT_DIR/bootstrap.sh" "${ORIG_ARGS[@]}"
  fi
  if [ "${NW_UPDATE_REEXEC:-0}" = "1" ] && [ -n "${UPDATE_SCRIPT_DIR:-}" ]; then
    trap 'rm -rf "$UPDATE_SCRIPT_DIR"' EXIT
  fi
  title "Warden Gateway 升级"
  if [ -z "$PUBLIC_URL" ]; then PUBLIC_URL="$(config_public_url)"; fi
  if [ "$MODE" = "docker" ]; then
    install_scripts
    install_cli
    pull_docker_image
    run_docker_container
    # Same as install: only the running container knows its version; prefetch
    # after it is up so the cache never holds a host binary's version (R2).
    cn_docker_cache_agent || true
  else
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    install_binary
    install_scripts
    install_cli
    write_version_from_binary
    install_unit
    install_logrotate
    systemctl daemon-reload
    systemctl start "$SERVICE"
    # Prefetch only after the gateway is back up: the cache downloads must not
    # sit inside the stopped-service window (F7).
    cache_agent_binaries || true
  fi
  sleep 2
  if [ -n "$PUBLIC_URL" ] && wait_health "$PUBLIC_URL" 1; then ok "升级完成"; else warn "升级后健康检查未通过"; fi
}

cmd_uninstall() {
  need_root
  title "Warden Gateway 卸载"
  confirm "确认卸载？配置和数据默认保留。" || { say "已取消"; exit 0; }
  if [ "$DRY_RUN" -eq 0 ]; then
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    systemctl disable "$SERVICE" >/dev/null 2>&1 || true
    docker rm -f "$SERVICE" >/dev/null 2>&1 || true
    rm -f "$UNIT_PATH" "$LOGROTATE_PATH" "$BIN_PATH" "$CLI_PATH" "$CN_CLI_PATH"
    systemctl daemon-reload
  fi
  if [ "$PURGE" -eq 1 ]; then
    confirm "同时删除配置、数据和备份？" || true
    if [ "$DRY_RUN" -eq 0 ]; then rm -rf "$CONFIG_DIR" "$DATA_DIR"; fi
  fi
  ok "卸载完成"
}

cmd_status() {
  title "Warden Gateway 状态"
  detect_installed_mode
  if have systemctl; then
    systemctl is-active "$SERVICE" 2>/dev/null || true
    systemctl show "$SERVICE" -p ActiveState,SubState,ExecMainPID --no-page 2>/dev/null || true
  fi
  if have docker; then
    docker inspect -f '{{.State.Status}} {{.Config.Image}}' "$SERVICE" 2>/dev/null || true
  fi
  if [ -f "$CONFIG_FILE" ]; then
    if [ -x "$BIN_PATH" ]; then "$BIN_PATH" check --config "$CONFIG_FILE" || true
    elif installed_docker; then docker exec "$SERVICE" /usr/local/bin/warden-gateway check --config "$CONFIG_FILE" || true; fi
    local url
    url="$(sed -n 's/.*"public_url": "\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -n 1)"
    [ -n "$url" ] && curl -kfsS --connect-timeout 3 --max-time 5 "$url/api/health" 2>/dev/null && say ""
  else
    warn "未找到配置 $CONFIG_FILE"
  fi
}

cmd_backup() {
  need_root
  detect_installed_mode
  run_bin backup --config "$CONFIG_FILE" --data-dir "$DATA_DIR"
}

cmd_restore() {
  need_root
  detect_installed_mode
  title "恢复备份"
  run_bin backups --config "$CONFIG_FILE" --data-dir "$DATA_DIR" | tail -n 12
  confirm "恢复备份 $RESTORE_ID？" || { say "已取消"; exit 0; }
  run_bin restore --config "$CONFIG_FILE" --data-dir "$DATA_DIR" --id "$RESTORE_ID"
  ok "恢复完成"
}


cmd_logs() {
  title "最近日志"
  detect_installed_mode
  if [ "$MODE" = "docker" ]; then
    docker logs --tail 100 "$SERVICE" 2>&1 || true
  elif have journalctl; then
    journalctl -u "$SERVICE" -n 100 --no-pager || true
  elif [ -f "$DATA_DIR/gateway.log" ]; then
    tail -n 100 "$DATA_DIR/gateway.log"
  else
    warn "未找到日志来源"
  fi
}

cmd_version() {
  title "版本"
  if [ -x "$BIN_PATH" ]; then "$BIN_PATH" version || true
  elif installed_docker; then docker exec "$SERVICE" /usr/local/bin/warden-gateway version || true
  fi
  if [ -f "$SCRIPT_DIR/VERSION" ]; then kv "脚本版本" "$(cat "$SCRIPT_DIR/VERSION")"; fi
  if [ -f "$CONFIG_FILE" ]; then kv "配置文件" "$CONFIG_FILE"; fi
}

cmd_self_test() {
  title "自检"
  detect_installed_mode
  run_bin check --config "$CONFIG_FILE"
  local url
  url="$(sed -n 's/.*"public_url": "\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -n 1)"
  [ -n "$url" ] || die "配置缺少 public_url"
  curl -kfsS --connect-timeout 3 --max-time 5 "$url/api/health" && say ""
  ok "自检通过"
}

cmd_singbox() {
  need_root
  title "sing-box 内核"
  if [ "${#SINGBOX_ARGS[@]}" -eq 0 ]; then
    run_bin singbox status --config "$CONFIG_FILE"
  else
    run_bin singbox "${SINGBOX_ARGS[@]}" --config "$CONFIG_FILE"
  fi
}

cn_only_allowed() {
  case "$1" in
    cn-setup|cn-restore|cn-check|cn-status|version|help|-h|--help|"") return 0;;
  esac
  return 1
}

main() {
  ORIG_ARGS=("$@")
  case "$(basename "${BASH_SOURCE[0]:-$0}")" in
    warden-cn) CN_ONLY=1;;
  esac
  if [ "$CN_ONLY" -eq 1 ]; then
    if [ "$#" -eq 0 ]; then
      usage_cn
      exit 0
    fi
    if ! cn_only_allowed "${1:-}"; then
      printf 'warden-cn 是国内机维护命令，只能执行：cn-setup、cn-check、cn-status、cn-restore。\n' >&2
      printf '安装、更新、卸载网关请到代理机上使用 warden 命令。\n' >&2
      exit 2
    fi
  fi
  parse_args "$@"
  if [ "$CN_ONLY" -eq 1 ]; then
    # 选项解析后 COMMAND 可能仍是默认值 install，国内机副本一律拒绝。
    case "$COMMAND" in
      cn-setup|cn-check|cn-status|cn-restore|version) ;;
      *)
        printf 'warden-cn 不能执行 %s：这是代理机操作。\n' "$COMMAND" >&2
        printf '国内机可用：cn-setup、cn-check、cn-status、cn-restore。\n' >&2
        exit 2;;
    esac
  fi
  case "$COMMAND" in
    install) cmd_install;;
    update) cmd_update;;
    uninstall) cmd_uninstall;;
    status) cmd_status;;
    logs) cmd_logs;;
    version) cmd_version;;
    backup) cmd_backup;;
    restore) cmd_restore;;
    doctor) cmd_doctor;;
    cn-setup) cn_setup;;
    cn-check) cn_check;;
    cn-status) cn_status;;
    cn-restore) cn_restore;;
    singbox) cmd_singbox;;
    self-test) cmd_self_test;;
    *) usage; exit 2;;
  esac
}

main "$@"
