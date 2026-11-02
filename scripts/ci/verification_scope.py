"""Select Linux checks from the actual PR diff, including removed files."""
import argparse
import json
import os
import subprocess

def classify(paths, full=False):
    paths = set(paths)
    policy = any(p.startswith(".github/workflows/") or p == "scripts/ci/verification_scope.py" for p in paths)
    game = full or policy or any(p.startswith("go-server/") or p in ("build.sh", "go.mod", "go.sum", "go.work", "go.work.sum") for p in paths)
    tooling = full or policy or any(p.startswith(("scripts/", "deploy/")) or p.endswith((".bat", ".py", ".sh")) for p in paths)
    container = full or any(p in ("deploy/docker/Dockerfile", ".dockerignore") for p in paths)
    return {"game": game, "tooling": tooling, "container": container}

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base")
    parser.add_argument("--full", action="store_true")
    args = parser.parse_args()
    paths = subprocess.check_output(["git", "diff", "--name-only", "-z", args.base, "HEAD"]).decode().split("\0") if args.base else []
    result = classify(paths, args.full)
    print(json.dumps(result))
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as out:
        for key, value in result.items():
            out.write(f"{key}={str(value).lower()}\n")

if __name__ == "__main__":
    main()
