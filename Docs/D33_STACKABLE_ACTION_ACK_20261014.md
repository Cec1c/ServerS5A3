# 道具动作回包：结果、槽位、列表与动作号（R58 同步）

本文件记录 !39 的 v96 客户端取证，并补充 R58 对当前主线/R52 runtime 的同步结论。当前成功体统一为 `result=1, slot:u16, list:u8, action:u32`；当前动作号为伤害字体 164、疲劳药 58、任务券 118/169/179。任务券历史动作 167 已被现行 action 表排除；无可完成任务使用带 slot/list/action 的失败回包。

## v96 客户端的原始读取证据

- `0x2523c9c..0x2523cf6` 按 packet class 分发；class1 在 `0x2523d00` 用 byte reader `0x37e82e0` 消费 body 首字节作为结果。非零成功时不会再读错误码；零结果才可能在 `0x2523d54` 读错误字节。
- `0x2523e4f..0x2523e5a` 将结果/错误参数传给 `0x251e1e0`。后者从已注册表取 handler，`0x251e260..0x251e26c` 依次传入 opcode、结果、错误、注册上下文。
- op515 在 `0x4480670` 注册 handler `0x24f3580`。成功路径 `0x24f35cf/0x24f35dd/0x24f35e9` 读 u16 槽位、u8 列表、u32 动作。列表与槽位用于定位背包物品；动作值传给 `0x24f1bf0` 并作为 `0x24ef3c0` 的分发键。
- 旧 body `01 57 00 00 00 03 00 00 00 01`（槽 87、余 3）在客户端读成 action 768。原 MR 按此改为 `01 57 00 00 a7 00 00 00`（167）；该数字只适用于当时的候选动作表。

离线模拟执行上述真实 x86 片段时，只有 packet reader 与已核实的注册表查询替换为测试桩；测试在成功头部读取完成处停止，未运行原生 UI，也未证明所有间接回调的尾部读取行为。原始剩余数量字段没有当前客户端读取依据，后续专属动作若有附加 payload，必须独立核实。

## R52/runtime 的现行动作与校验

- `171f53cd2716685a74a92eeb9caad58859c5c6a5` 将伤害字体动作从 162 修为 164，并新增 `BuildStackableActionAck` / `BuildStackableActionFailure`；R57 runtime `e526dabf223e4304061e6759f197e592fd1b31d8` 仍为 164。本次把同一常量与 helper 带入 MR 同步树，避免测试继续固化 162。
- 同一提交把疲劳药固定动作修为 58，并在扣药前校验 19 字节请求的 action 与全部保留字段；失败回包为 `result=0, error=1, slot:u16, list:u8, action:u8`。本次按该实现整理，不再使用不透明 `0x12345678` 作为成功动作，也不把固定 167 写回疲劳药。
- 任务券主线使用 118（一觉）、169（GM 任务完成）、179（等级完成）；成功回显请求 action，失败回显 action 低字节。`quest_coupon_clear` 的冲突完整保留主线实现，没有采用 !39 的 167/成功空操作刷新。

## 回归与证据边界

- 伤害字体：`TestDamageFontAckPreservesActionAcrossRemainingQuantities` 对余量 0/1/3/70000 断言 8 字节体与 action 164；既有 `TestDamageFontUnlockAndSelectionAreRepositoryBacked` 同步断言 164。
- 疲劳药：`TestRealPVFFatiguePotionAckPreservesRequestAction` 使用当前 PVF 的真实疲劳药，余量 1/3 断言 action 58、扣一瓶、实际恢复值和 ACK→op14→疲劳包顺序；`TestFatiguePotionRejectsNonNativeActionBeforeMutation` 证明非 58 动作在写入前落回其它路由；`TestFatiguePotionInvalidRequestUsesActionBearingFailure` 覆盖保留字段、列表和缺失角色的失败体。
- 任务券：`TestQuestCouponResultPreservesCurrentNativeActions` 覆盖 118/169/179 的成功/失败体，并证明 167 不再匹配；主线既有的成功、空操作和无仓库拒绝测试保留。

真实 PVF 专项使用当前 `Script.pvf`（SHA256 `a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290`）。该资源是现行礼盒期限修订档，不能用于宣称原始 `ad7d05c7…` 或其它固定哈希资源的回归通过；未设置 `DNFBRIDGE_REAL_PVF_SMOKE` 的跳过也不计为真实资源通过。本轮未重新反汇编当前客户端，未执行完整原生 UI/动作回调验收。

本树未推送远端、未合并到主线、未部署，未改玩家数据、PVF 或 DOCX。
