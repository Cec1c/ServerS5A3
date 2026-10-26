"""Consistent logical backup of the remote test database; never prints credentials."""
from datetime import datetime, timezone
import argparse
import gzip
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-dir", type=Path, default=Path(__file__).resolve().parents[2] / "runtime")
    parser.add_argument("--container", default="servers5a3-remote-db-1")
    parser.add_argument("--no-retention", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    directory = args.runtime_dir.resolve() / "backups"
    directory.mkdir(parents=True, exist_ok=True)
    name = datetime.now(timezone.utc).strftime("database-%Y%m%dT%H%M%SZ.sql.gz")
    temporary = directory / (name + ".partial")
    if temporary.exists() or (directory / name).exists():
        raise RuntimeError("Backup name already exists; never overwrite a recovery point")
    with temporary.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", compresslevel=6) as output:
        process = subprocess.Popen([
            "docker", "exec", args.container, "sh", "-c",
            'MYSQL_PWD="$MARIADB_PASSWORD" mariadb-dump -u dnf --single-transaction --routines --triggers --hex-blob dnf_local',
        ], stdout=subprocess.PIPE)
        for block in iter(lambda: process.stdout.read(1024 * 1024), b""):
            output.write(block)
        if process.wait():
            raise RuntimeError("Database backup failed; incomplete file retained as .partial")
    # A concurrent backup must never be replaced, even between the check above
    # and publication. Both files are in the same backup filesystem.
    os.link(temporary, directory / name)
    temporary.unlink()
    print("Database backup:", name)
    if not args.no_retention and os.geteuid() == 0:
        import backup_retention
        try:
            result = backup_retention.run(args.runtime_dir, execute=True)
            print("Backup retention:", result)
        except (OSError, ValueError, RuntimeError) as error:
            # A retained/corrupt/missing recovery point blocks deletion, not the
            # newly completed database backup. The timer journal records it.
            print("Backup retention deferred:", str(error))


if __name__ == "__main__":
    main()
