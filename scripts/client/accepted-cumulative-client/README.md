# Reviewed cumulative client recipe

This recipe rebuilds the reviewed cumulative client from one exact base image. It writes only a new output directory and does not install or execute the client, contact a service, or publish files.

| Stage | Input SHA-256 | Output SHA-256 |
| --- | --- | --- |
| Base | `9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250` | - |
| Buff group registration | `9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250` | `0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f` |
| Receive admission and key release | `0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f` | `748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82` |
| Live raid roster refresh | `748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82` | `1bdff5b5da1bfd54a1005a555e89a905cbedee3d1884ffe7ca22a54dfad381c9` |
| Retried movie prompt guard | `1bdff5b5da1bfd54a1005a555e89a905cbedee3d1884ffe7ca22a54dfad381c9` | `d323e8cba1116b87a99716b92da10f36bc29999ad740fcada2fc8143e721ceda` |
| Failure movie close | `d323e8cba1116b87a99716b92da10f36bc29999ad740fcada2fc8143e721ceda` | `69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544` |

All intermediates and the base are exactly 112607232 bytes. Each preceding recipe rejects unknown input identities, checks its selected hook bytes and RX padding, and records the changed byte ranges. The combined recipe verifies every step hash, every intermediate output hash, the final output hash, and that restoring those ranges reproduces the original image.

Build from the repository root after integrating this overlay:

```text
python -B scripts/client/accepted-cumulative-client/build_candidate.py --input <BASE_927_EXE> --output-dir <NEW_CANDIDATE_DIR>
```

The output directory must not exist. The result contains `DNF.exe` and `build.json`. Construction reuses the existing `quiet-profile-refresh` and `buff-local-snapshot` modules plus the minimal `local-patches` helpers included with this change. The existing `activeness-ui` verifier and receiver helper remain available to the separate offline verification paths.

This construction check does not run native verification, the game client, or an online service. Use the selected feature verifiers for bounded offline checks and keep game acceptance separate.
