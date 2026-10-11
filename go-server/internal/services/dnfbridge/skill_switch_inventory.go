package dnfbridge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 2026-09-30 correction: the historical protocol interpretation below is kept
// only for rollback compatibility. RVA 3C9FA0 proves the 6-byte command starts
// with selected skill, not a sequence prefix. DNFBRIDGE_BUFF_SWAP_REAPPLY=on
// bypasses this legacy path and uses buff_swap_reapply.go / native noti1369.

// 技能换装背包 / 「Buff 强化」换装栏（`ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY`，
// 本仓枚举值 1428，dnfenum/cmdpacket.go:1439）在 dnfbridge 侧的唯一归属点。
//
// # 开关（**一键回退**）
//
// `DNFBRIDGE_SKILL_SWITCH_INVENTORY`
//
//	未设 / project / on / fix / 1 / true / 其它未知取值（**默认 = 修复档**）
//	    → 落库 + 在五条边界上重投影（见下，含 type1345「场景就绪」之后那条）。
//	before_transition_only | early_only | project_before_transition
//	    → **与 2026-09-28 之前的默认逐字节相同**：仍然落库、仍然在
//	      op37 / mode1 后 op24 前 / op30 后 / op19 后四条边界投影，
//	      但**不在 legacy type1345「场景就绪」之后补第二条**。
//	      （这就是"选择角色后清空"那一轮之前的投影集合。）
//	record | persist_only
//	    → 只落库、**不下发任何字节**（上一版的受控取证档）。
//	reference_drop | off | 0 | disable | legacy
//	    → **与改动前逐字节相同**：一个下行字节都不发、不落库，
//	      只留一条具名且自带 `body_hex` 与逐字段解码的事件。
//
// ⚠️ 当前默认档是 `shape_guard`（不是 `project`）：先把包体形状与客户端 1428
// 入站解析器对齐，再决定发不发。因为客户端 1428 的**入站**解析器（DNF.exe
// RVA 0x565660）读的是 `u8 count + count×(u32 key,u32 value)`，与我们手里的
// 上行记录毫无交集 ⇒ 今天**一个字节都不发**，只记 projection-blocked。
//
// `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY`（**上行记录读法**，见 skillswitch/record.go）
//
//	未设 / v4 / fix / on / 未知取值（**默认 = 修复档**）
//	    → 按实机证实的 4 字节包体读：`u8 Slot | u8 Mode | u16 Value`。
//	      现行分帧交来的 6 字节切片按 dual-read 去掉前 2 个非包体字节；
//	      落库写 4 字节规范形；旧 6 字节行照读，重收一次时改写成规范形并打
//	      `layout_upgraded=true`。
//	v6 | legacy | today | old | 6（**一键回退 = 改动前**）
//	    → 把 6 字节（2 字节导线前缀 + 4 字节包体）当包体读，落库写 6 字节形，
//	      逐字节复现改动前的行为。
//
// `DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`（**下行实验**，见
// skill_switch_inventory_candidate_pairs.go）
//
//	未设 / off / 未知取值（**默认关**）→ 不发实验帧，逐字节等于今天。
//	identity12 → 在**同一条**合法投影边界上多发一帧
//	      `u8 count=12 + 12×(u32 key, u32 value)` 的恒等对（key==value），
//	      用于判定客户端 manager 329 期望的 pair 语义。这是**实验不是修复**：
//	      那 12 对只是"结构上最像"的候选，**未被证明**就是 manager 329 的 map。
//
// `DNFBRIDGE_SKILL_SWITCH_RESEND`（**跨选择角色的重下发**，默认 = 修复档；
// 见 skill_switch_inventory_resend.go）
//
//	未设 / slots / 未知取值（**默认 = 修复档**）→ 在既有重投影边界上按**落库的
//	      换装行**多发**一帧** `u8 count + count×(u32,u32)`（恒等对）。
//	      这是"选择角色后换装消失"那条缺陷的修复：在此之前默认档
//	      `shape_guard` 把**每一帧**都拦成 0 字节（上行记录永远不满足
//	      `1+8*count`），换装状态一行都发不出去。
//	shape_guard / off / today / legacy / rollback（**一键回退**）
//	      → 与改动前逐字节相同：本档一行代码都不执行、不发、不记。
//
// 回退到「只补 op37/op24 前/op30/op19 四条」用
// `DNFBRIDGE_SKILL_SWITCH_INVENTORY=before_transition_only` 重启；
// 回退到更早的「收下即丢」用 `reference_drop`。
//
// # 现场形状（HK `/opt/dnf90/logs/packet_log*.txt`，全库 `type=1428` 共 82 条 RECV）
//
//	RECV kind=game-legacy conn_id=game-000003 pkt_seq=1010 cmd=1 type=1428
//	     runtime_cmd_name=ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY runtime_cmd_known=true
//	     header_seq=337 body_len=6 len=19
//	     hex=019405130000002fbf999d510100000e030e00
//
// 本仓的 RECV 行 `len=13+body_len`、hex 是**整帧**，所以 handler 拿到的是行尾 6 字节。
// 包头 13 字节 = `cmd(u8)=01 | type(u16 LE)=0x0594 | total_len(u8)=0x13 | 00 00 00 |
// crc32(4) | seq(u16 LE)=header_seq`；`header_seq` 与 `[11:13]` 逐条相等（82/82）。
//
// ⚠️ **但这 6 字节不是包体**：客户端在这条上行里放的是 **u32 序号**（`[11:15]`），
// 本仓 `protocol.ParseLegacyGamePacket` 用 13 字节头、把序号读成 u16，于是
// `packet[13:]` = **序号高 16 位（2B） + 真包体（4B）**。实机证据（活进程报文缓冲）
// 证明真包体是 `u8 slot | u8 mode | u16 value`：`10 03 10 00` = slot 16 / mode 3 / value 16。
// 逐字节映射与 dual-read 规则在 `internal/modules/dnf/skillswitch/record.go` 顶部。
// 逐字段读法（含不变量与未确证项）也全部在 `skillswitch` 包。
//
// 当前客户端（操作者 2026-09-27 实机）上行的 4 条样本，全部是 `ModeConfirm` 形态
// （`Slot == Value`）：
//
//	00000c030c00 -> 包体 0c030c00 -> slot=12 mode=0x3 value=12
//	000013031300 -> 包体 13031300 -> slot=19 mode=0x3 value=19
//	000015031500 -> 包体 15031500 -> slot=21 mode=0x3 value=21
//	000016031600 -> 包体 16031600 -> slot=22 mode=0x3 value=22
//
// 与厂商形状的关系：**厂商没有 op1428**。`ServerS4A21-master`（1049 个 .cs）与
// `s4a21_master_latest_unzip` 全树对 `SKILL_SWITCH` / `SkillSwitch` /
// `SkillSwitchInventory` 命中 0，`PacketTypesA21.cs` 的 CMD 枚举 `END=0x04F7`(1271)、
// NOTI 枚举 `END=0x04C2`(1218) 都不含 1428；`90原神开源0.1.8` 那份 fork 也只有
// `dnfenum/cmdpacket.go` 的名字表登记。所以**没有"厂商字段序"可抄**，
// 本文件与 `skillswitch` 包一律以现场 6 字节为准（并在两处都写明这一点）。
//
// # 修复范围：四条边界上的**重投影**
//
// 操作者现场（2026-09-27）：会话内登记生效，但
// **穿脱装备 / 选择角色 / 重新登录 / 进出副本** 之后换装栏被重置。
// 因此本 owner 的职责是两段：
//
//	① 收：op1428 的每一条记录按角色落库
//	   （scope `character:<id>:skill_switch_inventory`，仓储 repos.Settings）；
//	② 放：在上面四条边界上把落库的记录**原样回放**给客户端。
//
// ② 的投影点全部落在既有的技能信息投影上（与 `character:<id>:skill_commands`
// 的加载点是同一个投影函数族，见 scene_skill_info.go）：
//
//	| 边界 | 投影点 |
//	|---|---|
//	| 重新登录（op37 finish-loading） | `sendCurrentSceneSkillInfo` @ scene_skill_info.go |
//	| 选择角色 / 进城（mode1 后、typed op24 前） | `sendCurrentSceneSkillInfo` @ initial_town_player_state.go |
//	| **选择角色 / 进城（legacy type1345「场景就绪」之后）** | `sendCurrentLegacyTownSceneReadyBoundary` @ game_select_scene_deferred_tail.go |
//	| 选择角色 / 进城副本返回（op30 之后） | `sendSelectedActorCurrentSceneSkillInfo` @ town_post_transition_player_state.go |
//	| 穿脱装备（op19 对齐后） | `sendSelectedActorCurrentSceneSkillInfo` @ game_aligned.go |
//	| 进出副本 | 上面两条：进城/进场走 sendCurrentSceneSkillInfo，副本返回走 sendSelectedActorCurrentSceneSkillInfo |
//
// # 为什么选择角色这条路上要**两条**（2026-09-28 现场证据）
//
// 操作者第二轮报障：「4buff依旧失效 选择角色后清空」——落库与投影两条路都在跑，
// 所以他报的是**客户端重新选角后看不到**。现场日志把三种解释收成一种：
//
//  1. **落库没问题**（`dnf_setting_values` 实测）：`character:1:skill_switch_inventory`
//     slot_count=13、`character:6:...` slot_count=11，`updated_at=2026-09-27T18:42:04Z`，
//     重选角色之后一行都没被抹掉；客户端也从不推「空清单」覆盖它们
//     （日志里 `type=1428` 的最后一条就是 18:42 的那一批，之后每次重选都没有上行）
//     ⇒ 「落库被覆盖」不成立。
//  2. **投影确实发生**在重选角色这条路上：`game-skill-switch-inventory-projection-send`
//     现场 15 条，其中 7 条的 source 是
//     `initial_town_before_final_transition_after_mode1_without_mode3_before_typed_op24`
//     （例：`packet_log.txt:24865` = conn game-000018 char 6，11 包 class-0/1428；
//     `:26966` = 重启后的 game-000005 char 6 同样 11 包）⇒ 「这条路上根本没回放」不成立。
//  3. **但这条路上只有"过早"的那一次**：另外两条边界在「客户端已经加载完、
//     只是回到选角界面再进一次」时**根本不会发生** ——
//     `request37_...finish_loading`（op37）与 `..._op19_reloaded_after_op30`（op30）
//     只出现在客户端整局登录的第一次进场或副本返回（session game-000018 第一次进场：
//     :22687 skip（op37）/ :23042 skip（op30）；同一连接的第二次进场（重选角色）
//     只有 :24865 这一条 pre-op24 投影，op37/op30 都没再发生）。
//     而客户端**自己**在每次进场都会发一次 legacy type1345「场景就绪」：
//     同一连接 :21945（第一次）与 :24938（重选角色）都有
//     `game-legacy-town-scene-ready-ack-accepted`，紧随其后 :24941 我们已经在发
//     `game-upper-select-character-scene-tail-send` —— 唯独没有补换装栏。
//     ⇒ 重选角色这条路上，客户端在场景/容器提交完之后**收不到任何一次**换装栏投影。
//
// 因此默认档在这条路上补第二条投影：type1345 之后（`legacy_type1345_scene_ready_ack_
// skill_switch_inventory_after_scene_ready`）。它**不发明任何形状**——回放的仍是
// 客户端自己上行的那条记录、走的仍是既有的 class-0 通道，只是多给一次"场景已提交"的时机。
// （`shape_guard` 档下这条边界同样会被形状闸门拦成 0 字节：回放的字节形是**上行**形状，
// 与下行解析器要求的 `u8 count + pairs` 毫无交集。）
//
// 残余不确定（与下一条同源，必须一起读）：客户端 trace（`90CN_trace.log`，45 MB）
// 对 `op1428` / `msg_id=1428` / `0594` **命中 0**，而它的 class-0 追踪只覆盖
// `class0-op24` / `class0-op2-mode0` 两条具名路径，没有通用分发追踪，
// 所以"客户端到底认不认 class-0/1428"**仍然无法从日志判定**。
// 判定点交给操作者：见 `_vendor-re` 报告的"看什么"清单——若补了第二条投影后重新选角仍然空白，
// 则**形状/opcode 不对**（不是时机）。**该判定已被触发并完成**：真实入站 handler 已定位
// （DNF.exe RVA 0x565660，形状见上），所以现在是"按真实形状构造"，不是"再加时机"。
//
// # 为什么用 class-0 / msg_id=1428 回放（**这是唯一没有厂商背书的一步**）
//
// 厂商没有 op1428 的 handler，也没有任何 S2C 回包形状；客户端侧也没有覆盖到
// 四次 op1428 会话的日志（`90CN_protocol.log` 只覆盖 2026-09-27 06:44→12:29，
// 四次会话全在区间外）。因此回放的**数值口径**取自本仓已有的、有据可查的约定：
//
//   - `dnfenum/enum.go`：`GameCmdNotice=0` / `GameCmdCommand=1`，
//     C2S 命令是 cmd=1，S2C 通知是 class-0；
//   - `dnfenum/cmdpacket_directional.go`：**同一个数值在上下行是两个不同的包**
//     （op3 = C2S Exit / S2C NotifyUserState；op38 = C2S UseSkill / S2C
//     NotifyDieMonster；op115 = C2S TraceError / S2C NotifyBossDieCheck）。
//     本仓已有 3 个 opcode 按这条约定下发，`dungeon_combat_runtime.go` /
//     `scene_finish_loading.go` 都走 `sendGameUpperRawClass(session, op, body, 0)`。
//   - 包体不重新编码：**原样回放**客户端上行记录的字节形（v4 档 = 4 字节包体，
//     v6 档 = 改动前的 6 字节行），与 op447 回放的
//     `frame_source=op447_verbatim_frame` 同口径。
//
// 这一步的**残余不确定**已在报告里明写：若该 EXE 的 class-0/1428 不是换装栏通知，
// 客户端会忽略它（不会改变今天的行为）；用 `reference_drop` 可一键回到旧行为。
//
// ⚠️ **仍未确证**：下行 `count` 是否等于"实际项数"、以及那 12 对 `(key,value)`
// 到底是什么，至今**没有**证据（`_dist/buff-pairs-measurement.md` §6/§8）：
// 恒等 12 对只是结构上最像 manager 329（12 项 `std::map<u32,u32>`）的候选，
// family-2「slot→源槽位」已被 1.39 GB 全内存扫描**证伪**（0 命中，永不使用）。
const skillSwitchInventoryModeEnv = "DNFBRIDGE_SKILL_SWITCH_INVENTORY"

// skillSwitchInventoryRecordBodyEnv 是**上行记录读法**的开关
// （v4 = 修复档 / v6 = 一键回退），归一化在 skillswitch.NormalizeLayout。
const skillSwitchInventoryRecordBodyEnv = "DNFBRIDGE_SKILL_SWITCH_RECORD_BODY"

const (
	// skillSwitchInventoryShapeGuardTier（新默认）先把包体形状与客户端 1428 入站解析器对齐，
	// 再决定发不发：只有 `len(frame) == 1+8*int(frame[0])`（u8 count + count×(u32 key,u32 value)）
	// 的帧才发，形状不符的**一个字节都不发**，只记 projection-blocked。
	//
	// 为什么必须这样（_dist/buff-skill-switch-diagnosis.md）：我们回放的是客户端自己的
	// **上行**记录（v4 档 = 4 字节包体 `u8 slot|u8 mode|u16 value`，v6 档 = 改动前的
	// 6 字节行），而客户端 1428 入站解析器（DNF.exe RVA 0x565660）读的是
	// u8 count + count×(u32 key,u32 value)：旧批 12 帧里 11 帧首字节 0x26 会被当成 38 对
	// （304 字节）去读，而读原语没有边界检查、会从缓冲区重填 ⇒ 不但填不上换装栏，
	// 还会把客户端入站流读乱。4 字节包体同样不可能满足 `len == 1+8*count`
	// （`1+8c` 只取 1,9,17,…；4 与 6 都不在其中）⇒ 默认档下**两种档位都是 0 下行**。
	// 宁可不下发，也不能发形状不对的字节。
	skillSwitchInventoryShapeGuardTier = "shape_guard"
	// skillSwitchInventoryProjectTier 落库 + 五条边界重投影
	// （含 legacy type1345「场景就绪」之后那条）。**原样回放**，用于取证，
	// 是形状闸门之前的默认；作为一键回退档保留。
	skillSwitchInventoryProjectTier = "project"
	// skillSwitchInventoryBeforeTransitionTier 是本改动**之前**的默认：
	// 落库 + 四条边界（op37 / op24 前 / op30 后 / op19 后）投影，
	// 但不在 type1345 之后补第二条。一键回退到"选择角色后清空"那一轮的投影集合。
	skillSwitchInventoryBeforeTransitionTier = "before_transition_only"
	// skillSwitchInventoryRecordTier 只落库、不下发（上一版的受控取证档）。
	skillSwitchInventoryRecordTier = "record"
	// skillSwitchInventoryReferenceDrop 与改动前逐字节相同：收下即丢。
	skillSwitchInventoryReferenceDrop = "reference_drop"
)

// currentSkillSwitchInventoryOpcode 是当前 EXE 上 `ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY`
// 的 game-legacy 命令号（现场 82/82 命中此值），也是重投影用的 class-0 msg_id。
const currentSkillSwitchInventoryOpcode uint16 = uint16(dnfenum.CmdPacketSkillSwitchInventory)

// currentSkillSwitchInventoryBodySize 是 handler 实际收到的切片长度：现场 82/82 都是 6，
// 但那是**2 字节导线前缀（客户端 u32 序号域的高 16 位）+ 4 字节真包体**，不是包体长度。
// 真包体长度 = `skillswitch.RecordBodySize`（4），见 skillswitch/record.go 顶部的映射。
const currentSkillSwitchInventoryBodySize = skillswitch.RecordWireSize

const (
	skillSwitchInventoryObservedEvent   = "game-skill-switch-inventory-observed"
	skillSwitchInventoryRecordedEvent   = "game-skill-switch-inventory-recorded"
	skillSwitchInventoryRejectedEvent   = "game-skill-switch-inventory-record-rejected"
	skillSwitchInventoryFailedEvent     = "game-skill-switch-inventory-record-failed"
	skillSwitchInventoryReplayedEvent   = "game-skill-switch-inventory-replayed"
	skillSwitchInventoryProjectionEvent = "game-skill-switch-inventory-projection-send"
	// skillSwitchInventoryProjectionBlockedEvent：形状闸门拦下的帧。它必须能被单独 grep，
	// 因为线上判读就靠"有没有 blocked、blocked_count 是多少"。
	skillSwitchInventoryProjectionBlockedEvent = "game-skill-switch-inventory-projection-blocked"
	skillSwitchInventorySkippedEvent           = "game-skill-switch-inventory-skipped"
	// skillSwitchRegistrationCommitEvent：重投影边界上关闭了仍然打开的登记窗口
	// （状态键集合被换成窗口集合 ⇒ 移除真的生效）。**下一次实机只看这一行**就能
	// 判读"这一次到底登记了哪些槽位、删掉了哪些"。只在真的提交时记，平时不出现。
	skillSwitchRegistrationCommitEvent = "game-skill-switch-inventory-registration-commit"
	// skillSwitchRegistrationCommitFailedEvent：边界提交写库失败。按 fire-and-forget
	// 契约**不打断边界**（与 applySkillSwitchInventory 同口径），只记事件。
	skillSwitchRegistrationCommitFailedEvent = "game-skill-switch-inventory-registration-commit-failed"
)

// normalizeSkillSwitchInventoryMode 归一化 `DNFBRIDGE_SKILL_SWITCH_INVENTORY`。
//
// 默认（空 / `shape_guard` / `on` / `fix` / 未知取值）= `shape_guard` = 修复档：
// 包体形状与客户端 1428 入站解析器不符时**不下发**（记 projection-blocked）。
// 写 `project`（别名 `verbatim` / `legacy_project`）= 形状闸门**之前**的默认：
// 原样回放客户端 6 字节上行记录（已知会把客户端入站流读乱，仅用于取证/回退）。
// 写 `before_transition_only`（别名 `early_only` / `project_before_transition`）
// = 改动前的四条边界档。
// 写 `reference_drop`（别名 `off`/`0`/`disable`/`legacy`）= 改动前逐字节相同的旧档。
func normalizeSkillSwitchInventoryMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchInventoryReferenceDrop, "off", "0", "disable", "legacy", "drop":
		return skillSwitchInventoryReferenceDrop
	case skillSwitchInventoryRecordTier, "persist_only", "record_only":
		return skillSwitchInventoryRecordTier
	case skillSwitchInventoryBeforeTransitionTier,
		"early_only",
		"project_before_transition",
		"before_transition",
		"no_scene_ready_projection":
		return skillSwitchInventoryBeforeTransitionTier
	case skillSwitchInventoryProjectTier, "verbatim", "legacy_project":
		return skillSwitchInventoryProjectTier
	default:
		return skillSwitchInventoryShapeGuardTier
	}
}

// skillSwitchInventoryMode 返回归一化后的档位。
// options 为空（零值）时落到 shape_guard —— 与 normalizeSkillSwitchInventoryMode 的默认档一致，
// 否则"默认档就是修复档"这句话在零值路径上不成立（测试会抓）。
func (s *Service) skillSwitchInventoryMode() string {
	if s == nil || strings.TrimSpace(s.options.skillSwitchInventoryMode) == "" {
		return skillSwitchInventoryShapeGuardTier
	}
	return s.options.skillSwitchInventoryMode
}

// skillSwitchInventoryProjects 表示该档位要在（四条）边界上下发重投影。
//
// `project`（默认）与 `before_transition_only`（改动前的默认）**都**为真：
// 后者只是**少一条** type1345 之后的投影，四条老边界一个包都不减。
func (s *Service) skillSwitchInventoryProjects() bool {
	if s == nil {
		return false
	}
	switch s.skillSwitchInventoryMode() {
	case skillSwitchInventoryShapeGuardTier,
		skillSwitchInventoryProjectTier,
		skillSwitchInventoryBeforeTransitionTier:
		return true
	default:
		return false
	}
}

// skillSwitchInventoryProjectsAfterSceneReady 表示该档位要在 legacy type1345
// 「场景就绪」之后**补第二条**投影。`shape_guard`（新默认）与 `project` 都为真；
// `before_transition_only` 为假 ⇒ 与改动前逐字节相同的一条投影都不多发。
func (s *Service) skillSwitchInventoryProjectsAfterSceneReady() bool {
	if s == nil {
		return false
	}
	switch s.skillSwitchInventoryMode() {
	case skillSwitchInventoryShapeGuardTier, skillSwitchInventoryProjectTier:
		return true
	default:
		return false
	}
}

// skillSwitchInventoryFrameShapeValid 判断一帧是否符合客户端 1428 入站解析器的形状：
// `u8 count` + `count × (u32 key, u32 value)`，即长度必须恰好是 1+8*count。
//
// 这是纯函数，便于表驱动测试；它对"我们自己的上行记录"一律返回 false ——
// v4 档的 4 字节包体与 v6 档的 6 字节行都**不可能**落在 `1+8*count` 上
// （该集合只有 1,9,17,…），那正是今天线上一个字节都不发的原因。
func skillSwitchInventoryFrameShapeValid(frame []byte) bool {
	if len(frame) == 0 {
		return false
	}
	return len(frame) == 1+8*int(frame[0])
}

// skillSwitchInventoryRecordBodyLayout 返回上行记录读法档位。
//
// 零值（测试里直接字面量构造的 Service / options）= `LayoutV4` = **修复档**，
// 与 `NormalizeLayout` 的默认分支、与 optionsFromConfig 的显式默认值三者一致；
// 否则"默认档就是修复档"这句话在零值路径上不成立（测试会抓）。
func (s *Service) skillSwitchInventoryRecordBodyLayout() skillswitch.Layout {
	if s == nil || strings.TrimSpace(string(s.options.skillSwitchInventoryRecordBodyLayout)) == "" {
		return skillswitch.LayoutV4
	}
	return s.options.skillSwitchInventoryRecordBodyLayout
}

// handleSkillSwitchInventoryCommand 是 1428 的唯一入口；handled=true 表示这条命令
// 已具名处理（调用方不要再落进 unhandled 桶）。
//
// **本命令没有立即回包**：现场证据显示会话内登记本来就生效，丢失只发生在
// 穿脱装备 / 选择角色 / 重新登录 / 进出副本这四条重新同步边界上，所以 op1428 是
// 一条"上行推送"（与 op447/op197/op331 同族），它的应答是边界上的重投影，
// 不是一条按请求 id 配对的 ACK。
//
// 读法由 `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY` 决定（默认 v4 = 4 字节真包体；
// 现行分帧交来的 6 字节切片按 dual-read 去掉前 2 个非包体字节）。
func (s *Service) handleSkillSwitchInventoryCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	if s == nil || session == nil {
		return false, nil
	}
	if typ != currentSkillSwitchInventoryOpcode {
		return false, nil
	}
	if buffSwapReapplyEnabled() {
		return s.handleNativeBuffSwapRegistration(session, body)
	}
	mode := s.skillSwitchInventoryMode()
	layout := s.skillSwitchInventoryRecordBodyLayout()
	record, parseErr := skillswitch.ParseRecordIn(layout, body)
	fields := []any{
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"type", typ,
		"runtime_cmd_name", runtimeCmdPacketName(byte(dnfenum.GameCmdCommand), typ),
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"body_layout", layout.String(),
		"mode", mode,
		// 登记窗口档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`）：上行这一行就自带档位，
		// 于是"这条记录会以什么语义落库"在 `packet_log.txt` 上不需要推算。
		"registration_switch", skillswitch.CurrentRegistrationMode(),
		"registration_switch_default", skillswitch.RegistrationWindow,
		"registration_rollback_literal", skillswitch.RegistrationUnion,
		// 记账起点档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）：上行这一行
		// 就自带"arm 之前那一段 burst 会不会被记账"，于是"这一段登记为什么没提交"
		// 在 packet_log.txt 上不需要推算。
		"registration_arm", skillswitch.CurrentRegistrationArm(),
		"registration_arm_default", skillswitch.RegistrationArmRecord,
		"registration_arm_rollback_literal", skillswitch.RegistrationArmSentinel,
		// 绑定保护档位 + 这一条记录**含不含绑定信息**：这两项加起来就能解释
		// "这一段登记能不能改掉已经登记好的换装"
		// （`Value == Slot` 的 `ModeConfirm` 不含绑定，现场 17:12 那 9 条就是它）。
		"registration_bind", skillswitch.CurrentRegistrationBind(),
		"registration_bind_default", skillswitch.RegistrationBindKeep,
		"registration_bind_rollback_literal", skillswitch.RegistrationBindReplace,
		"binding_bearing", record.BindingBearing(),
		"feature", "buff_enhancement_skill_switch_inventory",
		"unverified_attribution", "vendor has no op1428 handler; operator report attributes it to the Buff-enhancement swap bar",
		"downlink_count_semantics", "unverified: count == number of populated slots is an inference, not a measurement",
	}
	if parseErr != nil {
		fields = append(fields, "reason", "unexpected_body_len", "parse_error", parseErr)
		s.logPacketEvent(skillSwitchInventoryObservedEvent, fields...)
		return true, nil
	}
	fields = append(fields,
		// `wire_prefix` 是现行分帧残留在包体前的 2 字节（客户端 u32 序号域高 16 位），
		// **不是字段**：v4 档读 4 字节包体时它恒为 0，读 6 字节切片时是那 2 个字节。
		"wire_prefix", record.WirePrefix,
		"wire_prefix_hex", fmt.Sprintf("%04x", record.WirePrefix),
		"wire_prefix_is_body", false,
		"slot", record.Slot,
		"sub_mode", record.Mode,
		"value", record.Value,
		"value_none", record.Value == skillswitch.ValueNone,
		"sentinel", record.Sentinel(),
		"record_hex", record.Hex(),
		"record_body_len", len(record.Bytes()),
	)
	if skillID, ok := record.SkillID(); ok {
		fields = append(fields, "skill_id", skillID)
	}
	s.logPacketEvent(skillSwitchInventoryObservedEvent, fields...)
	if mode == skillSwitchInventoryReferenceDrop {
		// 旧档：与改动前（以及 86JP/A21/厂商 9.16）逐字节一致 —— 不下发任何字节、不落库。
		return true, nil
	}
	return true, s.applySkillSwitchInventory(session, record)
}

// applySkillSwitchInventory 把一条记录按既有 owner 落库。
//
// 失败只记事件、**不**把设置推送升级成会话错误（与 op447/op197/op331 的
// fire-and-forget 契约一致）。具名拒绝（`ErrSkillUnknown`）与
// 一般失败分成两个事件名，线上 grep 一眼能分开。
func (s *Service) applySkillSwitchInventory(session *gameSession, record skillswitch.Record) error {
	if session.selectedCharacterID == 0 {
		s.logPacketEvent(skillSwitchInventorySkippedEvent,
			"conn_id", session.connID,
			"char_id", 0,
			"record_hex", record.Hex(),
			"reason", "character_unresolved")
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		s.logPacketEvent(skillSwitchInventoryFailedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"record_hex", record.Hex(),
			"save_error", errSettingsRepositoryUnavailable)
		return nil
	}
	owner, err := skillswitch.NewOwner(repos, skillswitch.OwnerOptions{
		Catalog: s.skillSwitchSkillCatalog(),
		// 登记窗口档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`，默认 = 修复档）。
		// 显式传进去（而不是让 store 自读）是为了让"这一次落库用的是哪一档"
		// 在 bridge 这一层就定下来，日志与行为不会有第二个真源。
		Registration: skillswitch.CurrentRegistrationMode(),
		// 记账起点档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`，默认 = 修复档）：
		// arm 之前那一段 burst 会被记进 `pending_*`，由重投影边界提升并提交。
		RegistrationArm: skillswitch.CurrentRegistrationArm(),
		// 绑定保护档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`，默认 = 修复档）：
		// 一条不含绑定信息的记录（`Value == Slot`）既不覆盖、也不删除已落库的绑定行。
		RegistrationBind: skillswitch.CurrentRegistrationBind(),
		// 删除语义档位（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`，默认 = 修复档 `keep`）：
		// 窗口提交**不删**窗口外的行（现场 char 1 被一次 8 条的登记从 20 行缩成 8 行）。
		RegistrationDelete: skillswitch.CurrentRegistrationDelete(),
	})
	if err != nil {
		s.logPacketEvent(skillSwitchInventoryFailedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"record_hex", record.Hex(),
			"save_error", err)
		return nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.Apply(ctx, characterID, s.accountIDForSession(session), record)
	if err != nil {
		event := skillSwitchInventoryFailedEvent
		if errors.Is(err, skillswitch.ErrSkillUnknown) {
			// 数据规则：引用了 PVF 里不存在的技能号 ⇒ 具名拒绝，整条不落库。
			event = skillSwitchInventoryRejectedEvent
		}
		s.logPacketEvent(event,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"record_hex", record.Hex(),
			"slot", record.Slot,
			"sub_mode", record.Mode,
			"reason", err,
			"reject_error", err.Error())
		return nil
	}
	s.logPacketEvent(skillSwitchInventoryRecordedEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"record_key", skillSwitchInventoryWrittenEntryKey(record),
		"record_hex", record.Hex(),
		"record_value", record.Describe(),
		"record_body_layout", record.Layout.String(),
		"record_body_len", len(record.Bytes()),
		"wire_prefix", record.WirePrefix,
		"wire_prefix_is_body", false,
		"skill_id", result.SkillID,
		"skill_gate", result.SkillGate,
		"sentinel", result.Sentinel,
		"slot_count", result.SlotCount,
		"forensic_count", result.ForensicCount,
		"forensic_skipped", result.ForensicSkipped,
		"replayed", result.Replayed,
		"idempotent", result.Replayed,
		// 旧 6 字节行被改写成 v4 规范形（字段完全相同）：一次性、可 grep 的迁移信号。
		"layout_upgraded", result.LayoutUpgraded,
		// 登记窗口（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`）：哨兵是**分隔符**不是清空；
		// 窗口里有记录才会提交（空窗口永不提交）。窗口键不参与 slot_count / record_count。
		"registration_switch", registrationSwitch(result.RegistrationMode),
		"registration_switch_default", skillswitch.RegistrationWindow,
		"registration_rollback_literal", skillswitch.RegistrationUnion,
		// 记账起点档位 + "arm 之前那一段 burst"记了多少条：这两项加起来就能解释
		// "这一段登记为什么还没提交"（未 arm 且没有 pending ⇒ 这一条只是加法落库）。
		"registration_arm", registrationArm(result.RegistrationArm),
		"registration_arm_default", skillswitch.RegistrationArmRecord,
		"registration_arm_rollback_literal", skillswitch.RegistrationArmSentinel,
		// 绑定保护档位 + "这一条含不含绑定信息 + 有没有把已落库的绑定行原样保住"：
		// 现场判读"这一段登记会不会把换装栏的绑定改坏"只需要这三项。
		// `binding_preserved=true` 就是 2026-09-28T17:12:45Z 那 9 条本该有、而当时没有的那一行。
		"registration_bind", registrationBind(result.RegistrationBind),
		"registration_bind_default", skillswitch.RegistrationBindKeep,
		"registration_bind_rollback_literal", skillswitch.RegistrationBindReplace,
		// 删除语义档位 + "这一次本来会删掉哪些行、保住了哪些"：默认档下
		// `registration_dropped_count` 恒 0，那些行改记在 `registration_retained_*` 里。
		"registration_delete", registrationDelete(result.RegistrationDelete),
		"registration_delete_default", skillswitch.RegistrationDeleteKeep,
		"registration_delete_rollback_literal", skillswitch.RegistrationDeleteReplace,
		"registration_retained_count", len(result.RegistrationRetained),
		"registration_retained_entries", skillSwitchRecordEntries(result.RegistrationRetained),
		"binding_bearing", result.BindingBearing,
		"binding_preserved", result.BindingPreserved,
		"preserved_hex", result.PreservedHex,
		"registration_pending_count", result.RegistrationPendingCount,
		"registration_window_count", result.RegistrationWindowCount,
		"registration_committed", result.RegistrationCommitted,
		"registration_trigger", registrationTrigger(result.RegistrationTrigger),
		"registration_confirm_only", result.RegistrationConfirmOnly,
		"registration_protected_count", len(result.RegistrationProtected),
		"registration_protected_entries", skillSwitchRecordEntries(result.RegistrationProtected),
		"registration_kept_count", len(result.RegistrationKept),
		"registration_dropped_count", len(result.RegistrationDropped),
		"registration_kept_triples", skillSwitchRecordTriples(result.RegistrationKept),
		"registration_dropped_triples", skillSwitchRecordTriples(result.RegistrationDropped),
		"registration_dropped_entries", skillSwitchRecordEntries(result.RegistrationDropped),
		"source", skillswitch.SourceCurrentEXE,
		"save_error", error(nil))
	// 「登记当场回帧」（`DNFBRIDGE_SKILL_SWITCH_ANSWER_REGISTRATION`，默认 = 修复档）：
	// 这一条如果是**哨兵**（客户端给这一段登记收尾），当场把落库后的换装帧回一帧 ——
	// 在此之前服务端只在场景边界上答，玩家在面板里操作的那几秒是**沉默**的。
	// 失败只记事件、返回 nil（与落库同一条 fire-and-forget 契约）。
	s.answerSkillSwitchRegistration(session, result.Sentinel)
	return nil
}

// registrationSwitch 把"本次落库的档位"写进日志；空值（`union` 档不设该字段）
// 仍然显式写成回退字面量，免得出现一个空字段要靠猜。
func registrationSwitch(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return skillswitch.RegistrationUnion
	}
	return mode
}

// registrationArm 同上，口径是记账起点档位（`union` 档整段跳过 ⇒ 写回退字面量）。
func registrationArm(arm string) string {
	if strings.TrimSpace(arm) == "" {
		return skillswitch.RegistrationArmSentinel
	}
	return arm
}

// registrationDelete 同上，口径是**删除语义**档位（`union` 档整段跳过 ⇒ 写回退字面量）。
func registrationDelete(deleteTier string) string {
	if strings.TrimSpace(deleteTier) == "" {
		return skillswitch.RegistrationDeleteReplace
	}
	return deleteTier
}

// registrationBind 同上，口径是绑定保护档位（`union` 档整段跳过 ⇒ 写回退字面量）。
func registrationBind(bind string) string {
	if strings.TrimSpace(bind) == "" {
		return skillswitch.RegistrationBindReplace
	}
	return bind
}

// registrationTrigger 把"关闭窗口的那条边界"写进日志；没提交就写 `-`。
func registrationTrigger(trigger string) string {
	if strings.TrimSpace(trigger) == "" {
		return "-"
	}
	return trigger
}

// skillSwitchRecordTriples 把记录渲染成可读的 `(slot,mode,value)` 三元组列表
// （判定"落库的到底是不是客户端发的那几个字段"就看它）。空列表写成 `-`，
// 免得日志里出现"字段存在但值为空"这种要靠猜的形状。
func skillSwitchRecordTriples(records []skillswitch.Record) string {
	if len(records) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts,
			fmt.Sprintf("(%d,%d,%d)", record.Slot, record.Mode, record.Value))
	}
	return strings.Join(parts, "")
}

// skillSwitchRecordEntries 把记录渲染成落库字面形 `slot_<n>=<hex>`（升序）。
func skillSwitchRecordEntries(records []skillswitch.Record) string {
	if len(records) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, skillSwitchInventorySlotEntryKey(record)+"="+record.Hex())
	}
	return strings.Join(parts, ",")
}

// skillSwitchInventoryWrittenEntryKey 复现"这条记录**真的写了哪一个键**"。
//
// 对普通记录是 `slot_<Slot>`；对 `ModeClear` 哨兵是 `sentinel_<hex>` ——
// 哨兵**不写** `slot_*`（见 store.go 的 Apply）。此前这个字段一律按记录算，
// 于是哨兵会在日志里写成 `record_key=slot_33`，而 33 号槽其实一个字节都没写；
// 线上判读"这条到底登记了什么"时这是会误导人的（现场 13:52:40.582 那条就是它）。
func skillSwitchInventoryWrittenEntryKey(record skillswitch.Record) string {
	if record.Sentinel() {
		return "sentinel_" + record.Hex()
	}
	return skillSwitchInventorySlotEntryKey(record)
}

// skillSwitchInventorySlotEntryKey 复现落库时的状态键名，只用于日志对齐。
func skillSwitchInventorySlotEntryKey(record skillswitch.Record) string {
	return "slot_" + strconv.Itoa(int(record.Slot))
}

// skillSwitchSkillCatalog 返回装配期加载的 job-scoped PVF 技能目录
// （`skill/skilllist.lst` 的展开结果，与 op19/op29 用的是同一份）。
// 没加载时返回 nil，落库走 `skill_gate=catalog_unavailable` 的放行分支。
//
// # 2026-09-29：默认档不再把目录交给这道门（**具名拒绝是一条错误的规则**）
//
// 操作者报障「Buff 强化面板里登记杀意波动，选择角色后再打开是空的」。
// 真机取证（只读，见 `_dist/module4-instrument-audit.md`）：
//
//   - 客户端上行的 op1428 记录里，杀意波动是 **slot=26**（`record_hex=1a031a00`）；
//   - 而真实 PVF 里 job 0 的 **杀意波动是 id 52**（`Name == "杀意波动"`，另有
//     id 150「强化 - 杀意波动」），job 0 的技能号序列是
//     `1..25,27,29,30,…` —— **26 在 job 0 根本不存在**（job 1/2/3 才有 26）；
//   - 于是 `o.catalog.Find(0, 26)` 落空 ⇒ 每次登录都 `ErrSkillUnknown` 具名拒绝
//     （`game-skill-switch-inventory-record-rejected … job=0 skill=26`，4 次登录
//     拒 4 次）⇒ 从未落库 ⇒ 换角色后重发里没有 26 ⇒ 面板空。
//     （「关掉窗口再打开还在」是因为那是客户端本地状态，不经过服务端。）
//
// 结论：**客户端上行那个数字不是 job-scoped PVF 技能号**，服务端却拿它去技能表里
// 查。而服务端其实**不需要理解这个数字** —— 它的职责只是把客户端自己登记的这份
// 集合原样存下、原样补发。所以默认档不再提供目录，所有记录走
// `SkillGateCatalogAbsent` 的放行分支（仍然落库），那道存在性校验随之失效。
//
// # 开关
//
// `DNFBRIDGE_SKILL_SWITCH_SKILL_GATE`
//
//	未设 / 其它未知取值（**默认 = 修复档**）→ 不提供目录，全部落库。
//	job_scoped_pvf / strict / today / legacy / on / 1 / true
//	    → 与 2026-09-29 之前逐字节相同：提供目录，未知技能号具名拒绝。
func (s *Service) skillSwitchSkillCatalog() *dnfskill.Table {
	if s == nil {
		return nil
	}
	if !skillSwitchSkillGateStrict() {
		return nil
	}
	s.initialSkillsMu.Lock()
	defer s.initialSkillsMu.Unlock()
	return s.skillCatalog
}

// skillSwitchSkillGateStrict 报告是否保留"技能号必须存在于 job-scoped PVF 目录"
// 这条具名拒绝。默认 false = 修复档（放行并落库）。
func skillSwitchSkillGateStrict() bool {
	switch strings.ToLower(strings.TrimSpace(
		os.Getenv("DNFBRIDGE_SKILL_SWITCH_SKILL_GATE"))) {
	case "job_scoped_pvf", "strict", "today", "legacy", "on", "1", "true":
		return true
	default:
		return false
	}
}

// sendSkillSwitchInventoryProjection 是四条边界上的重投影。
//
// 语义与失败契约：
//   - `reference_drop` 档：**一个字节都不发**（与改动前逐字节相同）；
//   - `record` 档：也不发（只落库）；
//   - 没有落库记录：不发，只记一条带 `slot_count=0` 的 skipped 事件；
//   - settings 仓储缺失/读失败：记事件、**返回 nil**（读失败不能把登录打断，
//     与 op447 回放缺档时 skip 的口径一致）；
//   - 只有真正的写线失败才返回错误（调用方本来就在处理写线错误）。
//
// 候选对实验（`DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`，默认关）挂在**同一条边界**上，
// 位置在落库帧回放**之前**：默认档下回放帧全被形状闸门拦下（0 字节），
// 于是实验帧是这条边界上唯一被解析的帧。见
// skill_switch_inventory_candidate_pairs.go。
func (s *Service) sendSkillSwitchInventoryProjection(session *gameSession, source string) error {
	if buffSwapReapplyEnabled() {
		return nil // Native snapshots have explicit late lifecycle boundaries.
	}
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if !s.skillSwitchInventoryProjects() {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		s.logPacketEvent(skillSwitchInventorySkippedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"reason", "settings_repository_unavailable")
		return nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	layout := s.skillSwitchInventoryRecordBodyLayout()
	// 登记窗口：把"仍然打开"的窗口在这里关掉（哨兵分隔符之外的第二道关闭点）。
	// 现场实证需要它 —— char 6 @13:52 那段登记**只有开头一个哨兵**、没有收尾哨兵，
	// 只靠哨兵提交会让"移除"晚一次登记才生效。必须在 `Load` **之前**做，
	// 否则读到的是还没剪枝的并集。
	s.commitSkillSwitchRegistration(session, source, characterID, layout)
	state, found, err := skillswitch.Load(ctx, repos, characterID, layout)
	if err != nil || !found || len(state.Records) == 0 {
		s.logPacketEvent(skillSwitchInventorySkippedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"scope", skillswitch.Scope(characterID),
			"slot_count", len(state.Records),
			"load_error", err,
			"reason", "no_persisted_skill_switch_inventory")
		return nil
	}
	// 候选对实验帧（默认关）：结构合法（客户端 1428 入站形状），所以**先发**，
	// 免得 project 档那批形状不合法的回放帧先把客户端入站流读乱。
	if err := s.sendSkillSwitchCandidatePairs(session, source, characterID); err != nil {
		return err
	}
	// 落库换装行的**合法下行帧**（`DNFBRIDGE_SKILL_SWITCH_RESEND`，默认 = 修复档）：
	// 默认档下这是这条边界上唯一携带换装栏状态的下行。它按构造满足客户端 1428
	// 入站解析器形状，与下面那批"上行记录原样回放"（必然被闸门全拦）是两回事。
	// 位置刻意排在候选实验帧**之后**：两者若同时开，本帧后到 ⇒ 客户端那张 map
	// 上本帧胜出（事件里的 `supersedes_candidate_pairs` 会写明）。
	// 开关表、pair 取值的来源与未证实项全在 skill_switch_inventory_resend.go。
	if err := s.sendSkillSwitchLoadoutResend(session, source, characterID, state); err != nil {
		return err
	}
	// 本档（DNFBRIDGE_SKILL_SWITCH_DOWNLINK_SHAPE，默认 off ⇒ 零字节、逐字节等于现状）：
	// 把落库记录按【客户端自己那 6 字节的形状】`u8 count + count×6` 再下发一条。
	// 依据与取证见 skill_switch_inventory_downlink_record6.go 顶部。
	if err := s.sendSkillSwitchDownlinkRecord6(session, source, characterID, state.Records); err != nil {
		return err
	}
	// 本档（DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY，默认 off ⇒ 零字节、零日志）：
	// 在场景就绪边界补发一次【登记应答】—— 那一帧（count=15 / body_len=121 /
	// source=…registration_sentinel）是当场登记时唯一让面板亮起来的下行，
	// 而重进时客户端不发哨兵 ⇒ 服务端从不补发。依据见同目录
	// skill_switch_inventory_downlink_record6.go 的「重进边界补发登记应答」段。
	s.answerSkillSwitchRegistrationAtSceneReady(session, source)
	frames := state.Frames()
	// 形状闸门（新默认档）：只发形状与客户端 1428 入站解析器一致的帧。
	// `project` 档保留原样回放，用于取证与一键回退。
	sent := frames
	blocked := [][]byte(nil)
	if s.skillSwitchInventoryMode() == skillSwitchInventoryShapeGuardTier {
		sent = make([][]byte, 0, len(frames))
		for _, frame := range frames {
			if skillSwitchInventoryFrameShapeValid(frame) {
				sent = append(sent, frame)
			} else {
				blocked = append(blocked, frame)
			}
		}
		if len(blocked) > 0 {
			s.logPacketEvent(skillSwitchInventoryProjectionBlockedEvent,
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"char_id", session.selectedCharacterID,
				"source", source,
				"scope", skillswitch.Scope(characterID),
				"msg_id", currentSkillSwitchInventoryOpcode,
				"classification", 0,
				"slot_count", len(frames),
				"sent_count", len(sent),
				"blocked_count", len(blocked),
				"record_body_layout", layout.String(),
				"blocked_body_len", blockedFrameBodyLen(blocked),
				// 两个闸门分开写清楚：这批帧**通过**上行记录闸门（4/6 字节），
				// 只是**不是**合法下行帧（`1+8*count`）。被混用的那两个判据因此
				// 在日志上分得开（见 skill_switch_inventory_resend.go 顶部）。
				"uplink_record_gate", "len_in_4_or_6",
				"uplink_record_gate_result", skillSwitchUplinkRecordBodyValid(blockedFrameBodyLen(blocked)),
				"downlink_frame_gate", "len==1+8*count",
				"downlink_frame_gate_result", skillSwitchDownlinkFrameValid(blocked[0]),
				"reason", "body_shape_not_client_s2c_map",
				"blocked_hexes", skillSwitchInventoryFrameHexes(blocked))
		}
	}
	for _, frame := range sent {
		if err := s.sendGameUpperRawClass(session, currentSkillSwitchInventoryOpcode, frame, 0); err != nil {
			return err
		}
	}
	if len(sent) == 0 {
		// 形状闸门把所有帧都拦下了：一个字节都没发，不能记成一次成功投影。
		return nil
	}
	s.logPacketEvent(skillSwitchInventoryProjectionEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"scope", skillswitch.Scope(characterID),
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		"slot_count", len(frames),
		"sent_count", len(sent),
		"record_body_layout", layout.String(),
		"record_hexes", skillSwitchInventoryFrameHexes(frames),
		"body_source", "verbatim_replay_of_client_op1428_records",
		"downlink_count_semantics", "unverified",
		"persisted_at", state.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	return nil
}

// commitSkillSwitchRegistration 在重投影边界上关闭仍然打开的登记窗口。
//
// 语义与失败契约：
//   - 档位不是 `window`（`union` / `sentinel_only`）：**直接返回**，一个字节都不写；
//   - 该角色从没见过哨兵（没有 `window_*` 键）：不提交、不写库 —— 这就是
//     "没有哨兵 ⇒ 逐字节等于改动前"的落点，不是死代码（另见 registration.go 的档位表）；
//   - 真的提交了：记 `...-registration-commit`，字段自带"角色 / 收到多少条 /
//     解码后的 (slot,mode,value) / 写了什么到 settings / 删了什么 / 档位"；
//   - 任何错误（读不到、写不进）：只记事件、**返回 nil** —— 读失败不能把登录打断，
//     与 `applySkillSwitchInventory` 的 fire-and-forget 契约一致。
func (s *Service) commitSkillSwitchRegistration(
	session *gameSession,
	source string,
	characterID string,
	layout skillswitch.Layout,
) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	mode := skillswitch.CurrentRegistrationMode()
	if mode != skillswitch.RegistrationWindow {
		return
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	commit, committed, err := skillswitch.CommitRegistration(ctx, repos, characterID, layout, mode)
	if err != nil {
		s.logPacketEvent(skillSwitchRegistrationCommitFailedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"source", source,
			"registration_switch", mode,
			"save_error", err)
		return
	}
	if !committed {
		return
	}
	s.logPacketEvent(skillSwitchRegistrationCommitEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"source", source,
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		// 档位：默认档 + 回退字面量都写出来，下一次实机一看就知道生效的是哪一档。
		"registration_switch", mode,
		"registration_switch_default", skillswitch.RegistrationWindow,
		"registration_rollback_literal", skillswitch.RegistrationUnion,
		"registration_tier", "sentinel_and_boundary_commit",
		// 记账起点档位 + 这条窗口是被**哪一个**边界关掉的 + 提升了几条 pending：
		// "这一次到底为什么提交"在日志上不需要推算。
		//   commit_trigger=sentinel_delimiter            -> 哨兵关的（窗口里有记录）
		//   commit_trigger=boundary_open_window          -> 边界关的（窗口里有记录）
		//   commit_trigger=boundary_pre_arm_burst        -> 边界关的，窗口为空，
		//       提交的是"arm 之前那一段 burst"（现场 15:35 char 3 的形状）
		"registration_arm", commit.Arm,
		"registration_arm_default", skillswitch.RegistrationArmRecord,
		"registration_arm_rollback_literal", skillswitch.RegistrationArmSentinel,
		// 绑定保护档位 + "这一次登记有没有声明绑定 / 因此保住了哪些行"：
		// `confirm_only=true` + `protected_entries=` 就是"这一段登记不含绑定信息，
		// 所以它删不掉别人登记好的换装"那一条的现场读数。
		"registration_bind", registrationBind(commit.Bind),
		"registration_bind_default", skillswitch.RegistrationBindKeep,
		"registration_bind_rollback_literal", skillswitch.RegistrationBindReplace,
		"registration_delete", registrationDelete(commit.Delete),
		"registration_delete_default", skillswitch.RegistrationDeleteKeep,
		"registration_delete_rollback_literal", skillswitch.RegistrationDeleteReplace,
		"retained_count", len(commit.Retained),
		"retained_triples", skillSwitchRecordTriples(commit.Retained),
		"retained_entries", skillSwitchJoinEntries(commit.RetainedEntries),
		"confirm_only", commit.ConfirmOnly,
		"committed_window_binding_slots", skillSwitchRegistrationSlotList(
			skillSwitchBindingRecords(commit.Kept)),
		"protected_count", len(commit.Protected),
		"protected_triples", skillSwitchRecordTriples(commit.Protected),
		"protected_entries", skillSwitchJoinEntries(commit.ProtectedEntries),
		"commit_trigger", commit.Trigger,
		"promoted_records", commit.Promoted,
		// 收到多少条 = 窗口里的槽位数（= 这次边界实际登记的行数）。
		"window_records", commit.WindowRecords,
		"window_slots", skillSwitchRegistrationSlotList(commit.Kept),
		"slot_count", commit.SlotCount,
		"forensic_count", commit.ForensicCount,
		"committed", true,
		// 解码后的三元组 + 落库字面形：两样都给，线上不需要再推算。
		"kept_triples", skillSwitchRecordTriples(commit.Kept),
		"dropped_triples", skillSwitchRecordTriples(commit.Dropped),
		"written_entries", skillSwitchJoinEntries(commit.KeptEntries),
		"dropped_entries", skillSwitchJoinEntries(commit.DroppedEntries),
		"dropped_count", len(commit.Dropped),
		"record_body_layout", layout.String(),
		"body_source", "persisted_skill_switch_rows_after_window_commit",
		"loadout_updated_at", commit.UpdatedAt.UTC().Format(time.RFC3339Nano))
}

// skillSwitchRegistrationSlotList 是 `slot_<n>` 键名列表（升序），便于 grep 槽位集合。
func skillSwitchRegistrationSlotList(records []skillswitch.Record) string {
	if len(records) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, skillSwitchInventorySlotEntryKey(record))
	}
	return strings.Join(parts, ",")
}

// skillSwitchBindingRecords 从一批记录里挑出**携带绑定信息**的那些
// （`Value != Slot`）。提交日志里的 `committed_window_binding_slots` 就是它 ——
// 非空表示"这一次登记自己声明了绑定"，空表示这一次是 confirm-only。
func skillSwitchBindingRecords(records []skillswitch.Record) []skillswitch.Record {
	binding := make([]skillswitch.Record, 0, len(records))
	for _, record := range records {
		if record.BindingBearing() {
			binding = append(binding, record)
		}
	}
	return binding
}

// skillSwitchJoinEntries 把 `slot_<n>=<hex>` 列表接成一行；空列表写成 `-`。
func skillSwitchJoinEntries(entries []string) string {
	if len(entries) == 0 {
		return "-"
	}
	return strings.Join(entries, ",")
}

// blockedFrameBodyLen 是闸门日志里的体长（用于区分 v4 的 4 字节包体与改动前的 6 字节行）。
func blockedFrameBodyLen(frames [][]byte) int {
	if len(frames) == 0 {
		return 0
	}
	return len(frames[0])
}

// skillSwitchInventoryFrameHexes 是给日志用的确定性十六进制列表
// （`state.Frames()` 已按 Slot 升序，所以这里不需要再排序）。
func skillSwitchInventoryFrameHexes(frames [][]byte) string {
	parts := make([]string, 0, len(frames))
	for _, frame := range frames {
		parts = append(parts, hex.EncodeToString(frame))
	}
	return strings.Join(parts, ",")
}
