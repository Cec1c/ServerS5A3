"""Consistent logical backup of the remote test database; never prints credentials."""
from datetime import datetime, timezone
import gzip
import os
from pathlib import Path
import subprocess


def main():
    os.umask(0o077)
    directory = Path("/opt/servers5a3/runtime/backups")
    directory.mkdir(parents=True, exist_ok=True)
    name = datetime.now(timezone.utc).strftime("database-%Y%m%dT%H%M%SZ.sql.gz")
    temporary = directory / (name + ".partial")
    with gzip.open(temporary, "wb", compresslevel=6) as output:
        process = subprocess.Popen([
            "docker", "exec", "servers5a3-remote-db-1", "sh", "-c",
            'MYSQL_PWD="$MARIADB_PASSWORD" mariadb-dump -u dnf --single-transaction --routines --triggers --hex-blob dnf_local',
        ], stdout=subprocess.PIPE)
        for block in iter(lambda: process.stdout.read(1024 * 1024), b""):
            output.write(block)
        if process.wait():
            raise RuntimeError("Database backup failed; incomplete file retained as .partial")
    temporary.replace(directory / name)
    print("Database backup:", name)


if __name__ == "__main__":
    main()
