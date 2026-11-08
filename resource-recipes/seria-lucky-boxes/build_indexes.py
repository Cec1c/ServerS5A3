"""Generate the single frozen six-index set for the remaining batch."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("pvf", "gm-tool", "index-tool", "output", "common-builder"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    manifest = json.loads((HERE / "indexes-manifest.json").read_text("utf8"))
    common = args.common_builder.resolve()
    if hashlib.sha256(common.read_bytes()).hexdigest() != manifest["commonScriptSHA256"]:
        raise ValueError("unchanged common index builder required")
    spec = importlib.util.spec_from_file_location("batch_final_indexes", common)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    result = module.build(args.pvf.resolve(), args.gm_tool.resolve(), args.index_tool.resolve(), args.output.resolve(), manifest)
    print(json.dumps(dict(passed=result["passed"], indexesGeneratedOnce=True, pvfSha256=result["pvfSHA256"], files=len(result["files"]))))

if __name__ == "__main__":
    main()
