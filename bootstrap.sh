#!/bin/sh
# 安装 Tunlite v1 单文件二进制；不会注册或启动任何 Connection。
set -eu

main() {
  version="${TUNLITE_VERSION:-v1.0.0-preview.1}"
  install_dir="${TUNLITE_INSTALL_DIR:-${HOME}/.local/bin}"
  repository="https://github.com/tunlite/tunlite"

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

  temporary="$(mktemp -d)"
  install_temporary=""
  skill_temporary=""
  rollback="no"
  previous_binary="no"
  previous_skill="no"
  bundled_skill="no"
  binary_committed="no"
  skill_committed="no"
  cleanup() {
    result="$?"
    trap - EXIT HUP INT TERM
    recovery_failed="no"
    if [ "$rollback" = yes ]; then
      if [ "$binary_committed" = yes ]; then
        if [ "$previous_binary" = yes ]; then
          if ! mv -f "$temporary/previous-binary" "$install_dir/tunlite"; then recovery_failed="yes"; fi
        else
          if ! rm -f "$install_dir/tunlite"; then recovery_failed="yes"; fi
        fi
      fi
      if [ "$skill_committed" = yes ]; then
        if [ "$previous_skill" = yes ]; then
          if ! mv -f "$temporary/previous-skill" "$skill_file"; then recovery_failed="yes"; fi
        else
          if ! rm -f "$skill_file"; then recovery_failed="yes"; fi
        fi
      fi
    fi
    if [ "$recovery_failed" = yes ]; then
      echo "tunlite: 恢复失败；请手动恢复原文件，备份保留在 $temporary" >&2
      result=1
    else
      rm -rf "$temporary"
    fi
    if [ -n "$install_temporary" ]; then rm -f "$install_temporary"; fi
    if [ -n "$skill_temporary" ]; then rm -f "$skill_temporary"; fi
    exit "$result"
  }
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM

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

  tar -xOzf "${temporary}/${asset}" tunlite > "$temporary/tunlite"
  mkdir -p "$install_dir"
  # Resolve relative/custom directories before deriving the shared package prefix.
  install_dir="$(CDPATH= cd -- "$install_dir" && pwd)"
  skill_dir="$(dirname "$install_dir")/share/tunlite/skills/use-tunlite"
  skill_file="$skill_dir/SKILL.md"
  skill_entry="skills/use-tunlite/SKILL.md"
  entries="$(tar -tzf "${temporary}/${asset}")"
  if printf '%s\n' "$entries" | grep -Fx "$skill_entry" >/dev/null; then
    skill_mode="$(tar -tvzf "${temporary}/${asset}" "$skill_entry" | awk '{print $1}')"
    [ "$skill_mode" = '-rw-r--r--' ] || { echo "tunlite: Skill 归档条目必须是 0644 普通文件" >&2; exit 1; }
    tar -xOzf "${temporary}/${asset}" "$skill_entry" > "$temporary/SKILL.md"
    bundled_skill="yes"
  fi
  [ ! -d "$install_dir/tunlite" ] || { echo "tunlite: 二进制目标是目录" >&2; exit 1; }
  install_temporary="${install_dir}/.tunlite-install-$$"
  install -m 0755 "${temporary}/tunlite" "$install_temporary"
  "$install_temporary" version
  if [ "$bundled_skill" = yes ]; then
    mkdir -p "$skill_dir"
    [ ! -d "$skill_file" ] || { echo "tunlite: Skill 目标是目录" >&2; exit 1; }
    skill_temporary="$skill_dir/.SKILL-install-$$"
    install -m 0644 "$temporary/SKILL.md" "$skill_temporary"
    if [ -e "$skill_file" ] || [ -L "$skill_file" ]; then
      cp -pP "$skill_file" "$temporary/previous-skill"
      previous_skill="yes"
    fi
  fi
  if [ -e "$install_dir/tunlite" ] || [ -L "$install_dir/tunlite" ]; then
    cp -pP "$install_dir/tunlite" "$temporary/previous-binary"
    previous_binary="yes"
  fi
  rollback="yes"
  if [ "$bundled_skill" = yes ]; then
    mv -f "$skill_temporary" "$skill_file"
    skill_committed="yes"
    skill_temporary=""
  fi
  mv -f "$install_temporary" "${install_dir}/tunlite"
  binary_committed="yes"
  rollback="no"
  install_temporary=""
  echo "tunlite: 已安装到 ${install_dir}/tunlite"
  case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) echo "tunlite: 请把 ${install_dir} 加入 PATH" ;;
  esac
  if [ "$bundled_skill" = yes ]; then
    echo "tunlite: Skill 已安装到 $skill_dir"
    echo "tunlite: 请告诉你的 Agent：从目录 \"$skill_dir\" 安装并启用 use-tunlite Skill。"
  else
    skill_url="https://raw.githubusercontent.com/tunlite/tunlite/${version}/skill/use-tunlite/SKILL.md"
    echo "tunlite: 此旧版归档未包含 Skill；请告诉你的 Agent：从同版本地址 $skill_url 安装并启用 use-tunlite Skill。"
  fi
  echo "tunlite: 升级后请让 Agent 刷新已复制的 Skill；安装脚本不会修改 Agent 目录。"
}

main "$@"
