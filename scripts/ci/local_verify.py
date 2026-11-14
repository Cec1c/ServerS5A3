"""Run scoped Linux checks and a fresh server build in Docker, with persistent Go caches."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]


def git(root, *args):
    return subprocess.check_output(["git", *args], cwd=root).decode("utf-8").strip()


def source_identity(root):
    # Git checks the working tree; no Python rehash of every tracked file.
    status = git(root, "status", "--porcelain", "--untracked-files=all")
    return {"head": git(root, "rev-parse", "HEAD"),
            "tree": git(root, "rev-parse", "HEAD^{tree}"), "clean": not status}


def commands(packages, run=None, full=False, build_only=False, race=False, uncached=False):
    if sum((bool(packages), full, build_only)) != 1:
        raise ValueError("choose --package (repeatable), --full, or --build-only")
    if (run or race) and not packages:
        raise ValueError("--run and --race apply to scoped --package checks")
    if build_only and uncached:
        raise ValueError("--uncached applies to tests")
    for package in packages:
        if not re.fullmatch(r"\./[A-Za-z0-9_/-]+(?:/\.\.\.)?", package) or "/../" in package:
            raise ValueError("packages must be local paths relative to go-server")
        if package.startswith("./internal/services/dnfbridge") and not run:
            raise ValueError("dnfbridge requires an explicit --run; use --full for a broad regression")
    checks = [("modules", "cd go-server && go mod verify")]
    if full:
        checks.append(("full", ("GO_TEST_UNCACHED=1 " if uncached else "") + "sh scripts/ci/test_core.sh"))
    elif packages:
        args = ["go", "test", "-json", "-buildvcs=false", "-mod=readonly"]
        if run: args += ["-run", run]
        if race: args += ["-race"]
        if uncached: args += ["-count=1"]
        checks += [("targeted", "cd go-server && " + shlex.join(args + packages)),
                   ("vet", "cd go-server && " + shlex.join(["go", "vet", "-mod=readonly", *packages]))]
    checks.append(("build", "cd go-server && CGO_ENABLED=0 go build -tags=timetzdata "
                   "-buildvcs=false -mod=readonly -trimpath -ldflags='-s -w' "
                   "-o /output/DNF90Server ./cmd/server/dnf90"))
    return checks


def docker_command(root, output, image, cache_prefix, command):
    if not re.fullmatch(r"[a-z0-9][a-z0-9_.-]+", cache_prefix):
        raise ValueError("invalid cache prefix")
    git_dir = Path(git(root, "rev-parse", "--absolute-git-dir"))
    common = Path(git(root, "rev-parse", "--path-format=absolute", "--git-common-dir"))
    mounts = [(root, "/source", True), (git_dir, "/git", True),
              (common, "/git-common", True), (output, "/output", False)]
    args = ["docker", "run", "--rm", "--network=default"]
    for path, target, readonly in mounts:
        if "," in str(path): raise ValueError("Docker bind paths may not contain commas")
        args += ["--mount", f"type=bind,source={path},target={target}" + (",readonly" if readonly else "")]
    args += ["--mount", f"type=volume,source={cache_prefix}-gomod,target=/go/pkg/mod",
             "--mount", f"type=volume,source={cache_prefix}-gocache,target=/root/.cache/go-build",
             "-e", "GIT_DIR=/git", "-e", "GIT_COMMON_DIR=/git-common", "-e", "GIT_WORK_TREE=/source",
             "-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory",
             "-e", "GIT_CONFIG_VALUE_0=/source", "-e", "GOOS=linux", "-e", "GOARCH=amd64",
             "-w", "/source", image, "sh", "-c", command]
    return args


def sha256(path):
    with Path(path).open("rb") as stream:
        value = hashlib.sha256()
        for data in iter(lambda: stream.read(1024 * 1024), b""): value.update(data)
    return value.hexdigest()


def execute(root, output, checks, image, cache_prefix):
    before = source_identity(root)
    if not before["clean"]:
        raise ValueError("use a clean committed worktree so the receipt binds exact source")
    output.mkdir(parents=True, exist_ok=False)
    receipt = dict(schema=1, sourceCommit=before["head"], sourceTree=before["tree"],
                   mode="docker-linux-local", image=image, startedAt=datetime.now(timezone.utc).isoformat(),
                   passed=False, testsExecuted=any(n in ("full", "targeted") for n, _ in checks),
                   commands=[], artifactsRebuilt=False)
    try:
        for name, command in checks:
            started = time.monotonic()
            argv = docker_command(root, output, image, cache_prefix, command)
            observed = 0
            with (output / (name + ".log")).open("w", encoding="utf-8") as log:
                process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                           text=True, encoding="utf-8", errors="replace")
                for line in process.stdout:
                    print(line, end="", flush=True); log.write(line)
                    if name == "targeted":
                        try: event = json.loads(line)
                        except json.JSONDecodeError: continue
                        if event.get("Action") == "pass" and event.get("Test"): observed += 1
                code = process.wait()
            receipt["commands"].append(dict(stage=name, command=command, exitCode=code,
                seconds=round(time.monotonic()-started, 3), testsPassed=observed if name == "targeted" else None))
            if code: raise RuntimeError(name + " failed; see " + str(output / (name + ".log")))
            if name == "targeted" and not observed:
                raise ValueError("no tests matched; correct --package/--run before attesting success")
        if source_identity(root) != before:
            raise ValueError("source changed during validation")
        receipt.update(passed=True, artifactsRebuilt=True, serverSha256=sha256(output / "DNF90Server"))
    except BaseException as error:
        receipt["error"] = str(error)
        raise
    finally:
        receipt["completedAt"] = datetime.now(timezone.utc).isoformat()
        (output / "validation.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=ROOT)
    parser.add_argument("--package", action="append", default=[])
    parser.add_argument("--run")
    parser.add_argument("--full", action="store_true")
    parser.add_argument("--build-only", action="store_true")
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--uncached", action="store_true")
    parser.add_argument("--image", default="golang:1.26.5")
    parser.add_argument("--cache-prefix", default="dnf-local")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    checks = commands(args.package, args.run, args.full, args.build_only, args.race, args.uncached)
    execute(args.source.resolve(), args.output.resolve(), checks, args.image, args.cache_prefix)


if __name__ == "__main__": main()
