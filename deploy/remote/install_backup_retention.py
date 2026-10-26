"""Install the root-only retention timer; leaves the database backup timer intact."""
import argparse
from pathlib import Path
import re
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    root = args.root.resolve()
    if not re.fullmatch(r"/[A-Za-z0-9_./-]+", str(root)):
        raise ValueError("Unsupported installation path")
    script = root / "deploy/remote/backup_retention.py"
    if not script.is_file(): raise ValueError("Install backup_retention.py first")
    directory = Path("/etc/systemd/system")
    (directory / "servers5a3-backup-retention.service").write_text(
        "[Unit]\nDescription=ServerS5A3 bounded backup retention\nAfter=servers5a3-backup.service\n"
        "[Service]\nType=oneshot\nUser=root\nUMask=0077\n"
        f"ExecStart=/usr/bin/python3 {script} --runtime-dir {root / 'runtime'} --apply\n")
    (directory / "servers5a3-backup-retention.timer").write_text(
        "[Unit]\nDescription=ServerS5A3 daily backup retention\n[Timer]\n"
        "OnCalendar=*-*-* 20:30:00 UTC\nPersistent=true\n[Install]\nWantedBy=timers.target\n")
    subprocess.run(["systemctl", "daemon-reload"], check=True)
    subprocess.run(["systemctl", "enable", "--now", "servers5a3-backup-retention.timer"], check=True)


if __name__ == "__main__": main()
