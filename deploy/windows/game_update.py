"""Agent entrypoint for cumulative game updates; no GUI or shell-built commands."""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import http.client
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shlex
import ssl
import subprocess
import sys
import tarfile
import io
import time

ROOT = Path(__file__).resolve().parents[2]
WORKER = ROOT / "deploy/remote/game_update.py"
SSH_OPTIONS = ["-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"]
AUTHORITATIVE_URL = "https://gitgud.io/16ww/servers5a3.git"
RUNTIME_CHANNEL_TARGET = "runtime/docker/data/channel.override.etc"
RUNTIME_CHANNEL_CONTAINER = "/runtime/data/dnf/channel.override.etc"
RUNTIME_CHANNEL_FILE = "channel.override.etc"
EMERGENCY_MR226_PATHS = {"Docs/CHARACTER_SELECTION_TIMEOUT.md", "changlog.md"} | {
    "go-server/internal/services/dnfbridge/" + name for name in
    ("game_select_scene_entry.go", "game_select_state.go", "character_rename_current.go", "character_select_deadline_test.go")}


class AuthorityGate:
    """A live pull per checkpoint. Saved receipts are output, never authorization input."""
    def __init__(self, checkout, audit, run_id, emergency=None):
        self.checkout, self.audit, self.run_id = checkout, audit, run_id
        self.nonce = secrets.token_hex(32)
        self.baseline = None
        self.receipt = None
        self.count = 0
        self.emergency = emergency

    def check(self, source, phase):
        if source.resolve() == self.checkout.resolve():
            raise ValueError("Authority checkout must be separate from the candidate")
        self.receipt = None
        self.count += 1
        receipt = {"format": 1, "runId": self.run_id, "nonce": self.nonce,
                   "phase": phase, "repository": AUTHORITATIVE_URL, "branch": "master",
                   "command": ["git", "pull", "--ff-only", "--no-rebase", AUTHORITATIVE_URL, "master"],
                   "startedAt": datetime.now(timezone.utc).isoformat(), "passed": False}
        try:
            if not self.checkout.exists():
                subprocess.run(["git", "-c", "core.longpaths=true", "clone", "-c", "core.longpaths=true", "--no-tags", "--single-branch", "--branch", "master",
                                AUTHORITATIVE_URL, str(self.checkout)], check=True,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if git(self.checkout, "status", "--porcelain"):
                raise ValueError("Authority checkout must be clean; preserve and inspect its changes")
            if git(self.checkout, "branch", "--show-current") != "master":
                raise ValueError("Authority checkout must be on master")
            receipt["before"] = git(self.checkout, "rev-parse", "HEAD")
            pulled = subprocess.run(receipt["command"], cwd=self.checkout, check=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            receipt["pullOutput"] = pulled.stdout.decode("utf-8", errors="replace")
            receipt["after"] = git(self.checkout, "rev-parse", "HEAD")
            remote = git(self.checkout, "ls-remote", AUTHORITATIVE_URL, "refs/heads/master").split()
            if len(remote) != 2 or remote[0] != receipt["after"]:
                raise ValueError("Authoritative master changed during pull or checkout is ahead")
            if self.baseline and self.baseline != receipt["after"]:
                raise ValueError("Authoritative master changed during this run; integrate and validate a new run")
            subprocess.run(["git", "fetch", "--no-tags", str(self.checkout), "master"], cwd=source,
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            receipt["candidate"] = git(source, "rev-parse", "HEAD")
            contains = subprocess.run(["git", "merge-base", "--is-ancestor", receipt["after"], receipt["candidate"]],
                                      cwd=source, stdout=subprocess.PIPE, stderr=subprocess.PIPE).returncode == 0
            if self.emergency:
                e = self.emergency
                if receipt["candidate"] != e["candidateCommit"] or contains:
                    raise ValueError("Emergency candidate differs or already contains master")
                subprocess.run(["git", "merge-base", "--is-ancestor", e["selectedMergeCommit"], receipt["after"]],
                               cwd=source, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                receipt.update(integrationMode="emergency-selected-mr", selectedMR=e["selectedMR"],
                               selectedMergeCommit=e["selectedMergeCommit"], emergencyEvidenceSha256=e["evidenceSha256"])
            elif not contains:
                raise ValueError("Deployment candidate does not contain current authoritative master: " + receipt["after"])
            receipt["containsMaster"] = contains
            self.baseline = receipt["after"]
            receipt["passed"] = True
            self.receipt = receipt
            return receipt
        except Exception as error:
            receipt["failure"] = str(error)
            if isinstance(error, subprocess.CalledProcessError) and error.stderr:
                receipt["stderr"] = error.stderr.decode("utf-8", errors="replace")
            raise
        finally:
            receipt["checkedAt"] = datetime.now(timezone.utc).isoformat()
            save(self.audit / ("authority-%02d-%s.json" % (self.count, phase)), receipt)

    def require(self, phase):
        receipt = self.receipt
        if not receipt or not receipt.get("passed") or receipt["nonce"] != self.nonce or receipt["phase"] != phase:
            raise ValueError("A fresh in-process authoritative pull is required for " + phase)
        age = (datetime.now(timezone.utc) - datetime.fromisoformat(receipt["checkedAt"])).total_seconds()
        if not 0 <= age <= 300:
            raise ValueError("Authoritative pull expired; run a new live check")
        return receipt


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1024 * 1024), b""): h.update(block)
    return h.hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding="utf-8")


def git(source, *args):
    return subprocess.check_output(["git", *args], cwd=source, text=True, encoding="utf-8").strip()


def safe_path(rel):
    path = PurePosixPath(rel)
    if (not rel or path.is_absolute() or path.as_posix() != rel or
            any(p in (".", "..") for p in rel.split("/")) or "\\" in rel or ":" in rel or
            rel.startswith(("runtime/", ".cache/", "local-notes/", ".git/"))):
        raise ValueError("Invalid source path: " + rel)
    return rel


def source_state(source):
    if git(source, "status", "--porcelain"):
        raise ValueError("Use a clean cumulative source worktree")
    files = {}
    for rel in filter(None, git(source, "ls-files", "-z").split("\0")):
        safe_path(rel)
        path = source / rel
        if not path.is_file() or path.is_symlink():
            raise ValueError("Source member must be a regular file: " + rel)
        files[rel] = digest(path)
    return {"head": git(source, "rev-parse", "HEAD"), "files": files}


def write_archive(source, old_archive, old_files, changed, output):
    with tarfile.open(old_archive) as before, tarfile.open(output, "w:gz") as archive:
        members = {}
        for member in before.getmembers():
            safe_path(member.name)
            if not member.isfile() or member.name in members:
                raise ValueError("Invalid or duplicate base archive member")
            members[member.name] = member
        if set(members) != set(old_files):
            raise ValueError("Base archive members differ from manifest")
        for rel, member in members.items():
            if hashlib.sha256(before.extractfile(member).read()).hexdigest() != old_files[rel]:
                raise ValueError("Base archive member hash mismatch: " + rel)
        files = {}
        for rel in source_state(source)["files"]:
            path = source / rel
            data = path.read_bytes()
            if rel not in changed and hashlib.sha256(data).hexdigest() != old_files.get(rel):
                if rel not in members:
                    raise ValueError("Unreviewed source addition: " + rel)
                prior = before.extractfile(members[rel]).read()
                if prior.replace(b"\r\n", b"\n") != data.replace(b"\r\n", b"\n"):
                    raise ValueError("Unreviewed deployed source difference: " + rel)
                data = prior
            files[rel] = hashlib.sha256(data).hexdigest()
            member = tarfile.TarInfo(rel)
            member.size, member.mode = len(data), path.stat().st_mode & 0o777
            archive.addfile(member, io.BytesIO(data))
    actual = {p for p in files.keys() | old_files.keys() if files.get(p) != old_files.get(p)}
    if actual != changed:
        raise ValueError("Packaged source differs from reviewed scope")
    return files


def checked_runtime_channel_profile(profile):
    spec = profile.get("runtime_channel_update")
    if spec is None:
        return None
    expected = {"source", "target", "container_path", "channel_file", "base_channel_source", "channel_source",
                "base_sha256", "sha256", "pvf_sha256", "base_channels",
                "channels", "channel_id", "port", "channel_type", "group", "name"}
    if not isinstance(spec, dict) or set(spec) != expected:
        raise ValueError("runtime_channel_update has an invalid key set")
    if not isinstance(spec["source"], str) or not spec["source"]:
        raise ValueError("runtime_channel_update.source must be a non-empty path")
    if (spec["target"] != RUNTIME_CHANNEL_TARGET or
            spec["container_path"] != RUNTIME_CHANNEL_CONTAINER or
            spec["channel_file"] != RUNTIME_CHANNEL_FILE):
        raise ValueError("runtime channel target differs from the reviewed channel.override.etc path")
    for key in ("base_sha256", "sha256", "pvf_sha256"):
        if not isinstance(spec[key], str) or not re.fullmatch(r"[0-9a-f]{64}", spec[key]):
            raise ValueError("runtime channel sha256 fields must be lowercase SHA256")
    if (type(spec["base_channels"]) is not int or spec["base_channels"] != 119 or
            type(spec["channels"]) is not int or spec["channels"] != 119 or
            type(spec["channel_id"]) is not int or spec["channel_id"] != 187 or
            type(spec["port"]) is not int or spec["port"] != 10187 or
            type(spec["channel_type"]) is not int or spec["channel_type"] != 39 or
            spec["group"] != "tagtournament" or spec["name"] != "帝国竞技场" or
            spec["base_channel_source"] != "external:approved-119-empire-arena-v1" or
            spec["channel_source"] != "external:approved-119-empire-arena-pvfmap-v2"):
        raise ValueError("runtime channel update differs from the reviewed Imperial Arena binding")
    return spec


def check_profile(profile):
    checked_runtime_channel_profile(profile)
    if "channel_script_encoding" in profile:
        value = profile["channel_script_encoding"]
        if type(value) is not str or value not in ("utf8", "gbk"):
            raise ValueError("channel_script_encoding must be utf8 or gbk")
    if "main_quest_exp_percent" in profile:
        value = profile["main_quest_exp_percent"]
        if type(value) is not int or not 0 <= value <= 100:
            raise ValueError("main_quest_exp_percent must be an integer from 0 to 100")
    if "main_quest_exp_multiplier" in profile:
        value = profile["main_quest_exp_multiplier"]
        if type(value) is not int or not 1 <= value <= 100:
            raise ValueError("main_quest_exp_multiplier must be an integer from 1 to 100")
    if "dungeon_tournament" in profile and profile["dungeon_tournament"] not in ("off", "vendor"):
        raise ValueError("dungeon_tournament must be off or vendor")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", profile["ssh_alias"]):
        raise ValueError("Invalid SSH alias")
    if not re.fullmatch(r"/[A-Za-z0-9_./-]+", profile["remote_root"]) or ".." in profile["remote_root"].split("/"):
        raise ValueError("Invalid remote root")
    if 'edge_ssh_alias' in profile:
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', profile['edge_ssh_alias']):
            raise ValueError('Invalid edge SSH alias')
        root = profile.get('edge_remote_root', profile['remote_root'])
        if not re.fullmatch(r'/[A-Za-z0-9_./-]+', root) or '..' in root.split('/') or root == '/':
            raise ValueError('Invalid edge remote root')
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,62}", profile["compose_project"]):
        raise ValueError("Invalid compose project")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", profile["runtime_base_image"]):
        raise ValueError("A pinned runtime base image is required; do not grow a new layer chain per update")
    if not isinstance(profile.get("probe_command"), list) or not profile["probe_command"] or not all(isinstance(x, str) for x in profile["probe_command"]):
        raise ValueError("Headless channel probe must be an argv array")


def checked_changes(source, base, allowed, upstream=None, ancillary_review=None):
    if not isinstance(allowed, list) or not all(isinstance(p, str) for p in allowed):
        raise ValueError("Reviewed path list must be a JSON string array")
    if git(source, "status", "--porcelain"):
        raise ValueError("Use a clean cumulative source worktree")
    subprocess.run(["git", "merge-base", "--is-ancestor", base, "HEAD"], cwd=source, check=True)
    changed = set(filter(None, git(source, "diff", "--no-renames", "--name-only", "-z", base, "HEAD").split("\0")))
    if changed != set(allowed):
        raise ValueError("Candidate changes differ from reviewed path list: " + repr(sorted(changed ^ set(allowed))))
    exceptions = {}
    for rel in changed:
        safe_path(rel)
        if not (rel.startswith(("go-server/", "Docs/")) or rel in ("changlog.md", "AGENTS.md", "README.md")):
            # Upstream-only ancillary files are archived, not installed as runtime tooling/assets.
            def blob(ref):
                result = subprocess.run(["git", "rev-parse", "--verify", ref + ":" + rel], cwd=source,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                return result.stdout.strip() if result.returncode == 0 else None
            if not upstream or blob("HEAD") != blob(upstream):
                if not upstream or not rel.startswith(("deploy/", "scripts/")) or not rel.endswith(".py"):
                    raise ValueError("Non-game source must exactly match reviewed authoritative master: " + rel)
                def content_hash(ref):
                    if blob(ref) is None:
                        return None
                    data = subprocess.check_output(["git", "show", ref + ":" + rel], cwd=source)
                    return hashlib.sha256(data).hexdigest()
                exceptions[rel] = {"baseSha256": content_hash(base), "upstreamSha256": content_hash(upstream),
                                   "candidateSha256": content_hash("HEAD")}
    if exceptions or ancillary_review is not None:
        expected = {"format": 1, "purpose": "source-archive-only", "baseCommit": base,
                    "upstreamCommit": upstream, "candidateCommit": git(source, "rev-parse", "HEAD"),
                    "files": exceptions}
        if not exceptions or ancillary_review != expected:
            raise ValueError("Non-game source requires an exact three-version archive review; no runtime tools are installed")
    return changed


def checked_recovered_base(source, recovered, snapshot):
    """Accept an honest root snapshot only after every Git blob matches live manifest bytes."""
    if not re.fullmatch(r"[0-9a-f]{40}", recovered):
        raise ValueError("Recovery requires a full new root commit SHA")
    if len(git(source, "rev-list", "--parents", "-n", "1", recovered).split()) != 1:
        raise ValueError("Recovered snapshot must be a new root, without fabricated parents")
    subprocess.run(["git", "merge-base", "--is-ancestor", recovered, "HEAD"], cwd=source, check=True)
    entries = subprocess.check_output(["git", "ls-tree", "-rz", recovered], cwd=source).split(b"\0")
    objects = []
    for entry in filter(None, entries):
        meta, name = entry.split(b"\t", 1)
        mode, kind, oid = meta.split()
        rel = safe_path(name.decode("utf-8"))
        if kind != b"blob" or mode not in (b"100644", b"100755"):
            raise ValueError("Invalid recovered source member: " + rel)
        objects.append((rel, oid))
    data = subprocess.check_output(["git", "cat-file", "--batch"], cwd=source,
                                   input=b"".join(oid + b"\n" for _, oid in objects))
    stream = io.BytesIO(data)
    files = {}
    for rel, oid in objects:
        actual, kind, size = stream.readline().strip().split()
        if actual != oid or kind != b"blob": raise ValueError("Recovered Git object mismatch")
        files[rel] = hashlib.sha256(stream.read(int(size))).hexdigest()
        if stream.read(1) != b"\n": raise ValueError("Invalid Git batch stream")
    if files != snapshot["sourceManifest"]["files"]:
        raise ValueError("Recovered root does not match every deployed source byte")
    return {"recoveredBaseGitCommit": recovered, "originalDeployedSourceCommit": snapshot["active"]["sourceCommit"],
            "originalSourceArchiveSha256": snapshot["active"]["sourceArchiveSha256"],
            "verifiedFiles": len(files), "gitAncestryRecovered": False, "allFilesMatched": True}


class Update:
    def __init__(self, profile, run_id):
        check_profile(profile)
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}", run_id): raise ValueError("Invalid run ID")
        self.profile, self.run_id = json.loads(json.dumps(profile)), run_id
        self.stage = ROOT / "runtime/game-updates" / run_id
        self.remote_stage = profile["remote_root"] + "/runtime/game-updates/" + run_id
        self.worker_code = WORKER.read_bytes()
        self.authority = None
        self.recovery = None
        self.source = None
        self.emergency = None
        self.channel_asset = None

    def checkpoint(self, phase):
        self.authority.check(self.source, phase)
        if source_state(self.source) != self.pinned:
            raise ValueError("Candidate changed during authoritative pull")

    def require_authority(self, phase):
        if self.authority is None:
            raise ValueError("A fresh in-process authoritative pull is required for " + phase)
        return self.authority.require(phase)

    def checked_channel_asset(self, snapshot):
        spec = checked_runtime_channel_profile(self.profile)
        if spec is None:
            return None
        source = Path(spec["source"]).resolve(strict=True)
        if not source.is_file() or source.is_symlink():
            raise ValueError("Runtime channel asset must be one regular file")
        if digest(source) != spec["sha256"]:
            raise ValueError("Runtime channel candidate changed")
        live_channel = snapshot["channelSha256"]
        if live_channel not in (spec["base_sha256"], spec["sha256"]):
            raise ValueError("Live channel file is neither the reviewed 119-row base nor its mapped candidate")
        live_source = (snapshot.get("channel") or {}).get("channelSource")
        expected_source = spec["channel_source"] if live_channel == spec["sha256"] else spec["base_channel_source"]
        if live_source != expected_source:
            raise ValueError("Live channel_source does not match the selected channel state")
        if snapshot["pvfSha256"] != spec["pvf_sha256"]:
            raise ValueError("Live PVF differs from the PVF used for the Imperial Arena evidence")
        return {"path": source, "already_applied": live_channel == spec["sha256"],
                "manifest": {key: value for key, value in spec.items() if key != "source"}}

    def ssh(self, argv, code=None, capture=True):
        return subprocess.run(["ssh", *SSH_OPTIONS, self.profile["ssh_alias"], shlex.join(argv)],
                              input=code, stdout=subprocess.PIPE if capture else None,
                              stderr=subprocess.PIPE if capture else None, check=True)

    def worker(self, mode, capture=True):
        argv = ["sudo", "-n", "python3", "-", mode, "--root", self.profile["remote_root"],
                "--project", self.profile["compose_project"], "--run-id", self.run_id,
                "--image-base", self.profile["runtime_base_image"]]
        if mode in ("prepare", "generate", "deploy"):
            self.checkpoint(mode)
            receipt = self.require_authority(mode)
            raw = json.dumps(receipt, sort_keys=True, separators=(",", ":")).encode()
            path = self.remote_stage + "/authority-current.json"
            code = "from pathlib import Path; import os,sys; os.umask(0o077); p=Path(" + repr(path) + "); assert not p.is_symlink(); t=p.with_suffix('.new'); assert not t.exists(); t.write_bytes(sys.stdin.buffer.read()); os.replace(t,p)"
            self.ssh(["sudo", "-n", "python3", "-c", code], raw)
            argv += ["--authority-nonce", receipt["nonce"], "--authority-proof-sha256", hashlib.sha256(raw).hexdigest()]
        if 'edge_ssh_alias' in self.profile:
            edge_args = ['sudo', '-n', 'python3', '-', 'edge-snapshot', '--root', self.profile.get('edge_remote_root', self.profile['remote_root'])]
            result = subprocess.run(['ssh', *SSH_OPTIONS, self.profile['edge_ssh_alias'], shlex.join(edge_args)], input=self.worker_code, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
            value = json.loads(result.stdout)
            if value.get('format') != 1: raise ValueError('Invalid edge snapshot')
            raw = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
            path = self.profile['remote_root'] + '/runtime/game-update-edge/' + self.run_id + '.json'
            code = "from pathlib import Path; import os,sys; os.umask(0o077); p=Path(" + repr(path) + "); p.parent.mkdir(exist_ok=True); assert not p.is_symlink(); t=p.with_suffix('.new'); assert not t.exists(); t.write_bytes(sys.stdin.buffer.read()); os.replace(t,p)"
            self.ssh(['sudo', '-n', 'python3', '-c', code], raw)
            argv += ['--edge-evidence', path, '--edge-evidence-sha256', hashlib.sha256(raw).hexdigest()]
        return self.ssh(argv, self.worker_code, capture=capture)

    def probe(self, log):
        command = [part.format(root=str(ROOT), run_dir=str(self.stage)) for part in self.profile["probe_command"]]
        with log.open("wb") as f:
            subprocess.run(command, cwd=ROOT, stdout=f, stderr=subprocess.STDOUT, check=True)

    def compatibility(self, snapshot):
        from urllib.parse import urlsplit
        from cryptography.hazmat.primitives import serialization, hashes
        from cryptography.hazmat.primitives.asymmetric import padding
        profile_path = Path(self.profile["player_profile"].format(root=str(ROOT)))
        profile = json.loads(profile_path.read_text(encoding="utf-8-sig"))
        origin = urlsplit(profile["ApiUrl"])
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.check_hostname = False; context.verify_mode = ssl.CERT_NONE
        conn = http.client.HTTPSConnection(origin.hostname, origin.port or 443, context=context, timeout=30)
        conn.connect()
        if hashlib.sha256(conn.sock.getpeercert(True)).hexdigest() != profile["CertificateSha256"].lower():
            raise ValueError("Player gateway TLS pin mismatch")
        conn.request("GET", "/launcher/v2/bootstrap.json")
        response = conn.getresponse()
        if response.status != 200: raise ValueError("Player manifest request failed")
        raw = response.read(); conn.close(); envelope = json.loads(raw)
        payload = base64.b64decode(envelope["Payload"])
        serialization.load_pem_public_key(profile["UpdatePublicKey"].encode()).verify(
            base64.b64decode(envelope["Signature"]), payload,
            padding.PSS(mgf=padding.MGF1(hashes.SHA256()), salt_length=32), hashes.SHA256())
        published = json.loads(payload)
        if published["sequence"] != snapshot["sequence"]: raise ValueError("Player publication changed")
        for key, field in [("ExeSha256", "exeSha256"), ("PvfSha256", "pvfSha256")]:
            if published["profile"][key] != snapshot[field] or profile[key] != snapshot[field]:
                raise ValueError("Client/PVF release gate differs; finish the asset release workflow first")
        self.stage.mkdir(parents=True, exist_ok=True)
        (self.stage / "signed-bootstrap.json").write_bytes(raw)
        receipt = {"signatureVerified": True, "sequence": published["sequence"],
                   "exeSha256": snapshot["exeSha256"], "pvfSha256": snapshot["pvfSha256"]}
        save(self.stage / "compatibility-readback.json", receipt)
        return receipt

    def validate(self, source):
        if self.emergency:
            import shutil
            e = self.emergency
            for entry in e["evidenceFiles"]:
                if digest(Path(entry["path"])) != entry["sha256"]:
                    raise ValueError("Emergency validation evidence changed: " + entry["path"])
            results = json.loads(Path(e["validationResults"]).read_text(encoding="utf-8-sig"))
            required = {"list", "domain", "bridge", "control", "vet", "windows", "linux"}
            if {x["stage"] for x in results} != required or any(x["exitCode"] != 0 for x in results):
                raise ValueError("Emergency requires the complete successful existing validation")
            evidence_paths = {str(Path(x["path"]).resolve()) for x in e["evidenceFiles"]}
            if str(Path(e["validationResults"]).resolve()) not in evidence_paths:
                raise ValueError("Validation results are not pinned")
            if any(str(Path(x["log"]).resolve()) not in evidence_paths for x in results):
                raise ValueError("Validation logs are not pinned")
            if source_state(source)["files"] != e["files"]:
                raise ValueError("Emergency validated source changed")
            validated = Path(e["validatedSource"])
            if any(digest(validated / rel) != sha for rel, sha in e["files"].items()):
                raise ValueError("Emergency source differs from previously validated bytes")
            artifact = e["linuxArtifact"]
            path = Path(artifact["path"])
            if digest(path) != artifact["sha256"] or path.read_bytes()[:4] != b"\x7fELF":
                raise ValueError("Emergency Linux artifact differs")
            info = subprocess.check_output(["go", "version", "-m", str(path)], cwd=source / "go-server", text=True)
            if any(value not in info for value in ["-tags=timetzdata", "GOOS=linux", "GOARCH=amd64"]):
                raise ValueError("Emergency Linux artifact lacks release build settings")
            target = source / "dist/linux-amd64/DNF90Server"
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
            save(self.stage / "validation.json", {"passed": True, "sourceCommit": e["candidateCommit"],
                 "reusedValidation": True, "validatedFiles": len(e["files"]), "results": results,
                 "emergencyEvidenceSha256": e["evidenceSha256"], "linuxArtifact": artifact,
                 "buildInfo": info, "databaseDSNConfigured": False})
            print("PASS: identical validated source and timetzdata Linux artifact; existing tests reused", flush=True)
            return
        env = os.environ.copy(); env.pop("DNFBRIDGE_PLAYER_TEST_DSN", None)
        env.pop("GOOS", None); env.pop("GOARCH", None); env.pop("GOFLAGS", None)
        env.pop("DNFBRIDGE_REAL_PVF_SMOKE", None)
        commands = [
            ["go", "version"], ["go", "list", "-buildvcs=false", "./..."],
            ["go", "test", "-buildvcs=false", "-count=1", "./internal/modules/dnf/..."],
            ["go", "test", "-buildvcs=false", "-count=1", "./internal/services/dnfbridge"],
            ["go", "test", "-buildvcs=false", "-count=1", "./cmd/server/control"],
            ["go", "vet", "./cmd/server/control", "./cmd/server/dnf90", "./cmd/server/doctor", "./internal/app/dnf90", "./internal/services/dnfbridge"],
        ]
        if os.name == "nt":
            commands.append(["go", "build", "-buildvcs=false", "-o", str(self.stage / "DNF90Server.exe"), "./cmd/server/dnf90"])
        for index, command in enumerate(commands):
            print("Validate:", " ".join(command), flush=True)
            with (self.stage / ("check-" + str(index) + ".log")).open("wb") as log:
                subprocess.run(command, cwd=source / "go-server", env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        pvf_commands = self.profile.get("real_pvf_commands", [])
        if pvf_commands:
            path = Path(self.profile["pvf_file"].format(root=str(ROOT)))
            before = json.loads((self.stage / "runtime-before.json").read_text())
            if digest(path) != before["pvfSha256"]: raise ValueError("Test PVF differs from the running server")
            env["DNFBRIDGE_REAL_PVF_SMOKE"] = str(path)
            for index, command in enumerate(pvf_commands):
                with (self.stage / ("pvf-" + str(index) + ".log")).open("wb") as log:
                    subprocess.run(command, cwd=source / "go-server", env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
            env.pop("DNFBRIDGE_REAL_PVF_SMOKE")
        env["GOFLAGS"] = "-tags=timetzdata"
        command = ["cmd", "/c", "build.bat", "linux", "amd64"] if os.name == "nt" else ["sh", "build.sh", "linux", "amd64"]
        with (self.stage / "linux-build.log").open("wb") as log:
            subprocess.run(command, cwd=source, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        save(self.stage / "validation.json", {"sourceCommit": git(source, "rev-parse", "HEAD"),
             "commands": commands + pvf_commands + [command], "passed": True, "databaseDSNConfigured": False})

    def package(self, source, snapshot, changed):
        import shutil
        authority = self.require_authority("package")
        old_manifest = snapshot["sourceManifest"]
        archive_path = self.stage / "before-source.tar.gz"
        old_archive = str(PurePosixPath(snapshot["active"]["sourceDirectory"]).parent / "game-source.tar.gz")
        archive_path.write_bytes(self.ssh(["sudo", "-n", "cat", old_archive]).stdout)
        if digest(archive_path) != snapshot["active"]["sourceArchiveSha256"]: raise ValueError("Base source archive mismatch")
        files = write_archive(source, archive_path, old_manifest["files"], changed, self.stage / "game-source.tar.gz")
        shutil.copy2(source / "dist/linux-amd64/DNF90Server", self.stage / "DNF90Server")
        bundle = self.stage / "game-source.bundle"
        subprocess.run(["git", "bundle", "create", str(bundle), "HEAD"], cwd=source, check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        subprocess.run(["git", "bundle", "verify", str(bundle)], cwd=source, check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        manifest = {"sourceCommit": git(source, "rev-parse", "HEAD"), "baseCommit": snapshot["active"]["sourceCommit"],
                    "upstream": authority["after"], "authorityInvocation": authority["nonce"],
                    "publicGuildCommit": old_manifest.get("publicGuildCommit"),
                    "publicFixCommits": old_manifest.get("publicFixCommits", []), "sourceArchiveSha256": digest(self.stage / "game-source.tar.gz"),
                    "sourceGitBundleSha256": digest(bundle),
                    "serverSha256": digest(self.stage / "DNF90Server"), "files": files, "changedFiles": sorted(changed),
                    "build": ["build.bat", "linux", "amd64"], "GOFLAGS": "-tags=timetzdata"}
        if self.recovery: manifest["sourceRecovery"] = self.recovery
        if self.emergency:
            e = self.emergency
            if manifest["serverSha256"] != e["linuxArtifact"]["sha256"]:
                raise ValueError("Emergency release artifact changed during packaging")
            manifest["upstream"] = old_manifest["upstream"]
            manifest["observedUpstream"] = authority["after"]
            manifest["integrationMode"] = "emergency-selected-mr"
            manifest["emergency"] = {key: e[key] for key in ["baseCommit", "baseArchiveSha256", "candidateCommit",
                "selectedMR", "selectedMergeCommit", "evidenceSha256", "changes"]}
            manifest["build"] = e["linuxArtifact"]["command"]
            manifest["publicFixCommits"] = list(dict.fromkeys(manifest["publicFixCommits"] + [e["selectedSourceCommit"]]))
        if "dungeon_tournament" in self.profile:
            manifest["dungeonTournament"] = self.profile["dungeon_tournament"]
        if "main_quest_exp_multiplier" in self.profile:
            manifest["mainQuestExpMultiplier"] = self.profile["main_quest_exp_multiplier"]
        if "main_quest_exp_percent" in self.profile:
            manifest["mainQuestExpPercent"] = self.profile["main_quest_exp_percent"]
        if "channel_script_encoding" in self.profile:
            manifest["channelScriptEncoding"] = self.profile["channel_script_encoding"]
        if self.channel_asset and not self.channel_asset["already_applied"]:
            staged_channel = self.stage / RUNTIME_CHANNEL_FILE
            shutil.copy2(self.channel_asset["path"], staged_channel)
            if digest(staged_channel) != self.channel_asset["manifest"]["sha256"]:
                raise ValueError("Staged runtime channel candidate changed")
            manifest["runtimeChannelUpdate"] = self.channel_asset["manifest"]
        save(self.stage / "game-source-manifest.json", manifest)

    def deploy(self, source, allowed, recovered_base=None, emergency_evidence=None, ancillary_review=None):
        pinned = source_state(source)
        snapshot = json.loads(self.worker("snapshot").stdout)
        self.stage.mkdir(parents=True, exist_ok=False)
        save(self.stage / "runtime-before.json", snapshot)
        self.channel_asset = self.checked_channel_asset(snapshot)
        if self.channel_asset:
            save(self.stage / "channel-asset.json", {"alreadyApplied": self.channel_asset["already_applied"], **self.channel_asset["manifest"]})
        self.source = source
        self.pinned = pinned
        if emergency_evidence:
            e = json.loads(emergency_evidence.read_text(encoding="utf-8-sig"))
            if (e.get("authorization") != "urgent-service-recovery" or e.get("selectedMR") != 226 or
                e.get("selectedMergeCommit") != "963eabb6b125b09fba4312f650a81d90a96cc96a" or
                e.get("candidateCommit") != pinned["head"] or e.get("files") != pinned["files"] or
                e.get("baseCommit") != snapshot["active"]["sourceCommit"] or
                e.get("baseArchiveSha256") != snapshot["active"]["sourceArchiveSha256"] or
                not recovered_base or e.get("recoveredBase") != recovered_base):
                raise ValueError("Emergency authorization/source/active binding differs")
            changes = e["changes"]
            if len(changes) != 6 or {x["path"] for x in changes} != EMERGENCY_MR226_PATHS or set(allowed) != EMERGENCY_MR226_PATHS:
                raise ValueError("Emergency requires the exact reviewed six files")
            expected = dict(snapshot["sourceManifest"]["files"])
            for x in changes:
                if expected.get(x["path"]) != x["beforeSha256"]:
                    raise ValueError("Emergency before bytes differ")
                expected[x["path"]] = x["afterSha256"]
            if expected != pinned["files"]:
                raise ValueError("Emergency includes unreviewed source changes")
            for key in ("dungeon_tournament", "main_quest_exp_multiplier", "main_quest_exp_percent", "channel_script_encoding"):
                if key in self.profile:
                    raise ValueError("Emergency is source-only; omit runtime configuration changes")
            e["evidenceSha256"] = digest(emergency_evidence)
            self.emergency = e
            save(self.stage / "emergency-evidence.json", e)
        self.authority = AuthorityGate(self.stage / "authoritative-master", self.stage, self.run_id, self.emergency)
        self.checkpoint("start")
        base = snapshot["active"]["sourceCommit"]
        if recovered_base:
            self.recovery = checked_recovered_base(source, recovered_base, snapshot)
            save(self.stage / "source-recovery.json", self.recovery)
            base = recovered_base
        archive_review = None
        if ancillary_review:
            if self.emergency or recovered_base:
                raise ValueError("Archive review cannot be combined with emergency/recovery modes")
            archive_review = json.loads(ancillary_review.read_text(encoding="utf-8-sig"))
        changed = checked_changes(source, base, allowed, self.authority.baseline, archive_review)
        if archive_review:
            save(self.stage / "ancillary-source-review.json", archive_review)
        if pinned["head"] == snapshot["active"]["sourceCommit"]:
            if self.channel_asset and not self.channel_asset["already_applied"]:
                raise ValueError("A runtime channel update must accompany the next reviewed source deployment")
            mode = self.profile.get("dungeon_tournament")
            if mode is not None and snapshot.get("tournament", {}).get("sourceMode") != mode:
                raise ValueError("Tournament configuration changes require a reviewed source update")
            multiplier = self.profile.get("main_quest_exp_multiplier")
            if multiplier is not None and snapshot.get("mainQuestExperience", {}).get("sourceMultiplier") != multiplier:
                raise ValueError("Main quest experience configuration changes require a reviewed source update")
            percent = self.profile.get("main_quest_exp_percent")
            if percent is not None and snapshot.get("mainQuestExperience", {}).get("sourcePercent") != percent:
                raise ValueError("Main quest percent configuration changes require a reviewed source update")
            encoding = self.profile.get("channel_script_encoding")
            if encoding is not None and snapshot.get("channelScriptEncoding", {}).get("sourceEncoding") != encoding:
                raise ValueError("Channel script encoding changes require a reviewed source update")
            self.compatibility(snapshot); self.probe(self.stage / "probe.log")
            if source_state(source) != pinned: raise ValueError("Candidate changed during verification")
            save(self.stage / "no-op.json", {"sourceCommit": pinned["head"], "restarted": False, "verified": True})
            print("Already deployed: current process, signed profile and channel probe verified; no restart")
            return
        if not changed: raise ValueError("No source changes")
        save(self.stage / "scope.json", {"source": str(source), "changedFiles": sorted(changed)})
        self.compatibility(snapshot)
        self.probe(self.stage / "probe-before.log")
        self.validate(source)
        if source_state(source) != pinned: raise ValueError("Candidate changed during validation")
        self.checkpoint("package")
        self.package(source, snapshot, changed)
        if source_state(source) != pinned: raise ValueError("Candidate changed during packaging")
        self.checkpoint("upload")
        self.upload()
        (self.stage / "worker.py").write_bytes(self.worker_code)
        for mode in ["prepare", "generate"]:
            print("Remote:", mode, flush=True)
            self.worker(mode, capture=False)
        self.switch_and_check(pinned["head"], snapshot["active"]["sourceCommit"])
        try:
            self.cleanup(execute=True)
        except Exception as error:
            save(self.stage / "cleanup-deferred.json", {"reason": type(error).__name__, "deploymentVerified": True})
            print("Deployment verified; cleanup deferred to the daily timer. See receipt.", flush=True)
        print("Deployment verified. Game acceptance remains a separate check. Receipt:", self.stage)

    def upload(self):
        self.require_authority("upload")
        self.ssh(["mkdir", "-p", str(PurePosixPath(self.remote_stage).parent)])
        self.ssh(["mkdir", "-m", "700", self.remote_stage])
        names = ["DNF90Server", "game-source.tar.gz", "game-source.bundle", "game-source-manifest.json", "runtime-before.json", "compatibility-readback.json"]
        if self.channel_asset and not self.channel_asset["already_applied"]:
            names.append(RUNTIME_CHANNEL_FILE)
        for name in names:
            print("Upload:", name, flush=True)
            subprocess.run(["scp", "-q", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                            str(self.stage / name), self.profile["ssh_alias"] + ":" + self.remote_stage + "/" + name], check=True)

    def switch_and_check(self, expected, previous):
        try:
            self.worker("deploy", capture=False)
        except (subprocess.CalledProcessError, OSError):
            # SSH can disconnect after a successful switch. Never replay deploy.
            reconciled = None
            for _ in range(20):
                try:
                    reconciled = json.loads(self.worker("verify").stdout)
                    break
                except (subprocess.CalledProcessError, OSError):
                    print("Reconnecting to determine deployment outcome...", flush=True)
                    time.sleep(10)
            if reconciled is None:
                raise RuntimeError("Deployment outcome unknown. Run verify; do not repeat deploy or overwrite its receipt.")
            actual = reconciled["active"]["sourceCommit"]
            save(self.stage / "reconciled.json", reconciled)
            if actual != expected:
                raise RuntimeError("Deployment did not become active; " + ("previous version verified" if actual == previous else "another baseline is active"))
        try:
            verified = json.loads(self.worker("verify").stdout)
            if verified["active"]["sourceCommit"] != expected: raise ValueError("Running source differs")
            self.compatibility(verified)
            self.probe(self.stage / "probe-after.log")
            save(self.stage / "verified.json", verified)
            receipt = self.ssh(["sudo", "-n", "cat", self.remote_stage + "/game-deployment.json"]).stdout
            (self.stage / "game-deployment.json").write_bytes(receipt)
        except BaseException:
            print("Post-deployment check failed; restoring previous game image/config", flush=True)
            self.worker("rollback", capture=False)
            self.probe(self.stage / "probe-rollback.log")
            raise

    def cleanup(self, execute=False):
        argv = ["sudo", "-n", "python3", self.profile["remote_root"] + "/deploy/remote/backup_retention.py", "--runtime-dir", self.profile["remote_root"] + "/runtime"]
        if execute: argv.append("--apply")
        result = json.loads(self.ssh(argv).stdout)
        self.stage.mkdir(parents=True, exist_ok=True)
        save(self.stage / ("cleanup-result.json" if execute else "cleanup-plan.json"), result)
        print(json.dumps(result if execute else {"removeCount": len(result.get("remove", [])), "reclaimBytes": result.get("reclaimBytes"), "protected": result.get("protected")}, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["inspect", "deploy", "verify", "rollback", "cleanup"])
    parser.add_argument("--profile", type=Path, default=ROOT / "runtime/game-update/profile.json")
    parser.add_argument("--source", type=Path)
    parser.add_argument("--changed-files", type=Path, help="JSON array of reviewed repository-relative paths")
    parser.add_argument("--recovered-base", help="New root commit matching every deployed manifest byte; never the lost SHA")
    parser.add_argument("--emergency-evidence", type=Path, help="Explicitly authorized MR226 recovery with exact source/validation hashes")
    parser.add_argument("--ancillary-review", type=Path, help="Exact base/upstream/candidate hashes for reviewed Python source archives; never installs runtime tools")
    parser.add_argument("--run-id")
    parser.add_argument("--apply", action="store_true", help="Apply a backup cleanup plan")
    args = parser.parse_args()
    if args.command == "rollback" and not args.run_id: parser.error("rollback requires --run-id")
    run_id = args.run_id or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    update = Update(json.loads(args.profile.read_text(encoding="utf-8-sig")), run_id)
    if args.command == "deploy":
        if not args.source or not args.changed_files: parser.error("deploy requires --source and --changed-files")
        update.deploy(args.source.resolve(), json.loads(args.changed_files.read_text(encoding="utf-8-sig")), args.recovered_base, args.emergency_evidence, args.ancillary_review)
    elif args.command == "cleanup": update.cleanup(args.apply)
    elif args.command == "rollback":
        update.worker("rollback", capture=False)
        data = json.loads(update.worker("verify").stdout)
        update.stage.mkdir(parents=True, exist_ok=True)
        update.compatibility(data); update.probe(update.stage / "probe-rollback.log")
        save(update.stage / "rollback-verified.json", data)
    else:
        data = json.loads(update.worker("snapshot" if args.command == "inspect" else "verify").stdout)
        update.stage.mkdir(parents=True, exist_ok=False)
        save(update.stage / (args.command + ".json"), data)
        if args.command == "verify":
            update.compatibility(data); update.probe(update.stage / "probe.log")
        print(json.dumps({"sourceCommit": data["active"]["sourceCommit"], "serverSha256": data["serverSha256"],
                          "sessions": data["sessions"], "sequence": data["sequence"], "receipt": str(update.stage)}, indent=2))


if __name__ == "__main__": main()
