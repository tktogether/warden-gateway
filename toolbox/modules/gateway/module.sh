#!/usr/bin/env bash
# Warden Gateway 功能目录：调用仓库根目录的 bootstrap.sh。

if [[ -z "${TOOLBOX_ROOT:-}" ]]; then
  TOOLBOX_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
  export TOOLBOX_ROOT
fi
# shellcheck source=/dev/null
source "$TOOLBOX_ROOT/toolbox/lib/common.sh"

MODULE_ID="gateway"
MODULE_TITLE="Warden Gateway"
MODULE_DESCRIPTION="安装、升级、维护、备份恢复和卸载统一网关、面板、GitHub 与 Docker 代理。"
BOOTSTRAP="$TOOLBOX_ROOT/bootstrap.sh"

module_list() {
  printf 'Warden Gateway 操作：\n'
  printf '  %-14s %s\n' "install" "从零安装或重新配置"
  printf '  %-14s %s\n' "update" "升级程序"
  printf '  %-14s %s\n' "status" "显示服务、端口和健康状态"
  printf '  %-14s %s\n' "logs" "查看最近日志"
  printf '  %-14s %s\n' "version" "查看已安装版本"
  printf '  %-14s %s\n' "backup" "立即创建备份"
  printf '  %-14s %s\n' "restore" "恢复最新或指定备份"
  printf '  %-14s %s\n' "doctor" "一键诊断（仅网关自身）"
  printf '  %-14s %s\n' "uninstall" "卸载程序"
  printf '  %-14s %s\n' "remote" "显示远程运行命令"
  printf '\n国内机设置不再由维护命令执行：安装完成后在面板或诊断页复制生成的命令，到国内机上运行 bootstrap.sh cn-setup。\n'
}

module_run() {
  local action="${1:-}"
  if [[ $# -gt 0 ]]; then
    shift
  fi
  case "$action" in
    ""|list)
      module_list
      ;;
    install|update|status|logs|version|uninstall|backup|restore|doctor|self-test)
      if [[ ! -f "$BOOTSTRAP" ]]; then
        die "未找到 bootstrap.sh：$BOOTSTRAP"
      fi
      require_root
      exec bash "$BOOTSTRAP" "$action" "$@"
      ;;
    remote)
      local url
      printf '远程运行命令（按可用性选择）：\n'
      while IFS= read -r url; do
        printf '  curl -fsSL %s -o /tmp/warden-toolbox.sh && sudo bash /tmp/warden-toolbox.sh\n' "$url"
      done < <(github_url_candidates 'https://raw.githubusercontent.com/tktogether/warden-gateway/main/toolbox.sh')
      printf '\n安装：执行 /tmp/warden-toolbox.sh 后选择 1，或直接执行 sudo bash /tmp/warden-toolbox.sh install。\n'
      ;;
    help|--help|-h)
      module_list
      ;;
    *)
      die "未知操作：$action"
      ;;
  esac
}

module_menu() {
  local choice
  while true; do
    printf '\nWarden Gateway\n'
    printf '  1) install      从零安装或重新配置\n'
    printf '  2) update       升级程序\n'
    printf '  3) status       查看状态\n'
    printf '  4) logs         查看最近日志\n'
    printf '  5) version      查看已安装版本\n'
    printf '  6) backup       立即备份\n'
    printf '  7) restore      恢复备份\n'
    printf '  8) doctor       一键诊断\n'
    printf '  9) uninstall    卸载\n'
    printf ' 10) remote       远程运行命令\n'
    printf '  0) 返回\n'
    read -r -p '选择: ' choice || return 0
    case "$choice" in
      0|q) return 0 ;;
      1) module_run install ;;
      2) module_run update ;;
      3) module_run status ;;
      4) module_run logs ;;
      5) module_run version ;;
      6) module_run backup ;;
      7) module_run restore ;;
      8) module_run doctor ;;
      9) module_run uninstall ;;
     10) module_run remote ;;
      *) warn "无效选择。" ;;
    esac
  done
}
