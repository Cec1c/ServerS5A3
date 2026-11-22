"""Optional MariaDB redo log and flush_neighbors tuning.

Planning is read-only. Apply and rollback require explicit flags and a
caller-owned stage directory. This tool owns only the two optional settings.
The database image, data volume, buffer pool, engine version and durability
setting are read back and must remain unchanged.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import time

MIB = 1024**2
GIB = 1024**3
QUALIFIED_REDO_BYTES = (96 * MIB, 256 * MIB, 512 * MIB)
EXPERIMENTAL_REDO_BYTES = (GIB,)
OPTION_FILE = "/etc/mysql/conf.d/zz-dnf-optional-tuning.cnf"
OUTPUT_LIMIT = 256 * 1024


class TuningError(RuntimeError):
    pass


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


class FileLock:
    """Portable non-blocking exclusive lock for one tuning state directory."""

    def __init__(self, path):
        self.path = Path(path)
        self.handle = None
        self.locked = False

    def __enter__(self):
        if self.path.is_symlink():
            raise ValueError("database tuning lock is redirected")
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.handle = self.path.open("a+b")
        self.handle.seek(0, os.SEEK_END)
        if self.handle.tell() == 0:
            self.handle.write(b"\0")
            self.handle.flush()
        try:
            try:
                import fcntl
                fcntl.flock(self.handle.fileno(),
                            fcntl.LOCK_EX | fcntl.LOCK_NB)
            except ImportError:
                import msvcrt
                self.handle.seek(0)
                msvcrt.locking(self.handle.fileno(), msvcrt.LK_NBLCK, 1)
        except (BlockingIOError, OSError) as error:
            self.handle.close()
            self.handle = None
            raise TuningError(
                "another database tuning action is active or lock is unavailable") from error
        self.locked = True
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        if self.handle is None:
            return False
        try:
            if self.locked:
                try:
                    import fcntl
                    fcntl.flock(self.handle.fileno(), fcntl.LOCK_UN)
                except ImportError:
                    import msvcrt
                    self.handle.seek(0)
                    msvcrt.locking(self.handle.fileno(), msvcrt.LK_UNLCK, 1)
                finally:
                    self.locked = False
        finally:
            self.handle.close()
            self.handle = None
        return False


def checked_redo(value, allow_unverified_one_gib=False):
    if type(value) is not int:
        raise ValueError("redo log size must be an integer number of bytes")
    if value in QUALIFIED_REDO_BYTES:
        return value
    if value in EXPERIMENTAL_REDO_BYTES and allow_unverified_one_gib:
        return value
    if value in EXPERIMENTAL_REDO_BYTES:
        raise ValueError(
            "1 GiB redo is not qualified for this public tool; pass "
            "--allow-unverified-1g-redo only after an independent disk check")
    raise ValueError("redo log size must be 96, 256 or 512 MiB")


def default_lock_path(host):
    return (Path(tempfile.gettempdir()) /
            ("dnf90-database-tuning-" + host.project + ".lock"))


def redo_qualification(value):
    if value in QUALIFIED_REDO_BYTES:
        return "qualified"
    if value in EXPERIMENTAL_REDO_BYTES:
        return "disk-space-check-only"
    raise ValueError("unqualified redo log size")


def checked_flush_neighbors(value):
    if type(value) is not int or value not in (0, 1):
        raise ValueError("flush_neighbors must be 0 or 1")
    return value


def tuning_spec(redo=None, flush=None, allow_unverified_one_gib=False):
    if type(allow_unverified_one_gib) is not bool:
        raise ValueError("allow_unverified_one_gib must be a boolean")
    spec = {}
    if redo is not None:
        spec["redoLogBytes"] = checked_redo(
            redo, allow_unverified_one_gib)
    if flush is not None:
        spec["flushNeighbors"] = checked_flush_neighbors(flush)
    return spec


def option_bytes(values):
    lines = ["[mariadbd]"]
    if "redoLogBytes" in values:
        lines.append("innodb_log_file_size=" +
                     str(checked_redo(values["redoLogBytes"],
                                      allow_unverified_one_gib=True)))
    if "flushNeighbors" in values:
        lines.append("innodb_flush_neighbors=" +
                     str(checked_flush_neighbors(values["flushNeighbors"])))
    if len(lines) == 1:
        return b""
    return ("\n".join(lines) + "\n").encode()


def option_size(value):
    if not isinstance(value, str):
        raise ValueError("invalid MariaDB option size")
    match = re.fullmatch(r"([1-9][0-9]*)([kKmMgG]?)", value)
    if not match:
        raise ValueError("invalid MariaDB option size")
    power = {"": 0, "k": 1, "m": 2, "g": 3}[match.group(2).lower()]
    return int(match.group(1)) * 1024**power


def parse_option_bytes(raw):
    if raw is None:
        return {}
    if len(raw) > 4096:
        raise ValueError("owned tuning file exceeds its bound")
    try:
        text = raw.decode("utf8")
    except UnicodeDecodeError as error:
        raise ValueError("owned tuning file is not UTF-8") from error
    lines = text.splitlines()
    if not lines or lines[0] != "[mariadbd]":
        raise ValueError("owned tuning file has an unexpected header")
    values = {}
    for line in lines[1:]:
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator:
            raise ValueError("owned tuning file contains an invalid line")
        if key == "innodb_log_file_size":
            if "redoLogBytes" in values:
                raise ValueError("owned tuning file repeats redo log size")
            values["redoLogBytes"] = checked_redo(
                option_size(value), allow_unverified_one_gib=True)
        elif key == "innodb_flush_neighbors":
            if "flushNeighbors" in values:
                raise ValueError("owned tuning file repeats flush_neighbors")
            try:
                parsed = int(value)
            except ValueError as error:
                raise ValueError("owned tuning file has an invalid flush_neighbors") from error
            values["flushNeighbors"] = checked_flush_neighbors(parsed)
        else:
            raise ValueError("owned tuning file contains an unexpected key")
    return values


def parse_defaults(output):
    if not isinstance(output, str) or len(output) > OUTPUT_LIMIT:
        raise ValueError("MariaDB defaults output exceeds its bound")
    values = {"redoLogOptions": [], "flushNeighborsOptions": []}
    for argument in shlex.split(output):
        normalized = argument.replace("_", "-")
        if normalized.startswith("--loose-"):
            normalized = "--" + normalized[len("--loose-"):]
        if normalized.startswith("--innodb-log-file-size="):
            values["redoLogOptions"].append(
                option_size(normalized.split("=", 1)[1]))
        elif normalized.startswith("--innodb-flush-neighbors="):
            try:
                parsed = int(normalized.split("=", 1)[1])
            except ValueError as error:
                raise ValueError("invalid MariaDB flush_neighbors default") from error
            values["flushNeighborsOptions"].append(
                checked_flush_neighbors(parsed))
    return values


class Host:
    def __init__(self, project, runner=subprocess.run):
        if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{2,63}", project):
            raise ValueError("invalid database tuning project")
        self.project = project
        self.game = project + "-server-1"
        self.database = project + "-db-1"
        self.runner = runner

    def command(self, argv, timeout=15, input=None):
        process = self.runner(argv, input=input, capture_output=True,
                              text=True, timeout=timeout)
        if process.returncode:
            raise TuningError("database tuning command failed: " + argv[0])
        if len(process.stdout) > OUTPUT_LIMIT:
            raise ValueError("database tuning output exceeds its bound")
        return process.stdout.strip()

    def docker(self, *args, **options):
        return self.command(["docker", *args], **options)

    def inspect(self, name):
        values = json.loads(self.docker("inspect", name))
        if len(values) != 1:
            raise ValueError("database tuning inspect is ambiguous")
        return values[0]

    def sql(self, query, timeout=12, max_statement_time=8):
        return self.docker(
            "exec", "-i", self.database, "sh", "-c",
            'MYSQL_PWD="$MARIADB_ROOT_PASSWORD" exec mariadb '
            '--user=root --batch --raw --skip-column-names',
            input="SET SESSION max_statement_time=" +
                  str(max_statement_time) + ";\n" + query,
            timeout=timeout)

    def variables(self):
        query = ("SELECT VERSION(),@@GLOBAL.innodb_log_file_size,"
                 "@@GLOBAL.innodb_flush_neighbors,"
                 "@@GLOBAL.innodb_flush_log_at_trx_commit,"
                 "@@GLOBAL.innodb_buffer_pool_size;")
        values = self.sql(query).split("\t")
        if len(values) != 5:
            raise ValueError("incomplete database variable read")
        return {
            "version": values[0],
            "redoLogBytes": int(values[1]),
            "flushNeighbors": int(values[2]),
            "flushAtCommit": int(values[3]),
            "poolBytes": int(values[4]),
        }

    def defaults(self):
        last_error = None
        for program in ("mariadbd", "mysqld"):
            try:
                output = self.docker(
                    "exec", self.database, program, "--print-defaults")
            except TuningError as error:
                last_error = error
                continue
            return parse_defaults(output)
        raise TuningError("database server defaults reader is unavailable") from last_error

    def read_option(self):
        code = (
            "if [ -L " + OPTION_FILE + " ]; then exit 3; fi; "
            "if [ -f " + OPTION_FILE + " ]; then base64 -w0 " + OPTION_FILE +
            "; elif [ -e " + OPTION_FILE + " ]; then exit 4; "
            "else printf MISSING; fi")
        output = self.docker("exec", self.database, "sh", "-c", code)
        if output == "MISSING":
            return None
        try:
            raw = base64.b64decode(output, validate=True)
        except ValueError as error:
            raise ValueError("owned tuning file is not valid base64") from error
        if len(raw) > 4096:
            raise ValueError("owned tuning file exceeds its bound")
        return raw

    def install_option(self, data):
        if not isinstance(data, bytes) or not data or len(data) > 4096:
            raise ValueError("invalid owned tuning file")
        encoded = base64.b64encode(data).decode("ascii")
        code = (
            "set -eu; umask 077; d=$(dirname " + OPTION_FILE + "); "
            "test -d \"$d\"; test ! -L " + OPTION_FILE + "; "
            "t=$(mktemp \"$d/.dnf-tuning.XXXXXX\"); "
            "trap 'rm -f \"$t\"' EXIT; "
            "printf %s " + encoded + " | base64 -d > \"$t\"; "
            "chmod 644 \"$t\"; mv \"$t\" " + OPTION_FILE + "; "
            "sync \"$d\"")
        self.docker("exec", self.database, "sh", "-c", code, timeout=20)
        if self.read_option() != data:
            raise TuningError("installed database option differs from approved bytes")

    def remove_option(self, expected):
        if self.read_option() != expected:
            raise TuningError("refusing to remove a foreign database option file")
        code = ("set -eu; test ! -L " + OPTION_FILE +
                "; rm -f " + OPTION_FILE)
        self.docker("exec", self.database, "sh", "-c", code)
        if self.read_option() is not None:
            raise TuningError("database option file was not removed")

    def data_mount(self):
        container = self.inspect(self.database)
        mounts = [row for row in container.get("Mounts") or []
                  if row.get("Destination") == "/var/lib/mysql"]
        if (len(mounts) != 1 or mounts[0].get("Type") != "volume" or
                not mounts[0].get("RW") or not Path(mounts[0].get("Source", "")).is_dir()):
            raise ValueError("database data volume is not qualified")
        return mounts[0]

    def disk_free(self):
        return shutil.disk_usage(self.data_mount()["Source"]).free

    def check_redo_space(self, size):
        checked_redo(size, allow_unverified_one_gib=True)
        free = self.disk_free()
        if free < size + 512 * MIB:
            raise TuningError(
                "database volume lacks redo replacement and rollback space")
        return free

    def game_stopped(self):
        if self.inspect(self.game)["State"].get("Running"):
            raise TuningError(
                "game must be stopped before database tuning is applied")

    def wait(self, predicate, label, seconds=15):
        deadline = time.monotonic() + seconds
        while True:
            if predicate():
                return
            if time.monotonic() >= deadline:
                raise TuningError(label + " did not reach the verified target")
            time.sleep(0.15)

    def set_redo_log_size(self, size, allow_unverified_one_gib=False):
        checked_redo(size, allow_unverified_one_gib)
        self.check_redo_space(size)
        self.sql("SET GLOBAL innodb_log_file_size=" + str(size) + ";",
                 timeout=130, max_statement_time=120)
        self.wait(lambda: self.variables()["redoLogBytes"] == size,
                  "database redo resize")

    def set_flush_neighbors(self, value):
        checked_flush_neighbors(value)
        self.sql("SET GLOBAL innodb_flush_neighbors=" + str(value) + ";")
        self.wait(lambda: self.variables()["flushNeighbors"] == value,
                  "database flush_neighbors update")


def snapshot(host):
    container = host.inspect(host.database)
    state = container.get("State") or {}
    if not state.get("Running") or state.get("Paused"):
        raise TuningError("database must be running and unpaused")
    native_args = (container.get("Config") or {}).get("Cmd") or []
    if (not isinstance(native_args, list) or
            any(not isinstance(value, str) for value in native_args)):
        raise ValueError("database command inventory is not qualified")
    for argument in native_args:
        normalized = argument.replace("_", "-")
        if normalized.startswith("--loose-"):
            normalized = "--" + normalized[len("--loose-"):]
        if (normalized.startswith("--innodb-log-file-size") or
                normalized.startswith("--innodb-flush-neighbors") or
                normalized == "--skip-innodb-flush-neighbors"):
            raise TuningError(
                "database command overrides an owned tuning option")
    mounts = [row for row in container.get("Mounts") or []
              if row.get("Destination") == "/var/lib/mysql"]
    if (len(mounts) != 1 or mounts[0].get("Type") != "volume" or
            not mounts[0].get("RW") or
            not Path(mounts[0].get("Source", "")).is_dir()):
        raise ValueError("database data volume is not qualified")
    mount = mounts[0]
    variables = host.variables()
    if variables["flushAtCommit"] != 1:
        raise TuningError("database durability setting is not synchronized")
    option = host.read_option()
    return {
        "identity": {
            "id": container.get("Id"),
            "image": container.get("Image"),
            "started": state.get("StartedAt"),
            "restarts": container.get("RestartCount"),
            "volume": mount.get("Name"),
            "nativeArgs": list(native_args),
        },
        "variables": variables,
        "defaults": host.defaults(),
        "optionBase64": base64.b64encode(option).decode("ascii") if option is not None else None,
        "optionSha256": digest(option) if option is not None else None,
    }


def plan(host, redo=None, flush=None, allow_unverified_one_gib=False):
    spec = tuning_spec(redo, flush, allow_unverified_one_gib)
    baseline = snapshot(host)
    baseline_digest = digest(canonical(baseline))
    if not spec:
        return {
            "runtime": host.project,
            "baseline": baseline,
            "baselineSha256": baseline_digest,
            "changes": {},
            "targetValues": parse_option_bytes(
                base64.b64decode(baseline["optionBase64"])
                if baseline["optionBase64"] else None),
            "targetOptionSha256": baseline["optionSha256"],
            "carried": True,
            "qualification": None,
            "allowUnverifiedOneGiB": False,
            "diskFreeBytes": None,
        }
    raw_option = (base64.b64decode(baseline["optionBase64"])
                  if baseline["optionBase64"] else None)
    owned = parse_option_bytes(raw_option)
    target_values = dict(owned)
    if "redoLogBytes" in spec:
        target_values["redoLogBytes"] = spec["redoLogBytes"]
    if "flushNeighbors" in spec:
        target_values["flushNeighbors"] = spec["flushNeighbors"]
    target = option_bytes(target_values)
    changes = {}
    for key, source_key in (
            ("redoLogBytes", "redoLogOptions"),
            ("flushNeighbors", "flushNeighborsOptions")):
        if key not in spec:
            continue
        if spec[key] != baseline["variables"][key]:
            changes[key] = spec[key]
        source = baseline["defaults"][source_key]
        if key not in owned and source and source != [spec[key]]:
            raise TuningError(
                key + " is configured outside the owned option file")
    if "redoLogBytes" in changes or "redoLogBytes" in target_values:
        target_redo = target_values.get(
            "redoLogBytes", baseline["variables"]["redoLogBytes"])
        free = host.check_redo_space(
            max(target_redo, baseline["variables"]["redoLogBytes"]))
    else:
        free = host.disk_free()
    return {
        "runtime": host.project,
        "baseline": baseline,
        "baselineSha256": baseline_digest,
        "changes": changes,
        "targetValues": target_values,
        "targetOptionSha256": digest(target) if target else None,
        "carried": (not changes and target == (raw_option or b"")),
        "qualification": (redo_qualification(target_values["redoLogBytes"])
                          if "redoLogBytes" in target_values else None),
        "allowUnverifiedOneGiB": bool(allow_unverified_one_gib),
        "diskFreeBytes": free,
    }


def _write_new(path, value):
    with path.open("x", encoding="utf8") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    path.chmod(0o600)


def _read_stage(path):
    if path.is_symlink() or path.resolve() != path.absolute() or not path.is_file():
        raise ValueError("database tuning stage file is redirected or unavailable")
    return json.loads(path.read_bytes())


def verify(host, proposal):
    baseline = proposal["baseline"]
    current = snapshot(host)
    if current["identity"] != baseline["identity"]:
        raise TuningError("database identity or volume changed during tuning")
    if (current["variables"]["version"] != baseline["variables"]["version"] or
            current["variables"]["poolBytes"] != baseline["variables"]["poolBytes"] or
            current["variables"]["flushAtCommit"] != 1):
        raise TuningError("database engine, pool or durability changed during tuning")
    target_values = proposal["targetValues"]
    expected_redo = target_values.get(
        "redoLogBytes", baseline["variables"]["redoLogBytes"])
    expected_flush = target_values.get(
        "flushNeighbors", baseline["variables"]["flushNeighbors"])
    if (current["variables"]["redoLogBytes"] != expected_redo or
            current["variables"]["flushNeighbors"] != expected_flush):
        raise TuningError("database tuning does not match live variable readback")
    if current["optionSha256"] != proposal["targetOptionSha256"]:
        raise TuningError("database tuning option file does not match its target")
    for key, source_key in (
            ("redoLogBytes", "redoLogOptions"),
            ("flushNeighbors", "flushNeighborsOptions")):
        expected = ([target_values[key]] if key in target_values
                    else baseline["defaults"][source_key])
        if current["defaults"][source_key] != expected:
            raise TuningError(
                "database startup option reader differs from its target")
    return {
        "runtime": host.project,
        "baselineSha256": proposal["baselineSha256"],
        "targetOptionSha256": proposal["targetOptionSha256"],
        "redoLogBytes": expected_redo,
        "flushNeighbors": expected_flush,
        "bufferPoolBytes": current["variables"]["poolBytes"],
        "durabilityPreserved": True,
        "startupOptionVerified": True,
    }


def apply(host, proposal, stage, *, lock_path=None):
    stage = Path(stage)
    lock = (Path(lock_path) if lock_path is not None
            else default_lock_path(host))
    with FileLock(lock):
        return _apply_locked(host, proposal, stage)


def _apply_locked(host, proposal, stage):
    stage = Path(stage)
    if stage.exists():
        raise ValueError("database tuning stage already exists")
    if proposal.get("runtime") != host.project:
        raise ValueError("database tuning plan belongs to another project")
    baseline = snapshot(host)
    if digest(canonical(baseline)) != proposal["baselineSha256"]:
        raise TuningError("database tuning baseline drifted after planning")
    if proposal["carried"]:
        return {"carried": True, "baselineSha256": proposal["baselineSha256"]}
    host.game_stopped()
    stage.mkdir(parents=True, mode=0o700)
    _write_new(stage / "baseline.json", baseline)
    _write_new(stage / "plan.json", proposal)
    _write_new(stage / "apply-started.json", {"step": "apply-started"})
    last_stage = "apply-started"
    try:
        raw_option = (base64.b64decode(baseline["optionBase64"])
                      if baseline["optionBase64"] else None)
        target = option_bytes(proposal["targetValues"])
        if ((digest(target) if target else None) !=
                proposal["targetOptionSha256"]):
            raise ValueError("database tuning target file digest changed")
        if target != (raw_option or b""):
            last_stage = "option-install-started"
            _write_new(stage / (last_stage + ".json"), {"step": last_stage})
            if target:
                host.install_option(target)
            else:
                host.remove_option(raw_option)
        if "redoLogBytes" in proposal["changes"]:
            last_stage = "redo-started"
            _write_new(stage / (last_stage + ".json"), {"step": last_stage})
            host.set_redo_log_size(
                proposal["changes"]["redoLogBytes"],
                proposal.get("allowUnverifiedOneGiB", False))
        if "flushNeighbors" in proposal["changes"]:
            last_stage = "flush-started"
            _write_new(stage / (last_stage + ".json"), {"step": last_stage})
            host.set_flush_neighbors(proposal["changes"]["flushNeighbors"])
        result = verify(host, proposal)
        _write_new(stage / "apply-complete.json", result)
        return result
    except BaseException as error:
        _write_new(stage / "failure.json", {
            "stage": last_stage,
            "error": type(error).__name__,
        })
        raise


def rollback(host, stage, *, lock_path=None):
    stage = Path(stage)
    lock = (Path(lock_path) if lock_path is not None
            else default_lock_path(host))
    with FileLock(lock):
        return _rollback_locked(host, stage)


def _rollback_locked(host, stage):
    stage = Path(stage)
    if stage.is_symlink() or not stage.is_dir():
        raise ValueError("database tuning stage is unavailable")
    proposal = _read_stage(stage / "plan.json")
    baseline = _read_stage(stage / "baseline.json")
    if (baseline != proposal.get("baseline") or
            digest(canonical(baseline)) != proposal.get("baselineSha256")):
        raise ValueError("database tuning baseline record changed")
    if proposal.get("runtime") != host.project:
        raise ValueError("database tuning stage belongs to another project")
    if not (stage / "apply-started.json").is_file():
        raise ValueError("database tuning apply was never entered")
    complete = stage / "rollback-complete.json"
    if complete.exists():
        result = _read_stage(complete)
        if result.get("baselineSha256") != proposal.get("baselineSha256"):
            raise ValueError("database tuning rollback record changed")
        if digest(canonical(snapshot(host))) != proposal.get("baselineSha256"):
            raise TuningError(
                "database tuning changed after the completed rollback")
        return result
    if (stage / "rollback-started.json").exists():
        raise TuningError(
            "database tuning rollback already started; inspect original action")
    current = snapshot(host)
    if current["identity"] != baseline["identity"]:
        raise TuningError("database identity or volume changed during tuning")
    if (current["variables"]["version"] != baseline["variables"]["version"] or
            current["variables"]["poolBytes"] != baseline["variables"]["poolBytes"] or
            current["variables"]["flushAtCommit"] != 1):
        raise TuningError("database engine, pool or durability changed during tuning")
    target_values = proposal["targetValues"]
    target_redo = target_values.get(
        "redoLogBytes", baseline["variables"]["redoLogBytes"])
    target_flush = target_values.get(
        "flushNeighbors", baseline["variables"]["flushNeighbors"])
    if current["variables"]["redoLogBytes"] not in (
            baseline["variables"]["redoLogBytes"], target_redo):
        raise TuningError("database redo changed outside this tuning action")
    if current["variables"]["flushNeighbors"] not in (
            baseline["variables"]["flushNeighbors"], target_flush):
        raise TuningError("database flush_neighbors changed outside this action")
    raw_current = (base64.b64decode(current["optionBase64"])
                   if current["optionBase64"] else None)
    raw_baseline = (base64.b64decode(baseline["optionBase64"])
                    if baseline["optionBase64"] else None)
    raw_target = option_bytes(target_values)
    if ((digest(raw_target) if raw_target else None) !=
            proposal["targetOptionSha256"]):
        raise ValueError("database tuning target record changed")
    if (raw_current or b"") not in ((raw_baseline or b""), raw_target):
        raise TuningError("database option file changed outside this tuning action")
    if ((raw_current or b"") != (raw_baseline or b"") and
            not (stage / "option-install-started.json").is_file()):
        raise ValueError("missing database option installation record")
    if (current["variables"]["redoLogBytes"] !=
            baseline["variables"]["redoLogBytes"] and
            current["variables"]["redoLogBytes"] == target_redo and
            not (stage / "redo-started.json").is_file()):
        raise ValueError("missing database redo write record")
    if (current["variables"]["flushNeighbors"] !=
            baseline["variables"]["flushNeighbors"] and
            current["variables"]["flushNeighbors"] == target_flush and
            not (stage / "flush-started.json").is_file()):
        raise ValueError("missing database flush_neighbors write record")
    host.game_stopped()
    _write_new(stage / "rollback-started.json", {"step": "rollback-started"})
    if (current["variables"]["flushNeighbors"] !=
            baseline["variables"]["flushNeighbors"]):
        host.set_flush_neighbors(baseline["variables"]["flushNeighbors"])
    if (current["variables"]["redoLogBytes"] !=
            baseline["variables"]["redoLogBytes"]):
        host.set_redo_log_size(
            baseline["variables"]["redoLogBytes"],
            allow_unverified_one_gib=True)
    if (raw_current or b"") != (raw_baseline or b""):
        if raw_baseline is None:
            host.remove_option(raw_current)
        else:
            host.install_option(raw_baseline)
    if snapshot(host) != baseline:
        raise TuningError("original database tuning facts were not fully restored")
    result = {
        "runtime": host.project,
        "originalDatabaseTuningRestored": True,
        "baselineSha256": proposal["baselineSha256"],
    }
    _write_new(stage / "rollback-complete.json", result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", default="servers5a3-remote")
    parser.add_argument("--stage", type=Path,
                        default=Path("database-tuning-stage"))
    parser.add_argument("--lock-file", type=Path)
    parser.add_argument("--redo-mib", type=int,
                        choices=(96, 256, 512, 1024))
    parser.add_argument("--flush-neighbors", type=int, choices=(0, 1))
    parser.add_argument("--allow-unverified-1g-redo", action="store_true")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--rollback", action="store_true")
    args = parser.parse_args()
    if args.rollback and (args.apply or args.redo_mib is not None or
                          args.flush_neighbors is not None or
                          args.allow_unverified_1g_redo):
        parser.error("--rollback cannot be combined with apply or new values")
    host = Host(args.project)
    if args.rollback:
        result = rollback(host, args.stage, lock_path=args.lock_file)
    else:
        redo = (args.redo_mib * MIB
                if args.redo_mib is not None else None)
        proposal = plan(host, redo, args.flush_neighbors,
                        args.allow_unverified_1g_redo)
        result = (apply(host, proposal, args.stage,
                       lock_path=args.lock_file)
                  if args.apply else proposal)
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
