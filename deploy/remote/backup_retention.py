"""Bounded backup retention; plans are read-only unless --apply is explicit."""
import argparse
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil

DATABASE_NAME = re.compile(r"database-(\d{8}T\d{6}Z)\.sql\.gz\Z")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def fingerprint(path):
    """No symlinks, devices, or paths outside this immediate backup entry."""
    if path.is_symlink():
        raise ValueError("Symlink backup is not eligible: " + path.name)
    if path.is_file():
        return digest(path), path.stat().st_size
    if not path.is_dir():
        raise ValueError("Unsupported backup type: " + path.name)
    rows = []
    size = 0
    for child in sorted(path.rglob("*")):
        if child.is_symlink() or not child.resolve().is_relative_to(path.resolve()):
            raise ValueError("Unsafe backup member: " + path.name)
        if child.is_file():
            size += child.stat().st_size
            rows.append((child.relative_to(path).as_posix(), digest(child)))
        elif not child.is_dir():
            raise ValueError("Unsupported backup member: " + path.name)
    # Empty directories are not cleanup candidates.
    if not rows:
        raise ValueError("Empty backup is not eligible: " + path.name)
    return hashlib.sha256(json.dumps(rows).encode()).hexdigest(), size


def protection(runtime, keep_deployments=3):
    active_path = runtime / "active-game-baseline.json"
    active = json.loads(active_path.read_text())
    if not active.get("sourceCommit") or not active.get("backup"):
        raise ValueError("Active deployment receipt is incomplete")
    receipts = [(float("inf"), active)]
    candidates = list(runtime.glob("*/game-deployment.json"))
    candidates += list((runtime / "game-updates").glob("*/game-deployment.json"))
    for path in candidates:
        data = json.loads(path.read_text())
        if data.get("gameHealthy") and data.get("sourceCommit"):
            receipts.append((path.stat().st_mtime, data))
    selected, seen = [], set()
    for _, data in sorted(receipts, key=lambda row: row[0], reverse=True):
        if data["sourceCommit"] not in seen:
            selected.append(data)
            seen.add(data["sourceCommit"])
        if len(selected) >= keep_deployments:
            break
    backup_root = (runtime / "backups").resolve()
    protected = set()
    for data in selected:
        for key in ["backup", "databaseBackup"]:
            if not data.get(key):
                continue
            path = Path(data[key]).resolve()
            if path.parent != backup_root:
                raise ValueError("Receipt backup is outside the configured backup root")
            if not path.exists():
                raise ValueError("Required rollback backup is missing: " + path.name)
            protected.add(path.name)
    return protected, digest(active_path), [row["sourceCommit"] for row in selected]


def verify_database(path):
    with gzip.open(path, "rb") as stream:
        prefix = stream.read(4096)
        if b"MariaDB" not in prefix and b"MySQL" not in prefix:
            raise ValueError("Database backup header is not recognized: " + path.name)
        while stream.read(1024 * 1024):
            pass


def plan(runtime, now=None):
    runtime = runtime.resolve()
    backup_root = runtime / "backups"
    if backup_root.is_symlink():
        raise ValueError("Backup root cannot be a symlink")
    now = now or datetime.now(timezone.utc)
    protected, active_hash, commits = protection(runtime)
    entries, skipped = {}, []
    for path in backup_root.iterdir():
        try:
            sha, size = fingerprint(path)
        except ValueError as error:
            skipped.append({"name": path.name, "reason": str(error)})
            continue
        entries[path.name] = {"name": path.name, "sha256": sha, "bytes": size,
                              "directory": path.is_dir(), "mtime_ns": path.stat().st_mtime_ns}
    sql = []
    for name, row in entries.items():
        match = DATABASE_NAME.fullmatch(name)
        if match and not row["directory"]:
            when = datetime.strptime(match[1], "%Y%m%dT%H%M%SZ").replace(tzinfo=timezone.utc)
            sql.append((when, name))
    sql.sort(reverse=True)
    keep = set(protected)
    keep.update(name for _, name in sql[:3])
    days, weeks = set(), set()
    for when, name in sql:
        if when > now:
            keep.add(name)
        if when >= now - timedelta(days=7) and when.date() not in days:
            days.add(when.date()); keep.add(name)
        week = when.isocalendar()[:2]
        if when >= now - timedelta(weeks=4) and week not in weeks:
            weeks.add(week); keep.add(name)
    remove = {name: "superseded database recovery point" for _, name in sql if name not in keep}
    groups = {}
    for row in entries.values():
        if row["directory"]:
            groups.setdefault(row["sha256"], []).append(row)
    for group in groups.values():
        group.sort(key=lambda row: row["mtime_ns"], reverse=True)
        retained = [row for row in group if row["name"] in protected] or group[:1]
        survivors = {row["name"] for row in retained}
        for row in group:
            if row["name"] not in survivors:
                remove[row["name"]] = "exact duplicate of " + retained[0]["name"]
    # New workflow deployment snapshots have a fixed retention contract.
    managed = sorted((row for row in entries.values() if row["directory"] and
                      re.fullmatch(r"game-update-\d{8}T\d{6}Z", row["name"])),
                     key=lambda row: row["mtime_ns"], reverse=True)
    for row in managed[3:]:
        if row["name"] not in protected:
            remove[row["name"]] = "superseded managed deployment snapshot"
    return {"runtime": str(runtime), "activeReceiptSha256": active_hash,
            "protected": sorted(protected), "retainedDeploymentCommits": commits,
            "policy": {"deployments": 3, "latestDatabaseDumps": 3,
                       "dailyDays": 7, "weeklyWeeks": 4, "legacyDirectories": "exact duplicates only"},
            "remove": [{**entries[name], "reason": reason} for name, reason in sorted(remove.items())],
            "kept": sorted(set(entries) - set(remove)), "skipped": skipped,
            "reclaimBytes": sum(entries[name]["bytes"] for name in remove)}


def apply(runtime, proposal):
    runtime = runtime.resolve()
    root = (runtime / "backups").resolve()
    if proposal["runtime"] != str(runtime) or digest(runtime / "active-game-baseline.json") != proposal["activeReceiptSha256"]:
        raise ValueError("Active deployment changed since cleanup planning")
    protected, _, _ = protection(runtime)
    for name in proposal["kept"]:
        if DATABASE_NAME.fullmatch(name):
            verify_database(root / name)
    targets = []
    for row in proposal["remove"]:
        path = root / row["name"]
        if path.is_symlink() or path.resolve().parent != root or path.name in protected:
            raise ValueError("Unsafe or protected deletion target")
        if path.stat().st_mtime_ns != row["mtime_ns"] or fingerprint(path) != (row["sha256"], row["bytes"]):
            raise ValueError("Backup changed since cleanup planning: " + path.name)
        targets.append(path)
    for path in targets:
        if path.is_dir(): shutil.rmtree(path)
        else: path.unlink()
    return {"removed": [path.name for path in targets], "reclaimedBytes": proposal["reclaimBytes"],
            "retainedDatabaseBackupsVerified": True}


def run(runtime, execute=False):
    import fcntl
    os.umask(0o077)
    runtime = runtime.resolve()
    with (runtime / "guild-deployment.lock").open("a") as lock:
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return {"deferred": True, "reason": "deployment or cleanup is active"}
        proposal = plan(runtime)
        if execute:
            result = apply(runtime, proposal)
            receipt_dir = runtime / "backup-retention"
            receipt_dir.mkdir(exist_ok=True)
            stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
            (receipt_dir / (stamp + ".json")).write_text(json.dumps({"plan": proposal, "result": result}, indent=2))
            return result
        return proposal


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-dir", type=Path, default=Path(__file__).resolve().parents[2] / "runtime")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    print(json.dumps(run(args.runtime_dir, args.apply), indent=2))
