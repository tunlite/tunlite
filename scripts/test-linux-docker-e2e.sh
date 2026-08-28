#!/bin/sh
# Build the Linux release and exercise it inside Ubuntu with real systemd,
# OpenSSH, traffic, reboot persistence, CLI removal, ownership, and drift.
set -eu

if [ "$#" -gt 1 ]; then
    echo 'usage: scripts/test-linux-docker-e2e.sh [v1.0.0-preview.N|v1.0.0]' >&2
    exit 2
fi
command -v docker >/dev/null 2>&1 || {
    echo 'tunlite linux e2e: docker is required' >&2
    exit 1
}

version="${1:-v1.0.0-preview.1}"
plain_version="${version#v}"
temporary="$(mktemp -d)"
container="tunlite-linux-e2e-$$"
image="tunlite-linux-e2e:$plain_version"

cleanup() {
    status="$?"
    case "$container" in
        tunlite-linux-e2e-*) docker rm -f "$container" >/dev/null 2>&1 || true ;;
    esac
    if [ "$status" -eq 0 ] || [ "${TUNLITE_E2E_CLEAN_IMAGE:-}" = 1 ]; then
        case "$image" in
            tunlite-linux-e2e:*) docker image rm "$image" >/dev/null 2>&1 || true ;;
        esac
    else
        say "FAILED: retained Docker image $image for diagnosis and a cached rerun"
    fi
    rm -rf "$temporary"
}
trap cleanup EXIT HUP INT TERM

say() {
    printf 'tunlite linux e2e: %s\n' "$1"
}

run_user() {
    docker exec \
        --user tunlite \
        --env HOME=/home/tunlite \
        --env XDG_RUNTIME_DIR=/run/user/2000 \
        --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/2000/bus \
        "$container" "$@"
}

wait_for_user_manager() {
    attempt=0
    while ! run_user systemctl --user show-environment >/dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 100 ]; then
            echo 'tunlite linux e2e: systemd user manager did not become ready' >&2
            return 1
        fi
        sleep 0.1
    done
}

wait_for_traffic() {
    port="$1"
    mode="${2:-direct}"
    attempt=0
    while :; do
        if [ "$mode" = socks ]; then
            output="$(docker exec "$container" curl -fsS --max-time 2 --socks5-hostname "127.0.0.1:$port" http://127.0.0.1:19080/ 2>/dev/null || true)"
        else
            output="$(docker exec "$container" curl -fsS --max-time 2 "http://127.0.0.1:$port/" 2>/dev/null || true)"
        fi
        if [ "$output" = 'tunlite-linux-systemd-e2e-ok' ]; then
            return 0
        fi
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 100 ]; then
            echo "tunlite linux e2e: traffic on port $port did not become ready" >&2
            return 1
        fi
        sleep 0.1
    done
}

service_property() {
    run_user systemctl --user show tunlite-linux-real.service --property="$1" --value
}

say "building and verifying $version release archives"
scripts/package-release.sh "$version" "$temporary/dist"
scripts/verify-release.sh "$version" "$temporary/dist"
tar -xzf "$temporary/dist/tunlite_${plain_version}_linux_x64.tar.gz" -C "$temporary" tunlite

say "building the Ubuntu 24.04 systemd test image"
docker build -f validation/docker/systemd/Dockerfile -t "$image" .

say "booting an isolated systemd container"
docker run \
    --detach \
    --privileged \
    --cgroupns=private \
    --tmpfs /run \
    --tmpfs /run/lock \
    --name "$container" \
    "$image" >/dev/null

attempt=0
while ! docker exec "$container" systemctl is-system-running >/dev/null 2>&1; do
    state="$(docker exec "$container" systemctl is-system-running 2>/dev/null || true)"
    if [ "$state" = degraded ]; then
        break
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 100 ]; then
        echo "tunlite linux e2e: systemd did not boot (state=$state)" >&2
        exit 1
    fi
    sleep 0.1
done

docker cp "$temporary/tunlite" "$container:/usr/local/bin/tunlite"
docker exec "$container" chmod 0755 /usr/local/bin/tunlite
docker exec "$container" /usr/local/sbin/tunlite-e2e-setup
wait_for_user_manager

say "creating one connection with local, remote, and SOCKS forwards"
run_user tunlite add linux-real \
    -p 2222 \
    -i /home/tunlite/.ssh/id_ed25519 \
    -L 127.0.0.1:18080:127.0.0.1:19080 \
    -R 127.0.0.1:18081:127.0.0.1:19080 \
    -D 127.0.0.1:18082 tunlite@127.0.0.1 \
    --json --non-interactive

say "starting the real systemd user service and checking all traffic"
run_user tunlite start linux-real --json --non-interactive
wait_for_traffic 18080
wait_for_traffic 18081
wait_for_traffic 18082 socks

status="$(run_user tunlite status linux-real --json --non-interactive)"
printf '%s' "$status" | jq -e '.results[0].state.mode == "background" and .results[0].state.processState == "running"' >/dev/null
background_pid="$(service_property MainPID)"

say "enabling login persistence without restarting the SSH process"
run_user tunlite enable linux-real --json --non-interactive
persistent_pid="$(service_property MainPID)"
test "$persistent_pid" = "$background_pid"
test "$(run_user systemctl --user is-enabled tunlite-linux-real.service)" = enabled

say "killing SSH and waiting for systemd to recover it with a new PID"
docker exec "$container" kill -KILL "$persistent_pid"
attempt=0
while :; do
    restarted_pid="$(service_property MainPID)"
    if [ "$restarted_pid" -gt 0 ] && [ "$restarted_pid" != "$persistent_pid" ]; then
        break
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 150 ]; then
        echo 'tunlite linux e2e: SSH process did not restart' >&2
        exit 1
    fi
    sleep 0.1
done
wait_for_traffic 18080
test "$(service_property NRestarts)" -ge 1

say "removing the Tunlite CLI while the direct SSH service keeps running"
before_removal_pid="$(service_property MainPID)"
docker exec "$container" mv /usr/local/bin/tunlite /usr/local/bin/tunlite.removed
test "$(service_property MainPID)" = "$before_removal_pid"
wait_for_traffic 18080

say "rebooting the container with the Tunlite CLI still absent"
docker restart "$container" >/dev/null
wait_for_user_manager
wait_for_traffic 18080
test "$(run_user systemctl --user is-active tunlite-linux-real.service)" = active

say "restoring and replacing the CLI without restarting the connection"
docker exec "$container" mv /usr/local/bin/tunlite.removed /usr/local/bin/tunlite
after_boot_pid="$(service_property MainPID)"
docker cp "$temporary/tunlite" "$container:/tmp/tunlite-new"
docker exec "$container" install -m 0755 /tmp/tunlite-new /usr/local/bin/tunlite
test "$(service_property MainPID)" = "$after_boot_pid"
run_user tunlite version --json | jq -e --arg version "$plain_version" '.version == $version' >/dev/null

say "unmanaging and re-managing the running native service without interruption"
before_unmanage_pid="$(service_property MainPID)"
run_user tunlite unmanage linux-real --json --non-interactive
test "$(service_property MainPID)" = "$before_unmanage_pid"
run_user tunlite manage tunlite-linux-real.service --name linux-real --json --non-interactive
test "$(service_property MainPID)" = "$before_unmanage_pid"
wait_for_traffic 18080

say "injecting native-unit drift and resolving it from the Tunlite declaration"
docker exec "$container" sed -i 's/Description=Tunlite connection linux-real/Description=Externally edited Linux E2E service/' /home/tunlite/.config/systemd/user/tunlite-linux-real.service
run_user systemctl --user daemon-reload
drifted="$(run_user tunlite status linux-real --json --non-interactive)"
printf '%s' "$drifted" | jq -e '.results[0].state.consistencyState == "drifted"' >/dev/null
run_user tunlite resolve linux-real --use tunlite --dry-run --json --non-interactive
run_user tunlite resolve linux-real --use tunlite --json --non-interactive
consistent="$(run_user tunlite status linux-real --json --non-interactive)"
printf '%s' "$consistent" | jq -e '.results[0].state.consistencyState == "consistent"' >/dev/null
wait_for_traffic 18080

say "disabling and removing the isolated connection"
run_user tunlite disable linux-real --json --non-interactive
run_user tunlite remove linux-real --json --non-interactive
test "$(run_user systemctl --user is-active tunlite-linux-real.service 2>/dev/null || true)" != active

say "PASS: Linux systemd, real SSH traffic, reboot, CLI removal, ownership, and drift"
