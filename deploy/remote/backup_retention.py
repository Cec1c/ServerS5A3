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
import subprocess

DATABASE_NAME = re.compile(r"database-(\d{8}T\d{6}Z)\.sql\.gz\Z")
RECEIPT_FILES = ("backup-receipt.json", "game-deployment.json",
                 "prepared-game.json", "game-rollback.json")


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


def _json_receipt(path):
    if path.is_symlink() or path.resolve() != path.absolute() or not path.is_file():
        raise ValueError("Recovery receipt is redirected or unavailable")
    return json.loads(path.read_bytes())


def _collect_referenced_dirs(root, payload):
    """Collect first-level entries below root named by any receipt string."""
    root = root.resolve()
    found = set()
    if isinstance(payload, dict):
        for value in payload.values():
            found.update(_collect_referenced_dirs(root, value))
    elif isinstance(payload, list):
        for item in payload:
            found.update(_collect_referenced_dirs(root, item))
    elif isinstance(payload, str) and ("/" in payload or "\\" in payload):
        try:
            resolved = Path(payload).resolve()
            if resolved != root and resolved.is_relative_to(root):
                parts = resolved.relative_to(root).parts
                if parts:
                    found.add(parts[0])
        except OSError:
            pass
    return found


def _default_container_mount_sources():
    if not shutil.which("docker"):
        raise RuntimeError("container mount inventory is unavailable")
    try:
        ids = subprocess.check_output(["docker", "ps", "-aq"], text=True,
                                      stderr=subprocess.PIPE).splitlines()
    except (OSError, subprocess.SubprocessError) as error:
        raise RuntimeError("container mount inventory is unavailable") from error
    if not ids:
        return []
    if len(ids) > 256:
        raise ValueError("Container inventory exceeds the retention bound")
    try:
        inspected = json.loads(subprocess.check_output(
            ["docker", "inspect", *ids], text=True, stderr=subprocess.PIPE))
    except (OSError, subprocess.SubprocessError) as error:
        raise RuntimeError("container mount inventory is unavailable") from error
    if len(inspected) != len(ids):
        raise RuntimeError("container mount inventory is incomplete")
    sources = []
    for row in inspected:
        for mount in row.get("Mounts") or []:
            if mount.get("Source"):
                sources.append(mount["Source"])
    return sources


def _mount_snapshot(mount_runner=None):
    sources = list((mount_runner or _default_container_mount_sources)())
    if any(not isinstance(value, str) for value in sources):
        raise ValueError("container mount inventory contains a non-path value")
    return sources


def _superseded_preparation(receipt, directory, active, outcomes):
    """A missing pending dump is only benign after an independent healthy switch."""
    before = receipt.get("beforeImage")
    if (not re.fullmatch(r"sha256:[a-f0-9]{64}", str(before)) or
            not active.get("image") or before == active["image"]):
        return False
    base = receipt.get("baseCommit")
    if base and base == active.get("sourceCommit"):
        return False
    prepared = directory / "prepared-game.json"
    if not prepared.exists() or _json_receipt(prepared).get("beforeImage") != before:
        return False
    return any(row.get("beforeImage") == before and row.get("image") and
               row["image"] != before and (not base or row.get("baseCommit") == base)
               for row in outcomes)


def _protection_state(runtime, keep_deployments=3, mount_runner=None,
                      mount_sources=None):
    if type(keep_deployments) is not int or not 1 <= keep_deployments <= 50:
        raise ValueError("keep_deployments must be an integer between 1 and 50")
    active_path = runtime / "active-game-baseline.json"
    active = _json_receipt(active_path)
    if not active.get("sourceCommit") or not active.get("backup"):
        raise ValueError("Active deployment receipt is incomplete")
    backup_root = (runtime / "backups").resolve()
    updates_root = (runtime / "game-updates").resolve()
    receipts = [(float("inf"), None, active)]
    candidates = list(runtime.glob("*/game-deployment.json"))
    candidates += list((runtime / "game-updates").glob("*/game-deployment.json"))
    completed_backups = set()
    uncertain_receipts = []
    for path in candidates:
        data = _json_receipt(path)
        if (data.get("gameHealthy") is True and data.get("sourceCommit") and
                data.get("backup")):
            receipts.append((path.stat().st_mtime, path, data))
            backup = Path(data["backup"]).resolve()
            if backup.parent == backup_root:
                completed_backups.add(backup.name)
        else:
            uncertain_receipts.append((path, data))

    selected, seen, selected_update_names = [], set(), set()
    referenced_update_names = set()
    for _, origin_path, data in sorted(receipts, key=lambda row: row[0],
                                       reverse=True):
        if data["backup"] not in seen:
            selected.append(data)
            seen.add(data["backup"])
            referenced_update_names.update(
                _collect_referenced_dirs(updates_root, data))
            if (origin_path is not None and
                    origin_path.parent.resolve().parent == updates_root):
                selected_update_names.add(origin_path.parent.name)
        if len(selected) >= keep_deployments:
            break
    # Keep every staging receipt that shares a selected backup.
    for _, origin_path, data in receipts:
        if (origin_path is not None and data.get("backup") in seen and
                origin_path.parent.resolve().parent == updates_root):
            selected_update_names.add(origin_path.parent.name)
            referenced_update_names.update(
                _collect_referenced_dirs(updates_root, data))

    protected = set()
    for path, data in uncertain_receipts:
        for key in ("backup", "databaseBackup"):
            if not data.get(key):
                continue
            backup = Path(data[key]).resolve()
            if backup.parent != backup_root:
                raise ValueError("Receipt backup is outside the configured backup root")
            if backup.exists():
                protected.add(backup.name)
        if path.parent.resolve().parent == updates_root:
            selected_update_names.add(path.parent.name)

    for data in selected:
        for key in ("backup", "databaseBackup"):
            if not data.get(key):
                continue
            path = Path(data[key]).resolve()
            if path.parent != backup_root:
                raise ValueError("Receipt backup is outside the configured backup root")
            if not path.exists():
                raise ValueError("Required rollback backup is missing: " +
                                 path.name)
            protected.add(path.name)

    missing_superseded = []
    outcomes = [data for _, path, data in receipts if path is not None]
    roots = [active, *selected]
    roots.extend(data for _, data in uncertain_receipts)
    for path in sorted((runtime / "game-updates").glob("*/backup-receipt.json")):
        # Prepared or unknown requests retain their original recovery material.
        if (path.parent / "game-deployment.json").exists():
            continue
        data = _json_receipt(path)
        roots.append(data)
        selected_update_names.add(path.parent.name)
        for key in ("backup", "databaseBackup"):
            if not data.get(key):
                continue
            backup = Path(data[key]).resolve()
            if backup.parent != backup_root:
                raise ValueError("Pending recovery material is missing or outside runtime")
            if backup.exists():
                protected.add(backup.name)
            elif not _superseded_preparation(data, path.parent, active, outcomes):
                raise ValueError("Pending recovery material is missing or outside runtime")
            else:
                missing_superseded.append({
                    "receipt": path.parent.name + "/backup-receipt.json",
                    "field": key,
                    "missing": backup.name,
                })

    # Follow references transitively, including sources referenced by pending
    # preparations and mounts of stopped containers.
    mounts = (_mount_snapshot(mount_runner)
              if mount_sources is None else list(mount_sources))
    roots.extend(mounts)
    for mount in mounts:
        mount_path = Path(mount).resolve()
        for root in (updates_root, backup_root):
            if root.is_relative_to(mount_path):
                raise ValueError("A container mounts an entire retention root")
    pending = set(selected_update_names) | referenced_update_names
    if updates_root.is_dir():
        for directory in updates_root.iterdir():
            if (directory.is_dir() and
                    not (directory / "game-deployment.json").exists()):
                pending.add(directory.name)
    visited = set()
    while roots or pending:
        while roots:
            data = roots.pop()
            protected.update(_collect_referenced_dirs(backup_root, data))
            pending.update(_collect_referenced_dirs(updates_root, data) - visited)
        if pending:
            name = pending.pop()
            if name in visited:
                continue
            visited.add(name)
            directory = updates_root / name
            if directory.is_symlink():
                raise ValueError("Referenced game-update directory is redirected")
            for filename in RECEIPT_FILES:
                member = directory / filename
                if member.exists():
                    roots.append(_json_receipt(member))
    return {
        "protected": protected,
        "activeHash": digest(active_path),
        "commits": [row["sourceCommit"] for row in selected],
        "completedBackups": completed_backups,
        "missingSupersededPending": missing_superseded,
        "protectedUpdateNames": visited,
    }


def protection(runtime, keep_deployments=3, mount_runner=None):
    state = _protection_state(runtime, keep_deployments=keep_deployments,
                              mount_runner=mount_runner)
    return state["protected"], state["activeHash"], state["commits"]


def verify_database(path):
    with gzip.open(path, "rb") as stream:
        prefix = stream.read(4096)
        if b"MariaDB" not in prefix and b"MySQL" not in prefix:
            raise ValueError("Database backup header is not recognized: " +
                             path.name)
        while stream.read(1024 * 1024):
            pass


def _retired_game_updates_plan(runtime, state, max_remove=2,
                               mount_runner=None, mount_sources=None):
    if type(max_remove) is not int or not 1 <= max_remove <= 500:
        raise ValueError("max_game_update_remove must be an integer between 1 and 500")
    updates_root = runtime / "game-updates"
    if not updates_root.exists():
        return [], []
    if updates_root.is_symlink() or not updates_root.is_dir():
        raise ValueError("game-updates root is not a regular directory")
    updates_root = updates_root.resolve()
    protected_names = set(state["protectedUpdateNames"])
    mounts = (_mount_snapshot(mount_runner)
              if mount_sources is None else list(mount_sources))
    protected_names.update(
        _collect_referenced_dirs(updates_root, mounts))
    eligible, skipped = [], []
    for entry in sorted(updates_root.iterdir(),
                        key=lambda path: (path.lstat().st_mtime_ns, path.name)):
        if len(eligible) >= max_remove:
            break
        if entry.is_symlink() or not entry.is_dir():
            skipped.append({"name": entry.name,
                            "reason": "not a regular game-update directory"})
            continue
        if entry.name in protected_names:
            continue
        outcome_path = entry / "game-deployment.json"
        if not outcome_path.is_file() or outcome_path.is_symlink():
            # Incomplete or aborted staging directories stay untouched.
            continue
        outcome = _json_receipt(outcome_path)
        if outcome.get("gameHealthy") is not True or not outcome.get("sourceCommit"):
            continue
        try:
            sha, size = fingerprint(entry)
        except ValueError as error:
            skipped.append({"name": entry.name, "reason": str(error)})
            continue
        eligible.append({
            "name": entry.name,
            "sha256": sha,
            "bytes": size,
            "directory": True,
            "mtime_ns": entry.stat().st_mtime_ns,
            "reason": "superseded completed game-update staging directory",
        })
    eligible.sort(key=lambda row: (row["mtime_ns"], row["name"]))
    return eligible[:max_remove], skipped


def plan(runtime, now=None, *, keep_deployments=3, include_game_updates=False,
         max_game_update_remove=2, mount_runner=None):
    runtime = runtime.resolve()
    backup_root = runtime / "backups"
    if backup_root.is_symlink():
        raise ValueError("Backup root cannot be a symlink")
    now = now or datetime.now(timezone.utc)
    mount_sources = _mount_snapshot(mount_runner)
    state = _protection_state(runtime, keep_deployments=keep_deployments,
                              mount_sources=mount_sources)
    protected = state["protected"]
    active_hash = state["activeHash"]
    commits = state["commits"]
    entries, skipped = {}, []
    for path in backup_root.iterdir():
        try:
            sha, size = fingerprint(path)
        except ValueError as error:
            skipped.append({"name": path.name, "reason": str(error)})
            continue
        entries[path.name] = {
            "name": path.name,
            "sha256": sha,
            "bytes": size,
            "directory": path.is_dir(),
            "mtime_ns": path.stat().st_mtime_ns,
        }
    sql = []
    for name, row in entries.items():
        match = DATABASE_NAME.fullmatch(name)
        if match and not row["directory"]:
            when = datetime.strptime(match[1], "%Y%m%dT%H%M%SZ").replace(
                tzinfo=timezone.utc)
            sql.append((when, name))
    sql.sort(reverse=True)
    keep = set(protected)
    keep.update(name for _, name in sql[:3])
    days, weeks = set(), set()
    for when, name in sql:
        if when > now:
            keep.add(name)
        if when >= now - timedelta(days=7) and when.date() not in days:
            days.add(when.date())
            keep.add(name)
        week = when.isocalendar()[:2]
        if when >= now - timedelta(weeks=4) and week not in weeks:
            weeks.add(week)
            keep.add(name)
    remove = {name: "superseded database recovery point" for _, name in sql
              if name not in keep}
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
    # Managed deployment snapshots have a fixed retention contract.
    managed = sorted(
        (row for row in entries.values() if row["directory"] and
         re.fullmatch(r"game-update-\d{8}T\d{6}Z", row["name"])),
        key=lambda row: row["mtime_ns"], reverse=True)
    for row in managed[keep_deployments:]:
        if row["name"] not in protected:
            remove[row["name"]] = "superseded managed deployment snapshot"
    for name in sorted(state["completedBackups"]):
        if (name in entries and entries[name]["directory"] and
                name not in protected and name not in remove):
            remove[name] = "superseded managed deployment snapshot"
    retired_updates, skipped_updates = [], []
    if include_game_updates:
        retired_updates, skipped_updates = _retired_game_updates_plan(
            runtime, state, max_remove=max_game_update_remove,
            mount_sources=mount_sources)
    reclaim_bytes = (sum(entries[name]["bytes"] for name in remove) +
                     sum(row["bytes"] for row in retired_updates))
    result = {
        "runtime": str(runtime),
        "activeReceiptSha256": active_hash,
        "protected": sorted(protected),
        "retainedDeploymentCommits": commits,
        "missingSupersededPending": state["missingSupersededPending"],
        "policy": {
            "deployments": keep_deployments,
            "latestDatabaseDumps": 3,
            "dailyDays": 7,
            "weeklyWeeks": 4,
            "legacyDirectories": "exact duplicates only",
            "includeGameUpdates": bool(include_game_updates),
            **({"maxGameUpdateRemove": max_game_update_remove}
               if include_game_updates else {}),
        },
        "remove": [{**entries[name], "reason": reason}
                   for name, reason in sorted(remove.items())],
        "kept": sorted(set(entries) - set(remove)),
        "skipped": skipped,
        "reclaimBytes": reclaim_bytes,
    }
    if include_game_updates:
        result["retiredGameUpdates"] = retired_updates
        result["skippedGameUpdates"] = skipped_updates
    return result


def apply(runtime, proposal, *, mount_runner=None):
    runtime = runtime.resolve()
    root = (runtime / "backups").resolve()
    if (proposal["runtime"] != str(runtime) or
            digest(runtime / "active-game-baseline.json") !=
            proposal["activeReceiptSha256"]):
        raise ValueError("Active deployment changed since cleanup planning")
    keep_deployments = proposal.get("policy", {}).get("deployments", 3)
    mount_sources = _mount_snapshot(mount_runner)
    state = _protection_state(runtime, keep_deployments=keep_deployments,
                              mount_sources=mount_sources)
    protected = state["protected"]
    for name in proposal["kept"]:
        if DATABASE_NAME.fullmatch(name):
            verify_database(root / name)
    targets = []
    for row in proposal["remove"]:
        path = root / row["name"]
        if path.is_symlink() or path.resolve().parent != root or path.name in protected:
            raise ValueError("Unsafe or protected deletion target")
        if (path.stat().st_mtime_ns != row["mtime_ns"] or
                fingerprint(path) != (row["sha256"], row["bytes"])):
            raise ValueError("Backup changed since cleanup planning: " + path.name)
        targets.append(path)
    update_targets = []
    if proposal.get("retiredGameUpdates"):
        updates_root = (runtime / "game-updates").resolve()
        protected_updates = set(state["protectedUpdateNames"])
        protected_updates.update(
            _collect_referenced_dirs(updates_root, mount_sources))
        for row in proposal["retiredGameUpdates"]:
            path = updates_root / row["name"]
            if (path.is_symlink() or path.resolve().parent != updates_root or
                    path.name in protected_updates):
                raise ValueError("Unsafe or protected game-update deletion target")
            outcome_path = path / "game-deployment.json"
            if not outcome_path.is_file() or outcome_path.is_symlink():
                raise ValueError("Game-update outcome receipt missing before deletion")
            outcome = _json_receipt(outcome_path)
            if outcome.get("gameHealthy") is not True:
                raise ValueError("Game-update directory is still protected")
            if (path.stat().st_mtime_ns != row["mtime_ns"] or
                    fingerprint(path) != (row["sha256"], row["bytes"])):
                raise ValueError("Game-update directory changed since cleanup planning: " +
                                 path.name)
            update_targets.append(path)
    for path in targets:
        if path.is_dir():
            shutil.rmtree(path)
        else:
            path.unlink()
    for path in update_targets:
        shutil.rmtree(path)
    result = {
        "removed": [path.name for path in targets],
        "reclaimedBytes": proposal["reclaimBytes"],
        "retainedDatabaseBackupsVerified": True,
    }
    if proposal.get("policy", {}).get("includeGameUpdates"):
        result["removedGameUpdates"] = [path.name for path in update_targets]
    return result


def _write_json_new(path, value):
    with path.open("x", encoding="utf8") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    path.chmod(0o600)


def run(runtime, execute=False, *, keep_deployments=3,
        include_game_updates=False, max_game_update_remove=2,
        mount_runner=None):
    import fcntl
    if type(keep_deployments) is not int or not 1 <= keep_deployments <= 50:
        raise ValueError("keep_deployments must be an integer between 1 and 50")
    if (type(max_game_update_remove) is not int or
            not 1 <= max_game_update_remove <= 500):
        raise ValueError("max_game_update_remove must be an integer between 1 and 500")
    os.umask(0o077)
    runtime = runtime.resolve()
    with (runtime / "guild-deployment.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return {"deferred": True, "reason": "deployment or cleanup is active"}
        proposal = plan(
            runtime, keep_deployments=keep_deployments,
            include_game_updates=include_game_updates,
            max_game_update_remove=max_game_update_remove,
            mount_runner=mount_runner)
        if not execute:
            return proposal
        receipt_dir = runtime / "backup-retention"
        receipt_dir.mkdir(mode=0o700, exist_ok=True)
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        _write_json_new(receipt_dir / (stamp + ".plan.json"), proposal)
        try:
            result = apply(runtime, proposal, mount_runner=mount_runner)
        except Exception as error:
            _write_json_new(receipt_dir / (stamp + ".failure.json"), {
                "plan": proposal,
                "error": type(error).__name__,
                "message": str(error),
            })
            raise
        _write_json_new(receipt_dir / (stamp + ".json"), {
            "plan": proposal,
            "result": result,
        })
        return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-dir", type=Path,
                        default=Path(__file__).resolve().parents[2] / "runtime")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--keep-deployments", type=int, default=3)
    parser.add_argument("--include-game-updates", action="store_true")
    parser.add_argument("--max-game-update-remove", type=int, default=2)
    args = parser.parse_args()
    print(json.dumps(run(
        args.runtime_dir, args.apply,
        keep_deployments=args.keep_deployments,
        include_game_updates=args.include_game_updates,
        max_game_update_remove=args.max_game_update_remove), indent=2))
