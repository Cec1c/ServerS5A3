"""Run 620 fixed-v2 native cases, without rebuilding."""
from pathlib import Path
import hashlib
import json
import sys

sys.dont_write_bytecode = True
if not __debug__:
    raise RuntimeError("Assertions are required; do not use -O or PYTHONOPTIMIZE.")

import verify_core as core
import verify_render_path as render
import verify_direct_input as direct
import verify_completion as completion
import verify_callback as callback


def main(argv=None):
    args = core.parse_args(__doc__, argv)
    try:
        # Read only these portable sources and explicitly supplied images.
        sources = {}
        for name in ("verify_candidate.py", "verify_core.py", "verify_render_path.py",
                     "verify_direct_input.py", "verify_completion.py", "verify_callback.py", "README-verifier.md"):
            path = Path(__file__).resolve().parent / name
            sources[name] = hashlib.sha256(path.read_bytes()).hexdigest()
        inputs = core.load_inputs(args.base_client_exe, args.candidate_exe)
        output = core.create_output_dir(args.output_dir)
        groups = []
        for name, run, receipt_name, count in (
            ("core", core.run_suite, "native-verification-v2.json", 429),
            ("render_path", render.run_suite, "native-render-path-v2.json", 26),
            ("direct_input", direct.run_suite, "native-direct-input-v2.json", 17),
            ("completion", completion.run_suite, "native-completion-v1.json", 120),
            ("callback", callback.run_suite, "native-callback-v1.json", 28),
        ):
            print("Running {} ({} cases)".format(name, count), flush=True)
            result = core.run_logged(name, run, receipt_name, count, output)
            groups.append(dict(
                name=name, passed=result["passed"], caseCount=result["caseCount"],
                receipt=receipt_name, stdout=name + ".stdout.log", stderr=name + ".stderr.log",
                realExecution=result["realExecution"], fixtures=result["fixtures"],
                limitations=result.get("limitations", result.get("limits", [])),
            ))
        summary = dict(
            passed=True, caseCount=sum(group["caseCount"] for group in groups), groups=groups,
            inputs=inputs, sourceSha256=sources, dependencies=core.dependency_info(),
            scope="fixed-v2 bounded x86 execution with synthetic objects and fixture leaves",
            limitations=[
                "Original fixture boundaries and per-group limitations remain in effect.",
                "Resource lifecycle, physical layout compaction and full GUI are not covered.",
                "The historical five-case v1 gap evidence is not loaded or rerun.",
            ],
            v1NegativeControlRequired=False, v1NegativeControlExecuted=False,
            processAccess=False, rebuilt=False, deployed=False, guiAccepted=False,
        )
        core.write_result(output / "summary.json", summary)
    except Exception as exc:
        print("Verification failed: {}: {}".format(type(exc).__name__, exc), file=sys.stderr)
        print("Any created output directory and logs are retained; no aggregate pass is issued.",
              file=sys.stderr)
        return 1
    print(json.dumps(dict(passed=True, caseCount=summary["caseCount"],
                          candidateSha256=core.CANDIDATE_SHA, outputDir=str(output))))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
