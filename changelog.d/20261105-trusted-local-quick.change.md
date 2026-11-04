---
category: 功能修复
---

# Trusted developer local validation admission

## 变更

- Add `SOURCE_MERGE.bat --quick` for the two named repository writers to attest local validation of an exact SHA and merge without waiting for cloud CI.
- Keep ordinary CI and local attestations separate behind `merge-ready`; existing full checks are unchanged for ordinary PRs.
- Record source, developer, statement digest and merge readback; protect private statement contents, retain unknown-operation receipts, and do not trigger deployment.
- Batch completed fixes; prepare and backup before the user's switch trigger, with full end-to-end timing.

## 验证

- 本地精确SHA/身份/权限/异常续接与CI来源区分专项；私有入口另验直接累计、持久检出与既有扳机契约。
