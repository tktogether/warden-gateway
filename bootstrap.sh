#!/usr/bin/env bash
# Warden Gateway one-click installer.
set -eo pipefail

SERVICE="warden-gateway"
BIN_PATH="/usr/local/bin/warden-gateway"
CONFIG_DIR="/etc/warden-gateway"
CONFIG_FILE="$CONFIG_DIR/config.json"
DATA_DIR="/var/lib/warden-gateway"
UNIT_PATH="/etc/systemd/system/$SERVICE.service"
LOGROTATE_PATH="/etc/logrotate.d/$SERVICE"
PANEL_PATH="/warden"
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
NEW_ACCESS_KEY=""
ACCESS_KEY=""
IMAGE=""
CONFIG_ARGS=""

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
kv() { printf '  %-16s %s\n' "$1" "$2"; }
is_tty() { [ -t 0 ] && [ -t 1 ]; }
have() { command -v "$1" >/dev/null 2>&1; }
is_ipv4() {
  case "$1" in
    *[!0-9.]*|"") return 1;;
  esac
  [ "$(printf '%s' "$1" | awk -F. 'NF==4 && $1<=255 && $2<=255 && $3<=255 && $4<=255 {print "ok"}')" = "ok" ]
}
need_root() { [ "$(id -u)" -eq 0 ] || die "请使用 root 运行"; }
confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  [ "$NON_INTERACTIVE" -eq 1 ] && return 0
  [ ! -t 0 ] && return 0
  local answer
  printf '%s [Y/n] ' "$1"
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
    printf '%s [%s]: ' "$prompt" "$default"
  else
    printf '%s: ' "$prompt"
  fi
  read -r answer || true
  if [ -z "$answer" ]; then answer="$default"; fi
  printf '%s\n' "$answer"
}
run() {
  if [ "$DRY_RUN" -eq 1 ]; then printf '  + %s\n' "$*"; return 0; fi
  "$@"
}

usage() {
  cat <<'USAGE'
Warden Gateway 一键安装脚本

用法：
  bootstrap.sh [install] [选项]     安装或升级
  bootstrap.sh update [选项]        升级程序（保留配置和数据）
  bootstrap.sh uninstall [--purge]  卸载
  bootstrap.sh status               查看状态
  bootstrap.sh backup               立即备份
  bootstrap.sh restore [--id ID]    恢复备份（默认最新）
  bootstrap.sh doctor               检查环境
  bootstrap.sh self-test            自检

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
  --yes                  自动确认
  --non-interactive      不进行交互
  --dry-run              只打印步骤
  --purge                卸载时同时删除配置、数据和备份
  --id ID                恢复指定备份，默认 latest
USAGE
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      install|update|uninstall|status|backup|restore|doctor|self-test)
        COMMAND="$1"; shift;;
      --domain) DOMAIN="$2"; shift 2;;
      --public-url) PUBLIC_URL="$2"; shift 2;;
      --email) EMAIL="$2"; shift 2;;
      --access-key) ACCESS_KEY="$2"; shift 2;;
      --binary) BINARY="$2"; shift 2;;
      --version) VERSION="$2"; shift 2;;
      --docker) MODE="docker"; shift;;
      --no-docker) NO_DOCKER=1; shift;;
      --no-github) NO_GITHUB=1; shift;;
      --no-sni) NO_SNI=1; shift;;
      --yes|-y) ASSUME_YES=1; shift;;
      --non-interactive) NON_INTERACTIVE=1; shift;;
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

resolve_config_args() {
  local host ip tls entry acme https_listen
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
      tls="self"
      entry=""
      acme=""
      https_listen="0.0.0.0:443"
      [ -n "$PUBLIC_URL" ] || PUBLIC_URL="https://$DOMAIN$PANEL_PATH"
    else
      tls="auto"
      entry="0.0.0.0:443"
      acme="0.0.0.0:80"
      https_listen="127.0.0.1:8443"
      [ -n "$PUBLIC_URL" ] || PUBLIC_URL="https://$DOMAIN$PANEL_PATH"
    fi
  else
    [ -n "$ip" ] || ip="$(ask "服务器公网 IP" "")"
    [ -n "$ip" ] || die "无法确定公网 IP，请指定 --domain 或 --public-url"
    DOMAIN="$ip"
    tls="self"
    entry=""
    acme=""
    https_listen="0.0.0.0:443"
    [ -n "$PUBLIC_URL" ] || PUBLIC_URL="https://$ip$PANEL_PATH"
  fi
  if [ "$NO_SNI" -eq 1 ]; then
    entry=""
    https_listen="0.0.0.0:443"
  fi
  CONFIG_ARGS="init --config $CONFIG_FILE --data-dir $DATA_DIR --public-url $PUBLIC_URL --domain $DOMAIN --tls $tls --listen-https $https_listen --listen-entry=$entry --listen-acme=$acme"
  if [ -n "$EMAIL" ]; then CONFIG_ARGS="$CONFIG_ARGS --email $EMAIL"; fi
  if [ -n "$ACCESS_KEY" ]; then CONFIG_ARGS="$CONFIG_ARGS --access-key $ACCESS_KEY"; fi
  if [ "$NO_DOCKER" -eq 1 ]; then CONFIG_ARGS="$CONFIG_ARGS --no-docker"; fi
  if [ "$NO_GITHUB" -eq 1 ]; then CONFIG_ARGS="$CONFIG_ARGS --no-github"; fi
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
    -v "$CONFIG_DIR:$CONFIG_DIR" -v "$DATA_DIR:$DATA_DIR" "$IMAGE" serve --config "$CONFIG_FILE" >/dev/null
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
  local url="$1" host i
  host="$(printf '%s' "$url" | sed 's#^[a-zA-Z][a-zA-Z0-9+.-]*://##' | cut -d/ -f1 | cut -d: -f1)"
  for i in $(seq 1 20); do
    if curl -kfsS --connect-timeout 2 --max-time 3 "$url/api/health" >/dev/null 2>&1; then return 0; fi
    case "$url" in
      https://*)
        if curl -kfsS --connect-timeout 2 --max-time 3 --resolve "$host:443:127.0.0.1" "$url/api/health" >/dev/null 2>&1; then return 0; fi;;
    esac
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

cmd_install() {
  need_root
  [ "$(uname -s)" = "Linux" ] || die "仅支持 Linux"
  title "Warden Gateway 安装"
  have curl || have wget || die "需要 curl 或 wget"
  have systemctl || die "需要 systemd"
  resolve_config_args
  say "面板地址：$PUBLIC_URL"
  say "域名：$DOMAIN"
  if [ "$MODE" = "binary" ]; then say "安装方式：二进制"; else say "安装方式：Docker"; fi
  if [ "$ASSUME_YES" -eq 0 ] && [ "$NON_INTERACTIVE" -eq 0 ] && is_tty; then
    confirm "确认安装？" || { say "已取消"; exit 0; }
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    title "安装计划"
    kv "面板地址" "$PUBLIC_URL"
    kv "域名" "$DOMAIN"
    kv "配置目录" "$CONFIG_DIR"
    kv "数据目录" "$DATA_DIR"
    kv "安装方式" "$MODE"
    say "预览完成，未修改系统。"
    exit 0
  fi
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  chmod 0700 "$CONFIG_DIR" "$DATA_DIR"
  if [ "$MODE" = "docker" ]; then
    pull_docker_image
    init_docker_config
    run_docker_container
  else
    [ "$DRY_RUN" -eq 0 ] && { systemctl stop "$SERVICE" >/dev/null 2>&1 || true; }
    install_binary
    init_config
    install_unit
    install_logrotate
    install_service
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
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
  else
    title "预览完成（未执行）"
  fi
}

cmd_update() {
  need_root
  [ -f "$CONFIG_FILE" ] || die "未安装，请先执行 install"
  detect_installed_mode
  title "Warden Gateway 升级"
  if [ -z "$PUBLIC_URL" ]; then PUBLIC_URL="$(config_public_url)"; fi
  if [ "$MODE" = "docker" ]; then
    pull_docker_image
    run_docker_container
  else
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    install_binary
    install_unit
    install_logrotate
    systemctl daemon-reload
    systemctl start "$SERVICE"
  fi
  sleep 2
  if [ -n "$PUBLIC_URL" ] && wait_health "$PUBLIC_URL"; then ok "升级完成"; else warn "升级后健康检查未通过"; fi
}

cmd_uninstall() {
  need_root
  title "Warden Gateway 卸载"
  confirm "确认卸载？配置和数据默认保留。" || { say "已取消"; exit 0; }
  if [ "$DRY_RUN" -eq 0 ]; then
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    systemctl disable "$SERVICE" >/dev/null 2>&1 || true
    docker rm -f "$SERVICE" >/dev/null 2>&1 || true
    rm -f "$UNIT_PATH" "$LOGROTATE_PATH" "$BIN_PATH"
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

cmd_doctor() {
  title "环境检查"
  local arch ip domain resolve
  arch="$(detect_arch)"
  ip="$(detect_ip)"
  kv "架构" "$arch"
  if [ -n "$ip" ]; then kv "公网 IP" "$ip"; else kv "公网 IP" "未知"; fi
  if have systemctl; then kv "systemd" "可用"; else kv "systemd" "缺失"; fi
  if have docker; then kv "docker" "可用"; else kv "docker" "未安装"; fi
  if [ -f "$CONFIG_FILE" ]; then
    domain="$(sed -n 's/.*"domain": "\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -n 1)"
    kv "域名" "$domain"
    if [ -n "$domain" ] && have getent; then
      resolve="$(getent ahostsv4 "$domain" | awk '{print $1}' | head -n 1)"
      if [ -n "$resolve" ]; then kv "解析" "$resolve"; else kv "解析" "失败"; fi
      if [ "$resolve" = "$ip" ]; then ok "域名已指向本机"; else warn "域名未指向本机"; fi
    fi
    if have systemctl; then
      if systemctl is-active --quiet "$SERVICE"; then ok "服务运行中"; else warn "服务未运行"; fi
    fi
    if have ss; then ss -lntp 2>/dev/null | grep -E ':(80|443|8443)\b' || true; fi
  else
    warn "尚未安装"
  fi
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

main() {
  parse_args "$@"
  case "$COMMAND" in
    install) cmd_install;;
    update) cmd_update;;
    uninstall) cmd_uninstall;;
    status) cmd_status;;
    backup) cmd_backup;;
    restore) cmd_restore;;
    doctor) cmd_doctor;;
    self-test) cmd_self_test;;
    *) usage; exit 2;;
  esac
}

main "$@"
