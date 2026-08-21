#!/bin/sh
set -eu

say() {
    printf 'tunlite linux e2e: %s\n' "$1"
}

say "configuring the isolated OpenSSH server"
install -d -m 0700 -o tunlite -g tunlite /home/tunlite/.ssh
passwd -d tunlite >/dev/null
if [ ! -f /home/tunlite/.ssh/id_ed25519 ]; then
    runuser -u tunlite -- ssh-keygen -q -t ed25519 -N '' -f /home/tunlite/.ssh/id_ed25519
fi
install -m 0600 -o tunlite -g tunlite /home/tunlite/.ssh/id_ed25519.pub /home/tunlite/.ssh/authorized_keys
ssh-keygen -A
printf '%s\n' \
    'Port 2222' \
    'ListenAddress 127.0.0.1' \
    'PasswordAuthentication no' \
    'KbdInteractiveAuthentication no' \
    'PermitRootLogin no' \
    'AllowUsers tunlite' \
    > /etc/ssh/sshd_config.d/tunlite-e2e.conf
systemctl enable --now ssh.service
ssh-keyscan -p 2222 127.0.0.1 > /tmp/tunlite-e2e-known-hosts
install -m 0600 -o tunlite -g tunlite /tmp/tunlite-e2e-known-hosts /home/tunlite/.ssh/known_hosts

say "starting a boot-persistent HTTP target"
install -d -m 0755 /srv/tunlite-e2e
printf '%s\n' 'tunlite-linux-systemd-e2e-ok' > /srv/tunlite-e2e/index.html
printf '%s\n' \
    '[Unit]' \
    'Description=Tunlite Linux E2E HTTP target' \
    'After=network.target' \
    '' \
    '[Service]' \
    'ExecStart=/usr/bin/python3 -m http.server 19080 --bind 127.0.0.1 --directory /srv/tunlite-e2e' \
    'Restart=on-failure' \
    '' \
    '[Install]' \
    'WantedBy=multi-user.target' \
    > /etc/systemd/system/tunlite-e2e-target.service
systemctl daemon-reload
systemctl enable --now tunlite-e2e-target.service

say "starting the lingering systemd user manager"
install -d -m 0755 /var/lib/systemd/linger
touch /var/lib/systemd/linger/tunlite
if ! systemctl start user@2000.service; then
    systemctl status user-runtime-dir@2000.service user@2000.service --no-pager || true
    journalctl -u user-runtime-dir@2000.service -u user@2000.service --no-pager -n 100 || true
    exit 1
fi
attempt=0
while [ ! -S /run/user/2000/bus ]; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 100 ]; then
        echo 'tunlite linux e2e: systemd user bus did not become ready' >&2
        exit 1
    fi
    sleep 0.1
done

say "container services are ready"
