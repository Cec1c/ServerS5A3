from pathlib import Path
import hashlib,json,subprocess,sys
def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(args: list[str], *, accepted: tuple[int, ...] = (0,)) -> str:
    proc = subprocess.run(
        args,
        capture_output=True,
        creationflags=0x08000000 if sys.platform == "win32" else 0,
    )
    stdout = proc.stdout.decode("utf-8", "replace")
    stderr = proc.stderr.decode("utf-8", "replace")
    if proc.returncode not in accepted:
        raise RuntimeError(
            f"command failed ({proc.returncode}): {args!r}\nstdout:\n{stdout}\nstderr:\n{stderr}"
        )
    return stdout


def read_tokens(cli: Path, pvf: Path, archive_path: str, out_path: Path) -> list[dict]:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    if out_path.exists():
        out_path.unlink()
    run(
        [
            str(cli),
            "read",
            "--pvf",
            str(pvf),
            "--path",
            archive_path,
            "--format",
            "tokens",
            "--out",
            str(out_path),
        ]
    )
    return json.loads(out_path.read_text(encoding="utf-8"))["tokens"]


def read_text(cli: Path, pvf: Path, archive_path: str, out_path: Path) -> str:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    if out_path.exists():
        out_path.unlink()
    run(
        [
            str(cli),
            "read",
            "--pvf",
            str(pvf),
            "--path",
            archive_path,
            "--format",
            "text",
            "--out",
            str(out_path),
        ]
    )
    return out_path.read_text(encoding="utf-8-sig")


def signed_u32(value: int) -> int:
    return value if value < 0x80000000 else value - 0x100000000


def plan_token(token: dict) -> dict:
    kind = int(token["type"])
    raw = int(token.get("payload_u32", 0))
    item = {"Type": kind, "Value": signed_u32(raw)}
    if kind in (3, 5, 6, 7):
        item["Text"] = token.get("text", "")
        # Transferring a stream between archives must intern its resolved text;
        # raw string-table indices belong exclusively to the source archive.
        item["PreservePayload"] = False
    return item


def tag(value: str) -> dict:
    return {"type": 3, "text": value}


def string(value: str) -> dict:
    return {"type": 6, "text": value}


def number(value: int) -> dict:
    return {"type": 0, "payload_u32": value & 0xFFFFFFFF}


def token_text(token: dict) -> str:
    return token.get("text", "") if int(token["type"]) in (3, 5, 6, 7) else ""


def token_value(token: dict) -> int:
    return signed_u32(int(token["payload_u32"]))


def section_bounds(tokens: list[dict], name: str) -> tuple[int, int, bool]:
    opening = f"[{name}]"
    closing = f"[/{name}]"
    starts = [i for i, token in enumerate(tokens) if int(token["type"]) == 3 and token_text(token) == opening]
    if len(starts) != 1:
        raise AssertionError(f"expected exactly one {opening}, found {len(starts)}")
    start = starts[0]
    for end in range(start + 1, len(tokens)):
        if int(tokens[end]["type"]) == 3 and token_text(tokens[end]) == closing:
            return start, end, True
        if int(tokens[end]["type"]) == 3:
            return start, end, False
    return start, len(tokens), False


def replace_section(tokens: list[dict], name: str, values: list[dict]) -> None:
    start, end, _ = section_bounds(tokens, name)
    tokens[start + 1 : end] = values


def remove_section(tokens: list[dict], name: str) -> None:
    start, end, closed = section_bounds(tokens, name)
    del tokens[start : end + (1 if closed else 0)]


def append_before_section(tokens: list[dict], name: str, values: list[dict]) -> None:
    start, _, _ = section_bounds(tokens, name)
    tokens[start:start] = values


def assert_section_value(tokens: list[dict], name: str, expected: tuple[str, int] | None) -> None:
    start, end, _ = section_bounds(tokens, name)
    values = tokens[start + 1 : end]
    if expected is None:
        if values:
            raise AssertionError(f"[{name}] must be empty")
        return
    want_text, want_number = expected
    if len(values) != 2 or token_text(values[0]) != want_text or token_value(values[1]) != want_number:
        raise AssertionError(f"[{name}] mismatch: {values!r}")


def semantic_tokens(tokens: list[dict]) -> list[tuple[int, str | int]]:
    result: list[tuple[int, str | int]] = []
    for token in tokens:
        kind = int(token["type"])
        if kind in (3, 5, 6, 7):
            result.append((kind, token_text(token)))
        else:
            result.append((kind, token_value(token)))
    return result


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
