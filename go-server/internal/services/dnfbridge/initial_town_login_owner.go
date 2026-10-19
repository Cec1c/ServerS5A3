package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// currentCharacterListLoginTransition 决定"角色列表登录"这条入口把角色放进哪个城镇区域。
//
// 三级落点：
//  1. **该城镇有赛丽亚房间** → 落在该城镇自己的赛丽亚房间锚点（房间由运行时 PVF 该城镇的
//     `[area]` 地图路径判定，见 town_seria_room.go；落点取该房间的 `[gate]`，没有 gate 时取
//     该 area 声明的默认落点）。这就是操作者原话"去了城镇 A 就落在城镇 A 的赛丽亚房间"。
//  2. **该城镇没有赛丽亚房间**（运行时 PVF 实测 39~46 都没有，只有 38 有）→ 沿用角色记录里
//     持久化的精确落点（town/area/pos/direction/area_state，过运行时 PVF 校验），
//     fallback_reason=persisted_position。
//     **这一级默认关闭**：操作者 2026-09-26 报障「选择角色重新进入游戏会在上次下线的地方出现」，
//     而更早的定稿条目是「重新登陆固定刷新在艾尔文防线的赛利亚房间」（已完成）。两者冲突时
//     以"登录必须落在赛丽亚房间"为准：默认跳过第 2 级，直接走第 3 级（38/1 的赛丽亚房间）。
//     需要恢复"留在上次所在城镇"的旧观感时，显式设 DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep
//     （见 currentLoginPersistedTownKeep），此时第 2 级的落点、日志字段与写回字节与门控前逐字节相同。
//  3. 持久化信息缺失/非法（或该城镇连持久化区域都不存在），或第 2 级被门控跳过
//     → 回落既有的 38/1 赛丽亚房间 gate，fallback_reason 给出具体原因。
//
// 另有一个**默认关闭**的线上 A/B 开关：DNFBRIDGE_LOGIN_SERIA_FALLBACK=elvengard-seria
// 时无条件走第三级（强制 38/1，连第 1 级也跳过），用于对比"永远回赛丽亚房间"的旧观感。
//
// 每条分支都会在 game-character-list-login-town-location 里打全
// branch / town_id / area_id / location_source / position_source / fallback_reason，
// 便于线上判读。op1425/op36 的私房实例化那条第三路径只记录判定结果
// （private_room_instantiation），不在这里实现。
//
// 三级落点解析完之后、写回之前，还有一个**默认关闭**的区域门槛门控
// DNFBRIDGE_LOGIN_AREA_GATES（relaxed 默认 / log 只记录 / strict 重定向到 38/1）：
// 运行时 PVF 的区域可以声明 `[need level]` / `[need quest]`，落在不满足门槛的城镇区域上
// 是 op36 会拒绝、登录却放行的口径不一致。详见 initial_town_login_area_gates.go。
//
// prev-village 原点的绑定语义保持不变：origin 仍然是"落点之外的、可持久化的非赛丽亚位置"
// （pre-write 的持久化房间），op1425 仍然只在当前区域是赛丽亚房间时才被接受
// （town_prev_village.go）。
func (s *Service) currentCharacterListLoginTransition(
	ctx context.Context,
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	if session != nil && isPVPChannel(session.channel) {
		return s.currentPVPTownTransition(session, characterID, character)
	}
	if characterID == 0 || character.Stats == nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("selected character login location is unavailable")
	}
	loginLocation, persistedTownID, fallbackReason, seriaRoomReason, err := s.currentCharacterListLoginLocationForSession(session, characterID, character)
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}

	repositories, ok := s.repositoryGroup()
	if !ok {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("character repository is unavailable for character-list login location")
	}
	owner, err := dnftown.NewOwner(repositories)
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("character repository is unavailable for character-list login location: %w", err)
	}

	// Guild membership is an ownership boundary, independent of the optional
	// level/quest login gates. A former member must not resume inside a hall.
	guildHallMembershipLost := false
	hallMapPath := loginLocation.MapPath
	// Default login already selects Seria. Its saved return destination still
	// needs the same ownership check before it can be bound to the exit door.
	origins := []currentTownPositionSnapshot{session.townPrevVillageSnapshot, session.townSeriaRoomEntrySnapshot}
	if origin, found := currentTownPrevVillageOriginLocked(session, characterID, character); found {
		origins = append(origins, origin)
	}
	if origin, found := currentTownPrevVillageOriginFromPersistedStats(characterID, character); found {
		origins = append(origins, origin)
	}
	for _, origin := range origins {
		if path, found := s.currentTownPrevVillageOriginMapPath(origin); found && townMapPathLooksLikeGuildHall(path) {
			hallMapPath = path
			break
		}
	}
	if townMapPathLooksLikeGuildHall(hallMapPath) {
		_, scopeErr := s.currentTownAreaInstance(ctx, session, character, hallMapPath)
		if errors.Is(scopeErr, dnfguild.ErrNotMember) {
			guildHallMembershipLost = true
			if townMapPathLooksLikeGuildHall(loginLocation.MapPath) {
				fallbackLocation, fallbackErr := s.currentInitialTownSeriaLoginLocation()
				if fallbackErr != nil {
					return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fallbackErr
				}
				fallbackLocation.PositionSource = "runtime_pvf_guild_membership_fallback"
				loginLocation = fallbackLocation
			}
			s.logGameEvent(session, "game-login-guild-hall-redirected", "char_id", characterID)
		} else if scopeErr != nil {
			return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", scopeErr
		}
	}

	// 登录落点的区域门槛（默认关闭，见 initial_town_login_area_gates.go）：
	// 在**落点已解析、还没写回**的位置评估 PVF 的 `[need level]` / `[need quest]`，
	// 这样 strict 的重定向是被持久化的那一个，而不是先落进不允许的区域再纠正。
	// relaxed（默认）不评估、不打日志，落点/日志字段/写回字节与门控前逐字节相同。
	areaGatesMode := currentLoginAreaGatesMode()
	areaGateDenied := false
	areaGateDenyReason := ""
	if areaGatesMode != currentLoginAreaGatesRelaxed {
		if area, found := s.townArea(int64(loginLocation.TownID), int64(loginLocation.AreaID)); found {
			// 第二个返回值是 **evaluated**（"有没有真的评估过"），**不是 denied**：
			// 见 currentLoginAreaGateDenyReason 的注释，它在包括"任务仓储读失败、fail-open"
			// 在内的所有路径上都返回 true。把它当 denied 用会让**评估通过**的落点也被拒绝
			// （线上实测：strict 下持久化城镇 38 / 落点 38/1 的普通登录 branch 从
			// town_seria_room 变成 elvengard_seria_fallback，而同一次评估 reason 为空）。
			// **唯一可靠的"被拒"判据是理由非空**：只有等级不够（level=X<Y）或任务未完成
			// （quest=N_not_cleared）才会给出理由，其余路径（含 fail-open）都是空串。
			reason, evaluated := s.currentLoginAreaGateDenyReason(
				ctx,
				session,
				repositories,
				characterID,
				int64(loginLocation.TownID),
				area,
				int64(character.Level),
			)
			switch {
			case evaluated && reason != "":
				// 真的被拒：log 只记录；strict 把落点换成既有的第三级 38/1 赛丽亚房间，
				// **不再评估它**（运行时 PVF 实测的赛丽亚房间，二次评估没有意义，
				// 因此也不会递归回这里）。
				areaGateDenied = true
				areaGateDenyReason = reason
				deniedTownID := loginLocation.TownID
				deniedAreaID := loginLocation.AreaID
				deniedMapPath := loginLocation.MapPath
				deniedBranch := loginLocation.Branch
				areaGateFallback := ""
				if areaGatesMode == currentLoginAreaGatesStrict {
					fallbackLocation, fallbackErr := s.currentInitialTownSeriaLoginLocation()
					if fallbackErr != nil {
						return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fallbackErr
					}
					fallbackLocation.Branch = currentTownLoginBranchElvengardSeria
					fallbackLocation.PositionSource = "runtime_pvf_seria_area_gate"
					loginLocation = fallbackLocation
					areaGateFallback = currentLoginAreaGateFallback
				}
				// 被拒的 town/area（不是重定向之后的）留在日志里，日志里同时给出 fallback，
				// 线上据此区分"只记录、落点没变"（log）与"已重定向"（strict）。
				s.logGameEvent(session, "game-login-area-gate-denied",
					"mode", areaGatesMode,
					"char_id", characterID,
					"town_id", deniedTownID,
					"area_id", deniedAreaID,
					"character_level", character.Level,
					"need_level", area.MinLevel,
					"need_quests", fmt.Sprint(area.NeedQuests),
					"map_path", deniedMapPath,
					"branch", deniedBranch,
					"reason", areaGateDenyReason,
					"fallback", areaGateFallback)
			default:
				// 评估通过（reason 为空）或压根没评估（无法评估 → 放行）：落点、Branch、
				// PositionSource、fallback_reason 一律不动，与 relaxed 逐字节相同。
				// 这里只补一条"评估过、没被拒"的记录：线上排查据此判断门控跑没跑，
				// 同时保证不会出现 reason 为空的 -denied 事件（事件名与语义一致）。
				s.logGameEvent(session, "game-login-area-gate-evaluated",
					"mode", areaGatesMode,
					"char_id", characterID,
					"town_id", loginLocation.TownID,
					"area_id", loginLocation.AreaID,
					"character_level", character.Level,
					"need_level", area.MinLevel,
					"need_quests", fmt.Sprint(area.NeedQuests),
					"map_path", loginLocation.MapPath,
					"branch", loginLocation.Branch,
					"evaluated", evaluated,
					"denied", false)
			}
		}
	}

	recordCharacterID := character.CharacterID
	if recordCharacterID == "" {
		recordCharacterID = fmt.Sprintf("%d", characterID)
	}
	// 第三级**强制回退**落地（`elvengard_seria_fallback` / `forced_elvengard_seria`，
	// 含 area-gate strict 的重定向，它也复用同一个 branch 常量）时，本次**是否要把回退坐标
	// 写回 dnf_characters**。
	//
	// **默认档 = 写**（逐字节今天）。只有显式 `DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN=preserve`
	// 才保留持久化真值。判据只看本次落点的 branch，**不看坐标**（"坐标是否等于 38/1 落点"
	// 这种判据会在别人改了默认落点时静默失效）。
	//
	// 为什么这是缺陷（厂商对拍见 `GameChannelSpawnPolicy.Resolve`）：
	// 厂商的落点是从**持久化城镇自己**的 `[gate]` 推出来的（`Town.GetCeraRoomInfo(persistedTownId)`
	// → `roomInfo.Town = townId`），算不出来就抛 `InvalidOperationException("Town {townId} has no
	// Seria-room gate.")`；它**从不把玩家换到另一个城镇**，所以它在
	// `CharacterSelectHandler.cs:425-436` 的写回天然是"同 town 幂等"。我们第三级回退把玩家换到
	// 38/1 再把 38 写回 `dnf_characters.town_id` ⇒ **把真值毁掉**，而且第一级此后永远命中 38，
	// 第二级再也轮不到（线上 `persisted=true` 那两条 + 之后 `persisted_town_id=38` 可以复现）。
	keepPersistedLoginLocation := !guildHallMembershipLost && currentLoginFallbackKeepsTown() &&
		currentTownLoginBranchIsForcedFallback(loginLocation.Branch)
	// 本次**没有**覆盖的持久化落点（只在 preserve 档有值），只用于日志判读。
	keptPersistedTownID, keptPersistedAreaID := int64(-1), int64(-1)
	keptPersistedPosX, keptPersistedPosY := int64(0), int64(0)
	keptPersistedLocationBound := false
	if keepPersistedLoginLocation && character.Stats != nil {
		town, hasTown := character.Stats["town_id"]
		area, hasArea := character.Stats["area_id"]
		posX, hasPosX := character.Stats["pos_x"]
		posY, hasPosY := character.Stats["pos_y"]
		if hasTown && hasArea && hasPosX && hasPosY {
			keptPersistedTownID = town
			keptPersistedAreaID = area
			keptPersistedPosX = posX
			keptPersistedPosY = posY
			keptPersistedLocationBound = true
		}
	}
	prevVillageOrigin := currentTownPositionSnapshot{}
	prevVillageOriginSource := ""
	leavingRaidTown := currentLoginLeavingRaidTown(session, character)
	// 本次落点要写入/（preserve 档）本应由本次落点覆盖的六个键。
	loginLocationStats := map[string]int64{
		"town_id":    int64(loginLocation.TownID),
		"area_id":    int64(loginLocation.AreaID),
		"pos_x":      loginLocation.PositionX,
		"pos_y":      loginLocation.PositionY,
		"direction":  int64(loginLocation.Direction),
		"area_state": int64(loginLocation.AreaState),
	}
	result, err := owner.ApplyLoginLocation(ctx, dnftown.LoginLocationCommand{
		AccountID:   s.accountIDForSession(session),
		CharacterID: recordCharacterID,
		Project: func(current *dnfrepo.CharacterRecord) (bool, error) {
			if current == nil || current.Stats == nil {
				return false, fmt.Errorf("selected character login location is unavailable")
			}
			_, raidChannel := currentRaidLoginTownID(session)
			if origin, found := currentTownPrevVillageOriginLocked(session, characterID, *current); found && !raidChannel {
				if _, persistable := s.currentTownPrevVillageOriginIsPersistable(origin); persistable && !currentRegisteredRaidTown(int64(origin.TownID)) && (!guildHallMembershipLost || !s.currentGuildHallArea(origin.TownID, origin.AreaID)) {
					prevVillageOrigin = origin
					prevVillageOriginSource = "character_list_login_previous_non_seria_location"
				}
			}
			if !prevVillageOrigin.PositionValid && !raidChannel {
				if origin, found := currentTownPrevVillageOriginFromPersistedStats(characterID, *current); found {
					if _, persistable := s.currentTownPrevVillageOriginIsPersistable(origin); persistable && !currentRegisteredRaidTown(int64(origin.TownID)) && (!guildHallMembershipLost || !s.currentGuildHallArea(origin.TownID, origin.AreaID)) {
						prevVillageOrigin = origin
						prevVillageOriginSource = "character_list_login_existing_persisted_prev_village"
					}
				}
			}

			values := loginLocationStats
			changed := false
			staleGuildOrigin := guildHallMembershipLost && s.currentGuildHallArea(byte(current.Stats[currentTownPrevVillageTownStat]), byte(current.Stats[currentTownPrevVillageAreaStat]))
			if (!raidChannel && currentRegisteredRaidTown(current.Stats[currentTownPrevVillageTownStat])) || staleGuildOrigin {
				for _, key := range []string{currentTownPrevVillageTownStat, currentTownPrevVillageAreaStat, currentTownPrevVillagePosXStat, currentTownPrevVillagePosYStat, currentTownPrevVillageDirectionStat} {
					if _, exists := current.Stats[key]; exists {
						delete(current.Stats, key)
						changed = true
					}
				}
			}
			if keepPersistedLoginLocation {
				// preserve 档：**一个字都不写**这六个键，持久化真值（玩家真实位置）原样留在库里。
				// 别的键（含下面的 prev-village 原点）不受影响，仍然照旧写。
			} else {
				for key, value := range values {
					if valueBefore, exists := current.Stats[key]; !exists || valueBefore != value {
						current.Stats[key] = value
						changed = true
					}
				}
			}
			if prevVillageOrigin.PositionValid && currentTownPrevVillageApplyOriginStats(current.Stats, prevVillageOrigin) {
				changed = true
			}
			return changed, nil
		},
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("persist runtime-PVF character-list login location: %w", err)
	}

	if keepPersistedLoginLocation {
		// 库里保留真值，但**本次会话仍然按解析出来的回退落点走**：把落点补回内存记录，
		// 让下游 `currentInitialTownTransition`（op24 的 town/area/pos 与 map_path）与默认档
		// 逐字节一致。注意：这里**不落库**（`owner.ApplyLoginLocation` 因为没有改动而跳过写入）。
		if result.Character.Stats == nil {
			result.Character.Stats = make(map[string]int64, len(loginLocationStats))
		}
		for key, value := range loginLocationStats {
			result.Character.Stats[key] = value
		}
	}
	if _, raidChannel := currentRaidLoginTownID(session); raidChannel || leavingRaidTown {
		// A previous ordinary town is not a valid exit from this raid room.
		// Clear it only after the matching raid landing committed successfully.
		session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
		session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
	}
	if guildHallMembershipLost {
		if s.currentGuildHallArea(session.townPrevVillageSnapshot.TownID, session.townPrevVillageSnapshot.AreaID) {
			session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
		}
		if s.currentGuildHallArea(session.townSeriaRoomEntrySnapshot.TownID, session.townSeriaRoomEntrySnapshot.AreaID) {
			session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
		}
	}

	// 登录**直接**落在赛丽亚房间时，把"进门来源城镇"补绑成"上一次所在城镇"
	// （开关 `DNFBRIDGE_LOGIN_SERIA_ENTRY_TOWN`，**默认档 = 修好后的行为**，见其注释）。
	//
	// 背景（现场取证见 `_dist/seria-door-diagnosis.md` §4）：`session.townSeriaRoomEntrySnapshot`
	// （"这一次是从哪个 (城镇,区域,坐标) 走门进房的"）在改动前**只有一个绑定点** ——
	// `town_move.go:184` 的 op36 走门进房；登录路径一个字节都不绑。于是"登录直接落在房间 ⇒
	// 走下方门 op1425"必然在 `town_prev_village.go:253-262` 取失败分支 ⇒
	// `landing_source=seria_room_outer_door` = **房间自己所在城镇 38** 的固定外门口
	// （艾尔文防线主街 38/0 (622,196)），而不是玩家来的那个城镇的房间门口。
	// 线上实测（2026-09-28，conn `game-001151` char 6）：同一次登录里，登录后第一次走下方门
	// 落 38/0 (622,196)，而同局"走门进房再出房"落 11/0 (2032,185) 素喃门口 ⇒ 出口机制本来
	// 就正确，缺的只是登录这条路径的绑定。
	//
	// 这里复用的是**已经算好并已校验过**的 `prevVillageOrigin`（本函数 Project 里的两个来源 +
	// `currentTownPrevVillageOriginIsPersistable`），不新增任何坐标来源、不编造数据；
	// 并且只在落点**是赛丽亚房间**时才绑，其它落点逐字节不变。出口侧一行都不用改：
	// `currentTownSeriaExitDoorMode` 默认档 `entry_town` 本来就走
	// `town_prev_village.go:236-244` 的 `seria_room_entry_town_door` 分支。
	//
	// ⚠️ 本函数的生产调用点 `sendCurrentInitialTownRoute` 已经持有 `session.townMu`
	// （`initial_town_route_progress.go:107-108`），`sync.Mutex` 不可重入 ⇒ 下面**绝不加锁**
	// （与紧邻的既有两行同一口径：它们同样在持锁状态下直接写 session 字段）。
	seriaRoomEntryTownBound := false
	if prevVillageOrigin.PositionValid {
		session.townPrevVillageSnapshot = prevVillageOrigin
		if currentLoginSeriaEntryTownEnabled() && townMapPathLooksLikeSeria(loginLocation.MapPath) {
			session.townSeriaRoomEntrySnapshot = prevVillageOrigin
			seriaRoomEntryTownBound = true
		}
		s.logTownPrevVillageOriginBound(session, prevVillageOrigin, prevVillageOriginSource)
	}
	s.logGameEvent(session, "game-character-list-login-town-location",
		"char_id", characterID,
		"branch", loginLocation.Branch,
		"persisted_town_id", persistedTownID,
		"town_id", loginLocation.TownID,
		"area_id", loginLocation.AreaID,
		"position_x", loginLocation.PositionX,
		"position_y", loginLocation.PositionY,
		"direction", loginLocation.Direction,
		"area_state", loginLocation.AreaState,
		"map_path", loginLocation.MapPath,
		"location_source", loginLocation.Source,
		"position_source", loginLocation.PositionSource,
		"seria_room_reason", seriaRoomReason,
		"seria_fallback", loginLocation.Source == currentTownLoginLocationSeriaGateSource,
		"fallback_reason", fallbackReason,
		// 登录落点的区域门槛判读（默认 relaxed 时 mode=relaxed、未拒、原因为空）：
		// 见 initial_town_login_area_gates.go。
		"area_gates_mode", areaGatesMode,
		"area_gate_denied", areaGateDenied,
		"area_gate_deny_reason", areaGateDenyReason,
		// op1425/op36 私房实例化那条路径不在登录入口实现；这里只记录本次落点是否落在
		// "每个角色一份"的私有房间上，供线上判读。
		"private_room_instantiation", loginLocation.MapPath != "" &&
			townAreaInstanceForMapPath(loginLocation.MapPath, characterID) != 0,
		"prev_village_town_id", prevVillageOrigin.TownID,
		"prev_village_area_id", prevVillageOrigin.AreaID,
		"prev_village_position_x", prevVillageOrigin.PositionX,
		"prev_village_position_y", prevVillageOrigin.PositionY,
		"prev_village_bound", prevVillageOrigin.PositionValid,
		"prev_village_source", prevVillageOriginSource,
		// `DNFBRIDGE_LOGIN_SERIA_ENTRY_TOWN` 档的判读字段（见 currentLoginSeriaEntryTownEnv）：
		// `seria_entry_town_bound=true` 表示本次登录**补绑了进门来源城镇**，即
		// `session.townSeriaRoomEntrySnapshot` = 上面的 prev-village 原点 ⇒ 登录后第一次走
		// 下方门（op1425）会回答 `landing_source=seria_room_entry_town_door`（来源城镇的门口），
		// 而不是房间所在城镇 38 的固定外门口。默认档与改动前唯一的差别就是这一位。
		"seria_entry_town_bound", seriaRoomEntryTownBound,
		// `DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN=preserve` 档的判读字段：
		// `fallback_kept_persisted_town=true` 表示**本次第三级回退没有覆盖持久化落点**，
		// kept_* 是库里原样保留下来的那四个值（`kept_persisted_location_bound=false` 表示
		// 持久化行里缺列，没有可保留的真值）。默认档恒为 `false` / `-1`。
		"fallback_kept_persisted_town", keepPersistedLoginLocation,
		"kept_persisted_location_bound", keptPersistedLocationBound,
		"kept_town_id", keptPersistedTownID,
		"kept_area_id", keptPersistedAreaID,
		"kept_position_x", keptPersistedPosX,
		"kept_position_y", keptPersistedPosY,
		"persisted", result.Changed,
		"source", loginLocation.Source)
	return s.currentInitialTownTransition(ctx, session, characterID, result.Character)
}

// currentCharacterListLoginLocation 是三级落点本身。返回落点、持久化城镇（缺失为 -1）、
// fallback_reason、以及"该城镇没有可用赛丽亚房间"的原因（第一级没用上时才有值）。
func (s *Service) currentCharacterListLoginLocation(
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (currentTownLoginLocation, int64, string, string, error) {
	// 持久化城镇先读出来：即使走强制回落分支，日志里的 persisted_town_id 也要是真实值，
	// 否则线上 A/B 判读会看到 -1。
	townID, hasTown, townReason := currentPersistedTownLoginTownID(character)
	persistedTownID := int64(-1)
	if hasTown {
		persistedTownID = townID
	}

	if currentLoginForcedSeriaFallback() {
		location, err := s.currentInitialTownSeriaLoginLocation()
		if err != nil {
			return currentTownLoginLocation{}, persistedTownID, "", "", err
		}
		location.Branch = currentTownLoginBranchForcedElvengardSeria
		return location, persistedTownID, "forced_by_env_" + currentLoginForcedSeriaFallbackEnv, "", nil
	}

	// 第三级（回落）先解析：它本身不可用时整条登录路径就该失败（与旧行为一致），
	// 不静默放行一个没有运行时 PVF 依据的位置。
	fallbackLocation, err := s.currentInitialTownSeriaLoginLocation()
	if err != nil {
		return currentTownLoginLocation{}, persistedTownID, "", "", err
	}

	// 第一级：该城镇自己的赛丽亚房间。
	seriaRoomReason := townReason
	if hasTown {
		if location, resolved, reason := s.currentTownSeriaRoomLoginLocation(townID); resolved {
			return location, persistedTownID, "", "", nil
		} else {
			seriaRoomReason = reason
		}
	}

	// 第二级：留在该城镇，沿用持久化的精确落点（不跨城镇）。默认被门控关闭：
	// 打开时（DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep）行为与门控前逐字节一致。
	fallbackReason := townReason
	if currentLoginPersistedTownKeep() {
		if location, resolved, reason := s.currentPersistedTownLoginLocation(characterID, character); resolved {
			return location, persistedTownID, "persisted_position", seriaRoomReason, nil
		} else {
			fallbackReason = reason
		}
	} else if hasTown {
		// 门控跳过的原因写进 fallback_reason，线上据此区分"该城镇没有赛丽亚房间"
		// 与"有持久化位置但被门控跳过"。seria_fallback=true 同时出现在日志里。
		fallbackReason = "persisted_town_position_gated_by_default_env_" +
			currentLoginPersistedTownEnv
	}

	// 第三级：38/1 赛丽亚房间。
	fallbackLocation.Branch = currentTownLoginBranchElvengardSeria
	if fallbackReason == "" {
		fallbackReason = "persisted_login_location_unavailable"
	}
	return fallbackLocation, persistedTownID, fallbackReason, seriaRoomReason, nil
}

// currentLoginForcedSeriaFallbackEnv 是默认关闭的线上 A/B 开关：把它设成
// elvengard-seria 时，角色列表登录无条件走第三级（38/1 赛丽亚房间 gate），
// 用于和"按城镇落点"的新口径对比观感。
const currentLoginForcedSeriaFallbackEnv = "DNFBRIDGE_LOGIN_SERIA_FALLBACK"

const currentLoginForcedSeriaFallbackValue = "elvengard-seria"

func currentLoginForcedSeriaFallback() bool {
	return strings.EqualFold(
		strings.TrimSpace(os.Getenv(currentLoginForcedSeriaFallbackEnv)),
		currentLoginForcedSeriaFallbackValue,
	)
}

// currentLoginPersistedTownEnv 是第二级（"该城镇没有赛丽亚房间时留在该城镇、沿用持久化
// 精确落点"）的门控开关。**默认关闭**。
//
// 为什么默认关闭：操作者 2026-09-26 报障「选择角色重新进入游戏会在上次下线的地方出现」，
// 即登录落成了上次下线的那条街/那个房间；而更早的定稿条目是「重新登陆固定刷新在艾尔文防线的
// 赛利亚房间」（已完成）。默认（未设置）时登录不会再把角色放回持久化坐标，而是走第三级
// 38/1 的赛丽亚房间；只有显式写 keep/on/1/true/yes 才恢复"留在上次所在城镇"。
//
// 线上判读：默认口径的日志是 branch=elvengard_seria_fallback、seria_fallback=true、
// fallback_reason=persisted_town_position_gated_by_default_env_DNFBRIDGE_LOGIN_PERSISTED_TOWN；
// 开门控后回到 branch=persisted_town_position、location_source=character_persisted_town_location。
const currentLoginPersistedTownEnv = "DNFBRIDGE_LOGIN_PERSISTED_TOWN"

const currentLoginPersistedTownKeepValue = "keep"

func currentLoginPersistedTownKeep() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentLoginPersistedTownEnv))) {
	case currentLoginPersistedTownKeepValue, "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}

// currentLoginFallbackKeepsTownEnv 是"第三级强制回退落地时**不要**覆盖持久化落点"的开关。
//
// **默认关闭**（未设置/空/其它任何值 = 逐字节今天：回退落点照样写回 `dnf_characters`）。
// 只有显式 `preserve` 才在"本次落点是第三级回退"时保留原有的
// `town_id/area_id/pos_x/pos_y/direction/area_state` 不写。
//
// 为什么需要它（线上实测，2026-09-27）：
//
//	conn game-000016 char_id=3 `branch=elvengard_seria_fallback persisted_town_id=11
//	town_id=38 area_id=1 position_x=450 position_y=234 … persisted=true`
//	⇒ 玩家在素喃（town 11）下线，回退落地把 `town_id` 从 11 改成 38。
//	之后同一角色的登录全部变成 `persisted_town_id=38 … branch=town_seria_room
//	persisted=false`（第一级命中，值已经等于落点，写回是空操作）⇒ **真值无法自愈**，
//	第二级（`DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep`）再也轮不到。
//
// 厂商对拍（本版源码 `S4A21-完整包-20260923\源码\服务端\Server\DfoServer\`）：
//
//	`Network\Core\GameChannelSpawnPolicy.cs:63-88 Resolve(listenerGamePort, persistedTownId)`：
//	`townId = persistedTownId > 0 ? persistedTownId : 1`；raid/pvp 城镇换成 20；然后
//	`var gate = Town.GetCeraRoomInfo(townId); if (gate.Town <= 0) throw new InvalidOperationException(
//	$"Town {townId} has no Seria-room gate.");` ⇒ 落点用的就是**持久化城镇自己**的 gate，
//	算不出来是**抛错**，**从不换到另一个城镇**（`GameWorld\Town.cs:88-98` 只在找到
//	`AreaType == "gate"` 的区域时才填 `roomInfo.Town = townId`）。
//	`Network\Handlers\CharacterSelectHandler.cs:372-374` 取该落点 → `:425-436`
//	`if (GameChannelSpawnPolicy.ShouldPersistPosition(session.ListenerPort))
//	_characterRepository.UpdatePosition(cid, CurTownId, CurAreaId, CurPosX, CurPosY, CurDirection, CurAreaState);`
//	⇒ 厂商**确实会写回**，但写回的 `town_id` 恒等于它读到的 `town_id`（同 town 幂等），
//	只有 area/pos 被归一到该城镇自己的赛丽亚房间 ⇒ **不存在"回退把真值换掉"这条路径**。
//	我们第三级回退是**本仓自加**的"换城镇"落点，所以把它的结果写回才是缺陷；
//	这也是本开关只在"第三级回退"这一支生效的原因。
//
// 一键回退 = 删除该 env（或写成 `off`）。
//
// ⚠️ 已知语义代价（如实标注，见交付报告）：preserve 档下库里的 `town_id` 与本次会话实际
// 落点会**不一致**（会话仍然是 38/1，库是 11/2）。`town_move.go:134-145` 的
// `cross_town_route_not_proven_by_current_pvf_topology` 判据读的是**库里的** `town_id`，
// 所以在这种会话里从赛丽亚房间走右门（op36 目标 38/0、`opaque_u16_a=38`、
// `opaque_tail_u8=6`，见 `town_seria_right_door_select.go:46-47`）会被拒。
// 因此本档的推荐用法是**与 `DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep` 同时开**，或只用来
// "止住写坏"后再切到 keep。
const currentLoginFallbackKeepsTownEnv = "DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN"

// currentLoginFallbackKeepsTownValue 是唯一被承认的保留取值。
const currentLoginFallbackKeepsTownValue = "preserve"

// currentLoginFallbackKeepsTown 解析 DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN。
// **只有显式 `preserve` 才生效**；未设置/空/其它任何值（含 `on`/`true`/`1`）都是默认档
// （照旧覆盖），与 `readHellBlockedSlotsMode` / `readDungeonEntrySelectionMode` 同一约定。
func currentLoginFallbackKeepsTown() bool {
	return strings.EqualFold(
		strings.TrimSpace(os.Getenv(currentLoginFallbackKeepsTownEnv)),
		currentLoginFallbackKeepsTownValue,
	)
}

// currentTownLoginBranchIsForcedFallback 报告本次落点是不是"第三级强制回退"。
// 第一级 `town_seria_room` 与第二级 `persisted_town_position` 都**不是**回退，
// 玩家自己走进房间、自己过图走的是 op36 那条完全不同的写入路径（town_move.go），
// 与本判据无关。
func currentTownLoginBranchIsForcedFallback(branch string) bool {
	switch branch {
	case currentTownLoginBranchElvengardSeria, currentTownLoginBranchForcedElvengardSeria:
		return true
	default:
		return false
	}
}

// currentLoginSeriaEntryTownEnv 是"登录**直接**落在赛丽亚房间时，补绑进门来源城镇"的开关。
//
//	未设置 / 空 / 其它任何值（含 on/true/1/yes）= **修复档（默认）**：
//	    登录落点是赛丽亚房间时，把 `session.townSeriaRoomEntrySnapshot` 绑成
//	    `prevVillageOrigin`，于是登录后第一次走下方门（op1425）回到**来源城镇**里
//	    "通往本房间"的那扇门（`landing_source=seria_room_entry_town_door`）。
//	off / legacy / 0 / false / no = **逐字节改动前**（不绑 ⇒ op1425 走
//	    `town_prev_village.go:253-262` 的失败分支，`landing_source=seria_room_outer_door`，
//	    回到房间所在城镇 38 的外门口 38/0 (622,196) 艾尔文防线主街）。
//
// 为什么默认必须是修复档（现场取证与穷举见 `_dist/seria-door-diagnosis.md` §4.2/§4.3）：
// 出房间的来源城镇在会话里只有 `town_move.go:184`（op36 走门进房）这一个绑定点，
// 登录路径一个都没有 ⇒ **没有任何 env 组合**能让"登录后第一次走下方门"回到来源城镇的
// 房间门口（`LOGIN_PERSISTED_TOWN=keep` 根本不进房间；`FALLBACK_KEEPS_TOWN=preserve`
// 不动出口；`TOWN_SERIA_EXIT_DOOR=prev_village` 落的是"上次所在坐标"而非门口，
// 而且会关掉上一轮修好的"走门进房⇒原路出门"）。线上实测（2026-09-28，conn `game-001151`）：
// 登录后第一次走下方门 `landing_source=seria_room_outer_door entry_used=false door_town_id=38`，
// 同局"走门进房再出房"却是 `seria_room_entry_town_door entry_used=true entry_town_id=11`。
//
// 本开关只影响**登录落点本身是赛丽亚房间**的那条路径；玩家自己走门进房（op36）与
// `DNFBRIDGE_TOWN_SERIA_EXIT_DOOR` 的三档语义都不受影响。
//
// 一键回退 = 把它设成 `off`（删除该 env 即回到修复档）。
const currentLoginSeriaEntryTownEnv = "DNFBRIDGE_LOGIN_SERIA_ENTRY_TOWN"

// currentLoginSeriaEntryTownEnabled 解析 currentLoginSeriaEntryTownEnv。
// **只有显式写"回退档"取值才关闭**；未设置/空/未知值一律是修复档（默认行为即修复行为）。
func currentLoginSeriaEntryTownEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentLoginSeriaEntryTownEnv))) {
	case "off", "legacy", "0", "false", "no":
		return false
	default:
		return true
	}
}

// currentInitialTownSeriaLoginLocation 是既有的回落落点：运行时 PVF 艾尔文防线的赛丽亚房间
// （town 38 / area 1，`Cataclysm/Town/Elvengard/new_seria_room.map`）的 `[gate]`。
// 这个 (town, area) 与该房间的 gate 缺失或坐标越界时返回错误：登录路径没有可用的落点，
// 不允许静默编造一个坐标（initial_defaults.go 的注释固定了这组常量的 PVF 依据）。
func (s *Service) currentInitialTownSeriaLoginLocation() (currentTownLoginLocation, error) {
	area, found := s.townArea(int64(newCharacterInitialTownID), int64(newCharacterInitialAreaID))
	if !found || area.MapPath == "" || area.Gate == nil {
		return currentTownLoginLocation{}, fmt.Errorf(
			"runtime PVF Seria login area %d/%d has no gate",
			newCharacterInitialTownID,
			newCharacterInitialAreaID,
		)
	}
	if area.Gate.X < -1<<15 || area.Gate.X > 1<<15-1 || area.Gate.Y < -1<<15 || area.Gate.Y > 1<<15-1 {
		return currentTownLoginLocation{}, fmt.Errorf(
			"runtime PVF Seria gate (%d,%d) is outside i16",
			area.Gate.X,
			area.Gate.Y,
		)
	}
	return currentTownLoginLocation{
		TownID:         newCharacterInitialTownID,
		AreaID:         newCharacterInitialAreaID,
		PositionX:      area.Gate.X,
		PositionY:      area.Gate.Y,
		Direction:      newCharacterInitialDirection,
		AreaState:      newCharacterInitialAreaState,
		MapPath:        area.MapPath,
		Source:         currentTownLoginLocationSeriaGateSource,
		Branch:         currentTownLoginBranchElvengardSeria,
		PositionSource: "runtime_pvf_seria_area_gate",
	}, nil
}
