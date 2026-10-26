# R169 卢克 phase0 party-only 结算后的二阶段推进

## 结论

候选修复卢克团队 phase0 已提交全部 party 奖励卡后，仍等待一个不存在的 squad/团队牌展示窗口，导致客户端战况状态没有离开结算态、`666` 二阶段启动入口持续不可用的链路。

只在真实卢克 phase0、非单人、当前 PVF 无 squad 奖励且全部奖励组都已提交 party 卡时提前认定为奖励结算完成。阶段未完成、超时、单人、账号/频道/名册/等待区、不同奖励组尚未完成、重复启动及最终周账本边界均保留。没有生成 squad 卡、没有增加 PVF 未定义的团队奖励，也没有更改安东奖励表或资产事务。

## 真实资源与代码证据

- 真实 PVF：`H:/codex-work/assets/Script.pvf`，SHA256 `25675FBA8711216B72800E5FF034DF61209B37E7C22D3E4C3B3AD7B1906FC348`。
- v151 EXE：`H:/codex-work/assets/DNF-v151.exe`，SHA256 `2DEF3CC35C1EE00A9C697FE81D1F31259860468D042D6D477CD3B4BE08F0E6EB`。
- `etc/raid/luke/luke.etc` 的 phase0 `[CLEAR REWARD ITEM]` 仅有实际 `party_card 10304478` 与未接入的 `pcroom_card`，没有 `squad_item`；phase1 才出现多条 `squad_item`。
- 同一资源的 `[MOVIE END TIME]` 为 `27 27 65 27`，`[PHASE BREAK TIME]` 为 `600`。
- 共享奖励流在 party 卡全部提交后仍无条件设置 `SquadShownAt`，并等待最大电影窗 65 秒才设置 `FinishedAt/BreakUntil`。
- `beginCurrentAntonNextPhaseLocked` 原要求 `FinishedAt` 与 `BreakUntil` 非零；`currentRuntimeRaidMetadata` 也只在 `FinishedAt` 非零时给出可启动的 waiting state 5。两者共同造成 party-only 结算后的空等。

## 候选

- `raid_luke_next_phase_boundary.go`：只读检查全部 `f.Recipients` 的 party 卡是否已提交；要求真实卢克 phase0、非单人、`squadRequired()==false` 且接收者非空。
- `raid_next_phase.go`：二阶段入口改走统一结算边界；普通安东/卢克最终阶段仍要求原有 `FinishedAt/BreakUntil`，只有上述 party-only 特例跳过不存在的团队牌展示等待。
- `raid_current_client.go`：结算元数据使用同一只读边界，使客户端在 party 结算完成后进入 phase0 waiting state；阶段推进后仍恢复 active state 2/phase1。

## 回归

- 真实 PVF phase0 三圣所清关成功后，在自动 party 选牌刚完成、`FinishedAt` 仍为零时，两个不同奖励组均已提交 party 卡，允许 main-thread `666` 入口推进 phase1；未增加 squad 卡。
- 同一真实资源下篡改组2 party 完成位时，元数据仍为结算态且启动被拒；恢复全部组后允许推进。
- 第一阶段尚未清关时仍拒绝；第二阶段会再次扣票、重掷一阶段奖励或重复启动的断言均覆盖。
- 未执行 Go/gofmt/构建/部署或子代理，符合本轮限制。主线程可用：

```text
$env:DNFBRIDGE_REAL_PVF_SMOKE='H:/codex-work/assets/Script.pvf'
go test -buildvcs=false -count=1 ./internal/services/dnfbridge -run 'TestRealPVFR169Luke' -v
```

## 边界

- 自动休息时钟仍由既有 `BreakUntil` 调度；本候选修复客户端已看到结算完成后的手动二阶段启动与元数据入口，不改变 600 秒休息策略。
- 不覆盖客户端原生动画验收、全流程怪物战斗或现场请求抓包；这些仍需主线程统一验证及实机确认。
