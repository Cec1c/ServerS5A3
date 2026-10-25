# Preserve story choices when using level-up tickets and quest manuals

Level-up tickets previously swept story quests using a separate simplified path. This could complete unanswered Question nodes or stop at a choice without resuming after the player answered it. Quest manuals shared some of these gaps.

The patch centralizes sweep eligibility: preserve Question nodes until a valid, explicit answer is completed, enforce predecessors and mutually exclusive routes, and keep profession, special-permission and genuine reward-selection gates. Completed historical Questions without valid answer metadata are reported as damaged rather than silently rewritten.

A level-up ticket stores versioned continuation authorization in the existing character Stats transaction alongside its normal level increase and one-item consumption. The sweep ends at the ticket's target level minus one. Later normal Question completion resumes only an existing authorized sweep, without consuming another ticket or paying quest rewards again. Merely selecting an answer does not trigger continuation. Consecutive tickets merge authorization monotonically; retries use the existing completion receipt path. No new SQL tables are required.

Manual single-quest and bulk clearing use the same eligibility rules, but do not acquire new persistent ticket authorization. Ordinary characters without an authorization are not scanned by the continuation path.

After commit, project the completed quest list and the current map's eligible gate refresh, then active/acceptable quest state. This does not teleport a character, forge map data, or widen map access.

## Validation

Tests cover alternative answers, predecessor and mutual-exclusion gates, historical damaged choices, grant persistence, authorization merging, failed transaction rollback, repeated completion, and continuation without duplicate rewards. Disposable MySQL tests verify the real repository transactions. Resource tests require an explicitly supplied PVF; skipped prerequisites are not passes.

The operator has reported successful native level-up-ticket acceptance on the private integration build. This is user-observed acceptance, not an independently recorded proof of every Question/manual variant or the newest upstream base. No private welfare features, player records, credentials, server configuration or resources are included.

Rollback must preserve committed character progress and authorization fields. An older server does not interpret the continuation authorization; do not erase it or roll back the full database over subsequent player activity.


## 2026-10-25 补充

旧1429及当前1427预检/1454提交共同核对账号、角色代际和资产预约；事务中复核等级、职业与成长阶段，并在同一事务校验写入和返回提交快照。提交完成后先释放预约再刷新界面，过期会话不修改任务或发送旧角色结果。补充两入口等待后换代拒绝、预约释放及事务校验回滚测试。
