package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentTownSetUserPositionBodySize = 7

var errCurrentDungeonTownReturnOriginUnavailable = errors.New("current dungeon town-return origin is unavailable")

// currentTownSetUserPositionRequest is the exact plaintext written by the
// current EXE's sub_23E0660. Only the first two u16 values are proven to be
// the actor's town coordinates. The final u8/u16 remain deliberately opaque.
type currentTownSetUserPositionRequest struct {
	PositionX       uint16
	PositionY       uint16
	MovementCode    byte
	OpaqueScaledU16 uint16
}

// currentTownPositionSnapshot is session-owned. The repository remains the
// durable area owner; high-frequency op35 reports are not written to the DB.
// A snapshot can be reused only for the exact character/town/area scene that
// established it.
type currentTownPositionSnapshot struct {
	CharacterID     uint16
	TownID          byte
	AreaID          byte
	PositionX       uint16
	PositionY       uint16
	MovementCode    byte
	OpaqueScaledU16 uint16
	PositionValid   bool
}

func parseCurrentTownSetUserPositionRequest(body []byte) (currentTownSetUserPositionRequest, error) {
	if len(body) != currentTownSetUserPositionBodySize {
		return currentTownSetUserPositionRequest{}, fmt.Errorf(
			"current town op35 body length %d, want %d",
			len(body),
			currentTownSetUserPositionBodySize,
		)
	}
	return currentTownSetUserPositionRequest{
		PositionX:       binary.LittleEndian.Uint16(body[0:2]),
		PositionY:       binary.LittleEndian.Uint16(body[2:4]),
		MovementCode:    body[4],
		OpaqueScaledU16: binary.LittleEndian.Uint16(body[5:7]),
	}, nil
}

func (s *Service) handleTownSetUserPosition(session *gameSession, body []byte) error {
	s.pvpMu.Lock()
	inPVPRoom := session != nil && session.pvpRoom != nil
	s.pvpMu.Unlock()
	if inPVPRoom {
		return nil
	}
	if session == nil {
		return nil
	}
	request, err := parseCurrentTownSetUserPositionRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-town-set-user-position-blocked",
			"body_len", len(body),
			"reason", "current_exe_op35_writer_boundary_mismatch",
			"error", err)
		return nil
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		s.logTownSetUserPositionBlocked(session, request, "selected_character_missing")
		return nil
	}

	if err := s.commitPendingDungeonReturnBeforeTownPosition(session, request); err != nil {
		return err
	}

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime != nil {
		runtime.actorPositionX = request.PositionX
		runtime.actorPositionY = request.PositionY
		runtime.actorDirection = request.MovementCode
		runtime.actorPositionValid = true
		// 队伍级共享实例：把本次坐标记进实例位置表。
		runtime.recordPartyDungeonActorPosition()
	}
	session.dungeon.mu.Unlock()
	if runtime != nil {
		// 队友的位移转发（默认 off；开启后只用城镇里已被客户端消费成功的
		// noti0x16 形状发**其他**队员，绝不回发给上报者本人）。
		s.broadcastDungeonPartyPeerPosition(session, "current_exe_op35_dungeon_position_report")
		return nil
	}

	session.townMu.Lock()
	readyCharacterID := session.townSceneReadyCharacterID
	snapshot := session.townPositionSnapshot
	if readyCharacterID == characterID && snapshot.CharacterID == characterID {
		snapshot.PositionX = request.PositionX
		snapshot.PositionY = request.PositionY
		snapshot.MovementCode = request.MovementCode
		snapshot.OpaqueScaledU16 = request.OpaqueScaledU16
		snapshot.PositionValid = true
		session.townPositionSnapshot = snapshot
	}
	session.townMu.Unlock()

	if readyCharacterID != characterID {
		s.logTownSetUserPositionBlocked(session, request, "town_scene_player_state_not_finalized")
		return nil
	}
	if snapshot.CharacterID != characterID {
		s.logTownSetUserPositionBlocked(session, request, "town_scene_location_owner_missing")
		return nil
	}

	s.logGameEvent(session, "game-town-set-user-position-captured",
		"char_id", characterID,
		"town_id", snapshot.TownID,
		"area_id", snapshot.AreaID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"movement_code", request.MovementCode,
		"opaque_scaled_u16", request.OpaqueScaledU16,
		"motion_state_mode", currentTownPeerMotionStateMode(),
		"persisted", false,
		"source", "current_exe_sub_23E0660_exact_plaintext")

	// Broadcast position to other players in the same area.
	if s.onlinePlayers != nil {
		others := s.onlinePlayers.UpdatePosition(characterID, request.PositionX, request.PositionY)
		if len(others) > 0 {
			mover := &onlinePlayerInfo{
				CharacterID: characterID,
				TownID:      snapshot.TownID,
				AreaID:      snapshot.AreaID,
				ChannelID:   currentTownPresenceChannelID(session),
				PositionX:   request.PositionX,
				PositionY:   request.PositionY,
				Direction:   request.MovementCode,
				// 厂商口径：C2S op35 的 body[5:7] 就是 S2C noti0x16 的 body[7:9]
				// （TownHandler.cs:174/184 原样回填）。观者只能从这条包学到同屏
				// actor 的移动速率，所以这里必须带上，不能留给广播去写常量。
				MotionState: request.OpaqueScaledU16,
				Session:     session,
			}
			s.broadcastTownPlayerMove(mover, others)
		}
	}
	s.queueCurrentFiendWarEncounter(session)
	return nil
}

// commitPendingDungeonReturnBeforeTownPosition accepts the first town-side
// op35 position report as proof that a previously written typed op24 return
// was consumed by the client. Live Tower death logs show exactly this shape:
// op40 -> op32 -> 10s -> op24, then the client sends op35 with the frozen
// return coordinates. Blocking that op35 leaves the active dungeon runtime
// attached forever and the corpse never reaches a usable town scene.
func (s *Service) commitPendingDungeonReturnBeforeTownPosition(
	session *gameSession,
	request currentTownSetUserPositionRequest,
) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	characterID := session.selectedCharacterID

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	shouldCommit := runtime != nil && runtime.Session != nil &&
		runtime.townReturnPending && runtime.townReturnOp24Sent
	transition := currentDungeonTownTransition{}
	if shouldCommit {
		transition = cloneCurrentDungeonTownTransition(runtime.townReturnTransition)
	}
	session.dungeon.mu.Unlock()
	if !shouldCommit {
		// The runtime is detached before the staged town actor/HUD chain is
		// written. If an earlier confirmation reached that boundary but a
		// later mode0/mode1/creature/skill write failed, op35 is the next
		// current-client town proof and must resume only the unfinished suffix.
		return s.ensureCurrentConfirmedDungeonReturnPlayerState(
			session,
			"current_exe_op35_position_without_retained_runtime",
		)
	}

	if err := s.commitPendingDungeonReturnForSceneRequest(session, "current_exe_op35_position_after_pending_town_return"); err != nil {
		return err
	}

	session.dungeon.mu.Lock()
	stillHasRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if stillHasRuntime || session.selectedCharacterID != characterID {
		s.logGameEvent(session, "game-town-set-user-position-pending-return-committed",
			"char_id", characterID,
			"selected_character", session.selectedCharacterID,
			"runtime_retained", stillHasRuntime,
			"reason", "pending_return_committed_but_scene_owner_changed")
		return nil
	}

	session.townMu.Lock()
	clearCurrentTownSelectorOriginLocked(session)
	session.townSceneReadyCharacterID = characterID
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:     characterID,
		TownID:          transition.TownID,
		AreaID:          transition.AreaID,
		PositionX:       request.PositionX,
		PositionY:       request.PositionY,
		MovementCode:    request.MovementCode,
		OpaqueScaledU16: request.OpaqueScaledU16,
		PositionValid:   true,
	}
	session.townMu.Unlock()

	s.logGameEvent(session, "game-town-set-user-position-pending-return-committed",
		"char_id", characterID,
		"town_id", transition.TownID,
		"area_id", transition.AreaID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"movement_code", request.MovementCode,
		"opaque_scaled_u16", request.OpaqueScaledU16,
		"position_source", transition.PositionSource,
		"reason", "op35_is_first_town_side_confirmation_after_typed_op24_return")
	return nil
}

// currentTownLoginLocation 是"角色列表登录"入口解析出的落点：要写回角色记录的
// town_id/area_id/pos_x/pos_y/direction/area_state，以及该 (town, area) 在运行时 PVF 里的地图。
// 位置按 i16 有符号保存，与 op36/op24 的坐标域和 MySQL 的 SMALLINT 列域一致。
type currentTownLoginLocation struct {
	TownID    byte
	AreaID    byte
	PositionX int64
	PositionY int64
	Direction byte
	AreaState byte
	MapPath   string
	// Source 是 location_source：这次落点的锚点来源。
	Source string
	// Branch 是三级落点分支（town_seria_room / persisted_town_position /
	// elvengard_seria_fallback / forced_elvengard_seria），只用于日志判读。
	Branch string
	// PositionSource 记录坐标的来源（该 area 的 `[gate]`、它声明的默认落点，或持久化坐标），
	// 只用于日志判读，不参与协议编码。
	PositionSource string
}

const (
	// currentTownLoginLocationTownSeriaSource 表示"上一次所在城镇的赛丽亚房间"落点
	// （town = 持久化 town_id，area = 该城镇自己的 seria 区域）。
	currentTownLoginLocationTownSeriaSource = "runtime_pvf_town_seria_room"
	// currentTownLoginLocationPersistedSource 表示"上次离线所在房间"（角色记录里持久化的位置）。
	// 该城镇在运行时 PVF 里没有赛丽亚房间时（实测 39~46 都没有），登录留在该城镇并沿用它。
	currentTownLoginLocationPersistedSource = "character_persisted_town_location"
	// currentTownLoginLocationSeriaGateSource 表示既有赛丽亚房间 gate 落点
	// （town 38 / area 1）。持久化信息不足以留在原城镇时使用，或被 env 开关强制使用。
	currentTownLoginLocationSeriaGateSource = "runtime_pvf_town_38_area_1_gate"
)

const (
	// 三级落点分支名。日志字段 branch 直接取这组值，便于线上按分支统计。
	currentTownLoginBranchTownSeriaRoom        = "town_seria_room"
	currentTownLoginBranchPersistedPosition    = "persisted_town_position"
	currentTownLoginBranchElvengardSeria       = "elvengard_seria_fallback"
	currentTownLoginBranchForcedElvengardSeria = "forced_elvengard_seria"
)

// currentPersistedTownLoginTownID 读出角色记录里**已持久化**的"上一次所在城镇"。
//
// 它是三级落点的第一级主键：先看"这个城镇有没有赛丽亚房间"。town_id 必须是 u8
// （op24/op36 的城镇字段域，也是 MySQL 列域）。
//
// 真源是 op36 过图/回城、prev-village 回退、副本返回这些既有 owner 事务边界写入的
// dnf_characters.town_id 列；op35 的高频坐标上报按设计不落库，与城镇归属无关。
func currentPersistedTownLoginTownID(character dnfrepo.CharacterRecord) (int64, bool, string) {
	if character.Stats == nil {
		return 0, false, "selected_character_stats_missing"
	}
	townID, hasTownID := character.Stats["town_id"]
	if !hasTownID {
		return 0, false, "persisted_town_id_missing"
	}
	if townID < 0 || townID > int64(^byte(0)) {
		return 0, false, "persisted_town_id_outside_u8"
	}
	return townID, true, ""
}

// currentPersistedTownLoginLocation 解析"上一次离线时所在房间"作为登录落点（三级落点的第二级）。
//
// 真源是角色记录里**已持久化**的 town_id/area_id/pos_x/pos_y/direction/area_state
// （MySQL dnf_characters 的同名列，由 op36 过图/回城、prev-village 回退、副本返回这些既有 owner
// 事务边界写入），不是会话快照：op35 的高频坐标上报按设计**不落库**，所以这里恢复的是
// "上次进入的那个区域 + 那时记录的坐标"。
//
// 校验（任何一项不成立就拒绝，调用方进入第三级回落并把 reason 写进日志）：
// town_id/area_id 必须存在，且是运行时 PVF town 表里的一个区域（s.townArea）；
// 该区域必须有 map_path（没有客户端地图的场景不能当落点）；pos_x/pos_y 必须落在 i16
// （op24/op35 的坐标域）；direction/area_state 必须落在 u8。
//
// 运行时 PVF 的 town 文档只提供区域的 map_path 与（gate 区域的）gate 坐标，没有地图边界信息，
// 所以"坐标属于该区域"能证明的最强形式就是这里的 i16 域 + 区域存在性；精确落点由客户端进场景后的
// 第一次 op35/op36 上报收口，这与既有的频道重连路径（currentPersistedTownTransition）完全同域。
func (s *Service) currentPersistedTownLoginLocation(
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (currentTownLoginLocation, bool, string) {
	if s == nil || characterID == 0 || character.Stats == nil {
		return currentTownLoginLocation{}, false, "selected_character_stats_missing"
	}
	townID, hasTownID := character.Stats["town_id"]
	if !hasTownID {
		return currentTownLoginLocation{}, false, "persisted_town_id_missing"
	}
	if townID < 0 || townID > int64(^byte(0)) {
		return currentTownLoginLocation{}, false, "persisted_town_id_outside_u8"
	}
	areaID, hasAreaID := character.Stats["area_id"]
	if !hasAreaID {
		return currentTownLoginLocation{}, false, "persisted_area_id_missing"
	}
	if areaID < 0 || areaID > int64(^byte(0)) {
		return currentTownLoginLocation{}, false, "persisted_area_id_outside_u8"
	}
	positionX, hasX := character.Stats["pos_x"]
	positionY, hasY := character.Stats["pos_y"]
	if !hasX || !hasY {
		return currentTownLoginLocation{}, false, "persisted_position_missing"
	}
	if positionX < -1<<15 || positionX > 1<<15-1 || positionY < -1<<15 || positionY > 1<<15-1 {
		return currentTownLoginLocation{}, false, "persisted_position_outside_i16"
	}
	direction, hasDirection := character.Stats["direction"]
	if !hasDirection || direction < 0 || direction > int64(^byte(0)) {
		return currentTownLoginLocation{}, false, "persisted_direction_missing_or_outside_u8"
	}
	areaState, hasAreaState := character.Stats["area_state"]
	if !hasAreaState || areaState < 0 || areaState > int64(^byte(0)) {
		return currentTownLoginLocation{}, false, "persisted_area_state_missing_or_outside_u8"
	}
	area, found := s.townArea(townID, areaID)
	if !found {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_area_%d_missing_from_runtime_pvf",
			townID,
			areaID,
		)
	}
	if area.MapPath == "" {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_area_%d_has_no_runtime_pvf_map_path",
			townID,
			areaID,
		)
	}
	return currentTownLoginLocation{
		TownID:         byte(townID),
		AreaID:         byte(areaID),
		PositionX:      positionX,
		PositionY:      positionY,
		Direction:      byte(direction),
		AreaState:      byte(areaState),
		MapPath:        area.MapPath,
		Source:         currentTownLoginLocationPersistedSource,
		Branch:         currentTownLoginBranchPersistedPosition,
		PositionSource: "character_persisted_position",
	}, true, ""
}

// setCurrentTownPositionSceneLocked establishes the exact town scene that may
// own later op35 reports. The caller must hold session.townMu. A scene change
// invalidates the previous coordinate report even when the same character is
// moving between two areas of the same town.
func setCurrentTownPositionSceneLocked(session *gameSession, characterID uint16, townID, areaID byte) {
	if session == nil {
		return
	}
	clearCurrentTownSelectorOriginLocked(session)
	advanceImageCommunicationSceneLocked(session)
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: characterID,
		TownID:      townID,
		AreaID:      areaID,
	}
}

// bindCurrentTownSelectorOrigin freezes the town origin owned by the exact
// op15/op27 selector context. A duplicate op15 keeps the first binding. This
// prevents a later town-position report from silently changing where op132
// returns, while the same frozen snapshot remains available after op16 has
// committed an active dungeon runtime.
func bindCurrentTownSelectorOrigin(session *gameSession) (currentTownPositionSnapshot, bool) {
	if session == nil || session.selectedCharacterID == 0 {
		return currentTownPositionSnapshot{}, false
	}
	characterID := session.selectedCharacterID
	session.townMu.Lock()
	defer session.townMu.Unlock()
	if session.townSelectorOriginBound {
		if session.townSelectorOriginSnapshot.CharacterID == characterID {
			return session.townSelectorOriginSnapshot, true
		}
		clearCurrentTownSelectorOriginLocked(session)
	}
	snapshot := session.townPositionSnapshot
	if session.townSceneReadyCharacterID != characterID || snapshot.CharacterID != characterID || !snapshot.PositionValid {
		return currentTownPositionSnapshot{}, false
	}
	session.townSelectorOriginSnapshot = snapshot
	session.townSelectorOriginBound = true
	return snapshot, true
}

// freezeCurrentDungeonTownReturnOrigin copies the exact op35 origin into an
// ordinary dungeon runtime before that runtime becomes visible. Tutorial
// dungeons deliberately retain their independent persisted birth-town return
// owner because a new character can enter them before any town op35 exists.
func freezeCurrentDungeonTownReturnOrigin(session *gameSession, runtime *runtimeDungeonState) error {
	if session == nil || runtime == nil || runtime.Session == nil {
		return fmt.Errorf("%w: runtime owner missing", errCurrentDungeonTownReturnOriginUnavailable)
	}
	if e := runtime.raidEntry; e != nil && e.ChannelType == 42 {
		origin := e.fiendWarOrigin
		if origin.CharacterID != session.selectedCharacterID || !origin.PositionValid {
			return fmt.Errorf("超时空副本返回坐标归属无效")
		}
		runtime.townReturnOrigin = origin
		if runtime.Character.Stats == nil {
			runtime.Character.Stats = map[string]int64{}
		}
		runtime.Character.Stats["town_id"], runtime.Character.Stats["area_id"] = int64(origin.TownID), int64(origin.AreaID)
		runtime.Character.Stats["pos_x"], runtime.Character.Stats["pos_y"] = int64(origin.PositionX), int64(origin.PositionY)
		runtime.Character.Stats["direction"] = int64(origin.MovementCode)
		runtime.Character.Stats["area_state"] = 0
		return nil
	}
	if isPVFTutorialDungeon(runtime) {
		runtime.townReturnOrigin = currentTownPositionSnapshot{}
		return nil
	}
	if transfer := runtime.antonHatcheryTransfer; transfer != nil {
		session.dungeon.mu.Lock()
		defer session.dungeon.mu.Unlock()
		current := session.dungeon.runtime
		committed := current == runtime && runtime.lifecycleToken != 0 && runtime.lifecycleToken == session.dungeon.runToken
		if (current != transfer.Old && !committed) || session.selectedCharacterID != transfer.Character || session.characterGeneration != transfer.Generation {
			return fmt.Errorf("%w: Anton transfer old return owner changed", errCurrentDungeonTownReturnOriginUnavailable)
		}
		townID, areaID, err := currentSceneTransitionLocation(runtime.Character, true)
		if err != nil {
			return err
		}
		if err := validateCurrentDungeonTownReturnOrigin(transfer.ReturnOrigin, transfer.Character, townID, areaID); err != nil {
			return err
		}
		runtime.townReturnOrigin = transfer.ReturnOrigin
		return nil
	}
	characterID := session.selectedCharacterID
	if characterID == 0 || !dungeonRuntimeOwnsCharacter(runtime, characterID) {
		return fmt.Errorf("%w: runtime character owner mismatch", errCurrentDungeonTownReturnOriginUnavailable)
	}
	townID, areaID, err := currentSceneTransitionLocation(runtime.Character, true)
	if err != nil {
		return fmt.Errorf("%w: %v", errCurrentDungeonTownReturnOriginUnavailable, err)
	}
	session.townMu.Lock()
	origin := session.townSelectorOriginSnapshot
	bound := session.townSelectorOriginBound
	session.townMu.Unlock()
	if !bound {
		// 现场（2026-09-24 21:47，队长角色 3 的 op16）：会话里没有 op35 绑定的 selector
		// origin 时，这里直接把整队挡在"选择地图"界面
		// （game-dungeon-select-blocked … reason=ordinary_dungeon_town_return_origin_unavailable），
		// 队员侧此时已经 routed-and-prepared，于是两人一起卡在选择器。
		// 返回城镇原点的持久化真源是角色记录的 town_id/area_id/pos_x/pos_y（登录、过图都会写），
		// 会话快照缺失时按它回落；只有持久化位置也不可用时才继续拒绝（保留既有拒绝语义）。
		fallback, ok := persistedCurrentDungeonTownReturnOrigin(runtime.Character, true, characterID, townID, areaID)
		if !ok {
			return fmt.Errorf("%w: selector origin not bound", errCurrentDungeonTownReturnOriginUnavailable)
		}
		runtime.townReturnOrigin = fallback
		return nil
	}
	if err := validateCurrentDungeonTownReturnOrigin(origin, characterID, townID, areaID); err != nil {
		return err
	}
	runtime.townReturnOrigin = origin
	return nil
}

// currentTownSelectorOriginBound 回答"会话里是否已经有 op35 绑定的选择器原点"。
// 调用方用它区分"用了 op35 快照"与"回落用了角色记录里的持久化位置"，便于复测时对日志。
func currentTownSelectorOriginBound(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.townMu.Lock()
	defer session.townMu.Unlock()
	return session.townSelectorOriginBound
}

// persistedCurrentDungeonTownReturnOrigin 用角色记录里的持久化位置（town_id/area_id/
// pos_x/pos_y）构造返回城镇原点。op35 快照只存在于会话内（高频上报不写库），
// 所以"刚过图/刚登录就点开始冒险"这类时机本来就没有快照；库里的位置是权威的，
// 用它构造出的原点形状与 op35 快照一致（坐标按 u16 存，与 op35 的读取方式相同）。
func persistedCurrentDungeonTownReturnOrigin(
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	characterID uint16,
	townID byte,
	areaID byte,
) (currentTownPositionSnapshot, bool) {
	if !hasCharacter || character.Stats == nil || characterID == 0 {
		return currentTownPositionSnapshot{}, false
	}
	positionX, hasX := character.Stats["pos_x"]
	positionY, hasY := character.Stats["pos_y"]
	if !hasX || !hasY {
		return currentTownPositionSnapshot{}, false
	}
	if positionX < -1<<15 || positionX > 1<<15-1 || positionY < -1<<15 || positionY > 1<<15-1 {
		return currentTownPositionSnapshot{}, false
	}
	return currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        townID,
		AreaID:        areaID,
		PositionX:     uint16(int16(positionX)),
		PositionY:     uint16(int16(positionY)),
		PositionValid: true,
	}, true
}

func validateCurrentDungeonTownReturnOrigin(
	origin currentTownPositionSnapshot,
	characterID uint16,
	townID byte,
	areaID byte,
) error {
	if !origin.PositionValid {
		return fmt.Errorf("%w: op35 position was not reported", errCurrentDungeonTownReturnOriginUnavailable)
	}
	if origin.CharacterID != characterID || origin.TownID != townID || origin.AreaID != areaID {
		return fmt.Errorf(
			"%w: origin character/town/area %d/%d/%d does not match runtime %d/%d/%d",
			errCurrentDungeonTownReturnOriginUnavailable,
			origin.CharacterID,
			origin.TownID,
			origin.AreaID,
			characterID,
			townID,
			areaID,
		)
	}
	return nil
}

func clearCurrentTownSelectorOrigin(session *gameSession) {
	if session == nil {
		return
	}
	session.townMu.Lock()
	clearCurrentTownSelectorOriginLocked(session)
	session.townMu.Unlock()
}

func clearCurrentTownSelectorOriginLocked(session *gameSession) {
	if session == nil {
		return
	}
	session.townSelectorOriginSnapshot = currentTownPositionSnapshot{}
	session.townSelectorOriginBound = false
}

func currentTownPositionForTransition(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
) (currentTownPositionSnapshot, string, bool, error) {
	if session == nil || characterID == 0 {
		return currentTownPositionSnapshot{}, "", false, nil
	}
	session.townMu.Lock()
	selectorSnapshot := session.townSelectorOriginSnapshot
	selectorBound := session.townSelectorOriginBound
	session.townMu.Unlock()
	if selectorBound {
		if selectorSnapshot.CharacterID != characterID ||
			selectorSnapshot.TownID != townID ||
			selectorSnapshot.AreaID != areaID {
			return currentTownPositionSnapshot{}, "", false, fmt.Errorf(
				"selector origin character/town/area %d/%d/%d does not match transition %d/%d/%d",
				selectorSnapshot.CharacterID,
				selectorSnapshot.TownID,
				selectorSnapshot.AreaID,
				characterID,
				townID,
				areaID,
			)
		}
		if !selectorSnapshot.PositionValid {
			return currentTownPositionSnapshot{}, "", false, fmt.Errorf(
				"%w: bound selector origin has no op35 position",
				errCurrentDungeonTownReturnOriginUnavailable,
			)
		}
		return selectorSnapshot, "current_exe_op35_selector_origin_snapshot", true, nil
	}
	return currentTownPositionSnapshot{}, "", false, nil
}

func (s *Service) applyCurrentTownPositionToTransition(
	session *gameSession,
	characterID uint16,
	transition currentDungeonTownTransition,
) (currentDungeonTownTransition, bool, error) {
	snapshot, positionSource, ok, err := currentTownPositionForTransition(
		session,
		characterID,
		transition.TownID,
		transition.AreaID,
	)
	if err != nil {
		return currentDungeonTownTransition{}, false, err
	}
	if !ok {
		return transition, false, nil
	}
	transition, err = applyCurrentTownPositionSnapshotToTransition(
		transition,
		snapshot,
		characterID,
		positionSource,
	)
	if err != nil {
		return currentDungeonTownTransition{}, false, err
	}
	return transition, true, nil
}

func applyCurrentTownPositionSnapshotToTransition(
	transition currentDungeonTownTransition,
	snapshot currentTownPositionSnapshot,
	characterID uint16,
	positionSource string,
) (currentDungeonTownTransition, error) {
	if err := validateCurrentDungeonTownReturnOrigin(
		snapshot,
		characterID,
		transition.TownID,
		transition.AreaID,
	); err != nil {
		return currentDungeonTownTransition{}, err
	}
	row := currentSceneTransitionRow{
		ObjectOrResourceKey: transition.ActorObjectKey,
		Value1:              snapshot.PositionX,
		Value2:              snapshot.PositionY,
		Value3:              transition.Direction,
		Value4:              transition.AreaState,
	}
	body, err := buildCurrentSceneTransitionBody(
		transition.TownID,
		transition.AreaID,
		[]currentSceneTransitionRow{row},
	)
	if err != nil {
		return currentDungeonTownTransition{}, err
	}
	transition.PositionX = int16(snapshot.PositionX)
	transition.PositionY = int16(snapshot.PositionY)
	transition.PositionSource = positionSource
	transition.Body = body
	return transition, nil
}

func (s *Service) logTownSetUserPositionBlocked(
	session *gameSession,
	request currentTownSetUserPositionRequest,
	reason string,
) {
	s.logGameEvent(session, "game-town-set-user-position-blocked",
		"char_id", session.selectedCharacterID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"movement_code", request.MovementCode,
		"opaque_scaled_u16", request.OpaqueScaledU16,
		"reason", reason)
}
