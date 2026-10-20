package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"strings"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 本文件回答操作者清单第 36 条的另一半：**赛丽亚私有房间（登录落点 38/1）里走进
// "右边那扇门"时，应不应该由服务端弹出"选择地图"（副本选择界面）**。
//
// # 一、先写真源：这间房里到底有哪几扇门（全部来自运行时 PVF，一坐标都不写死）
//
// 真档 `Script.pvf`（sha256 见 initial_town_seria_room_real_pvf_test.go 的同源断言）
// 的 `map/cataclysm/town/elvengard/(r)new_seria_room.map` 全文只有一节门声明：
//
//	[town movable area]
//	858 183 30 100 38 0      ← **右边那扇门**（矩形 858..888 × 183..283）⇒ 艾尔文防线主街 38/0
//	433 311 0 0 39 0         ← 传送阵目的地清单第 1 条（点触发，w=h=0）
//	… 共 18 条同样以 (433,311) 为触发点的城镇目的地 …
//	49 224 34 52 101 0       ← 左边那扇门（矩形 49..83 × 224..276）⇒ 101/0 `CoupleRoom.twn`（结婚礼堂）
//	[/town movable area]
//
//	[town movable area with animation]
//	[movable area]
//	[area]
//	363 327 150 40           ← 房间中央偏左下的"传送阵"（行走区），见下面的 [is seria room warp]
//	[draw layer]
//	`[bottom]`
//	[is seria room warp]
//	1
//	[/movable area]
//	[/town movable area with animation]
//
// 三条独立证据把"右边那扇门 = 858 183 30 100 ⇒ 38/0"钉死：
//
//  1. 门矩形与 sprite 对得上：房间脚本 import 的 `map/common/(r)gate_seria.map` 的
//     `[animation]` 里有 `../Common/Seria_Room/chn_10th/right_door.ani` `[bottom]` **876** 221 0
//     与 `left_door.ani` `[bottom]` **23** 153 0；`[virtual movable area]` 的可行走包络是
//     x ∈ [30, 869]，所以 x=858 的门就是右边缘那扇、x=49 的是左边缘那扇。
//  2. 客户端上行现场（香港线上 130+ 份 packet_log）：
//     进房 = op36 目标 (38,1) 落点 **(873,233)**（= 右门矩形中心 858+30/2, 183+100/2）；
//     出房 = op36 目标 (38,0) `opaque_u16_a=38`（来源城镇）`opaque_u16_b=1`（来源区域 = 房间）
//     `opaque_u32=0` `opaque_tail_u8=6`。⇒ 客户端自己就是拿这扇门在 38/1 ↔ 38/0 之间走。
//  3. 全库 `teleport_array_request=true`（尾字节 5 = 传送阵/世界地图选点）的样本里存在
//     来源 `a=38 b=1`（就在这间房里）的那一条 ⇒ 房间里"选择地图"的那条链路（传送阵）
//     本来就是客户端自己的，服务端只回答过图。
//
// # 二、取证结论（决定这条到底该不该由服务端改）
//
// **"走到右边门弹出副本选择界面"没有任何上行包**：全库 `game-upper-town-enter-select-origin-bound`
// 共 406 条（= 客户端真正发 op15 打开选择器的每一次），按来源区域统计为
// 38/2 ×203、38/0 ×46、40/3 ×39、40/4 ×29、42/3 ×22、41/2 ×22、30/2 ×14、39/5 ×12、39/0 ×5、
// 43/1 ×4、31/2 ×4、35/1 ×3、11/2 ×2、46/4 ×1 —— **town 38 area 1（这间房）0 条**。
// 也就是说：客户端站在这间房里时**从不**请求副本选择器（`ENUM_CMDPACKET_ENTER_SELECT_DUNGEON`
// = class1/op15）；它只在 `[dungeon gate]` 区域（38/2 那类地图里 `[town movable area] 1 193 80 160 -1 -1`
// 的 `-1 -1` 行，厂商 `Town.TryFindDungeonGateReturnPosition` 注释原文 "the local dungeon-selection
// gate"）或城镇地图界面上发 op15。这间房的 PVF 里**没有** `-1 -1` 行。
// 厂商服务端同样没有赛丽亚房间分支（`Network/Handlers/TownHandler.cs` 的
// `SetUserAreaCoreAsync` 只照客户端给的 town/area 落位），它的选择页只在客户端 op15/op16
// 到达时才发（`Dungeon/DungeonEntryHandler.cs` 的 `HandleEnterSelectDungeonCore`，
// 注释 "Party followers do not send ENTER_SELECT_DUNGEON themselves."）。
//
// ⇒ **本文件不是"修好一个已知缺陷"，而是一个默认关闭的 A/B 实验阀**：开关打开时，把
// "在赛丽亚私有房间里走进它自己城镇那扇门"这一条 op36 **换成**走既有的
// `sendEnterSelectDungeonState`（class1/op15 成功体 + 疲劳 + op27 选择页上下文，
// 与 `force_select_trigger.go` 逐字节同一条发送路径）。默认档逐字节等于今天：右侧门照旧
// 过图回主街，零新增日志。
//
// # 三、判据（窄到什么程度，每个字段的来源）
//
// 生效必须**同时**满足，任何一条不满足都 fail-closed（不动作，走今天的过图）：
//
//	① 当前权威位置（`character.Stats` 的 town_id/area_id，经 `currentSceneTransitionLocation`）
//	   落在**运行时 PVF 声明的赛丽亚房间**里：`currentTownSeriaRoomArea(当前城镇)` 返回的
//	   room.ID == 当前 area（判据 = 该区域地图路径含 "seria"，见 town_seria_room.go）。
//	   真档实测只有 town 38 area 1 命中（initial_town_seria_room_real_pvf_test.go）。
//	② 请求目标城镇 == 当前城镇（右侧门通向的正是**房间自己城镇**的主街）。
//	③ 该房间的**地图文档**里存在一条"走过去的门"触发点，其目标 == 请求目标 (town, area)：
//	   section = `town movable area`（六元组，带 w/h）且 w>0、h>0（点触发 w=h=0 是传送阵
//	   目的地清单，不是一扇门）；且**唯一**（多于一条 ⇒ 判据不成立，fail-closed）。
//	   元素与顺序由 `currentSeriaRoomDoorTriggersInMapOrder` 从 `Map.SourceSections` 的
//	   原始 token 读出（既有实现，见 town_seria_room_door.go）。
//	④ 请求本身是当前客户端的"走过去开门"现场形状（全库实测，见上面证据 2）：
//	   `OpaqueU32 == 0`（排除传送药剂，那条带道具 id）、`OpaqueTailU8 == 6`（排除传送阵/世界地图
//	   选点的 5 与普通过图的 0）、`OpaqueU16A == 当前城镇`、`OpaqueU16B == 当前区域`。
//	⑤ `currentTownEnterSelectReady` 为真（否则选择页只有背景、没有副本列表 ⇒ 宁可不换行为）。
//
// 这套判据里没有任何写死的坐标：门的坐标只用于日志判读，"右侧"这个方位由 PVF 的门矩形
// （858..888 落在可行走包络 30..869 的最右）与 `right_door.ani` 的 x=876 共同说明，
// 判据本身只吃"目标 (town, area)"这一个来源 PVF 的字段。
//
// # 四、开关
//
//	`DNFBRIDGE_SERIA_RIGHT_DOOR_SELECT`
//	  - 默认（空）/其它任何值 = **今天的行为**：右侧门照旧过图回主街，逐字节不变、零新增日志；
//	  - `on` / `1` / `true` / `yes` / `enable` / `enabled` = 打开实验档：右侧门触发改为
//	    走 `sendEnterSelectDungeonState`（不发 op24 过图）。
//
// ⚠️ 打开档的已知语义代价（如实标注）：这条请求原本会让客户端过图到主街；换成选择页后
// 客户端停在房间里、屏幕上出现副本选择界面。若现场观察结果是"界面不对/卡住"，把开关删掉
// 或无值重启即逐字节回到今天。
const seriaRightDoorSelectEnv = "DNFBRIDGE_SERIA_RIGHT_DOOR_SELECT"

// seriaRightDoorSelectSource 是这条实验档自己的事件 source 标签（与 force_select 的
// `force_select_command_file` 区分开，便于现场判读是哪条路径打开的选择页）。
const seriaRightDoorSelectSource = "seria_right_door_select_switch"

// currentSeriaRoomDoorWalkInTailByte 是当前客户端"走进一扇门"过图请求的尾字节现场实测值。
// 出处：全部 packet_log 里"从房间 38/1 到主街 38/0"的 `game-town-set-user-area-transition-send`
// 都是 `opaque_tail_u8=6`（例如 opts: a=38 b=1 u32=0 tail=6）；同镇的普通过图（主街 → 房间）
// 是 tail=0，传送阵/世界地图选点是 tail=5。只在开关打开档参与判据，默认档完全不读它。
const currentSeriaRoomDoorWalkInTailByte = 6

// seriaRightDoorSelectEnabled 只有显式打开值才算开（与工程其它实验阀同一口径：打错字 = 保持今天）。
func seriaRightDoorSelectEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(seriaRightDoorSelectEnv))) {
	case "on", "1", "true", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}

// currentSeriaRoomExitDoor 是"这间房通向它自己城镇的那扇走过去的门"（PVF 触发点原始矩形）。
type currentSeriaRoomExitDoor struct {
	TownID  int64
	AreaID  int64
	X       int64
	Y       int64
	Width   int64
	Height  int64
	Section string
}

// currentSeriaRoomExitDoorForRequest 在房间自己的地图文档里找"目标 == 请求目标"的那扇
// 走过去的门。返回值 found=false 时 reason 说明失败关闭的原因。
func (s *Service) currentSeriaRoomExitDoorForRequest(
	room dnftown.Area,
	targetTownID int64,
	targetAreaID int64,
) (currentSeriaRoomExitDoor, bool, string) {
	if targetTownID < 0 || targetTownID > int64(^byte(0)) ||
		targetAreaID < 0 || targetAreaID > int64(^byte(0)) {
		return currentSeriaRoomExitDoor{}, false, "request_target_outside_u8"
	}
	if targetAreaID == room.ID {
		// 目标就是这间房自己：不是"走出去的门"。
		return currentSeriaRoomExitDoor{}, false, "request_target_is_the_room_itself"
	}
	readMap, ok, reason := s.currentSeriaRoomDoorMapReader()
	if !ok {
		return currentSeriaRoomExitDoor{}, false, reason
	}
	parsed, ok := readMap(room)
	if !ok {
		return currentSeriaRoomExitDoor{}, false, "seria_room_map_document_unavailable_in_runtime_pvf"
	}
	matched := make([]currentSeriaRoomDoorTrigger, 0, 1)
	for _, trigger := range currentSeriaRoomDoorTriggersInMapOrder(parsed) {
		// 只有六元组形式的 `[town movable area]` 门带 w/h；带开门动画的那一节由
		// `[move town]` 声明，其矩形语义不同，这里明确不认。
		if trigger.Section != "town_movable_area" {
			continue
		}
		// 点触发（w=h=0）= 传送阵/世界地图的目的地清单，不是一扇"走过去的门"。
		if trigger.Width <= 0 || trigger.Height <= 0 {
			continue
		}
		if trigger.TargetTownID != targetTownID || trigger.TargetAreaID != targetAreaID {
			continue
		}
		matched = append(matched, trigger)
	}
	if len(matched) == 0 {
		return currentSeriaRoomExitDoor{}, false,
			"seria_room_map_declares_no_walk_through_door_to_the_requested_target"
	}
	if len(matched) > 1 {
		// 判据必须唯一：PVF 里同一目标有多扇门时无法确定客户端走的是哪一扇。
		return currentSeriaRoomExitDoor{}, false,
			"seria_room_map_declares_multiple_walk_through_doors_to_the_requested_target"
	}
	trigger := matched[0]
	return currentSeriaRoomExitDoor{
		TownID:  targetTownID,
		AreaID:  targetAreaID,
		X:       trigger.X,
		Y:       trigger.Y,
		Width:   trigger.Width,
		Height:  trigger.Height,
		Section: trigger.Section,
	}, true, ""
}

// looksLikeSeriaRoomDoorWalkIn 是判据 ④：当前客户端的"走过去开门"现场形状。
func (request currentTownSetUserAreaRequest) looksLikeSeriaRoomDoorWalkIn(
	sourceTownID int64,
	sourceAreaID int64,
) bool {
	if sourceTownID <= 0 || sourceAreaID < 0 || sourceAreaID > int64(^byte(0)) {
		return false
	}
	return request.OpaqueU32 == 0 &&
		request.OpaqueTailU8 == currentSeriaRoomDoorWalkInTailByte &&
		int64(request.OpaqueU16A) == sourceTownID &&
		int64(request.OpaqueU16B) == sourceAreaID
}

// tryHandleSeriaRoomRightDoorSelect 在开关打开时，把"赛丽亚私有房间里走进它自己城镇那扇门"
// 这一条 op36 换成"打开副本选择地图"。
//
// 返回值 handled=true 表示这条请求已被本实验档消费（调用方直接 return，不再走城镇过图）；
// handled=false 表示按今天的路径继续（开关关闭、或任何一条判据不满足的 fail-closed）。
//
// 调用点必须在 `session.townMu` 之外：`sendEnterSelectDungeonState` 自己会取这把锁。
func (s *Service) tryHandleSeriaRoomRightDoorSelect(
	session *gameSession,
	request currentTownSetUserAreaRequest,
) (bool, error) {
	if s == nil || session == nil || !seriaRightDoorSelectEnabled() {
		return false, nil
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		return false, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterKey := strconv.FormatUint(uint64(characterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterKey)
	if err != nil {
		return false, err
	}
	if !found || character.Stats == nil {
		// 现场路径自己会记 selected_character_location_missing，这里不重复。
		return false, nil
	}
	currentTownID, currentAreaID, err := currentSceneTransitionLocation(character, true)
	if err != nil {
		// 位置不完整：fail-closed，交给今天的路径按既有口径拒绝并记日志。
		return false, nil
	}
	room, roomFound := s.currentTownSeriaRoomArea(int64(currentTownID))
	if !roomFound || room.ID != int64(currentAreaID) {
		// 判据 ①：当前不在 PVF 声明的赛丽亚私有房间里 ⇒ 一个字都不碰（连日志都不记）。
		return false, nil
	}
	if int64(request.TownID) != int64(currentTownID) {
		// 判据 ②：目标不是"房间自己城镇"。左门（101/0 结婚礼堂）、传送阵（39/0 等）
		// 与跨城镇门都在这里被挡掉，且不记日志（它们本来就有各自的既有路径）。
		return false, nil
	}
	door, doorFound, doorReason := s.currentSeriaRoomExitDoorForRequest(
		room, int64(request.TownID), int64(request.AreaID))
	if !doorFound {
		s.logGameEvent(session, "game-town-seria-right-door-select-skipped",
			"char_id", characterID,
			"town_id", currentTownID,
			"area_id", currentAreaID,
			"map_path", room.MapPath,
			"target_town_id", request.TownID,
			"target_area_id", request.AreaID,
			"reason", doorReason,
			"response", "today_transition")
		return false, nil
	}
	if !request.looksLikeSeriaRoomDoorWalkIn(int64(currentTownID), int64(currentAreaID)) {
		s.logGameEvent(session, "game-town-seria-right-door-select-skipped",
			"char_id", characterID,
			"town_id", currentTownID,
			"area_id", currentAreaID,
			"map_path", room.MapPath,
			"target_town_id", request.TownID,
			"target_area_id", request.AreaID,
			"door_trigger_x", door.X,
			"door_trigger_y", door.Y,
			"door_trigger_width", door.Width,
			"door_trigger_height", door.Height,
			"opaque_u16_a", request.OpaqueU16A,
			"opaque_u16_b", request.OpaqueU16B,
			"opaque_u32", request.OpaqueU32,
			"opaque_tail_u8", request.OpaqueTailU8,
			"reason", "request_is_not_the_current_client_walk_in_door_shape",
			"response", "today_transition")
		return false, nil
	}
	ready, readyReason := s.currentTownEnterSelectReady(session)
	if !ready {
		// 判据 ⑤：选择页此刻装不出来（只有背景、没有副本列表）⇒ 宁可不换行为。
		s.logGameEvent(session, "game-town-seria-right-door-select-skipped",
			"char_id", characterID,
			"town_id", currentTownID,
			"area_id", currentAreaID,
			"map_path", room.MapPath,
			"target_town_id", request.TownID,
			"target_area_id", request.AreaID,
			"reason", readyReason,
			"response", "today_transition")
		return false, nil
	}
	if err := s.sendEnterSelectDungeonState(session, seriaRightDoorSelectSource, false, true); err != nil {
		return true, err
	}
	s.logGameEvent(session, "game-town-seria-right-door-select-open",
		"char_id", characterID,
		"town_id", currentTownID,
		"area_id", currentAreaID,
		"map_path", room.MapPath,
		"target_town_id", request.TownID,
		"target_area_id", request.AreaID,
		"door_town_id", door.TownID,
		"door_area_id", door.AreaID,
		"door_trigger_x", door.X,
		"door_trigger_y", door.Y,
		"door_trigger_width", door.Width,
		"door_trigger_height", door.Height,
		"door_trigger_section", door.Section,
		"opaque_u16_a", request.OpaqueU16A,
		"opaque_u16_b", request.OpaqueU16B,
		"opaque_u32", request.OpaqueU32,
		"opaque_tail_u8", request.OpaqueTailU8,
		"switch", seriaRightDoorSelectEnv,
		"source", seriaRightDoorSelectSource,
		"response", "enter_select_state_instead_of_town_transition",
		"reason", "operator_item_36_right_door_should_open_the_map_selector")
	return true, nil
}
