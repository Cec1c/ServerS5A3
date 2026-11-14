"""Prepare a compact local game payload; remote activation requires protocol support."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile

SCHEMA = "dnf-game-compact-v1"


def git(source, *args):
    return subprocess.check_output(["git", *args], cwd=source).decode("utf-8").strip()


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""): value.update(data)
    return value.hexdigest()


def tool_path(source, relative):
    path = PurePosixPath(relative)
    if (not relative or path.is_absolute() or path.as_posix() != relative or
            ".." in path.parts or "\\" in relative or ":" in relative or
            not relative.startswith("deploy/") or path.suffix not in (".py", ".sh") or
            path.name.startswith("test_")):
        raise ValueError("expected an explicit deploy Python/shell tool: " + relative)
    target = source / relative
    if (not target.is_file() or any(p.is_symlink() for p in [target, *target.parents]) or
            not target.resolve().is_relative_to(source.resolve())):
        raise ValueError("tool must be a regular file within source")
    return target


def package(source, build_receipt, base, resources, output, extra_tools=()):
    source = source.resolve()
    if not re.fullmatch("[a-f0-9]{40}", base): raise ValueError("full base commit required")
    if git(source, "status", "--porcelain", "--untracked-files=all"):
        raise ValueError("source must be clean")
    head, tree = git(source, "rev-parse", "HEAD"), git(source, "rev-parse", "HEAD^{tree}")
    subprocess.run(["git", "merge-base", "--is-ancestor", base, head], cwd=source, check=True)
    receipt_raw = build_receipt.read_bytes()
    build = json.loads(receipt_raw)
    if (build.get("sourceCommit") != head or build.get("sourceTree") != tree or
            build.get("passed") is not True or build.get("artifactsRebuilt") is not True or
            build.get("mode") != "docker-linux-local"):
        raise ValueError("require a successful Docker build receipt for this exact source")
    binary = build_receipt.parent / "DNF90Server"
    if binary.is_symlink() or not re.fullmatch("[a-f0-9]{64}", str(build.get("serverSha256"))):
        raise ValueError("invalid program identity")
    with binary.open("rb") as stream: header = stream.read(20)
    if header[:6] != b"\x7fELF\x02\x01" or header[18:20] != b"\x3e\x00":
        raise ValueError("program must be Linux ELF amd64")
    keys = ("exeSha256", "pvfSha256", "channelSha256")
    if set(resources) != set(keys) or any(not re.fullmatch("[a-f0-9]{64}", str(resources[k])) for k in keys):
        raise ValueError("provide only the three verified resource baseline SHA256 values")
    tracked = set(git(source, "ls-files", "-z", "--", "deploy").split("\0"))
    names = sorted({p for p in tracked if p.startswith("deploy/docker/") and
                    PurePosixPath(p).suffix in (".py", ".sh") and
                    not PurePosixPath(p).name.startswith("test_")} | set(extra_tools))
    if not names or any(p not in tracked for p in names): raise ValueError("tools must be tracked")
    paths = {name: tool_path(source, name) for name in names}
    output.mkdir(parents=True, exist_ok=False)
    program_hash, program_bytes = hashlib.sha256(), 0
    with (output / "DNF90Server.gz").open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed:
        with binary.open("rb") as stream:
            for data in iter(lambda: stream.read(1024 * 1024), b""):
                program_hash.update(data); program_bytes += len(data); compressed.write(data)
    if program_hash.hexdigest() != build["serverSha256"]:
        raise ValueError("program differs from the build receipt")
    files = {}
    with (output / "deploy-tools.tar.gz").open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            for name, path in paths.items():
                data = path.read_bytes()
                files[name] = hashlib.sha256(data).hexdigest()
                member = tarfile.TarInfo(name); member.size = len(data); member.mode = 0o644
                archive.addfile(member, io.BytesIO(data))
    if git(source, "rev-parse", "HEAD") != head or git(source, "status", "--porcelain", "--untracked-files=all"):
        raise ValueError("source changed while packaging")
    if build_receipt.read_bytes() != receipt_raw:
        raise ValueError("build receipt changed while packaging")
    manifest = dict(schema=SCHEMA, sourceCommit=head, sourceTree=tree, baseCommit=base,
                    serverSha256=build["serverSha256"], serverBytes=program_bytes,
                    resourceIdentity=resources, deployTools=files,
                    buildReceiptSha256=hashlib.sha256(receipt_raw).hexdigest(),
                    buildImage=build["image"], buildCommands=build["commands"],
                    receiverCapability=SCHEMA, sourceArchiveIncluded=False, gitBundleIncluded=False)
    manifest["transport"] = {name: dict(sha256=digest(output/name), bytes=(output/name).stat().st_size)
                             for name in ("DNF90Server.gz", "deploy-tools.tar.gz")}
    (output/"manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--build-receipt", type=Path, required=True)
    parser.add_argument("--base-commit", required=True)
    parser.add_argument("--resource-identity", type=Path, required=True)
    parser.add_argument("--tool", action="append", default=[])
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = package(args.source, args.build_receipt, args.base_commit,
                     json.loads(args.resource_identity.read_text("utf-8-sig")), args.output, args.tool)
    print(json.dumps({"schema": result["schema"], "sourceCommit": result["sourceCommit"],
                      "transport": result["transport"], "uploaded": False}, indent=2))


if __name__ == "__main__": main()
