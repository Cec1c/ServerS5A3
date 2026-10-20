package dnfbridge

// 本文件是**登录后装备栏按"没穿上"渲染（0 耐久 / 非权威属性 / 像被卸下）**这条缺陷的
// 第四轮修法。前三轮都是"多发一点同样的东西"，全部被现场证伪；本轮**只改一个相对顺序**，
// 不新增任何一帧、不新增任何一份行。
//
// ── 一、前三轮的现场结论（一句话各自）
//
//	第 1 轮 `login-zero-durability.md`：**行本身是对的**。登录与进图两条链路的武器行
//	  逐字节相同（槽 12 / item 101040493 / +10 耐久 48 / 实例 999999998 / raw=0），
//	  客户端读耐久的偏移就是 `sub_1D77560` RVA 0x19776AB（= 行 +10）。
//	第 2 轮 `login-client-slot-state.md`：在登录路径的场景就绪点**补发 list 3 容器**
//	  （31 行、`12:48`）→ 画面不变 ⇒ **容器不是客户端在提交那一刻消费的那件东西**。
//	第 3 轮 `login-actor-generation-rebuild.md`：在"客户端提交之后的第一条报文"上补发
//	  **mode0 + 带 31 条装备 create 行的 mode1**（现场 `frame_count=2 mode1_create_count=31
//	  durability_carried=…12:48…` 全部齐备）→ 画面仍不变 ⇒ **提交之后重建 actor 世代无效**，
//	  客户端是在**提交那一刻**用**它最后收到的那件载体**定型本地那一代对象的。
//
// ── 二、当场把"最后一件载体"数出来（`packet_log.txt`，本次登录，conn game-000021，角色 6）
//
// 行号 / `t=` / 包（`grep` 原文，只读）：
//
//	139988  15:34:33.249409608Z SEND class0/op2 498   mode0 actor create（外观表，owner 10）
//	139991  15:34:33.249435338Z SEND class0/op2 163   mode1 binding-only（**无**装备 create 行）
//	140020-140026 15:34:33.41x    SEND class0/op13 ×7  引导容器 {0,1,2,7,12,38,**3**}（list 3 = 31 行）
//	140056  15:34:33.430447747Z SEND class0/op1340 7445 冒险团模型（mode1 的前置）
//	140063  15:34:33.436936944Z SEND class0/op2 **2161** **mode1 完整装备 create 行（31 行，12:48）**
//	140067  15:34:33.440325133Z SEND class0/op14 122  勋章宝石（**物品行**，不是 actor 世代）
//	140070  15:34:33.465001779Z SEND class0/op2 **498**  **mode0 name-tag 重放 ← 提交前最后一件 actor 载体**
//	140137  15:34:33.549150012Z SEND class0/op24 12   **typed op24 场景最终提交**
//
// ⇒ **提交之前最后一件与 actor/装备有关的载体，不是那份完整的 31 行装备 mode1，
//   而是一份 `initial_town_player_state.go:222` 的 name-tag mode0 重放（498 字节）。**
//
// 这份 mode0 是**残缺**的（三条都是代码层可判的事实）：
//
//	1. 装备表只有 **外观槽 0..13**（+ 有名字牌时的槽 28 一行），现场事件
//	   `game-upper-current-scene-object-list-built … equip_rows=15 mode0_equip_summary_rows=15
//	   mode0_equip_summary_source=character_equipment_repository_current_exe_full_14_slot_typed_mode0_tail
//	   equip_raw_source_bytes=0`（第 139970 / 140068 行，两次 mode0 逐字段相同）；
//	   **31 个已穿戴槽里的 14..32 一个都不在里面**。
//	2. 那 19 字节的外观行**没有耐久字段**：`character_scene_object_tail.go:190-206` 的行布局是
//	   `u8 槽 / u32 itemID / u32 rawLen(+raw) / u8 flags / u32 link / u32 aux / u8 auxFlag`；
//	   现场槽 12 的行逐字节读出来是 `0c 6dc10506 00000000 1e 00000000 00000000 00`
//	   （itemID 在、**rawLen=0**、没有耐久字），而 mode1 的装备 create 行是
//	   `0c 6dc10506 fec99a3b 0f 3000 …`（`fec99a3b`=999999998 实例、`3000`=48 耐久）。
//	3. 客户端自己复述出来的本地那份就是**实例 0**：第 140199 行
//	   `RECV … type=19 … src=(0,12,0x00000000) count=1 dst=(3,12,0x0605C16D)`
//	   （第 140202 行的 `game-aligned-command-real-response … requestA=(0,12) requestB=(3,12)
//	   item=101040493 changed=false`）—— 客户端手里那把武器**没有实例值**，
//	   与上面那份"没有实例字段的外观行"同形，而不是与 mode1 的 999999998 同形。
//
// ── 三、两条**正常**路径的共同不变量（本轮据此判"顺序被违反"）
//
//	进副本（`packet_log.txt` conn game-000028，角色 6，15:37:18Z）：
//	  145041 `game-upper-post-start-map-local-mode1-send … owner_channel=0`
//	  145043 SEND class0/op2 **2161** mode1 完整装备 create 行（31 行）**← 本地 actor 最后一件载体**
//	  145060 SEND op120（落位）
//	  145079/145081 SEND class0/op2 468 / 1585 = **队友**（145076
//	         `game-dungeon-party-peer-actor-send … actor_char_id=3 object_key=3 owner_channel=0`），
//	         不是本地 actor。
//	  代码：`scene_post_start_map.go:176-192`（mode0）**在前**、`:381-406`（带装备 mode1）**在后**；
//	        并且 `game-upper-post-start-map-mode0-skipped … descriptor_and_actor_bound_before_op27_page_enter`
//	        证明这条路**不再补第二次 mode0**。
//	回城：`town_post_transition_player_state.go:118-127`（mode0）在前、`:128-137`（带装备 mode1）在后。
//
// ⇒ 不变量：**场景定型之前，完整装备 mode1 必须是最后一件 actor 载体；mode0（外观表）只能更早。**
//   登录路径**违反了它**：`initial_town_player_state.go:203-205`（装备 mode1）之后
//   `:222` 又发了一次 mode0（name-tag 重放），于是提交那一刻客户端手里的最后一份是**残缺的外观表**。
//
// ── 四、本轮的修法（**顺序改动**，零新增字节）
//
// 把 `initial_town_player_state.go` 里那次 name-tag mode0 重放从**装备 mode1 之后**挪到
// **装备 mode1 之前**（`loginAuthoritativeBeforeCommitNameTagReplay`，仍然调用原来的同一个
// `currentReapplyNameTagAfterMode1`，一个字节都不改）。修完顺序是：
//
//	容器 + op1340 → **name-tag mode0（外观表）** → **mode1 完整装备 create 行（最后一件）** → typed op24
//
// 于是"提交前最后一件 actor 载体"回到**完整的 31 行装备 mode1**（与进副本/回城同形）。
// **没有**新增容器、**没有**新增任何一份行、**没有**新增 mode0/mode1 帧 —— 只是把已有的那一帧
// 提前发射。name-tag 自身的绑定前置（worn 槽 30 的对象存在）由**它前面**的 list 3 引导容器
// （`initial_town_player_state.go:107` → `select_character_inventory_bootstrap.go`，
// 现场 140026 `packet_index=6 list_type=3 entry_count=31`）满足；这与名字牌购买路径
// `name_tag_card.go:200-234` 的既有先例同族（`class0_op14_slot30_then_mode0_actor_endpoint`）。
// 另外第 3 轮那条提交后 hook（默认档）本来就会再发一次**同一台构建器**的 mode0（`mode0 → mode1`，
// mode1 在后），所以名字牌在那一侧还有第二次绑定机会。
//
// ── 五、开关（消费点自读 env，不进 options.go；默认即修复，回退必须显式）
//
//	DNFBRIDGE_LOGIN_AUTHORITATIVE_BEFORE_COMMIT
//	  默认（含未设置/写错的值）= equipment_mode1_last_before_op24 → 上面那个顺序；
//	  name_tag_replay_after_mode1（别名 legacy/today/after_mode1/off/0/false/no/disable/
//	  disabled/none）→ name-tag 重放回到装备 mode1 **之后** = 今天逐字节；
//	  事件 `game-login-authoritative-before-commit-legacy-order`。
//
// 事件 `game-login-authoritative-before-commit-ordered`（修复档每次进城恰好一条）带
// `ordering_position` / `frames_before_typed_op24` / `nametag_mode0_position` /
// `last_actor_carrier_before_typed_op24` / 31 行逐槽耐久 / 开关值。

import (
	"context"
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	// loginAuthoritativeBeforeCommitEnv 是"提交前权威载体的相对顺序"的开关名。
	loginAuthoritativeBeforeCommitEnv = "DNFBRIDGE_LOGIN_AUTHORITATIVE_BEFORE_COMMIT"

	loginAuthoritativeBeforeCommitEquipmentMode1Last = "equipment_mode1_last_before_op24"
	loginAuthoritativeBeforeCommitNameTagAfterMode1  = "name_tag_replay_after_mode1"

	loginAuthoritativeBeforeCommitOrderPosition = "equipment_bearing_mode1_is_the_last_actor_carrier_before_typed_op24"
	loginAuthoritativeBeforeCommitFramesFixed   = "containers_and_op1340_then_nametag_mode0_scene_object_list_then_equipment_mode1_actor_equipment_create_rows_then_typed_op24"
	loginAuthoritativeBeforeCommitFramesLegacy  = "containers_and_op1340_then_equipment_mode1_actor_equipment_create_rows_then_nametag_mode0_scene_object_list_then_typed_op24"
	loginAuthoritativeBeforeCommitInvariant     = "dungeon_entry_scene_post_start_map_176_192_mode0_before_381_406_equipment_mode1_and_town_return_town_post_transition_player_state_118_127_before_128_137_both_leave_the_equipment_bearing_mode1_as_the_last_local_actor_carrier"
	loginAuthoritativeBeforeCommitAuthority     = "the_last_actor_carrier_before_the_typed_op24_commit_is_what_the_client_commits_with_the_mode0_scene_object_list_carries_only_the_14_slot_appearance_table_without_durability_or_instance_values"
	loginAuthoritativeBeforeCommitRedundant     = "DNFBRIDGE_LOGIN_EQUIPMENT_SCENE_READY_REPLAY,DNFBRIDGE_LOGIN_ACTOR_GENERATION_REBUILD"

	// loginAuthoritativeBeforeCommitMode0CarrierFacts 是那份 mode0 载体的形状判据（代码层固定事实，
	// 见本文件头 §二）：19 字节外观行、无耐久字段、无实例种子字段、raw 块长度恒 0。
	loginAuthoritativeBeforeCommitMode0RowBytes = 19
)

// loginAuthoritativeBeforeCommitEnabled 回答"要不要把 name-tag mode0 重放挪到装备 mode1 之前"。
// 只有**显式**写回退字面量才关闭；其余取值（含未设置、写错的串）都是修复档 ——
// 默认即修复，回退必须显式（与 loginEquipmentSceneReadyReplayEnabled 同一取舍）。
func loginAuthoritativeBeforeCommitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginAuthoritativeBeforeCommitEnv))) {
	case loginAuthoritativeBeforeCommitNameTagAfterMode1, "after_mode1",
		"off", "0", "false", "no", "legacy", "today", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginAuthoritativeBeforeCommitSwitch 只用于事件/诊断。
func loginAuthoritativeBeforeCommitSwitch() string {
	if loginAuthoritativeBeforeCommitEnabled() {
		return loginAuthoritativeBeforeCommitEquipmentMode1Last
	}
	return loginAuthoritativeBeforeCommitNameTagAfterMode1
}

// loginAuthoritativeBeforeCommitNameTagReplay 是这次**顺序改动**的唯一动作：按**原来那一个**
// 发送器（`currentReapplyNameTagAfterMode1`，内部 = `currentSendNameTagActorRefresh`，
// `name_tag_card.go:189-198`）把 name-tag mode0 重放发出去，只是**调用点提前到装备 mode1 之前**。
//
// 它不构造任何新包体、不改任何字节、不新增任何帧：调用者决定这次调用落在哪一侧。
// 返回值：
//
//	sent     —— 这一次到底有没有真的发出一帧（判据与发送器自己的前置完全同一个读取器
//	            `loadCurrentSelectedActorNameTagState`，所以"说发了就真发了"）；
//	evidence —— 前置取证串（worn 槽 30 的道具由**它前面**的 list 3 引导容器交付），只进日志。
func (s *Service) loginAuthoritativeBeforeCommitNameTagReplay(ctx context.Context, session *gameSession) (bool, string, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false, "no_selected_character", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	itemID, _, found, err := s.loadCurrentSelectedActorNameTagState(ctx, session, session.selectedCharacterID)
	if err != nil {
		return false, "name_tag_state_load_failed", err
	}
	if !found || itemID == 0 {
		// 没有佩戴名字牌 ⇒ 今天也不会发这一帧 ⇒ 两档逐字节相同。
		return false, "no_worn_name_tag_slot30_item", nil
	}
	evidence := "worn_slot30_item_" + strconv.FormatUint(uint64(itemID), 10) +
		"_delivered_by_list3_bootstrap_container_before_this_replay"
	if err := s.currentReapplyNameTagAfterMode1(ctx, session); err != nil {
		return false, evidence, err
	}
	return true, evidence, nil
}

// loginAuthoritativeMode0CarrierFacts 报告那份 mode0 载体的形状（**代码层固定事实**，
// 不重新构包、不额外打 `game-upper-current-scene-object-list-built`）：外观行 19 字节、
// 无耐久字段、无实例种子字段。它是"残缺载体"这一判决的日志凭证。
func loginAuthoritativeMode0CarrierFacts() []any {
	return []any{
		"mode0_carrier", "class0_op2_mode0_scene_object_list",
		"mode0_row_bytes", loginAuthoritativeBeforeCommitMode0RowBytes,
		"mode0_carries_durability", 0,
		"mode0_carries_instance_seed", 0,
		"mode0_row_fields", "slot_u8_itemid_u32_rawlen_u32_flags_u8_link_u32_aux_u32_auxflag_u8",
		"mode0_authority", loginAuthoritativeBeforeCommitAuthority,
	}
}

// logLoginAuthoritativeBeforeCommitOrder 打出本轮的判读事件：**这一条登录路由在 typed op24
// 之前到底按什么顺序交了哪些载体**、最后一件是不是那份完整的 31 行装备 mode1、以及开关取值。
//
// 两个档位各打自己的事件名，所以操作者在 `packet_log.txt` 里一眼就能区分
// "修复档没生效"与"我写了回退字面量"。
func (s *Service) logLoginAuthoritativeBeforeCommitOrder(
	session *gameSession,
	source string,
	characterID uint16,
	objectKey uint16,
	mode1Body []byte,
	mode1Sent bool,
	orderFixed bool,
	nameTagReplaySent bool,
	nameTagEvidence string,
	mode0AppearanceRows int,
	mode0AppearanceSource string,
) {
	event := "game-login-authoritative-before-commit-ordered"
	lastCarrier := "class0_op2_mode1_actor_equipment_create_rows"
	frames := loginAuthoritativeBeforeCommitFramesFixed
	nameTagPosition := "before_equipment_mode1"
	if !orderFixed {
		event = "game-login-authoritative-before-commit-legacy-order"
		nameTagPosition = "after_equipment_mode1"
		frames = loginAuthoritativeBeforeCommitFramesLegacy
		if nameTagReplaySent {
			// 回退档 = 今天：提交前最后一件 actor 载体就是那份残缺的 mode0 外观表。
			lastCarrier = "class0_op2_mode0_scene_object_list_appearance_table_only"
		}
	}
	if !mode1Sent {
		// 这条路由按 policy 跳过了第二份完整 mode1（`includeSecondFullMode1=false`）：
		// 本文件的顺序改动在该档下不产生任何效果，如实写出来。
		lastCarrier = "equipment_mode1_skipped_by_route_policy"
	}
	fields := []any{
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", characterID,
		"object_key", objectKey,
		"owner_channel", currentTownActorOwnerContext(session),
		"ordering_position", loginAuthoritativeBeforeCommitOrderPosition,
		"frames_before_typed_op24", frames,
		"last_actor_carrier_before_typed_op24", lastCarrier,
		"nametag_mode0_position", nameTagPosition,
		"nametag_replay_emitted", boolDigit(nameTagReplaySent),
		"nametag_slot30_prerequisite", nameTagEvidence,
		"equipment_mode1_sent", boolDigit(mode1Sent),
		"equipment_mode1_body_len", len(mode1Body),
		"equipment_mode1_msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"mode0_equipment_appearance_rows", mode0AppearanceRows,
		"mode0_equipment_appearance_source", mode0AppearanceSource,
		"scene_path_invariant", loginAuthoritativeBeforeCommitInvariant,
		"switch", loginAuthoritativeBeforeCommitSwitch(),
		"switch_env", loginAuthoritativeBeforeCommitEnv,
		"redundant_switches", loginAuthoritativeBeforeCommitRedundant,
		"row_facts_companion_event", "game-upper-selected-userinfo-mode1-equipment-create-rows",
	}
	fields = append(fields, loginAuthoritativeMode0CarrierFacts()...)
	if mode1Sent {
		createCount, slots, durability, nonzero, weapon, auditOK := loginActorGenerationMode1WireRowAudit(mode1Body)
		fields = append(fields,
			"mode1_row_walk", boolDigit(auditOK),
			"mode1_create_count", createCount,
			"actor_slot_count", len(slots),
			"actor_slots", strings.Join(slots, ","),
			"durability_carried", strings.Join(durability, ","),
			"nonzero_durability_carried", strings.Join(nonzero, ","),
		)
		fields = append(fields, weapon...)
	}
	s.logPacketEvent(event, fields...)
}

// loginAuthoritativeMode0AppearanceRows 报告本次 mode0 载体里那份装备外观表的行数
// （只读仓储，不构包、不发包）：它是"这份载体残缺"的现场数字。
func (s *Service) loginAuthoritativeMode0AppearanceRows(ctx context.Context, session *gameSession, charID uint16) (int, string) {
	rows, source, found, err := s.loadCurrentSelectedActorMode0AppearanceSummary(ctx, session, charID)
	if err != nil || !found {
		return 0, "unavailable"
	}
	return len(rows), source
}
