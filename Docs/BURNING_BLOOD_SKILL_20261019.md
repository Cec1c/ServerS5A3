# 燃烧之血学习门禁修复（2026-10-19）

## 现场结论

狂战士学习“燃烧之血”时，服务端旧门禁使用 `[skill fitness growtype]` 的原始值判断职业，导致该技能被错误拒绝。真实 PVF 中，燃烧之血的 ID 是 **248**，文件为 `skill/Swordman/Rejection.skl`。当前方向的可用性由 `[growtype maximum level]` 决定，不是由 fitness 元数据决定。

真实 `Script.pvf` 的 SHA256 为：

```text
a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290
```

`skill/SwordmanSkill.lst` 的原文映射为：

```text
248 `Swordman/Rejection.skl`
```

`Rejection.skl` 的关键字段：

```text
[name]
`燃烧之血`
[type]
`[passive]`
[required level]
15
[maximum level]
1
[growtype maximum level]
0 0 0 1 0 1
[skill fitness growtype]
1
```

这里的槽位 3 是狂战士方向。该方向可学习上限为 1；槽位 1 为 0，不能把 fitness 的 `1` 当作“剑魂分支”门禁。当前客户端的学习页和 A21 参考实现均以 maximum-level 表作为技能方向和等级上限的所有者。

## 根因与修复

`skillcmd.applyBuyMutation` 在检查具体可学习等级之前调用 `Skill.SupportsCharacterGrowth`。旧实现同时比较 `[skill fitness growtype]` 和 `[skill fitness second growtype]`。燃烧之血的 fitness 是 `1`，而角色是 `grow_type=3`，因此请求在到达 `[required level]=15` 与最大等级 1 的合法校验前就返回 `ErrSkillGrowType`。

本次把 `SupportsCharacterGrowth` 改为只判断 `MaxLevelForGrowType(packedGrowType) > 0`。该函数已按 PVF 规则处理：

- `[second growtype maximum level]` 的非零槽优先；
- 第二觉醒表为零时回落到 `[growtype maximum level]`；
- 两张表都缺省时回落到 `[maximum level]`；
- 槽位为 0 时仍拒绝该方向。

`SupportsGrowType` 保留为原始 fitness 元数据查询，但注释已明确它不得用于学习门禁。`SkillsWithUnavailableDirection` 的登录诊断也改用同一最大等级判据。

修复后的预期行为：狂战士从 0 级学习燃烧之血成功到 1 级；随后客户端显示“已精通”是 PVF `[maximum level]=1` 的正常结果。若角色记录已经持久化为 1 级，本修复不修改玩家数据，也不把它退回 0。

## 回归测试

单元回归 `TestBurningBloodUsesGrowtypeMaximumLevelNotFitnessMetadata` 固定 ID 248 的真实字段形状，验证：

- `grow_type=3` 可用；
- `grow_type=1` 仍不可用；
- 15 级可学习上限为 1；
- 14 级不可学习。

真实归档回归 `TestRealScriptPVFBerserkerCanLearnBurningBlood` 从 `SwordmanSkill.lst` 读取 ID，核对名称、路径和全部关键字段，然后通过正式 `skillcmd.Owner.ApplyBuy` 路径在内存仓储中学习：

- 学习前不存在 248；
- 学习结果为 1 级；
- 持久化的内存技能记录为 1 级且 enabled；
- 消耗为 0 的该技能不改变 SP；
- 再次加点超过 1 级仍拒学。

红态证据：

```text
Berserker grow_type=3 must be available from the maximum-level owner; fitness=[1] max=[0 0 0 1 0 1]
real skill direction rules = grow3=false grow1=true cap=1
```

绿态验证：

```text
go test -buildvcs=false -count=1 ./internal/modules/dnf/skill       PASS
go test -buildvcs=false -count=1 ./internal/modules/dnf/skillcmd    PASS
go test -buildvcs=false -count=1 ./internal/modules/dnf/...         PASS
go test -buildvcs=false -count=1 ./internal/services/dnfbridge      PASS (25.231s)
go test ... ./internal/services/dnfbridge -run '^TestRealScriptPVFBerserkerCanLearnBurningBlood$'
                                                                  PASS (2.59s)
go vet ./internal/modules/dnf/skill ./internal/modules/dnf/skillcmd PASS
```

一次误把真实 PVF 环境变量带入完整 `dnfbridge` 的宽测试已主动中止；该次没有得出失败，也不能计为通过。最终全量与实机验收由主代理统一执行。

## 边界

- 未修改任务、副本、数据库或玩家记录；
- 未写 PVF，也未部署服务端；
- 只修改技能领域规则文件、技能学习/上限测试和本说明。

