package dnfbridge

import (
	"fmt"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 本文件回答一个问题：**运行时 PVF 里"某个城镇的赛丽亚房间"是哪一个 area？**
//
// 判据来自运行时 PVF 的 town 文档本身（`town/town.lst` + `town/<name>.twn`）：
// 每个 `[area] <id> `<map path>`` 只声明区域号、地图路径与可选落点标记，文档里
// **没有** `[seria]` 之类的显式"赛丽亚房间"标记。可判定的最强形式因此是地图路径：
// 工程既有识别 townMapPathLooksLikeSeria（town_prev_village.go，op1425"回上一个村镇"
// 与 op36 私房实例化都在用同一条识别）——路径里含 "seria" 的区域就是赛丽亚房间。
//
// 运行时 Script.pvf 实测（见 initial_town_seria_room_real_pvf_test.go）：
// 只有 town 38 `艾尔文防线`（`town/new_Elvengard.twn`）声明了赛丽亚房间——
// area 1 = `Cataclysm/Town/Elvengard/new_seria_room.map`，`[gate]` 450 234。
// 城镇 39/40/41/42/43/44/45/46（赫顿玛尔/西海岸/阿法利亚营地/…）的 `[area]` 全部是
// 街道、后巷、酒馆或 `[dungeon gate]`，没有任何 seria 房间；个别活动镇
// （107/110/111 等）虽然文本里出现 "seriaroom"（`[show cutscene seriaroom out]`）
// 或 dream_seria_room 地图，但不是常住城镇的赛丽亚房间。
// 因此本文件只识别物理房间区域，不据此判断房间所属城镇。普通登录还会查街道地图
// 的真实房门（initial_town_linked_seria.go）；共用38/1时仍保留来源城镇的房门及世界上下文。

// currentTownSeriaRoomArea 返回该城镇的赛丽亚房间区域。
//
// 判据只有运行时 PVF 的地图路径；同一个城镇文档里区域号唯一，命中多个时取最小
// area_id，保证同一份 PVF 下结果稳定。
func (s *Service) currentTownSeriaRoomArea(townID int64) (dnftown.Area, bool) {
	areas, found := s.townAreas(townID)
	if !found {
		return dnftown.Area{}, false
	}
	room := dnftown.Area{}
	foundRoom := false
	for _, area := range areas {
		if area.MapPath == "" || !townMapPathLooksLikeSeria(area.MapPath) {
			continue
		}
		if !foundRoom || area.ID < room.ID {
			room = area
			foundRoom = true
		}
	}
	return room, foundRoom
}

// currentTownSeriaRoomLandingPosition 取"该房间的入口坐标"：优先区域自己声明的
// `[gate]`，退而取该 area 声明的默认落点（`[normal]` 后面直接跟的两个整数）。
// 两者都没有时返回 false：运行时 PVF 没有给出真实坐标，绝不合成一个。
func currentTownSeriaRoomLandingPosition(area dnftown.Area) (int64, int64, string, bool) {
	if area.Gate != nil {
		return area.Gate.X, area.Gate.Y, "runtime_pvf_seria_area_gate", true
	}
	if area.DefaultPosition != nil {
		return area.DefaultPosition.X, area.DefaultPosition.Y, "runtime_pvf_seria_area_default_position", true
	}
	return 0, 0, "", false
}

// currentTownSeriaRoomLoginLocation 解析"上一次所在城镇的赛丽亚房间"作为登录落点
// （三级落点的第一级）。
//
// 落点必须来自该城镇自己的 seria 区域：area 取该区域，坐标取该区域声明的入口/默认落点，
// direction/area_state 用城镇房间的既有常量（与旧登录路径写死的 5/3 一致）。
//
// 第二个返回值表示解析成功；失败时第三个返回值是写进日志 fallback_reason 的原因，
// 调用方据此进入第二级（该城镇的持久化精确落点）或第三级（38/1）。
func (s *Service) currentTownSeriaRoomLoginLocation(townID int64) (currentTownLoginLocation, bool, string) {
	room, found := s.currentTownSeriaRoomArea(townID)
	if !found {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_has_no_seria_room_in_runtime_pvf",
			townID,
		)
	}
	if room.ID < 0 || room.ID > int64(^byte(0)) {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_seria_area_id_%d_outside_u8",
			townID,
			room.ID,
		)
	}
	positionX, positionY, positionSource, ok := currentTownSeriaRoomLandingPosition(room)
	if !ok {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_seria_area_%d_has_no_landing_position",
			townID,
			room.ID,
		)
	}
	if positionX < -1<<15 || positionX > 1<<15-1 || positionY < -1<<15 || positionY > 1<<15-1 {
		return currentTownLoginLocation{}, false, fmt.Sprintf(
			"persisted_town_%d_seria_area_%d_landing_position_outside_i16",
			townID,
			room.ID,
		)
	}
	return currentTownLoginLocation{
		TownID:         byte(townID),
		AreaID:         byte(room.ID),
		PositionX:      positionX,
		PositionY:      positionY,
		Direction:      newCharacterInitialDirection,
		AreaState:      newCharacterInitialAreaState,
		MapPath:        room.MapPath,
		Source:         currentTownLoginLocationTownSeriaSource,
		Branch:         currentTownLoginBranchTownSeriaRoom,
		PositionSource: positionSource,
	}, true, ""
}
