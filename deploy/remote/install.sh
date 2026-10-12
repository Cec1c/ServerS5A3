#!/bin/sh
# Run with sudo after placing the reviewed source/assets under /opt/servers5a3
# and configuring /etc/wireguard/wg90cn.conf with the server's private key.
set -eu
test "$(id -u)" = 0
test -f /opt/servers5a3/runtime/docker/instance.json
test -f /etc/wireguard/wg90cn.conf
usermod -aG docker ubuntu
chmod 700 /opt/servers5a3/runtime
chmod 600 /opt/servers5a3/runtime/docker/instance.json
chown -R ubuntu:ubuntu /opt/servers5a3/runtime
systemctl enable --now wg-quick@wg90cn docker
mkdir -p /etc/systemd/system/docker.service.d
cat > /etc/systemd/system/docker.service.d/90cn-vpn.conf <<'EOF'
[Unit]
Wants=wg-quick@wg90cn.service
After=wg-quick@wg90cn.service
EOF
cat > /etc/systemd/system/servers5a3.service <<'EOF'
[Unit]
Description=ServerS5A3 private remote test environment
Requires=docker.service wg-quick@wg90cn.service
After=network-online.target docker.service wg-quick@wg90cn.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
User=ubuntu
Group=ubuntu
SupplementaryGroups=docker
WorkingDirectory=/opt/servers5a3
ExecStart=/usr/bin/python3 deploy/docker/manage.py up --project servers5a3-remote
ExecStop=/usr/bin/docker compose -p servers5a3-remote -f runtime/docker/compose.json stop
TimeoutStartSec=420
TimeoutStopSec=90

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable servers5a3.service
cat > /etc/systemd/system/servers5a3-backup.service <<'EOF'
[Unit]
Description=ServerS5A3 consistent database backup
After=servers5a3.service
[Service]
Type=oneshot
User=ubuntu
SupplementaryGroups=docker
UMask=0077
ExecStart=/usr/bin/python3 /opt/servers5a3/deploy/remote/backup.py
EOF
cat > /etc/systemd/system/servers5a3-backup.timer <<'EOF'
[Unit]
Description=ServerS5A3 daily database backup
[Timer]
OnCalendar=*-*-* 20:00:00 UTC
Persistent=true
[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now servers5a3-backup.timer
echo 'Prepared servers5a3.service; start after importing any existing database.'
