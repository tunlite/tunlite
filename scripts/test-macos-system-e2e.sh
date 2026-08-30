#!/bin/sh
# Destructive, opt-in macOS hardware gate for the real LaunchDaemon lifecycle.
# It uses a unique Tunlite identity, isolated OpenSSH/HTTP processes, an isolated
# TUNLITE_HOME, and fixed-prefix cleanup. It never uses or probes port 19999.
set -eu

say() {
  printf 'tunlite macos system e2e: %s\n' "$1"
}

fail() {
  printf 'tunlite macos system e2e: ERROR: %s\n' "$1" >&2
  exit 1
}

cleanup_bounded() {
  cleanup_step="$1"
  cleanup_limit="$2"
  shift 2
  cleanup_output="$temporary/cleanup-$cleanup_step.log"
  printf 'tunlite macos system e2e: cleanup: %s' "$cleanup_step" >&2
  "$@" </dev/null >"$cleanup_output" 2>&1 &
  cleanup_pid="$!"
  cleanup_elapsed=0
  while /bin/kill -0 "$cleanup_pid" >/dev/null 2>&1; do
    if [ "$cleanup_elapsed" -ge "$cleanup_limit" ]; then
      /bin/kill -TERM "$cleanup_pid" >/dev/null 2>&1
      sleep 1
      /bin/kill -KILL "$cleanup_pid" >/dev/null 2>&1
      wait "$cleanup_pid" >/dev/null 2>&1
      printf ' timeout after %ss; continuing\n' "$cleanup_limit" >&2
      return 124
    fi
    sleep 1
    cleanup_elapsed=$((cleanup_elapsed + 1))
    printf '.' >&2
  done
  if wait "$cleanup_pid"; then
    printf ' done\n' >&2
    return 0
  else
    cleanup_result="$?"
    printf ' failed (exit %s); continuing\n' "$cleanup_result" >&2
    sed -n '1,20p' "$cleanup_output" >&2
    return "$cleanup_result"
  fi
}

cleanup_try() {
  if cleanup_bounded "$@"; then
    return 0
  fi
  cleanup_any_failed=1
  return 0
}

if [ "${TUNLITE_SYSTEM_E2E_SELFTEST:-}" = cleanup-bounded ]; then
  selftest_base="${TMPDIR:-/tmp}"
  selftest_base="${selftest_base%/}"
  temporary="$(mktemp -d "$selftest_base/tunlite-system-e2e-helper.XXXXXX")"
  case "$temporary" in
    "$selftest_base"/tunlite-system-e2e-helper.*) ;;
    *) fail "self-test received an unsafe temporary path: $temporary" ;;
  esac
  trap '/bin/rm -rf "$temporary"' 0
  if cleanup_bounded exit-seven 2 sh -c 'exit 7'; then
    fail 'cleanup_bounded self-test lost the child failure'
  else
    selftest_result="$?"
  fi
  [ "$selftest_result" -eq 7 ] || fail "cleanup_bounded returned $selftest_result, expected 7"
  say 'PASS: cleanup_bounded preserves child exit status 7'
  exit 0
fi

if [ "$(uname -s)" != Darwin ]; then
  fail 'this hardware gate runs only on macOS (Darwin)'
fi
if [ ! -t 0 ] || [ ! -t 1 ] || [ ! -t 2 ]; then
  fail 'an interactive terminal on stdin, stdout, and stderr is required'
fi
if [ "${TUNLITE_REAL_SYSTEM_E2E:-}" != 1 ]; then
  fail 'refusing real LaunchDaemon mutations; explicitly set TUNLITE_REAL_SYSTEM_E2E=1'
fi
if [ "$#" -gt 2 ]; then
  echo 'usage: TUNLITE_REAL_SYSTEM_E2E=1 scripts/test-macos-system-e2e.sh [v1.0.0-preview.N|v1.0.0] [release-directory]' >&2
  exit 2
fi

uid="$(id -u)"
username="$(id -un)"
case "$uid:$username" in
  0:*|*:root) fail 'run this gate as the non-root user whose tunnel should be supervised' ;;
esac
case "$username" in
  ''|*[!A-Za-z0-9._-]*) fail "unsafe current user name: $username" ;;
esac

for required in awk curl date grep id mktemp plutil ps sed shasum stat tar; do
  command -v "$required" >/dev/null 2>&1 || fail "required command is unavailable: $required"
done
command -v sudo >/dev/null 2>&1 || fail 'sudo is required for interactive LaunchDaemon registration and removal'
command -v ssh-keygen >/dev/null 2>&1 || fail 'ssh-keygen is required'
ssh_path="$(command -v ssh || true)"
[ -n "$ssh_path" ] || fail 'the OpenSSH client is required'
sshd_path="$(command -v sshd || true)"
if [ -z "$sshd_path" ] && [ -x /usr/sbin/sshd ]; then
  sshd_path=/usr/sbin/sshd
fi
[ -n "$sshd_path" ] && [ -x "$sshd_path" ] || fail 'the OpenSSH server is required'
python_path="$(command -v python3 || true)"
[ -n "$python_path" ] || fail 'python3 is required for isolated port allocation and HTTP targets'

repo_root="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"
[ -x scripts/package-release.sh ] || fail 'run this script from a complete Tunlite v1 checkout'
[ -x scripts/verify-release.sh ] || fail 'release verification script is unavailable'

version="${1:-v1.0.0-preview.1}"
plain_version="${version#v}"
case "$version" in
  v1.0.0-preview.*|v1.0.0) ;;
  *) fail "unsupported v1 preview/stable version: $version" ;;
esac

temp_base="${TMPDIR:-/tmp}"
temp_base="${temp_base%/}"
temporary="$(mktemp -d "$temp_base/tunlite-system-e2e.XXXXXX")"
case "$temporary" in
  "$temp_base"/tunlite-system-e2e.*) ;;
  *) fail "mktemp returned an unsafe path: $temporary" ;;
esac
case "$temporary" in
  *[[:space:]]*) fail "temporary path contains whitespace unsupported by the isolated sshd config: $temporary" ;;
esac

name="it-system-e2e-$uid-$$"
case "$name" in
  it-system-e2e-[0-9]*-[0-9]*) ;;
  *) fail "unsafe generated connection name: $name" ;;
esac
label="dev.tunlite.connection.$name"
case "$label" in
  dev.tunlite.connection.it-system-e2e-[0-9]*-[0-9]*) ;;
  *) fail "unsafe generated launchd label: $label" ;;
esac

daemon_plist="/Library/LaunchDaemons/$label.plist"
agent_plist="$HOME/Library/LaunchAgents/$label.plist"
log_dir="$HOME/Library/Logs/Tunlite"
stdout_log="$log_dir/$name.stdout.log"
stderr_log="$log_dir/$name.stderr.log"
user_demand="$log_dir/.$name.user.active"
system_demand="$log_dir/.$name.system.active"
export TUNLITE_HOME="$temporary/tunlite-home"
record_path="$TUNLITE_HOME/state/core/services/$name.json"
connection_path="$TUNLITE_HOME/config/connections.json"

tunlite=''
sshd_pid=''
http1_pid=''
http2_pid=''
cleanup_running=0
root_cleanup_failed=0
cleanup_any_failed=0
completed=0
global_cleanup_owned=0
ownership_token="$temporary/global-cleanup-ownership"

safe_generated_paths() {

  [ "$name" = "it-system-e2e-$uid-$$" ] || return 1
  [ "$label" = "dev.tunlite.connection.$name" ] || return 1
  [ "$daemon_plist" = "/Library/LaunchDaemons/$label.plist" ] || return 1
  [ "$agent_plist" = "$HOME/Library/LaunchAgents/$label.plist" ] || return 1
  case "$temporary" in
    "$temp_base"/tunlite-system-e2e.*) ;;
    *) return 1 ;;
  esac
}

job_exists() {
  /bin/launchctl print "$1/$label" >/dev/null 2>&1
}

path_occupied() {
  [ -e "$1" ] || [ -L "$1" ]
}

global_cleanup_authorized() {
  [ "$global_cleanup_owned" -eq 1 ] || return 1
  [ -f "$ownership_token" ] || return 1
  [ "$(sed -n '1p' "$ownership_token")" = "$label" ] || return 1
  safe_generated_paths
}

cleanup() {
  cleanup_status="$?"
  if [ "$cleanup_running" -eq 1 ]; then
    return
  fi
  cleanup_running=1
  # A second signal must not abort the remaining exact cleanup steps.
  trap '' HUP INT TERM
  set +e
  say 'cleanup: stopping only resources covered by this run ownership token'
  if global_cleanup_authorized; then
    if [ "$completed" -ne 1 ] && [ -n "$tunlite" ] && [ -x "$tunlite" ] && [ -e "$connection_path" ]; then
      cleanup_try tunlite-stop 15 "$tunlite" stop "$name" --json --non-interactive
      cleanup_try tunlite-disable 15 "$tunlite" disable "$name" --json --non-interactive
      cleanup_try tunlite-remove 10 "$tunlite" remove "$name" --json --non-interactive
    fi

    if job_exists "gui/$uid"; then
      cleanup_try user-bootout 5 /bin/launchctl bootout "gui/$uid/$label"
    fi
    cleanup_try user-artifacts 5 /bin/rm -f "$agent_plist" "$stdout_log" "$stderr_log" "$user_demand"

    # Never prompt from a trap. A still-valid timestamp may clean a failed
    # system mutation; otherwise leave the evidence and print exact recovery.
    if job_exists system; then
      cleanup_try system-bootout 5 /usr/bin/sudo -n /bin/launchctl bootout "system/$label"
    fi
    if path_occupied "$daemon_plist"; then
      cleanup_try system-artifact 5 /usr/bin/sudo -n /bin/rm -f "$daemon_plist"
    fi
    if path_occupied "$system_demand"; then
      cleanup_try system-demand 5 /bin/rm -f "$system_demand"
    fi

    if path_occupied "$daemon_plist" || job_exists system; then
      root_cleanup_failed=1
      printf '\nROOT CLEANUP INCOMPLETE. Run these exact commands manually:\n' >&2
      printf '  sudo /bin/launchctl bootout system/%s\n' "$label" >&2
      printf '  sudo /bin/rm -f %s\n' "$daemon_plist" >&2
      printf 'Diagnostic state retained at: %s\n\n' "$temporary" >&2
    fi
  elif [ "$global_cleanup_owned" -eq 1 ]; then
    root_cleanup_failed=1
    printf 'tunlite macos system e2e: refusing global cleanup because the ownership token or path validation failed\n' >&2
  else
    say 'cleanup: no global ownership token was acquired; leaving all global paths untouched'
  fi

  for helper_pid in "$sshd_pid" "$http1_pid" "$http2_pid"; do
    case "$helper_pid" in
      ''|*[!0-9]*) ;;
      *)
        /bin/kill "$helper_pid" >/dev/null 2>&1
        helper_attempt=0
        while /bin/kill -0 "$helper_pid" >/dev/null 2>&1 && [ "$helper_attempt" -lt 50 ]; do
          sleep 0.1
          helper_attempt=$((helper_attempt + 1))
        done
        if /bin/kill -0 "$helper_pid" >/dev/null 2>&1; then
          /bin/kill -KILL "$helper_pid" >/dev/null 2>&1
          sleep 0.1
        fi
        wait "$helper_pid" >/dev/null 2>&1
        if /bin/kill -0 "$helper_pid" >/dev/null 2>&1; then
          cleanup_any_failed=1
          printf 'tunlite macos system e2e: cleanup: helper PID %s did not exit\n' "$helper_pid" >&2
        fi
        ;;
    esac
  done

  if global_cleanup_authorized; then
    if job_exists "gui/$uid"; then
      cleanup_any_failed=1
      printf 'tunlite macos system e2e: cleanup: user launchd job is still loaded\n' >&2
    fi
    for remaining_user_path in "$agent_plist" "$stdout_log" "$stderr_log" "$user_demand"; do
      if path_occupied "$remaining_user_path"; then
        cleanup_any_failed=1
        printf 'tunlite macos system e2e: cleanup: user artifact remains: %s\n' "$remaining_user_path" >&2
      fi
    done
  fi

  if [ "$cleanup_any_failed" -ne 0 ]; then
    printf 'tunlite macos system e2e: CLEANUP INCOMPLETE; diagnostics retained at %s\n' "$temporary" >&2
  fi
  if [ "$root_cleanup_failed" -eq 0 ] && [ "$cleanup_any_failed" -eq 0 ]; then
    /bin/rm -rf "$temporary"
  fi
  if [ "$completed" -eq 1 ] && [ "$cleanup_status" -eq 0 ] && [ "$root_cleanup_failed" -eq 0 ] && [ "$cleanup_any_failed" -eq 0 ]; then
    say 'cleanup complete'
  elif [ "$root_cleanup_failed" -eq 0 ] && [ "$cleanup_any_failed" -eq 0 ]; then
    say "cleanup complete after failure (exit $cleanup_status)"
  fi
}
trap cleanup 0
trap 'exit 130' HUP INT TERM

for occupied_path in "$daemon_plist" "$agent_plist" "$stdout_log" "$stderr_log" "$user_demand" "$system_demand"; do
  if path_occupied "$occupied_path"; then
    fail "refusing occupied generated resource (including symlinks): $occupied_path"
  fi
done
if job_exists system || job_exists "gui/$uid"; then
  fail "refusing occupied launchd identity: $label"
fi
printf '%s\n' "$label" >"$ownership_token"
/bin/chmod 600 "$ownership_token"
global_cleanup_owned=1
say "acquired cleanup ownership token for $label"

reserve_port() {
  "$python_path" -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()'
}

new_safe_port() {
  port_attempt=0
  port_now="$(date +%s)"
  port_deadline=$((port_now + 5))
  while [ "$port_attempt" -lt 50 ] && [ "$(date +%s)" -le "$port_deadline" ]; do
    candidate_port="$(reserve_port)"
    port_attempt=$((port_attempt + 1))
    [ "$candidate_port" = 19999 ] && continue
    case " ${allocated_ports:-} " in
      *" $candidate_port "*) continue ;;
    esac
    allocated_ports="${allocated_ports:-} $candidate_port"
    return
  done
  fail 'could not allocate a unique safe port within 50 attempts and 5 seconds'
}

wait_http() {
  wait_port="$1"
  wait_marker="$2"
  wait_label="$3"
  wait_attempt=0
  printf 'tunlite macos system e2e: waiting for %s' "$wait_label" >&2
  while [ "$wait_attempt" -lt 150 ]; do
    wait_body="$(curl -fsS --max-time 1 "http://127.0.0.1:$wait_port/" 2>/dev/null || true)"
    if [ "$wait_body" = "$wait_marker" ]; then
      printf ' ready\n' >&2
      return 0
    fi
    wait_attempt=$((wait_attempt + 1))
    if [ $((wait_attempt % 10)) -eq 0 ]; then
      printf '.' >&2
    fi
    sleep 0.1
  done
  printf ' timeout\n' >&2
  return 1
}

json_extract() {
  json_document="$1"
  json_path="$2"
  printf '%s' "$json_document" | plutil -extract "$json_path" raw -o - -
}

status_json() {
  status_stdout="$temporary/status.stdout"
  status_stderr="$temporary/status.stderr"
  "$tunlite" status "$name" --json --non-interactive </dev/null >"$status_stdout" 2>"$status_stderr" &
  status_watch_pid="$!"
  status_watch_attempt=0
  while /bin/kill -0 "$status_watch_pid" >/dev/null 2>&1; do
    if [ "$status_watch_attempt" -ge 80 ]; then
      /bin/kill -TERM "$status_watch_pid" >/dev/null 2>&1
      sleep 1
      /bin/kill -KILL "$status_watch_pid" >/dev/null 2>&1
      wait "$status_watch_pid" >/dev/null 2>&1
      printf 'tunlite macos system e2e: status command exceeded its outer 8-second watchdog\n' >&2
      return 124
    fi
    sleep 0.1
    status_watch_attempt=$((status_watch_attempt + 1))
  done
  if ! wait "$status_watch_pid"; then
    sed -n '1,20p' "$status_stderr" >&2
    return 1
  fi
  cat "$status_stdout"
}

wait_status_pid() {
  expected_scope="$1"
  expected_mode="$2"
  previous_pid="$3"
  status_attempt=0
  printf 'tunlite macos system e2e: waiting for %s/%s service state' "$expected_scope" "$expected_mode" >&2
  while [ "$status_attempt" -lt 180 ]; do
    current_json="$(status_json || true)"
    current_scope="$(json_extract "$current_json" 'results.0.state.scope' 2>/dev/null || true)"
    current_mode="$(json_extract "$current_json" 'results.0.state.mode' 2>/dev/null || true)"
    current_process="$(json_extract "$current_json" 'results.0.state.processState' 2>/dev/null || true)"
    current_consistency="$(json_extract "$current_json" 'results.0.state.consistencyState' 2>/dev/null || true)"
    current_pid="$(json_extract "$current_json" 'results.0.state.platform.pid' 2>/dev/null || true)"
    case "$current_pid" in
      ''|*[!0-9]*) current_pid=0 ;;
    esac
    if [ "$current_scope" = "$expected_scope" ] && [ "$current_mode" = "$expected_mode" ] && \
       [ "$current_process" = running ] && [ "$current_consistency" = consistent ] && \
       [ "$current_pid" -gt 0 ] && { [ "$previous_pid" -eq 0 ] || [ "$current_pid" != "$previous_pid" ]; }; then
      printf ' ready (pid %s)\n' "$current_pid" >&2
      printf '%s\n' "$current_pid"
      return 0
    fi
    status_attempt=$((status_attempt + 1))
    if [ $((status_attempt % 10)) -eq 0 ]; then
      printf '.' >&2
    fi
    sleep 0.1
  done
  printf ' timeout\n' >&2
  printf 'last status: %s\n' "$current_json" >&2
  return 1
}

assert_record_matches_plist() {
  [ -f "$record_path" ] || fail "service record is missing: $record_path"
  [ -f "$daemon_plist" ] || fail "LaunchDaemon plist is missing: $daemon_plist"
  record_scope="$(plutil -extract scope raw -o - "$record_path")"
  record_mode="$(plutil -extract mode raw -o - "$record_path")"
  record_artifact="$(plutil -extract artifactPath raw -o - "$record_path")"
  record_unit="$(plutil -extract unitName raw -o - "$record_path")"
  record_plan="$(plutil -extract planDigest raw -o - "$record_path")"
  record_baseline="$(plutil -extract baselineDigest raw -o - "$record_path")"
  plist_plan="$(sed -n 's/^<!-- Generated by Tunlite\. DO NOT EDIT\. Plan-SHA256: \([0-9a-f][0-9a-f]*\) -->$/\1/p' "$daemon_plist")"
  plist_baseline="$(shasum -a 256 "$daemon_plist" | awk '{print $1}')"
  [ "$record_scope" = system ] || fail "record scope is $record_scope, expected system"
  [ "$record_mode" = persistent ] || fail "record mode is $record_mode, expected persistent"
  [ "$record_artifact" = "$daemon_plist" ] || fail 'record artifact path does not match the exact LaunchDaemon plist'
  [ "$record_unit" = "$label" ] || fail 'record unit name does not match the generated label'
  [ "$record_plan" = "$plist_plan" ] || fail 'record plan digest does not match the plist plan digest'
  [ "$record_baseline" = "$plist_baseline" ] || fail 'record baseline digest does not match the exact plist bytes'
}

invalidate_sudo_timestamp() {
  say 'invalidating the sudo timestamp for the no-credential behavior gate (no credential prewarming)'
  /usr/bin/sudo -k
  if /usr/bin/sudo -n /usr/bin/true >/dev/null 2>&1; then
    fail 'sudo remains generally passwordless after sudo -k; this machine cannot run the no-credential behavior gate'
  fi
  if /usr/bin/sudo -n /bin/launchctl print "system/$label" >/dev/null 2>&1; then
    fail 'command-specific NOPASSWD permits sudo launchctl print; this machine cannot run the no-credential behavior gate'
  fi
}

assert_sudo_timestamp_invalid() {
  if /usr/bin/sudo -n /usr/bin/true >/dev/null 2>&1; then
    fail 'the sudo timestamp became valid during a no-credential behavior gate'
  fi
}

run_bounded_without_tty_input() {
  bounded_seconds="$1"
  bounded_stdout="$2"
  bounded_stderr="$3"
  shift 3
  "$@" </dev/null >"$bounded_stdout" 2>"$bounded_stderr" &
  bounded_pid="$!"
  bounded_elapsed=0
  printf 'tunlite macos system e2e: bounded command running' >&2
  while /bin/kill -0 "$bounded_pid" >/dev/null 2>&1; do
    if [ "$bounded_elapsed" -ge "$bounded_seconds" ]; then
      printf ' timeout\n' >&2
      /bin/kill -TERM "$bounded_pid" >/dev/null 2>&1
      sleep 1
      /bin/kill -KILL "$bounded_pid" >/dev/null 2>&1
      wait "$bounded_pid" >/dev/null 2>&1
      return 124
    fi
    sleep 1
    bounded_elapsed=$((bounded_elapsed + 1))
    printf '.' >&2
  done
  if wait "$bounded_pid"; then
    bounded_result=0
  else
    bounded_result="$?"
  fi
  printf ' done\n' >&2
  return "$bounded_result"
}

if [ "$#" -eq 2 ]; then
  release_directory="$2"
  [ -d "$release_directory" ] || fail "supplied release directory does not exist: $release_directory"
  say "reusing and verifying release artifacts in $release_directory"
else
  release_directory="$temporary/dist"
  say "building $version release archives (this can take several minutes)"
  package_stdout="$temporary/package.stdout"
  package_stderr="$temporary/package.stderr"
  if ! run_bounded_without_tty_input 900 "$package_stdout" "$package_stderr" \
    scripts/package-release.sh "$version" "$release_directory"; then
    cat "$package_stdout" >&2
    cat "$package_stderr" >&2
    fail 'release packaging failed or exceeded its 15-minute watchdog'
  fi
fi
say 'verifying checksums, archive contents, Universal 2 slices, and version'
scripts/verify-release.sh "$version" "$release_directory"
tar -xzf "$release_directory/tunlite_${plain_version}_macos.tar.gz" -C "$temporary" tunlite
tunlite="$temporary/tunlite"
[ -x "$tunlite" ] || fail 'verified archive did not produce an executable tunlite binary'
actual_version_json="$("$tunlite" version --json)"
actual_version="$(json_extract "$actual_version_json" version)"
[ "$actual_version" = "$plain_version" ] || fail "binary version is $actual_version, expected $plain_version"

allocated_ports=''
new_safe_port
sshd_port="$candidate_port"
new_safe_port
target1_port="$candidate_port"
new_safe_port
target2_port="$candidate_port"
new_safe_port
local_port="$candidate_port"
for selected_port in "$sshd_port" "$target1_port" "$target2_port" "$local_port"; do
  [ "$selected_port" != 19999 ] || fail 'internal safety error: port 19999 was selected'
done

say "reserved isolated ports: sshd=$sshd_port target1=$target1_port target2=$target2_port forward=$local_port (19999 excluded)"
marker1="tunlite-system-e2e-target-one-$$"
marker2="tunlite-system-e2e-target-two-$$"
http_server_code='import http.server, sys
port = int(sys.argv[1]); marker = sys.argv[2].encode()
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(marker)
    def log_message(self, fmt, *args):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()'
"$python_path" -u -c "$http_server_code" "$target1_port" "$marker1" >"$temporary/http1.log" 2>&1 &
http1_pid="$!"
"$python_path" -u -c "$http_server_code" "$target2_port" "$marker2" >"$temporary/http2.log" 2>&1 &
http2_pid="$!"
wait_http "$target1_port" "$marker1" 'HTTP target 1' || fail "HTTP target 1 failed; see $temporary/http1.log"
wait_http "$target2_port" "$marker2" 'HTTP target 2' || fail "HTTP target 2 failed; see $temporary/http2.log"

say 'generating isolated OpenSSH host and client keys'
host_key="$temporary/host_key"
client_key="$temporary/client_key"
authorized_keys="$temporary/authorized_keys"
known_hosts="$temporary/known_hosts"
sshd_config="$temporary/sshd_config"
ssh_wrapper="$temporary/ssh"
ssh-keygen -q -t ed25519 -N '' -f "$host_key"
ssh-keygen -q -t ed25519 -N '' -f "$client_key"
/bin/cp "$client_key.pub" "$authorized_keys"
/bin/chmod 600 "$authorized_keys"
host_public="$(awk '{print $1 " " $2}' "$host_key.pub")"
printf '[127.0.0.1]:%s %s\n' "$sshd_port" "$host_public" >"$known_hosts"
/bin/chmod 600 "$known_hosts"
cat >"$sshd_config" <<EOF
Port $sshd_port
ListenAddress 127.0.0.1
HostKey $host_key
AuthorizedKeysFile $authorized_keys
PidFile $temporary/sshd.pid
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
StrictModes no
UsePAM no
AllowTcpForwarding yes
GatewayPorts no
PermitRootLogin no
LogLevel VERBOSE
AllowUsers $username
EOF
"$sshd_path" -t -f "$sshd_config"
printf '#!/bin/sh\nexec "%s" -o "UserKnownHostsFile=%s" -o StrictHostKeyChecking=yes "$@"\n' "$ssh_path" "$known_hosts" >"$ssh_wrapper"
/bin/chmod 700 "$ssh_wrapper"
export TUNLITE_SSH="$ssh_wrapper"

say "starting isolated sshd as $username (never as root)"
"$sshd_path" -D -e -f "$sshd_config" >"$temporary/sshd.log" 2>&1 &
sshd_pid="$!"
wait_attempt=0
printf 'tunlite macos system e2e: waiting for isolated sshd' >&2
while ! "$ssh_path" -F none -p "$sshd_port" -i "$client_key" \
  -o UserKnownHostsFile="$known_hosts" -o StrictHostKeyChecking=yes -o BatchMode=yes \
  -o ConnectTimeout=1 -o ConnectionAttempts=1 \
  "$username@127.0.0.1" true >/dev/null 2>&1; do
  wait_attempt=$((wait_attempt + 1))
  [ "$wait_attempt" -lt 100 ] || { printf ' timeout\n' >&2; fail "sshd failed; see $temporary/sshd.log"; }
  if [ $((wait_attempt % 10)) -eq 0 ]; then printf '.' >&2; fi
  sleep 0.1
done
printf ' ready\n' >&2

forward1="127.0.0.1:$local_port:127.0.0.1:$target1_port"
forward2="127.0.0.1:$local_port:127.0.0.1:$target2_port"
if ! "$python_path" -c 'import socket, sys
s = socket.socket()
try:
    s.bind(("127.0.0.1", int(sys.argv[1])))
finally:
    s.close()' "$local_port"; then
  fail "forward port became occupied before any Tunlite mutation: $local_port"
fi
say 'adding the isolated connection and enabling a real user LaunchAgent'
"$tunlite" add "$name" -p "$sshd_port" -i "$client_key" -L "$forward1" \
  "$username@127.0.0.1" --non-interactive
"$tunlite" enable "$name" --non-interactive
user_pid="$(wait_status_pid user persistent 0)" || fail 'user LaunchAgent did not become healthy'
wait_http "$local_port" "$marker1" 'user-scope -L traffic' || fail 'user-scope forward did not reach target 1'

say 'migrating USER -> SYSTEM; Tunlite should now request administrator authorization once'
"$tunlite" enable "$name" --scope system
system_pid="$(wait_status_pid system persistent 0)" || fail 'system LaunchDaemon did not become healthy'
[ "$system_pid" != "$user_pid" ] || fail 'USER -> SYSTEM migration did not replace the provider PID'
wait_http "$local_port" "$marker1" 'system-scope -L traffic' || fail 'system-scope forward did not reach target 1'

say 'verifying exact root LaunchDaemon ownership, mode, domain, identity, and record baseline'
[ -f "$daemon_plist" ] || fail "expected LaunchDaemon plist is absent: $daemon_plist"
plist_stat="$(stat -f '%Su:%Sg:%Lp' "$daemon_plist")"
[ "$plist_stat" = 'root:wheel:644' ] || fail "LaunchDaemon ownership/mode is $plist_stat, expected root:wheel:644"
[ "$(plutil -extract Label raw -o - "$daemon_plist")" = "$label" ] || fail 'LaunchDaemon Label is not canonical'
[ "$(plutil -extract UserName raw -o - "$daemon_plist")" = "$username" ] || fail 'LaunchDaemon UserName is not the invoking user'
/bin/launchctl print "system/$label" >/dev/null || fail 'LaunchDaemon is not loaded in the system domain'
if job_exists "gui/$uid"; then
  fail 'the migrated identity is still loaded in the user domain'
fi
[ ! -e "$agent_plist" ] || fail 'the migrated user LaunchAgent plist still exists'
assert_record_matches_plist

say 'changing the system execution plan; traffic must switch to target 2 and PID must change'
plist_before_set="$(shasum -a 256 "$daemon_plist" | awk '{print $1}')"
"$tunlite" set "$name" -L "$forward2"
set_pid="$(wait_status_pid system persistent "$system_pid")" || fail 'system definition replacement did not produce a healthy new PID'
plist_after_set="$(shasum -a 256 "$daemon_plist" | awk '{print $1}')"
[ "$plist_before_set" != "$plist_after_set" ] || fail 'system set did not change the LaunchDaemon definition'
grep -F ">$forward2<" "$daemon_plist" >/dev/null || fail 'new forward is absent from the LaunchDaemon plist'
grep -F ">$forward1<" "$daemon_plist" >/dev/null && fail 'old forward remained in the LaunchDaemon plist'
assert_record_matches_plist
wait_http "$local_port" "$marker2" 'post-set -L traffic to target 2' || fail 'system set did not switch traffic to target 2'

say 'running the no-credential restart behavior gate; unit tests are the hard no-sudo call audit'
invalidate_sudo_timestamp
restart_stdout="$temporary/restart.stdout"
restart_stderr="$temporary/restart.stderr"
if ! run_bounded_without_tty_input 45 "$restart_stdout" "$restart_stderr" \
  "$tunlite" restart "$name" --json --non-interactive; then
  cat "$restart_stdout" >&2
  cat "$restart_stderr" >&2
  fail 'system restart failed or exceeded its 45-second watchdog'
fi
if grep -i 'password' "$restart_stderr" >/dev/null 2>&1; then
  fail 'system restart emitted a password prompt after sudo -k'
fi
assert_sudo_timestamp_invalid
restart_pid="$(wait_status_pid system persistent "$set_pid")" || fail 'system restart did not converge to a new healthy PID'
wait_http "$local_port" "$marker2" 'post-restart -L traffic' || fail 'traffic did not recover after the no-credential restart gate'

say 'disabling SYSTEM; interactive administrator authorization is expected again'
"$tunlite" disable "$name"
[ ! -e "$daemon_plist" ] || fail 'system disable retained the root LaunchDaemon plist'
if job_exists system; then
  fail 'system disable retained the system-domain launchd job'
fi
[ -f "$record_path" ] || fail 'system disable removed the service record'
[ "$(plutil -extract scope raw -o - "$record_path")" = system ] || fail 'system disable lost system scope intent'
[ "$(plutil -extract mode raw -o - "$record_path")" = stopped ] || fail 'system disable did not record stopped mode'
[ ! -e "$system_demand" ] && [ ! -L "$system_demand" ] || fail 'system disable retained the system demand marker'
if plutil -extract artifactPath raw -o - "$record_path" >/dev/null 2>&1; then
  fail 'system disable retained the root artifact path in the service record'
fi

say 'running the no-credential remove behavior gate for user-owned declaration and record state'
invalidate_sudo_timestamp
remove_stdout="$temporary/remove.stdout"
remove_stderr="$temporary/remove.stderr"
if ! run_bounded_without_tty_input 15 "$remove_stdout" "$remove_stderr" \
  "$tunlite" remove "$name" --json --non-interactive; then
  cat "$remove_stdout" >&2
  cat "$remove_stderr" >&2
  fail 'remove failed or exceeded its 15-second watchdog'
fi
if grep -i 'password' "$remove_stderr" >/dev/null 2>&1; then
  fail 'remove emitted a password prompt after sudo -k'
fi
assert_sudo_timestamp_invalid
[ ! -e "$record_path" ] || fail 'remove retained the service record'
if [ -e "$connection_path" ]; then
  if ! "$python_path" -c 'import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    document = json.load(source)
name = sys.argv[2]
if any(item.get("metadata", {}).get("name") == name for item in document.get("items", [])):
    raise SystemExit(1)' "$connection_path" "$name"; then
    fail 'remove retained the generated connection declaration'
  fi
fi

completed=1
say "PASS: real macOS LaunchDaemon lifecycle on $(uname -m), including set/restart/disable/remove and -L traffic"
