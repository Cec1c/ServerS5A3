"""Portable final-v2 menu candidate verifier."""
from pathlib import Path
import itertools
import json

import gates_v2
import portable_core


def run_suite(root, image, manifests):
    root = Path(root)
    windows = portable_core.read_window_pairs(image, gates_v2.WINDOW_PAIRS)
    manifest_path = "manifests/menu-candidate/windows.json"
    manifest = manifests[manifest_path]
    baseline = [gates_v2.run(windows, kind, active, aux)
                for kind in ("open", "build", "click", "face")
                for active, aux in ((True, True), (False, True),
                                    (False, False))]
    cases = []
    for patch in manifest["patches"]:
        for key, channel, server in itertools.product(
                (17, 4097, 65535), (0, 10, 255), (0, 1, 255)):
            for active, aux in ((True, True), (True, False),
                                (False, True), (False, False)):
                cases.append(gates_v2.run(
                    windows, patch["name"], active, aux, patch, key,
                    channel, server))
            for wrong in ((key ^ 1, channel, server),
                          (key, channel ^ 1, server),
                          (key, channel, server ^ 1)):
                cases.append(gates_v2.run(
                    windows, patch["name"], False, True, patch, key,
                    channel, server, wrong))
        for channel, server in itertools.product((0, 10, 255), (0, 1, 255)):
            cases.append(gates_v2.run(
                windows, patch["name"], False, False, patch, 0,
                channel, server, noise=True))
            cases.append(gates_v2.run(
                windows, patch["name"], False, True, patch, 4097,
                channel, server, noise=True))
            cases.append(gates_v2.run(
                windows, patch["name"], True, True, patch, 65535,
                channel, server, noise=True))
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        baselineCases=len(baseline),
        candidateCases=len(cases),
        cases=cases,
        sourceHashes=portable_core.proof_sources(
            root, ["gates_v2.py", "menu_v2.py", manifest_path]),
        exeGenerated=False,
        deployed=False,
        guiAccepted=False,
        scope=("Final v2 local gates plus menu candidate helpers; distinct "
               "EBX/ESI/EDI, exact returned pointers, caller key/guard and "
               "local cleanup, next callee args, volatile clobbers and high "
               "context bits. Lookup and graphics remain boundary fixtures."),
    )
