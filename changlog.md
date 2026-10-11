# Repository changelog

## 2026-10-11 — Online repository acceptance handoff

- Record source import commit 22960444f648a4f2f5bcc7b8a3fbe7086eebc5cc as merged into master.
- Add Docs/ACCEPTANCE_HANDOFF.md with previously accepted items, exact acceptance boundaries and merged source status.
- Preserve all pending GUI items and r98 deployment/paused-task boundaries; no new code or runtime changes.

## 2026-10-11 — Initial Go server source import

- Import complete 3,356-file D22/D23 source closure without changing source bytes.
- Add Windows BAT and POSIX shell build entrypoints with ignored dist outputs.
- Add Linux/Windows amd64 GitLab builds and core tests; preserve SAST include.
- Include example configuration with placeholder password; exclude runtime assets, credentials, player data and binaries.
- Record candidate/online version boundaries and pending acceptance in Docs/SOURCE_IMPORT.md.
- Build organization reference: rewio/ServerS4A21 commit 1f4a4e80610c767278008cd666588cb9a9629e34.
