"""Install the root-only retention timer; leaves the database backup timer intact."""
import argparse
from pathlib import Path
import re
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--keep-deployments", type=int, default=3)
    parser.add_argument("--max-game-update-remove", type=int, default=2)
    args = parser.parse_args()
    if not 1 <= args.keep_deployments <= 50:
        raise ValueError("keep_deployments must be an integer between 1 and 50")
    if not 1 <= args.max_game_update_remove <= 500:
        raise ValueError("max_game_update_remove must be an integer between 1 and 500")
    root = args.root.resolve()
    if not re.fullmatch(r"/[A-Za-z0-9_./-]+", str(root)):
        raise ValueError("Unsupported installation path")
    script = root / "deploy/remote/backup_retention.py"
    if not script.is_file(): raise ValueError("Install backup_retention.py first")
    directory = Path("/etc/systemd/system")
    (directory / "servers5a3-backup-retention.service").write_text(
        "[Unit]\nDescription=ServerS5A3 bounded backup retention\nAfter=servers5a3-backup.service\n"
        "[Service]\nType=oneshot\nUser=root\nUMask=0077\n"
        f"ExecStart=/usr/bin/python3 {script} --runtime-dir {root / 'runtime'} --apply "
        f"--keep-deployments {args.keep_deployments} --include-game-updates "
        f"--max-game-update-remove {args.max_game_update_remove}\n")
    (directory / "servers5a3-backup-retention.timer").write_text(
        "[Unit]\nDescription=ServerS5A3 daily backup retention\n[Timer]\n"
        "OnCalendar=*-*-* 20:30:00 UTC\nPersistent=true\n[Install]\nWantedBy=timers.target\n")
    subprocess.run(["systemctl", "daemon-reload"], check=True)
    subprocess.run(["systemctl", "enable", "--now", "servers5a3-backup-retention.timer"], check=True)


if __name__ == "__main__": main()
