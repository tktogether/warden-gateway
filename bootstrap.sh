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
CN_DEFAULT_DOMAINS="github.com,api.github.com,codeload.github.com,raw.githubusercontent.com,objects.githubusercontent.com,gist.githubusercontent.com,ghcr.io,registry-1.docker.io,auth.docker.io,production.cloudfront.docker.com"

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
  --gateway-ip IP        国内机指向的网关地址
  --no-hosts             不写 /etc/hosts 托管块
  --no-local-dns         不安装本地 dnsmasq（容器将拿不到网关地址，不建议）
  --local-dns-addr IP    本地 dnsmasq 监听地址，默认 198.18.0.1
  --local-dns-upstream A 本地 dnsmasq 上游，逗号分隔，默认沿用本机现有 DNS 与 223.5.5.5
  --no-nm-guard          不写 NetworkManager dns=none（resolv.conf 可能被重写）
  --no-watchdog          不安装看门狗定时器
  --watch-interval SEC   看门狗探测周期，默认 60
  --no-apt               不修改 APT 镜像
  --no-bbr               不启用 BBR
  --no-docker-mirror     不修改 Docker 镜像
  --no-git               不修改 Git 镜像
  --no-apt-update        不执行 apt-get update
  --apt-mirror URL       APT 镜像地址，默认 https://mirrors.aliyun.com
  --docker-mirror URL    Docker 镜像地址，默认使用网关地址
  --git-mirror URL       Git 镜像地址，默认使用网关地址
  --cn-domains LIST      参与分流的域名，逗号分隔
  --cn-mode MODE         auto（默认，本地 DNS + hosts）或 none（不启用分流）
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
  --gateway-ip IP          代理机地址（必填或自动探测本机出口 IP）
  --cn-domains LIST        参与分流的域名，逗号分隔
  --cn-mode MODE           auto（默认）或 none
  --no-hosts               不写 /etc/hosts 托管块
  --no-local-dns           不装本地 dnsmasq（容器将无法解析分流域名）
  --local-dns-addr IP      本地 DNS 监听地址，默认 198.18.0.1
  --local-dns-upstream LIST 本地 DNS 上游，逗号分隔，默认沿用本机 resolver
  --no-nm-guard            不写 NetworkManager 的 dns=none 配置
  --no-watchdog            不安装看门狗
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
    changed=1
    cn_snapshot_file "$file"
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  + 替换 %s 的源地址为 %s\n' "$file" "$mirror"
    else
      sed -E -i "s#https?://[A-Za-z0-9._:-]+#${mirror}#g" "$file"
    fi
  done
  [ "$changed" -eq 1 ] || { warn "未找到可替换的 APT 源地址"; return 1; }
  ok "APT 源已替换为 $mirror"
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

cn_setup() {
  need_root
  title "国内机设置"
  validate_options
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

# cn_check 只读体检：探测代理机、检查 hosts 块 / 本地 DNS / 看门狗状态，不改任何文件。
cn_check() {
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

# cn_status 打印当前分流配置快照（只读）。
cn_status() {
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
