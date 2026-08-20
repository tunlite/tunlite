#!/bin/sh
# 安装 Tunlite v1 单文件二进制；不会注册或启动任何 Connection。
set -eu

main() {
  version="${TUNLITE_VERSION:-v1.0.0-preview.1}"
  install_dir="${TUNLITE_INSTALL_DIR:-${HOME}/.local/bin}"
  repository="https://github.com/yuanyuanzijin/tunlite"

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version)
        [ "$#" -ge 2 ] || { echo "tunlite: --version 需要版本号" >&2; exit 2; }
        version="$2"
        shift 2
        ;;
      --install-dir)
        [ "$#" -ge 2 ] || { echo "tunlite: --install-dir 需要目录" >&2; exit 2; }
        install_dir="$2"
        shift 2
        ;;
      *)
        echo "tunlite: 未知参数 $1" >&2
        exit 2
        ;;
    esac
  done

  case "$version" in
    v1.0.0) ;;
    v1.0.0-preview.*)
      preview_number="${version#v1.0.0-preview.}"
      case "$preview_number" in
        ''|0|0*|*[!0-9]*) echo "tunlite: 不支持的版本格式 $version" >&2; exit 2 ;;
      esac
      ;;
    *) echo "tunlite: 不支持的版本格式 $version" >&2; exit 2 ;;
  esac

  case "$(uname -s)" in
    Darwin)
      case "$(uname -m)" in
        x86_64|arm64) ;;
        *) echo "tunlite: 不支持的 Mac CPU 架构 $(uname -m)" >&2; exit 1 ;;
      esac
      asset_suffix="macos"
      ;;
    Linux)
      case "$(uname -m)" in
        x86_64|amd64) target_arch="x64" ;;
        arm64|aarch64) target_arch="arm64" ;;
        *) echo "tunlite: 不支持的 CPU 架构 $(uname -m)" >&2; exit 1 ;;
      esac
      asset_suffix="linux_${target_arch}"
      ;;
    *) echo "tunlite: 当前安装脚本仅支持 macOS 和 Linux" >&2; exit 1 ;;
  esac

  plain_version="${version#v}"
  asset="tunlite_${plain_version}_${asset_suffix}.tar.gz"
  base_url="${TUNLITE_RELEASE_BASE_URL:-${repository}/releases/download/${version}}"

  if [ -n "${TUNLITE_HOME:-}" ]; then
    legacy_config="${TUNLITE_HOME}/config/config.json"
    legacy_manifest="${TUNLITE_HOME}/data/install.json"
  else
    legacy_config="${XDG_CONFIG_HOME:-${HOME}/.config}/tunlite/config.json"
    legacy_manifest="${XDG_STATE_HOME:-${HOME}/.local/state}/tunlite/install.json"
  fi
  if [ -f "$legacy_config" ] || [ -f "$legacy_manifest" ]; then
    echo "tunlite: 检测到 0.x 配置或安装记录，安装脚本不会直接覆盖旧版本。" >&2
    echo "tunlite: 请下载 v1 原生二进制到临时路径，并按照升级说明导入旧配置：" >&2
    echo "https://github.com/yuanyuanzijin/tunlite/blob/master/docs/UPGRADE_FROM_0_X.md" >&2
    exit 1
  fi

  temporary="$(mktemp -d)"
  install_temporary=""
  trap 'rm -rf "$temporary"; if [ -n "$install_temporary" ]; then rm -f "$install_temporary"; fi' EXIT HUP INT TERM

  command -v curl >/dev/null 2>&1 || { echo "tunlite: 需要 curl" >&2; exit 1; }
  curl -fsSL "${base_url}/${asset}" -o "${temporary}/${asset}"
  curl -fsSL "${base_url}/SHA256SUMS" -o "${temporary}/SHA256SUMS"

  expected="$(awk -v name="$asset" '$2 == name { print $1 }' "${temporary}/SHA256SUMS")"
  [ -n "$expected" ] || { echo "tunlite: SHA256SUMS 中没有 $asset" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${temporary}/${asset}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${temporary}/${asset}" | awk '{print $1}')"
  else
    echo "tunlite: 需要 sha256sum 或 shasum 来校验下载内容" >&2
    exit 1
  fi
  [ "$actual" = "$expected" ] || { echo "tunlite: 下载文件 checksum 不匹配" >&2; exit 1; }

  tar -xzf "${temporary}/${asset}" -C "$temporary" tunlite
  mkdir -p "$install_dir"
  install_temporary="${install_dir}/.tunlite-install-$$"
  install -m 0755 "${temporary}/tunlite" "$install_temporary"
  "$install_temporary" version
  mv -f "$install_temporary" "${install_dir}/tunlite"
  install_temporary=""
  echo "tunlite: 已安装到 ${install_dir}/tunlite"
  case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) echo "tunlite: 请把 ${install_dir} 加入 PATH" ;;
  esac
}

main "$@"
