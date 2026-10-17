package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// The current EXE sends op237 (ENUM_CMDPACKET_TELEPORT) when 瞬间移动药剂
// (item 2600014) is used. Body layout, read off every live request body found in
// the 2026-09-25 packet logs (13-byte body, 13-byte transport header):
//
//	i16 potionSlot, i32 itemCode, u8 unused, u8 townID, u8 targetAreaID,
//	u16 clickX, u16 clickY
//
// 现场样本（body hex → 解析）：
//
//	08004eac27000026004302cc00 → slot=8 item=2600014 town=38 x=579  y=204
//	08004eac2700002600e702df00 → slot=67 item=2600014 town=38 x=743  y=223
//	08004eac270000260042032501 → slot=8 item=2600014 town=38 x=834  y=293
//	08004eac2700002700c6072901 → slot=8 item=2600014 town=39 x=1990 y=297
//	06004eac270000270074093001 → slot=6 item=2600014 town=39 x=2420 y=304
//	08004eac2700002b00b5057d03 → slot=8 item=2600014 town=43 x=1461 y=893
//	08004eac2700000b00df042501 → slot=8 item=2600014 town=11 x=1247 y=293
//	08004eac2700000c00ba012101 → slot=8 item=2600014 town=12 x=442  y=289
//
// 尾部这对 u16 就是操作者在**目标地区地图**上点的那个点。真档 PVF 里 2600014
// （stackable/stackable.lst → professional/potion/ptn_instantmovement.stk）的说明是
// 「使用后， 可以将自己的角色传送到其他地区的任意位置。」，所以请求里带点击点、
// 区域由请求显式指定、服务端按运行时PVF核对。早期实现只解析前 8 字节、把落点固定成目标城镇的 PVF
// `[gate]` 区坐标，于是"落点和点击的地方不一样"（操作者报障）。
//
// On success the server teleports the character to the clicked point on the
// explicitly selected destination map through the proven op36 teleport-array
// commit chain, consumes one potion, refreshes the bag, and returns the op237
// ACK {01, i16 slot, i32 code}. The failure ACK {00, 23} drives the client's own
// op237 error text (id 36288) — 操作者截图「对方正忙，无法对其使用该功能。」
// 就是它。无效区域或无法取得该区域落点时沿用此失败应答。
type currentTeleportPotionRequest struct {
	PotionSlotIndex int16
	ItemCode        int32
	TownID          byte
	// The current EXE writes the selected map record's area separately from town.
	TargetAreaID  int64
	HasTargetArea bool
	// ClickX/ClickY 是客户端请求里带的点击点（目标地区地图坐标）。只有完整的
	// 13 字节 body 才有；更短的旧体没有坐标，此时按目标区域自带的默认落点提交。
	ClickX        int64
	ClickY        int64
	HasClickPoint bool
}

const currentTeleportPotionErrorUnsupported byte = 23

// currentTeleportPotionLandingDecision 是提交给 op36 的"目标区域 + 落点"成对决定。
// 区域与坐标必须来自同一张图，完整请求保留客户端明确选中的地图。
type currentTeleportPotionLandingDecision struct {
	Area dnftown.Area
	X, Y int64
	// PositionSource 是坐标来源（client_click_point / target_area_gate /
	// target_area_default_position / town_declared_position_fallback /
	// character_current_position_fallback / zero_fallback / pvf_gate_legacy）。
	PositionSource string
	// AreaSource 见 currentTeleportPotionAreaSource* 常量。
	AreaSource string
	// Reason 只在这次走的不是默认判据时非空（逐字节回退档 / gate 回退档）。
	Reason string
	// BoundarySignals 是 area 0 被判成边界门图时命中的信号，取证用。
	BoundarySignals string
}

// 落点策略开关（默认 = 修好后的行为，回退值见下）：
//
//	DNFBRIDGE_TELEPORT_POTION_LANDING
//	  ""/"region-main-click"（默认）：完整请求使用显式区域；旧请求的目标区域按地图/区域语义解析为
//	    城镇主图**（dnftown.Table.FindPrincipalArea），落点 = 客户端点击点。
//	    area 0 本来就不是城镇边界门图的城镇（真档 38/53）解析结果就是 area 0，
//	    与改动前逐字节一致；area 0 是边界门图的城镇（真档 15/53，例：town 31 中央公园
//	    area 0 = `Town/CentralPark/Gate_CentralPark.map`、主街在 area 1 =
//	    `Town/CentralPark/CentralPark.map`）改取主城区 —— 操作者报障的假死就是
//	    "把主街点击点提交给 Gate_ 边界图"造成的。解析不出来时逐字节回退到下面这条旧链。
//	  "gate"（回退值）：修好前的行为 —— 目标区域 = PVF `[gate]` 区，落点 = 门坐标，
//	    忽略请求里的点击点。
const (
	currentTeleportPotionLandingEnv     = "DNFBRIDGE_TELEPORT_POTION_LANDING"
	currentTeleportPotionLandingDefault = "region-main-click"
	currentTeleportPotionLandingLegacy  = "gate"
)

// 区域来源取值（进事件日志 landing_area_source=，供线上按档核对落点判据）：
//
//	main_area_document_order       area 0 不是边界门图 ⇒ 就是它（改动前的结果）
//	main_area_after_boundary_gate  area 0 是边界门图 ⇒ 按语义改取主城区（本修复）
//	legacy_area_kept               主城区解析不出来 ⇒ 逐字节回退到改动前的选择
//	legacy_gate_mode               DNFBRIDGE_TELEPORT_POTION_LANDING=gate 回退档
const (
	currentTeleportPotionAreaSourceDocumentOrder = "main_area_document_order"
	currentTeleportPotionAreaSourceAfterGate     = "main_area_after_boundary_gate"
	currentTeleportPotionAreaSourceLegacyKept    = "legacy_area_kept"
	currentTeleportPotionAreaSourceLegacyGate    = "legacy_gate_mode"
	currentTeleportPotionAreaSourceRequestedArea = "client_requested_area"
)

// 区域门槛开关（默认 = 修好后的行为，回退值见下）：
//
//	DNFBRIDGE_TELEPORT_POTION_AREA_GATES
//	  ""/"relaxed"（默认）：道具传送不套用目标区域的 `[need level]`/`[need quest]`。
//	    依据是道具自己的 PVF 说明"传送到其他地区的任意位置"：客户端列出的地区已经
//	    按同一份 PVF 过滤过，区域门槛是给"走门进图"那条链路用的；套在道具上会让
//	    操作者看到"点了没反应"（op36 被静默拒绝，药水不消耗、客户端也没有回应）。
//	  "strict"（回退值）：修好前的行为 —— 按 PVF 区域门槛拒绝。
const (
	currentTeleportPotionAreaGatesEnv     = "DNFBRIDGE_TELEPORT_POTION_AREA_GATES"
	currentTeleportPotionAreaGatesRelaxed = "relaxed"
	currentTeleportPotionAreaGatesStrict  = "strict"
)

func currentTeleportPotionLandingMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentTeleportPotionLandingEnv))) {
	case currentTeleportPotionLandingLegacy, "legacy", "legacy-gate":
		return currentTeleportPotionLandingLegacy
	default:
		return currentTeleportPotionLandingDefault
	}
}

func currentTeleportPotionAreaGatesMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentTeleportPotionAreaGatesEnv))) {
	case currentTeleportPotionAreaGatesStrict, "pvf", "legacy":
		return currentTeleportPotionAreaGatesStrict
	default:
		return currentTeleportPotionAreaGatesRelaxed
	}
}

func parseCurrentTeleportPotionRequest(body []byte) (currentTeleportPotionRequest, error) {
	if len(body) < 8 {
		return currentTeleportPotionRequest{}, fmt.Errorf("current teleport potion body length %d, want >= 8", len(body))
	}
	request := currentTeleportPotionRequest{
		PotionSlotIndex: int16(binary.LittleEndian.Uint16(body[0:2])),
		ItemCode:        int32(binary.LittleEndian.Uint32(body[2:6])),
		TownID:          body[7],
	}
	// 完整现场 body 13 字节：body[8]=0（townID 的高字节），body[9:11]=clickX，
	// body[11:13]=clickY。点击点 (0,0) 视为"没带坐标"（城镇地图上没有这样的可点位置，
	// 用区域默认落点更安全）。
	if len(body) >= 13 {
		request.TargetAreaID = int64(body[8])
		request.HasTargetArea = true
		clickX := int64(binary.LittleEndian.Uint16(body[9:11]))
		clickY := int64(binary.LittleEndian.Uint16(body[11:13]))
		if clickX != 0 || clickY != 0 {
			request.ClickX = clickX
			request.ClickY = clickY
			request.HasClickPoint = true
		}
	}
	return request, nil
}

func (s *Service) handleTeleportPotion(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	request, err := parseCurrentTeleportPotionRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-town-teleport-potion-blocked",
			"body_len", len(body),
			"reason", "current_exe_op237_writer_boundary_mismatch",
			"error", err)
		return nil
	}
	landingMode := currentTeleportPotionLandingMode()
	s.logGameEvent(session, "game-town-teleport-potion-request",
		"char_id", session.selectedCharacterID,
		"item_code", request.ItemCode,
		"potion_slot", request.PotionSlotIndex,
		"town_id", request.TownID,
		"request_area_id", request.TargetAreaID,
		"has_request_area", request.HasTargetArea,
		"has_click_point", request.HasClickPoint,
		"click_x", request.ClickX,
		"click_y", request.ClickY,
		"landing_mode", landingMode,
		"body_len", len(body))
	if session.selectedCharacterID == 0 {
		return s.sendCurrentTeleportPotionFailure(session, request, "selected_character_missing")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return s.sendCurrentTeleportPotionFailure(session, request, "runtime_pvf_catalog_unavailable")
	}
	if !currentTeleportPotionIsValid(catalog, request.ItemCode) {
		return s.sendCurrentTeleportPotionFailure(session, request, "item_is_not_a_pvf_teleport_potion")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterItems == nil || repositories.Character == nil {
		return s.sendCurrentTeleportPotionFailure(session, request, "character_item_repository_unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.Stats == nil {
		return s.sendCurrentTeleportPotionFailure(session, request, "selected_character_location_missing")
	}
	currentTown, ok := character.Stats["town_id"]
	if !ok || currentTown < 0 {
		return s.sendCurrentTeleportPotionFailure(session, request, "selected_character_town_id_missing")
	}
	// 目标区域 + 落点：**成对**解析（区域与坐标必须来自同一张图）。
	// 默认档按地图/区域语义取"该地区真正的城镇主图"，解析不出来时逐字节回退到
	// 改动前的选择（见 currentTeleportPotionLandingDecision 的注释）。
	landing, landingFailure := s.currentTeleportPotionLandingDecision(request, character)
	if landingFailure != "" {
		return s.sendCurrentTeleportPotionFailure(session, request, landingFailure)
	}
	landingArea := landing.Area
	landingX, landingY, landingSource := landing.X, landing.Y, landing.PositionSource
	potionKey, err := currentTeleportPotionSlot(ctx, repositories, characterID, request)
	if err != nil {
		return s.sendCurrentTeleportPotionFailure(session, request, "teleport_potion_missing_from_main_inventory")
	}
	potionList, potionSlot, slotOK := parseSceneInventorySlotKey(potionKey)
	if !slotOK || potionList != 0 || potionSlot < 0 {
		return s.sendCurrentTeleportPotionFailure(session, request, "teleport_potion_slot_not_in_main_inventory")
	}

	// Reuse the proven op36 teleport-array commit chain: the resolved area + the
	// clicked point with the source-town teleport shape, which emits the current
	// op24 transition, persists the new town position, and unlocks PVF
	// need-quests.
	relaxAreaGates := currentTeleportPotionAreaGatesMode() == currentTeleportPotionAreaGatesRelaxed
	commitErr := func() error {
		if !relaxAreaGates {
			return s.handleTownSetUserAreaWithQuestNotice(session, buildCurrentTeleportPotionMoveBody(request.TownID, landingArea.ID, landingX, landingY, currentTown), false)
		}
		// 只在这一次提交期间放宽目标区域的 [need level]/[need quest]，defer 保证
		// 无论提交成功、被拒还是出错都立刻复位，绝不留给后续的普通 op36。
		session.townMu.Lock()
		session.townTeleportPotionLandingRelaxed = true
		session.townMu.Unlock()
		defer func() {
			session.townMu.Lock()
			session.townTeleportPotionLandingRelaxed = false
			session.townMu.Unlock()
		}()
		return s.handleTownSetUserAreaWithQuestNotice(session, buildCurrentTeleportPotionMoveBody(request.TownID, landingArea.ID, landingX, landingY, currentTown), false)
	}()
	if commitErr != nil {
		return commitErr
	}
	moved, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || moved.Stats["town_id"] != int64(request.TownID) || moved.Stats["area_id"] != landingArea.ID {
		return s.sendCurrentTeleportPotionFailure(session, request, "town_transition_not_committed")
	}
	if err := consumeCurrentTeleportPotion(ctx, repositories, characterID, potionKey); err != nil {
		return err
	}
	// Only the consumed potion changed. Replaying full op13 containers in the
	// live destination scene rebuilds unrelated item objects while peer actors
	// are arriving. The v85 live trace then sent an unflagged worn-slot claim
	// 250ms later and the domain actually unequipped the weapon. Use the same
	// repository-backed op14 changed-row path as ordinary item consumption.
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "teleport_potion_consumed_after_op24",
		[]alignedcmd.ItemSlotRefresh{{ListType: potionList, SlotIndex: potionSlot}}); err != nil {
		return err
	}
	s.logGameEvent(session, "game-town-teleport-potion-committed",
		"char_id", session.selectedCharacterID,
		"item_code", request.ItemCode,
		"potion_slot", potionKey,
		"town_id", request.TownID,
		"area_id", landingArea.ID,
		"position_x", landingX,
		"position_y", landingY,
		"map_path", landingArea.MapPath,
		"has_click_point", request.HasClickPoint,
		"click_x", request.ClickX,
		"click_y", request.ClickY,
		"landing_source", landingSource,
		"landing_area_source", landing.AreaSource,
		"boundary_signals", landing.BoundarySignals,
		"reason", landing.Reason,
		"landing_mode", landingMode,
		"area_gates", currentTeleportPotionAreaGatesMode())
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketTeleport), buildCurrentTeleportPotionSuccessAck(request), dnfproto.DefaultChannelClassification)
}

// currentTeleportPotionLandingDecision 返回"目标区域 + 落点"的**成对**决定，
// 第二个返回值非空时表示要按老口径发失败包（{00, 23}），一个字节都不提交。
//
// 为什么必须成对：客户端的点击点是**画在目标地区的某一张图上**算出来的。服务端把
// 这个点提交给另一张图，客户端就拿到一个在该图非法的坐标 —— 现场症状正是
// 「角色模型消失、可以交互 NPC、但不能移动」（画不出 actor，位置校验不过）。
//
// 判据与来源（真档 Script.pvf sha256 cc517a6e2b7c77a4ba0f30a1869a3ab234e281fe6251e35d60f0f04a82aacee1，
// 2026-09-27 全量扫描，53 个城镇）：
//
// 完整请求按当前EXE发送的区域选择落点，不以主城区覆盖显式区域。
// 缺失区域字段的旧8字节请求继续使用主城区/入口回退链；gate开关保留原行为。
// 默认位置必须属于选中的区域，不能借用另一张地图的入口坐标。
func (s *Service) currentTeleportPotionLandingDecision(request currentTeleportPotionRequest, character dnfrepo.CharacterRecord) (currentTeleportPotionLandingDecision, string) {
	townID := int64(request.TownID)
	if currentTeleportPotionLandingMode() == currentTeleportPotionLandingLegacy {
		area, found := s.townGateArea(townID)
		if !found || strings.TrimSpace(area.MapPath) == "" {
			return currentTeleportPotionLandingDecision{}, "target_town_landing_area_missing_from_runtime_pvf"
		}
		if area.Gate == nil {
			return currentTeleportPotionLandingDecision{}, "target_town_gate_area_missing_from_runtime_pvf"
		}
		return currentTeleportPotionLandingDecision{
			Area:           area,
			X:              area.Gate.X,
			Y:              area.Gate.Y,
			PositionSource: "pvf_gate_legacy",
			AreaSource:     currentTeleportPotionAreaSourceLegacyGate,
			Reason:         "landing_mode_gate_switch",
		}, ""
	}
	// Current EXE: hit-test 0x2510c80 returns the selected map record's
	// town/area and transformed coordinates; 0x1f40160 stores that tuple;
	// 0x248b268 writes it as u8 town, u8 area, u16 x, u16 y.
	// Coordinates in different maps may overlap. Never infer area from them
	// or silently substitute the principal map for an explicit selection.
	if request.HasTargetArea {
		area, found := s.townArea(townID, request.TargetAreaID)
		if !found || strings.TrimSpace(area.MapPath) == "" {
			return currentTeleportPotionLandingDecision{}, "requested_area_missing_from_runtime_pvf"
		}
		if !request.HasClickPoint && area.Gate == nil && area.DefaultPosition == nil {
			return currentTeleportPotionLandingDecision{}, "requested_area_has_no_declared_position"
		}
		x, y, source := s.currentTeleportPotionLandingPosition(request, area, character)
		return currentTeleportPotionLandingDecision{
			Area: area, X: x, Y: y, PositionSource: source,
			AreaSource: currentTeleportPotionAreaSourceRequestedArea,
		}, ""
	}
	// Legacy requests have no area field. Preserve their existing selection.
	legacyArea, legacyFound := s.townTeleportPotionLandingArea(townID)
	principalArea, principalFound := s.townTeleportPotionPrincipalArea(townID)
	boundarySignals := s.townTeleportPotionBoundarySignals(townID)
	if !principalFound {
		if !legacyFound || strings.TrimSpace(legacyArea.MapPath) == "" {
			return currentTeleportPotionLandingDecision{}, "target_town_landing_area_missing_from_runtime_pvf"
		}
		x, y, source := s.currentTeleportPotionLandingPosition(request, legacyArea, character)
		return currentTeleportPotionLandingDecision{
			Area:            legacyArea,
			X:               x,
			Y:               y,
			PositionSource:  source,
			AreaSource:      currentTeleportPotionAreaSourceLegacyKept,
			Reason:          "principal_area_missing_from_runtime_pvf",
			BoundarySignals: boundarySignals,
		}, ""
	}
	if !legacyFound || principalArea.ID == legacyArea.ID {
		// area 0 本来就不是城镇边界门图 ⇒ 逐字节等于改动前（含点击点链路）。
		x, y, source := s.currentTeleportPotionLandingPosition(request, principalArea, character)
		return currentTeleportPotionLandingDecision{
			Area:           principalArea,
			X:              x,
			Y:              y,
			PositionSource: source,
			AreaSource:     currentTeleportPotionAreaSourceDocumentOrder,
		}, ""
	}
	// 本修复：area 0 是边界门图，主城区是另一个区域。
	if request.HasClickPoint {
		return currentTeleportPotionLandingDecision{
			Area:            principalArea,
			X:               request.ClickX,
			Y:               request.ClickY,
			PositionSource:  "client_click_point",
			AreaSource:      currentTeleportPotionAreaSourceAfterGate,
			BoundarySignals: boundarySignals,
		}, ""
	}
	if principalArea.Gate != nil {
		return currentTeleportPotionLandingDecision{
			Area:            principalArea,
			X:               principalArea.Gate.X,
			Y:               principalArea.Gate.Y,
			PositionSource:  "target_area_gate",
			AreaSource:      currentTeleportPotionAreaSourceAfterGate,
			BoundarySignals: boundarySignals,
		}, ""
	}
	if principalArea.DefaultPosition != nil {
		return currentTeleportPotionLandingDecision{
			Area:            principalArea,
			X:               principalArea.DefaultPosition.X,
			Y:               principalArea.DefaultPosition.Y,
			PositionSource:  "target_area_default_position",
			AreaSource:      currentTeleportPotionAreaSourceAfterGate,
			BoundarySignals: boundarySignals,
		}, ""
	}
	// 主城区自己没声明落点、请求也没带点击点 ⇒ 不发明坐标：保持改动前那一对。
	x, y, source := s.currentTeleportPotionLandingPosition(request, legacyArea, character)
	return currentTeleportPotionLandingDecision{
		Area:            legacyArea,
		X:               x,
		Y:               y,
		PositionSource:  source,
		AreaSource:      currentTeleportPotionAreaSourceLegacyKept,
		Reason:          "main_area_declares_no_landing_position_and_request_has_no_click_point",
		BoundarySignals: boundarySignals,
	}, ""
}

// currentTeleportPotionLandingPosition 解析提交给 op36 的落点（**改动前的逐级回退链**，
// 顺序与取值一字未改，供"逐字节回退"档共用）。
//
// 优先客户端点击点 —— 这就是"点哪落哪"；请求不带坐标（旧体/回城类）时才用目标
// 区域自带的默认落点，并且逐级回退，绝不因为目标城镇没有 `[gate]` 区而硬失败。
//
// 注意：这条链的 town_declared_position_fallback 可能返回**别的区域**声明过的坐标
// （真档 town 31 主街 area 1 不声明坐标，能拿到的只有边界图 area 0 的 `[gate]` 474 234）。
// 默认档只在"主城区解析不出来"或"请求没带点击点"时才会走到这里，此时它与改动前的
// 行为逐字节一致；若要把它当成主城区的默认落点用，必须先在档里找到该图的落点声明。
func (s *Service) currentTeleportPotionLandingPosition(request currentTeleportPotionRequest, area dnftown.Area, character dnfrepo.CharacterRecord) (int64, int64, string) {
	if request.HasClickPoint {
		return request.ClickX, request.ClickY, "client_click_point"
	}
	if area.Gate != nil {
		return area.Gate.X, area.Gate.Y, "target_area_gate"
	}
	if area.DefaultPosition != nil {
		return area.DefaultPosition.X, area.DefaultPosition.Y, "target_area_default_position"
	}
	if x, y, ok := s.townFirstDeclaredPosition(int64(request.TownID)); ok {
		return x, y, "town_declared_position_fallback"
	}
	if character.Stats != nil {
		posX, hasX := character.Stats["pos_x"]
		posY, hasY := character.Stats["pos_y"]
		if hasX && hasY {
			return posX, posY, "character_current_position_fallback"
		}
	}
	return 0, 0, "zero_fallback"
}

func currentTeleportPotionIsValid(catalog *pvfDungeonDropCatalog, itemCode int32) bool {
	if catalog == nil || itemCode <= 0 {
		return false
	}
	definition, err := catalog.ResolveItem(uint32(itemCode))
	if err != nil || definition.Kind != dungeonDropItemStackable {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(definition.StackableType), "[teleport potion]")
}

// currentTeleportPotionSlot verifies the claimed slot first, then falls back
// to an id-based scan, matching the 86JP slot-mismatch fallback.
func currentTeleportPotionSlot(ctx context.Context, repositories dnfrepo.Group, characterID string, request currentTeleportPotionRequest) (string, error) {
	var matched string
	err := repositories.CharacterItems.WithinCharacterItems(ctx, characterID, func(inventory dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		record, found, err := inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("inventory record missing: %s", characterID)
		}
		claimed := fmt.Sprintf("0:%d", request.PotionSlotIndex)
		if stack, ok := record.Slots[claimed]; ok && stack.Count > 0 && stack.ItemID == int64(request.ItemCode) {
			matched = claimed
			return nil
		}
		keys := make([]string, 0, len(record.Slots))
		for key, stack := range record.Slots {
			list, slot, valid := parseSceneInventorySlotKey(key)
			if valid && list == 0 && slot >= 0 && stack.ItemID == int64(request.ItemCode) && stack.Count > 0 {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			return fmt.Errorf("teleport potion %d not found", request.ItemCode)
		}
		for _, key := range keys {
			if matched == "" || key < matched {
				matched = key
			}
		}
		return nil
	})
	return matched, err
}

func consumeCurrentTeleportPotion(ctx context.Context, repositories dnfrepo.Group, characterID string, potionKey string) error {
	return repositories.CharacterItems.WithinCharacterItems(ctx, characterID, func(inventory dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		record, found, err := inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("inventory record missing: %s", characterID)
		}
		record = dnfrepo.CloneInventory(record)
		stack, ok := record.Slots[potionKey]
		if !ok || stack.Count <= 0 {
			return fmt.Errorf("teleport potion slot %s empty", potionKey)
		}
		stack.Count--
		if stack.Count <= 0 {
			delete(record.Slots, potionKey)
		} else {
			if stack.Extra == nil {
				stack.Extra = make(map[string]string, 4)
			}
			stack.Extra["amount_or_count"] = strconv.FormatInt(stack.Count, 10)
			stack.Extra["amount"] = strconv.FormatInt(stack.Count, 10)
			if len(stack.RawEntry) == currentItemListEntryWireSize {
				stack.RawEntry = append([]byte(nil), stack.RawEntry...)
				value := uint32(stack.Count)
				stack.RawEntry[0x06] = byte(value)
				stack.RawEntry[0x07] = byte(value >> 8)
				stack.RawEntry[0x08] = byte(value >> 16)
				stack.RawEntry[0x09] = byte(value >> 24)
			}
			record.Slots[potionKey] = stack
		}
		record.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveInventoryFields(ctx, inventory, record, dnfrepo.InventoryFieldSlots)
	})
}

func buildCurrentTeleportPotionMoveBody(townID byte, areaID int64, positionX int64, positionY int64, sourceTownID int64) []byte {
	body := make([]byte, currentTownSetUserAreaBodySize)
	body[0] = townID
	body[1] = byte(areaID)
	binary.LittleEndian.PutUint16(body[2:4], uint16(positionX))
	binary.LittleEndian.PutUint16(body[4:6], uint16(positionY))
	body[6] = 0
	// opaqueA = source town: required by the current-EXE teleport-array shape
	// (LooksLikeTeleportArraySelection), which admits PVF-visible quest-gated
	// and cross-town targets and unlocks need-quests on commit.
	binary.LittleEndian.PutUint16(body[7:9], uint16(sourceTownID))
	body[15] = 5
	return body
}

func buildCurrentTeleportPotionSuccessAck(request currentTeleportPotionRequest) []byte {
	return []byte{
		1,
		byte(request.PotionSlotIndex), byte(request.PotionSlotIndex >> 8),
		byte(request.ItemCode), byte(request.ItemCode >> 8), byte(request.ItemCode >> 16), byte(request.ItemCode >> 24),
	}
}

func (s *Service) sendCurrentTeleportPotionFailure(session *gameSession, request currentTeleportPotionRequest, reason string) error {
	s.logGameEvent(session, "game-town-teleport-potion-blocked",
		"char_id", session.selectedCharacterID,
		"item_code", request.ItemCode,
		"town_id", request.TownID,
		"reason", reason)
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketTeleport), []byte{0, currentTeleportPotionErrorUnsupported}, dnfproto.DefaultChannelClassification)
}
