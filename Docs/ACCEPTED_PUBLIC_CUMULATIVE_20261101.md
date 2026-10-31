# 累计通用修复验收来源记录

本记录固定2026-11-01累计通用修复所采用的公共来源。MR与提交均位于公开仓库 `16ww/servers5a3`。功能范围依据各来源采用的来源提交的标题及对应 `Docs/` 文档整理。

来源状态有三项需要单独保留：MR 108、264是历史工具及辅助归档记录的来源，不是本轮客户端正式基线；MR 305已进入master；MR 315由MR 349的新版等效实现取代。MR 352采用下方列出的已验收来源提交，其分支当前头提交为 `ace057d059dad4ca9a538f9539bedbe7eca6008b`。

## 来源清单

| MR | 采用的来源提交 | 标题 | 功能或记录范围 | 对应Docs |
| --- | --- | --- | --- | --- |
| [108](https://gitgud.io/16ww/servers5a3/-/merge_requests/108) | [`63a84391a0fbeb2f6ddc472bbf2b28ecd1381d55`](https://gitgud.io/16ww/servers5a3/-/commit/63a84391a0fbeb2f6ddc472bbf2b28ecd1381d55) | docs: record current ordinary ridable validation [skip ci] | 普通载具验证记录；属于历史工具来源，不是本轮客户端正式基线。 | [QST-004-ridable-client-gate.md](seeker/QST-004-ridable-client-gate.md) |
| [264](https://gitgud.io/16ww/servers5a3/-/merge_requests/264) | [`d43c36c0fca9b5052bdf5829d433f8662232686b`](https://gitgud.io/16ww/servers5a3/-/commit/d43c36c0fca9b5052bdf5829d433f8662232686b) | Integrate current accepted upstream for ancillary archive review [skip ci] | 辅助归档审阅的整合记录；属于历史工具来源，不是本轮客户端正式基线。 | 无 |
| [305](https://gitgud.io/16ww/servers5a3/-/merge_requests/305) | [`d77f3329778eddb6e85ca2ab8674db80d951acb1`](https://gitgud.io/16ww/servers5a3/-/commit/d77f3329778eddb6e85ca2ab8674db80d951acb1) | Merge branch 'codex/selector-default-log-diagnostics-20261027' into 'master' | 保留选图阶段诊断并过滤敏感报文字段；按本轮清单已在master。 | 无 |
| [316](https://gitgud.io/16ww/servers5a3/-/merge_requests/316) | [`03abe927552bfbb90221610aa78fa8f8b405f9dd`](https://gitgud.io/16ww/servers5a3/-/commit/03abe927552bfbb90221610aa78fa8f8b405f9dd) | 优化连续死亡请求接续并预载深渊碎片索引 | 在经验提交前接纳已到达的连续死亡请求，并将深渊碎片等级索引加入监听前静态预载；保留原事务、通知、掉落和收包顺序边界。 | [DUNGEON_DEATH_CONTINUATION.md](DUNGEON_DEATH_CONTINUATION.md) |
| [318](https://gitgud.io/16ww/servers5a3/-/merge_requests/318) | [`4b1a204b543fef2eda4ed343ed41fd3ab5b03b16`](https://gitgud.io/16ww/servers5a3/-/commit/4b1a204b543fef2eda4ed343ed41fd3ab5b03b16) | 补齐诞生之芽区域材料并修复89级普通装备零上浮窗口 | 在指定魔界区域普通怪物材料池接入诞生之芽；允许普通装备等级窗口使用上浮值0，同时保留负值、越界值和空窗口的不产出规则。 | [BIRTH_BUD_AREA_DROP.md](BIRTH_BUD_AREA_DROP.md)、[EQUIPMENT_ZERO_UP_WINDOW.md](EQUIPMENT_ZERO_UP_WINDOW.md) |
| [327](https://gitgud.io/16ww/servers5a3/-/merge_requests/327) | `8373af868aebf445deb1795d7d346065e9389595` | 修正PVP与帝国外部频道编号及等级窗口 | 修正PVP与帝国外部频道编号及等级窗口；保留完整外部频道及原收件协议约束。 | [CHANNEL_TABLE_DEPLOYMENT.md](CHANNEL_TABLE_DEPLOYMENT.md) |
| [328](https://gitgud.io/16ww/servers5a3/-/merge_requests/328) | `2dab832c1b0b9808746d4947de775eacd9a059dc` | 修复邮件收件人默认大区与账号内角色列表接收 | 修复邮件收件人默认大区与账号内角色列表接收；保留完整外部频道及原收件协议约束。 | [MAIL_RECIPIENT_DEFAULT_REGION.md](MAIL_RECIPIENT_DEFAULT_REGION.md) |
| [329](https://gitgud.io/16ww/servers5a3/-/merge_requests/329) | [`cbad0643aeca006bb54d1574cf26f212fedad46f`](https://gitgud.io/16ww/servers5a3/-/commit/cbad0643aeca006bb54d1574cf26f212fedad46f) | 修复周常副本开放状态并提供精确客户端接收许可补丁 | 服务端补齐完整活动目录后的五条class0/op855开放状态，并提供匹配精确客户端输入的独立接收许可补丁；入场次数、等级和前置校验仍由原流程处理。 | [DAY_DUNGEON_OPEN_STATE.md](DAY_DUNGEON_OPEN_STATE.md) |
| [332](https://gitgud.io/16ww/servers5a3/-/merge_requests/332) | [`39ac8f42df8e3f5499c4067a5952ea71c0a40f7a`](https://gitgud.io/16ww/servers5a3/-/commit/39ac8f42df8e3f5499c4067a5952ea71c0a40f7a) | fix: broadcast equipment upgrades across channel and share account difficulty | 在频道内广播装备升级，并共享账号难度状态。 | 无 |
| [335](https://gitgud.io/16ww/servers5a3/-/merge_requests/335) | [`48e5261e8e8b7782c509a77233196332ef5e449a`](https://gitgud.io/16ww/servers5a3/-/commit/48e5261e8e8b7782c509a77233196332ef5e449a) | 增加徽章合成Shift右键快捷加入及操作说明 | 徽章合成窗口支持Shift＋右键加入首个空位，并显示短操作说明；拖动、原右键分派、品级和数量上限保持。 | [EMBLEM_RIGHT_SELECTION.md](EMBLEM_RIGHT_SELECTION.md) |
| [341](https://gitgud.io/16ww/servers5a3/-/merge_requests/341) | [`e072943b7a1856e6f3c628162a34fafa1bed6c68`](https://gitgud.io/16ww/servers5a3/-/commit/e072943b7a1856e6f3c628162a34fafa1bed6c68) | Fix relic box compact armor selection | 修复遗物盒子的紧凑装备选择。 | 无 |
| [342](https://gitgud.io/16ww/servers5a3/-/merge_requests/342) | [`f75470d588e69ab87aa732eed1f81c52e39202b8`](https://gitgud.io/16ww/servers5a3/-/commit/f75470d588e69ab87aa732eed1f81c52e39202b8) | Apply 2018 failure penalty to nonweapon reinforcement | 非武器强化失败时应用2018规则对应惩罚。 | 无 |
| [343](https://gitgud.io/16ww/servers5a3/-/merge_requests/343) | [`53c066c59df164f030368fbc4123807756b6ae9b`](https://gitgud.io/16ww/servers5a3/-/commit/53c066c59df164f030368fbc4123807756b6ae9b) | Fix Anton epic jar double material charge | 修复安徒恩史诗罐重复扣除材料。 | 无 |
| [345](https://gitgud.io/16ww/servers5a3/-/merge_requests/345) | [`8c01d6eb57862c5508fe1b1d57c90739e017191a`](https://gitgud.io/16ww/servers5a3/-/commit/8c01d6eb57862c5508fe1b1d57c90739e017191a) | Optimize generic dungeon drop pool warmup | 优化通用地下城掉落池预热。 | 无 |
| [315](https://gitgud.io/16ww/servers5a3/-/merge_requests/315) → [349](https://gitgud.io/16ww/servers5a3/-/merge_requests/349) | [`64d2852a4e52c9e4c00bb4e8504e88d9af0f4d3e`](https://gitgud.io/16ww/servers5a3/-/commit/64d2852a4e52c9e4c00bb4e8504e88d9af0f4d3e) | Merge branch 'fix/runtime-write-amplification-20261030' into 'master' | MR 315由MR 349新版等效取代：缩减成长、任务与资产读写，并修复金币和货架校验。 | [RUNTIME_WRITE_AMPLIFICATION.md](RUNTIME_WRITE_AMPLIFICATION.md) |
| [352](https://gitgud.io/16ww/servers5a3/-/merge_requests/352) | [`fe23e93e79893ae6c5f646e5938f9c0b598e20e9`](https://gitgud.io/16ww/servers5a3/-/commit/fe23e93e79893ae6c5f646e5938f9c0b598e20e9) | fix(dnf): persist and fan out personal quest material drops | 组队共享击杀后冻结收件成员，持久化各成员的个人任务材料判定并对非报告成员投递；保留独立持有上限、重放和恢复边界。 | [PARTY_QUEST_MATERIAL_DELIVERY.md](PARTY_QUEST_MATERIAL_DELIVERY.md) |

## 验证状态

- 公共MR编号、已验收来源提交、标题及对应Docs已完成只读核对。
- 领域测试已通过；桥接回归完成一次全量执行后，任务材料用例已修正事务内读取探针并通过专项，最终完整验证待归档。
- 部署与实机验收状态保持独立，本记录不预填这两类结果。
