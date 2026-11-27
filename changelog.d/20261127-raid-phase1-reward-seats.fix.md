---
category: 功能修复
---

# 安徒恩与卢克一阶段单人散人同组冲突导致翻牌异常修复

## 变更

- 抽取 `boundedCurrentRaidRewardGroups` 供一阶段开战准备（`prepareCurrentRaidBattle`）与跨阶段切换（`nextPhaseRewardGroups`）共用，在保留实时团本成员状态的前提下，对安徒恩与卢克团本中与满编普通队伍同组或越界的 `OrdinaryPartySolo` 成员重映射至 `1..maxGroup` 内未满 4 人的分组，避免中途换人导致同组达标人数超过 4 张原生牌并触发 `frozen_group_capacity` 无法翻牌。
- 新增一阶段 4+4+4+1 单人散人与满编队同组且中途换人后 13 人全部达标翻牌的真实 PVF 回归测试，以及卢克一/二阶段单人冲突分组回归测试，并补齐此前仅存于私仓的二阶段 6+4+4 翻牌席位回归测试。

## 验证

- 精确用例、修复前负向复现、真实 PVF、Linux vet/构建及版本绑定以本 PR 的本地验证声明为准；部署与玩家实测分别记录。
