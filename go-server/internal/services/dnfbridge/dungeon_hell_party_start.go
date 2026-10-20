package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"接受客户端 op235「深渊派对开始」"（`DNFBRIDGE_HELL_PARTY_START_CMD`）。
//
// ── 厂商口径 ───────────────────────────────────────────────────────────────
//
// 「深渊派对开始」与分解机请求**共用同一个 opcode**：
// `Network\Core\PacketTypesA21.cs:1466 REQUEST_DISJOINT_ITEM = 0x00EB /* 235 */`。
// 分派在 `Network\Protocol\GameProtocolHandler.cs:970-978`：
//
//	private Task HandleSharedDisjointOrHellParty(session, header, body)
//	    => _expertJobStoreHandler.HasEnteredStore(session)
//	        ? _expertJobStoreHandler.HandleDisjoint(session, header, body)
//	        : _dungeonHandler.Handle_ENUM_CMDPACKET_HELLPARTY_START(session, header, body);
//
// `DungeonMapHandler.cs:944-963 HandleHellPartyStart` 只做一件事：
//
//	if (!TryGetCurrentRoomState(session, out var roomState) || !roomState.IsHellPartyRoom) { 记录 ignored; return; }
//	if (roomState.HellPartyPhase == HellPartyPhase.WaitingStart) roomState.HellPartyPhase = HellPartyPhase.Started;
//	else 记录 ignored: phase=…;
//	**不回任何包**。
//
// 本仓现状：op235 只登记在 expert-job 模块（`gameplay_module_expert_job.go:130`
// `registerStore(uint16(dnfenum.CmdPacketRequestDisjointItem), (*Service).handleCurrentExpertJobStoreDisjoint)`），
// 不在分解机里就回一条 `sendGameUpperFailure(235, 19)`；线上全库 `type=235` = 0 次
// （客户端在"手里没有深渊怪物"时压根不会发这一条）。
//
// ── 开关 ──────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_PARTY_START_CMD
//
//	accept（默认，= 修复档）：当前房间是**带波次的深渊房间**时，op235 按
//	                        「深渊派对开始」收下（记一次状态 + 日志，不回包），
//	                        其余一切情况（不在深渊房间 / 波次为空）**原样落到**
//	                        既有的分解机分支，行为逐字节等于今天。
//	legacy（一键回退）     ：完全不接管，任何情况都走既有分解机分支。
//
// 默认 = 修复档（判据取 `== legacy`）。
const hellPartyStartEventAccepted = "game-dungeon-hell-party-start-accepted"

// handleCurrentExpertJobStoreDisjointOrHellPartyStart 是 ok235 的分派入口，逐条对应
// 厂商 `GameProtocolHandler.cs:970-978` 的 `HandleSharedDisjointOrHellParty`：
// 不在分解机状态时按「深渊派对开始」处理，其余情况交给既有的分解机 handler。
//
// 只替换了"在深渊房间里"这一条判据的落点：本仓的"是否在分解机里"由既有
// `handleCurrentExpertJobStoreDisjoint` 自己判（`currentExpertJobEnteredStore`），
// 所以这里**先**问深渊房间，答不上来就原样委派。
func (s *Service) handleCurrentExpertJobStoreDisjointOrHellPartyStart(
	session *gameSession,
	opcode uint16,
	body []byte,
) error {
	if handled, err := s.acceptCurrentHellPartyStartCommand(session, opcode, body); handled || err != nil {
		return err
	}
	return s.handleCurrentExpertJobStoreDisjoint(session, opcode, body)
}

// acceptCurrentHellPartyStartCommand 报告这条 op235 是否被当成「深渊派对开始」收下。
// 返回 (true, nil) 表示**已经处理完、不产生任何回包**（厂商同）。
func (s *Service) acceptCurrentHellPartyStartCommand(
	session *gameSession,
	opcode uint16,
	body []byte,
) (bool, error) {
	if s == nil || session == nil {
		return false, nil
	}
	if !hellPartyStartCommandModeFromEnv().accepts() {
		return false, nil
	}
	if opcode != a21CmdHellPartyStart {
		return false, nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		return false, nil
	}
	// 厂商判据 `roomState.IsHellPartyRoom`：本仓的等价物 = 这条 runtime 冻结了深渊房间，
	// **并且**这张房图上真的解析出了波次（没有波次的"深渊房间"收下它毫无意义，
	// 那种情况必须原样落到分解机分支，保证回退/无关路径逐字节不变）。
	if !runtime.HellRoomSet || len(runtime.HellPartyWaves) == 0 {
		return false, nil
	}
	scene, hasScene := runtime.Session.Scene()
	if !hasScene || scene.Map.Map.ID != runtime.HellRoom.MapID {
		return false, nil
	}
	duplicate := runtime.HellPartyStarted
	runtime.HellPartyStarted = true
	s.logGameEvent(session, hellPartyStartEventAccepted,
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"msg_id", opcode,
		"body_len", len(body),
		"duplicate", duplicate,
		"waves", len(runtime.HellPartyWaves),
		"difficulty", runtime.HellPartyDifficulty,
		"phase", "started",
		"switch", hellPartyStartCommandModeFromEnv().String(),
		"response", "none_vendor_DungeonMapHandler_cs_944_963_HellPartyStart_sends_no_packet")
	return true, nil
}

// currentHellPartyMapIsHellRoom 报告"这张地图 id"是不是本次冻结的深渊房间地图
// （厂商 `roomState.IsHellPartyRoom` 在本仓的等价判据）。
func currentHellPartyMapIsHellRoom(hellRoom dungeonHellRoom, hellRoomSet bool, mapID int64) bool {
	return hellRoomSet && mapID == hellRoom.MapID
}

// currentHellPartySceneIsHellRoom 是上一条对房间场景的包装（供 START_MAP / NOTI 落点判据复用）。
func currentHellPartySceneIsHellRoom(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) bool {
	if runtime == nil {
		return false
	}
	return currentHellPartyMapIsHellRoom(runtime.HellRoom, runtime.HellRoomSet, scene.Map.Map.ID)
}
