package dnfbridge

import "encoding/binary"

// currentTownWorldTownContextNotificationMsgID 是当前客户端注册在 sub_1D9332E 的
// class0 通知号。sub_1D82120 读一个 i32（LE）并把它交给世界地图上下文的
// sub_3406800（缓存到 WorldMapContext+38C）。
//
// **这不是**同号的 C2S CmdPacket 枚举项（dnfenum.CmdPacketSizukiArenaSeason2EventUniv
// = 1375），两者只是数字相同、互不相关。
const currentTownWorldTownContextNotificationMsgID uint16 = 1375

// 世界城镇上下文通知（class0/op1375）的口径开关。**默认 = 修复档**（发送）。
//
// 为什么需要这一条：
//   - op24 的读帧本身也会调用 sub_3406800，但该 setter 对标记了
//     `[exclude user pretown info]` 的城镇**故意不覆盖**已有缓存。
//   - 当前 PVF 的城镇 38（`town/new_Elvengard.twn`，即共用登录房间/赛丽亚房间所在城镇）
//     的第一行就是 `[exclude user pretown info]`；城镇 39/40
//     （`new_HendonMyre.twn` / `new_WestCoast.twn`）没有这个标记。
//     证据：`pvfsearch.exe -read town/new_Elvengard.twn` 原文首行。
//   - 于是"普通进房间"会保留此前在外城的缓存；而**冷登录**时缓存里什么都没有，
//     必须由服务端先把角色已验证的"上一个外城"送进去。
//
// 数据来源被刻意限制：只用 session.townPrevVillageSnapshot —— 一份**已验证、属于本角色**
// 的上一村镇快照（currentTownPrevVillageOriginIsPersistable：必须坐标有效、城镇/区域能在
// 运行时 PVF 里查到、且不是赛丽亚房间）。快照不可用时**什么都不发**，绝不编造默认值或
// 借用别的角色的城镇。
//
//	DNFBRIDGE_INITIAL_TOWN_SERIA_WORLD_TOWN
//	  ""（默认）/ 任何其它值：**修复档**，冷登录/重连进赛丽亚房间前补发这条 i32 通知。
//	  "legacy"/"off"/"0"/"false"/"no"：**回滚档**，完全不发这条通知 —— 逐字节回到修复前
//	    的下发序列（noti16 → noti17 → op24），仅用于实机 A/B 对照。
//
// **诚实的边界（必须一起读）**：同血统 2026-09-24 归档的 PROTOCOL_INDEX.md:257 记录，
// 这一条通知本身**没有**让 UI 验收通过 —— "v3 verifies correct cache40 but wrong actual
// minimap region18 in cold/manual room entry; notification alone failed UI acceptance."
// 后续围绕小地图 region 的 v10–v12 工作在归档里是 "v11 cold rejected; v12 live
// visual/production acceptance pending"，**从未落地**。
// 也就是说：这一条修的是**原生缓存的归属城镇上下文**（可证伪：缓存应变成外城号），
// 不是"小地图一定正确"。不要拿它去承诺小地图验收。
const currentTownSeriaWorldTownEnv = "DNFBRIDGE_INITIAL_TOWN_SERIA_WORLD_TOWN"

// currentTownSeriaWorldTownEnabled 每次调用都读环境变量（与同包其它城镇开关一致，
// 档位判定复用 town_copresence_actor_resend.go 的 currentTownPresencePublishDisabled，
// 回退字面量与全仓其它开关保持同一组）。
func currentTownSeriaWorldTownEnabled() bool {
	return !currentTownPresencePublishDisabled(currentTownSeriaWorldTownEnv)
}

// currentTownSeriaWorldTownMode 是判读事件/测试用的档位串。
func currentTownSeriaWorldTownMode() string {
	if currentTownSeriaWorldTownEnabled() {
		return "native_i32_previous_town"
	}
	return "legacy_disabled"
}

// sendCurrentInitialTownSeriaWorldTownContextLocked 在共享赛丽亚房间被 op24 构造之前
// 初始化它的归属城镇上下文。
//
// 移植自同血统 2026-09-24 归档（`_dsh_tmp/newsrc/server/DNF90-source-oneclick`）的
// go-server/internal/services/dnfbridge/initial_town_seria_world_town.go（2259 B / 55 行）。
// 归档 PROTOCOL_INDEX.md:257 的条目：
//
//	**class0** `op1375` / `0x55F` | Current native world-map owning-town context,
//	one LE i32 | Registered reader 01D82120 calls setter 03406800, caching context+38C.
//	Op24 also calls this setter, but town38 has `[exclude user pretown info]` and
//	preserves the cache. Cold Seria login sends validated previous town after
//	op22/op23 and before actual room38/1 op24. This is NOT the unrelated C2S1375
//	event command or an actor/area refresh.
//
// 这条通知**不改变实际场景，也不改变受控 actor**：它只写世界地图上下文的归属城镇缓存。
// 调用方持有 session.townMu，并拥有"初始位置通知"的一次性检查点 —— 所以本函数返回
// error 时，调用方**不能**推进检查点，否则重试会丢掉这条通知。
func (s *Service) sendCurrentInitialTownSeriaWorldTownContextLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
) error {
	if !currentTownSeriaWorldTownEnabled() {
		return nil
	}
	if session == nil || characterID == 0 || session.selectedCharacterID != characterID {
		return nil
	}
	room, found := s.townArea(int64(townID), int64(areaID))
	if !found || !townMapPathLooksLikeSeria(room.MapPath) {
		return nil
	}
	origin := session.townPrevVillageSnapshot
	if origin.CharacterID != characterID {
		return nil
	}
	if _, valid := s.currentTownPrevVillageOriginIsPersistable(origin); !valid {
		return nil
	}
	// 数据源是那份已验证、属于本角色的上一村镇快照（与"出房间返回"共用同一份）。
	// 快照不可用时绝不用默认值补齐，也不借用别的角色的城镇。
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, uint32(origin.TownID))
	if err := s.sendCurrentSceneFixedClass0Packet(session,
		currentTownWorldTownContextNotificationMsgID, body,
		"initial_town_seria_world_town_current_exe_sub_1D82120"); err != nil {
		return err
	}
	s.logGameEvent(session, "game-initial-town-seria-world-town-sent",
		"char_id", characterID,
		"room_town_id", townID,
		"room_area_id", areaID,
		"world_town_id", origin.TownID,
		"world_town_source", "validated_character_prev_village",
		"world_town_mode", currentTownSeriaWorldTownMode(),
		"sequence", "initial_op22_op23_then_noti1375_world_town_then_room_op24")
	return nil
}
