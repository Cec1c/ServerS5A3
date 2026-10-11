package dnfbridge

import (
	"os"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件解析"走出赛丽亚房间以后应该落在城镇的哪个 (城镇, 区域, 坐标)"。
// 判据全部来自运行时 PVF，一坐标都不写死。
//
// # 报障一（2026-09-25 19:2x，香港 packet_log.20260925-192600.txt）
//
// 操作者原话：「从赛丽亚房间出来会到副本门口，而不是赛丽亚房间外门口」。
// 现场（会话 game-000016 角色 6，登录落点=三级规则的赛丽亚房间 38/1 450,234）：
//
//	:2456 RECV type=1425 ENUM_CMDPACKET_PREV_VILLAGE        ← 客户端出房间前问"回哪"
//	:2461 EVENT game-town-prev-village-op24-send town_id=38 area_id=2
//	      position_x=1085 position_y=249 map_path=.../new_D_Elvengard_L.map   ← 我们答"副本门口"
//	:2467 EVENT game-town-set-user-area-committed town_id=38 area_id=0 position_x=55 ...
//	      （客户端随即落到 area 0 的 (55,267) = 通往 area 2 的门触发点中心）
//
// 也就是说**出房间的落点完全由我们对 op1425 的回答决定**，而当时
// `session.townPrevVillageSnapshot` 来自登录时绑定的"持久化上一位置"
// （`initial_town_login_owner.go` 的 character_list_login_existing_persisted_prev_village），
// 那个位置恰好是**副本门口**（38/2 `Cataclysm/Town/Elvengard/new_D_Elvengard_L.map`，
// PVF 里该 area 声明的就是 `[dungeon gate] 2`）。第二次起（玩家自己走门进过房间）原点
// 变成门边的真实坐标，落点就对了——与操作者"只有第一次错"的观感一致。
//
// 该轮的修法：出口落点改为"**该房间所挂城镇**的外门口"（见下面 resolveSeriaRoomOuterDoor…）。
//
// # 报障二（2026-09-25 20:2x，香港 packet_log.txt，即本文件本轮修掉的缺陷）
//
// 操作者原话：「赛丽亚房间 下方显示西海岸 我也应该在西海岸 但是我从下面门出去进入了艾尔文防线。
// 我最一开始是从西海岸进入赛丽亚房间的，所以下发的显示是对的」。
// 现场（会话 game-000003 角色 3）：
//
//	:403 EVENT game-town-prev-village-origin-bound char_id=3 origin_town_id=40 origin_area_id=0
//	     origin_position_x=635 origin_position_y=265 origin_position_valid=true
//	     source=current_exe_op36_enter_seria_room      ← 进来时的来源城镇 = 40 西海岸（服务端有！）
//	:404 EVENT game-town-set-user-area-transition-send town_id=38 area_id=1 position_x=433
//	     position_y=311 map_path=.../new_seria_room.map
//	:414 EVENT game-town-private-area-entered town_id=38 area_id=1
//	:492 EVENT game-town-prev-village-op24-send town_id=38 area_id=0 position_x=622 position_y=196
//	     landing_source=seria_room_outer_door door_town_id=38 door_area_id=0
//	     door_trigger_x=622 door_trigger_y=196      ← 答的是**艾尔文防线**，来源城镇 40 被丢掉
//
// 根因一句话：出口落点写死成"该房间所挂城镇（38 艾尔文防线）的外门口"，而不是
// "我进来时所在的那个城镇"。来源城镇在会话里一直存在（op36 进房时绑定的 origin），只是出口没用它。
//
// # PVF 依据（真档 runtime/data/dnf/Script.pvf，逐字段）
//
//   - town 38 `艾尔文防线`（town/new_Elvengard.twn）：
//     area 0 = `Cataclysm/Town/Elvengard/new_Elvengard.map`（`[normal]`，街道）
//     area 1 = `Cataclysm/Town/Elvengard/new_seria_room.map`（`[gate]` 450 234，赛丽亚房间）
//     area 2 = `Cataclysm/Town/Elvengard/new_D_Elvengard_L.map`（`[dungeon gate] 2`，副本门口）
//   - town 40 `西海岸`（town/new_westcoast.twn）：
//     area 0 = `Cataclysm\Town\west_coast/WestCoast.map`，area 1 = `WestCoast_post.map`，
//     area 2 = `Magicguild.map` …（全部 `[normal]`/`[dungeon gate]`，没有 seria 房间）
//   - 街道地图 `new_elvengard.map` 的 `[town movable area]` 六元组（x y w h 城镇 区域）里，
//     通往赛丽亚房间（38/1）的有两条：`622 196 0 0 38 1` 与 `571 129 50 50 38 1`；
//     文档序第一条 ⇒ 门口矩形中心 = (622,196)。
//   - **跨城镇的赛丽亚房门在 PVF 里是另一种声明形式**：不是 `[town movable area]` 六元组，
//     而是带开门动画的
//     `[town movable area with animation]` → `[movable area]` → `[area] x y w h` +
//     `[move town] 城镇 区域`。真档扫全文（pvftextscan 'move town'）实测该形式是
//     **PVF 全局的赛丽亚房门写法**，例如：
//     西海岸 `map/cataclysm/town/west_coast/WestCoast.map` 的 `[area] 473 127 100 30` +
//     `[move town] 38 1` ⇒ 门口中心 (523,142)；
//     赫顿玛尔 `new_Hendon_Main.map` `546 129 100 30`；后巷 `new_backstreet_2.map`
//     `265 173 100 30`；alfhlyra_r / underfoot_north / stormpass / metrocenter /
//     centralpark / gent / jelva / lufthafen / sainthorn / shonan / slough … 同形，
//     目标几乎都是 `38 1`（少数活动镇指向 98/0、19/0、32/0 等别的赛丽亚房）。
//     ⇒ 出口要在**来源城镇**里找门，就必须认这一种形式；只认六元组会漏掉西海岸这样的城镇。
//   - 客户端用的是"进入某区域时落在该区域里**指回来源区域**的那个触发点"这条规则：
//     area 2 的 `1040 157 90 185 38 0` 中心 = (1085,249)，正是客户端进副本门口时报的坐标；
//     area 0 的 `10 196 90 142 38 2` 中心 = (55,267)，正是客户端从副本门口回来时报的坐标。
//     厂商 9.16 参考实现的同一动作落在 (642,193)
//     （参考日志 packet_log.txt:2212 出房间 op36 town=38 area=0 position=642,193），
//     紧邻同一个触发点 (622,196) ⇒ 参考实现口径与本文件推出的门口一致。
//
// # 开关
//
// `DNFBRIDGE_TOWN_SERIA_EXIT_DOOR`：
//   - 默认（空）/`entry_town`/`entry`/`pvf_door`/`pvf`/`1`/`on`/`true`/`yes`
//     = **修好后的行为**：回到"进来时所在的那个城镇/区域"，坐标取该来源城镇 PVF 里
//     指向本房间的触发点矩形中心；来源城镇里没有这条触发点时退回玩家进门时站的那一点。
//   - `room_town_door`/`room_town`/`fixed_door`/`fixed` = 一键回退到**上一轮**口径
//     （房间所在城镇的固定外门口，忽略来源城镇）。
//   - `prev_village`/`prev`/`off`/`0`/`false`/`no`/`legacy` = 回退到**更旧**口径
//     （沿用登录/进城时绑定的 prev-village 原点，逐字节不变）。
const currentTownSeriaExitDoorEnv = "DNFBRIDGE_TOWN_SERIA_EXIT_DOOR"

// currentTownSeriaExitMode 是 DNFBRIDGE_TOWN_SERIA_EXIT_DOOR 的三档语义。
type currentTownSeriaExitMode uint8

const (
	// currentTownSeriaExitModeEntryTown（默认）= 回到进来时所在的那个城镇/区域。
	currentTownSeriaExitModeEntryTown currentTownSeriaExitMode = iota
	// currentTownSeriaExitModeRoomTownDoor = 上一轮口径：房间所在城镇的固定外门口。
	currentTownSeriaExitModeRoomTownDoor
	// currentTownSeriaExitModePrevVillage = 更旧口径：登录/进城时绑定的 prev-village 原点。
	currentTownSeriaExitModePrevVillage
)

// currentTownSeriaExitDoorMode 解析开关。无法识别的取值一律按**修好后的默认档**处理
// （把"打错字"变成"用新行为"，而不是悄悄退回旧行为）。
func currentTownSeriaExitDoorMode() currentTownSeriaExitMode {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentTownSeriaExitDoorEnv)))
	switch value {
	case "room_town_door", "room_town", "fixed_door", "fixed":
		return currentTownSeriaExitModeRoomTownDoor
	case "prev_village", "prev", "off", "0", "false", "no", "legacy":
		return currentTownSeriaExitModePrevVillage
	default:
		// "", "entry_town", "entry", "pvf_door", "pvf", "1", "on", "true", "yes"
		return currentTownSeriaExitModeEntryTown
	}
}

// currentTownSeriaExitDoorModeName 把开关档位写成日志字符串（判读用）。
func currentTownSeriaExitDoorModeName() string {
	switch currentTownSeriaExitDoorMode() {
	case currentTownSeriaExitModeRoomTownDoor:
		return "room_town_door"
	case currentTownSeriaExitModePrevVillage:
		return "prev_village"
	default:
		return "entry_town"
	}
}

// currentSeriaRoomDoorKey 是解析缓存的键：一个赛丽亚房间 (城镇, 区域) + 进来时的来源
// (城镇, 区域)。EntryTownID/EntryAreaID 为 -1 表示"来源未知"（登录直接落在房间那条路径）。
type currentSeriaRoomDoorKey struct {
	TownID      int64
	AreaID      int64
	EntryTownID int64
	EntryAreaID int64
}

// currentSeriaRoomDoor 是"赛丽亚房间门口"的完整描述（含触发点原始矩形，便于日志判读）。
type currentSeriaRoomDoor struct {
	TownID    byte
	AreaID    byte
	MapPath   string
	PositionX uint16
	PositionY uint16
	TriggerX  int64
	TriggerY  int64
	TriggerW  int64
	TriggerH  int64
	// TriggerSection 是这条门在 PVF 里的声明形式：
	// "town movable area"（六元组）或 "town movable area with animation"（带开门动画的传送门）。
	TriggerSection string
}

type currentSeriaRoomDoorEntry struct {
	Door   currentSeriaRoomDoor
	Found  bool
	Reason string
}

// currentSeriaRoomOuterDoorLocation 是"该房间所挂城镇的固定外门口"（上一轮口径）。
// 等价于"来源城镇未知"的 currentSeriaRoomReturnDoorLocation。
func (s *Service) currentSeriaRoomOuterDoorLocation(townID, areaID int64) (currentSeriaRoomDoor, bool, string) {
	return s.currentSeriaRoomReturnDoorLocation(townID, areaID, -1, -1)
}

// currentSeriaRoomReturnDoorLocation 返回"走出这间赛丽亚房间"的落点门（带进程级缓存）。
//
// entryTownID/entryAreaID 是玩家**进来时**所在的城镇/区域；-1 表示未知。已知来源城镇时，
// 门在**来源城镇**里找（优先来源区域自己的地图）——从西海岸进的房间出来就回西海岸；
// 未知时才退回房间所在城镇的固定外门口（上一轮口径，登录直接落在房间那条路径）。
//
// 解析失败时 found=false 且给出 reason；调用方必须保守回退，绝不编造坐标。
func (s *Service) currentSeriaRoomReturnDoorLocation(
	seriaTownID, seriaAreaID, entryTownID, entryAreaID int64,
) (currentSeriaRoomDoor, bool, string) {
	if s == nil {
		return currentSeriaRoomDoor{}, false, "service_unavailable"
	}
	key := currentSeriaRoomDoorKey{
		TownID:      seriaTownID,
		AreaID:      seriaAreaID,
		EntryTownID: entryTownID,
		EntryAreaID: entryAreaID,
	}
	s.seriaRoomDoorMu.Lock()
	if entry, ok := s.seriaRoomDoors[key]; ok {
		s.seriaRoomDoorMu.Unlock()
		return entry.Door, entry.Found, entry.Reason
	}
	s.seriaRoomDoorMu.Unlock()

	door, found, reason := s.resolveCurrentSeriaRoomReturnDoorLocation(
		seriaTownID, seriaAreaID, entryTownID, entryAreaID)
	s.seriaRoomDoorMu.Lock()
	if s.seriaRoomDoors == nil {
		s.seriaRoomDoors = make(map[currentSeriaRoomDoorKey]currentSeriaRoomDoorEntry, 4)
	}
	s.seriaRoomDoors[key] = currentSeriaRoomDoorEntry{Door: door, Found: found, Reason: reason}
	s.seriaRoomDoorMu.Unlock()
	return door, found, reason
}

// resolveCurrentSeriaRoomOuterDoorLocation 是上一轮口径的解析入口（房间所在城镇）。
// 真档钉子用例直接调它，所以保留这个名字。
func (s *Service) resolveCurrentSeriaRoomOuterDoorLocation(townID, areaID int64) (currentSeriaRoomDoor, bool, string) {
	return s.resolveCurrentSeriaRoomReturnDoorLocation(townID, areaID, -1, -1)
}

// resolveCurrentSeriaRoomReturnDoorLocation 在运行时的城镇目录 + 地图文档里找"门"：
// 门所在的城镇 = 来源城镇（未知时 = 房间所在城镇），区域优先来源区域自己的地图。
func (s *Service) resolveCurrentSeriaRoomReturnDoorLocation(
	seriaTownID, seriaAreaID, entryTownID, entryAreaID int64,
) (currentSeriaRoomDoor, bool, string) {
	seriaArea, found := s.townArea(seriaTownID, seriaAreaID)
	if !found || !townMapPathLooksLikeSeria(seriaArea.MapPath) {
		return currentSeriaRoomDoor{}, false, "current_area_is_not_a_seria_room_in_runtime_pvf"
	}
	readMap, ok, reason := s.currentSeriaRoomDoorMapReader()
	if !ok {
		return currentSeriaRoomDoor{}, false, reason
	}
	doorTownID := seriaTownID
	preferredAreaID := int64(-1)
	if entryTownID >= 0 && entryAreaID >= 0 {
		doorTownID = entryTownID
		preferredAreaID = entryAreaID
	}
	areas, areasOK := s.townAreas(doorTownID)
	if !areasOK {
		if doorTownID == seriaTownID {
			return currentSeriaRoomDoor{}, false, "town_areas_unavailable_in_runtime_pvf"
		}
		return currentSeriaRoomDoor{}, false, "entry_town_areas_unavailable_in_runtime_pvf"
	}
	return resolveSeriaRoomDoorFromAreas(seriaTownID, seriaAreaID, doorTownID, preferredAreaID, areas, readMap)
}

// currentSeriaRoomDoorMapReader 返回"读一份城镇区域地图文档"的函数：生产走运行时 PVF，
// 单测注入 seriaRoomDoorMapReader。
func (s *Service) currentSeriaRoomDoorMapReader() (func(area dnftown.Area) (worldmap.Map, bool), bool, string) {
	if s == nil {
		return nil, false, "service_unavailable"
	}
	if s.seriaRoomDoorMapReader != nil {
		return s.seriaRoomDoorMapReader, true, ""
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil || archive == nil {
		return nil, false, "runtime_pvf_unavailable"
	}
	return func(area dnftown.Area) (worldmap.Map, bool) {
		return currentTownAreaMapDocument(archive, area)
	}, true, ""
}

// resolveSeriaRoomOuterDoorFromAreas 是上一轮口径的纯判定（等价于"来源城镇未知"）。
func resolveSeriaRoomOuterDoorFromAreas(
	townID int64,
	seriaAreaID int64,
	areas []dnftown.Area,
	readMap func(area dnftown.Area) (worldmap.Map, bool),
) (currentSeriaRoomDoor, bool, string) {
	return resolveSeriaRoomDoorFromAreas(townID, seriaAreaID, townID, -1, areas, readMap)
}

// resolveSeriaRoomDoorFromAreas 是纯判定：在 doorTown 的区域里按"优先来源区域、其余文档序"
// 依次读地图文档，取第一条"走进某矩形 ⇒ 传送到该赛丽亚房间"的触发点，门口 = 它的矩形中心。
//
// 这条判据只吃 PVF 文档，读地图的方式由调用方注入 ⇒ 单测可以直接喂两份不同坐标的地图，
// 证明落点**随 PVF 变化**（不是写死的坐标）。
func resolveSeriaRoomDoorFromAreas(
	seriaTownID int64,
	seriaAreaID int64,
	doorTownID int64,
	preferredAreaID int64,
	areas []dnftown.Area,
	readMap func(area dnftown.Area) (worldmap.Map, bool),
) (currentSeriaRoomDoor, bool, string) {
	if readMap == nil {
		return currentSeriaRoomDoor{}, false, "town_area_map_reader_unavailable"
	}
	for _, candidate := range orderSeriaRoomDoorCandidateAreas(areas, preferredAreaID) {
		if candidate.MapPath == "" || townMapPathLooksLikeSeria(candidate.MapPath) {
			continue
		}
		parsed, ok := readMap(candidate)
		if !ok {
			continue
		}
		if door, found := seriaRoomDoorInParsedMap(seriaTownID, seriaAreaID, doorTownID, candidate, parsed); found {
			return door, true, ""
		}
	}
	return currentSeriaRoomDoor{}, false, "no_town_area_declares_a_door_to_the_seria_room"
}

// orderSeriaRoomDoorCandidateAreas 把候选区域排成"优先区域在前，其余保持文档序"。
func orderSeriaRoomDoorCandidateAreas(areas []dnftown.Area, preferredAreaID int64) []dnftown.Area {
	if preferredAreaID < 0 {
		return areas
	}
	ordered := make([]dnftown.Area, 0, len(areas))
	for _, area := range areas {
		if area.ID == preferredAreaID {
			ordered = append(ordered, area)
		}
	}
	if len(ordered) == 0 {
		return areas
	}
	for _, area := range areas {
		if area.ID != preferredAreaID {
			ordered = append(ordered, area)
		}
	}
	return ordered
}

// seriaRoomDoorInParsedMap 在一份地图文档里找"通往 (seriaTownID, seriaAreaID)"的第一条
// 门触发点（文档序），并投影成门口落点。
func seriaRoomDoorInParsedMap(
	seriaTownID int64,
	seriaAreaID int64,
	doorTownID int64,
	area dnftown.Area,
	parsed worldmap.Map,
) (currentSeriaRoomDoor, bool) {
	for _, trigger := range currentSeriaRoomDoorTriggersInMapOrder(parsed) {
		if trigger.TargetTownID != seriaTownID || trigger.TargetAreaID != seriaAreaID {
			continue
		}
		centerX := trigger.X + trigger.Width/2
		centerY := trigger.Y + trigger.Height/2
		if centerX < 0 || centerX > int64(^uint16(0)) ||
			centerY < 0 || centerY > int64(^uint16(0)) {
			continue
		}
		return currentSeriaRoomDoor{
			TownID:         byte(doorTownID),
			AreaID:         byte(area.ID),
			MapPath:        area.MapPath,
			PositionX:      uint16(centerX),
			PositionY:      uint16(centerY),
			TriggerX:       trigger.X,
			TriggerY:       trigger.Y,
			TriggerW:       trigger.Width,
			TriggerH:       trigger.Height,
			TriggerSection: trigger.Section,
		}, true
	}
	return currentSeriaRoomDoor{}, false
}

// currentSeriaRoomDoorTrigger 是一条候选门触发点（PVF 两种声明形式的统一投影）。
type currentSeriaRoomDoorTrigger struct {
	X            int64
	Y            int64
	Width        int64
	Height       int64
	TargetTownID int64
	TargetAreaID int64
	Section      string
}

// currentSeriaRoomDoorTriggersInMapOrder 按**文档序**枚举一份地图里所有
// "走进某矩形 ⇒ 传送到 (城镇, 区域)" 的触发点，覆盖 PVF 的两种声明形式：
//
//  1. `[town movable area]` 的六元组 `x y w h 城镇 区域`
//     （艾尔文防线 `new_Elvengard.map` 通往 38/1 的 `622 196 0 0 38 1`）；
//  2. `[town movable area with animation]` → `[movable area]` → `[area] x y w h`
//     + `[move town] 城镇 区域`（带开门动画的赛丽亚房门；西海岸 `WestCoast.map` 的
//     `[area] 473 127 100 30` + `[move town] 38 1`）。
//
// 第 2 种是本缺陷的关键：它在 worldmap 的已解析字段里**没有**（`[town movable area with
// animation]` 不在 knownMapSections 里，整节落进 UnknownSections/Extensions），所以这里直接读
// `Map.SourceSections`——那里保留了每一节的**原始 token 与作用域**，一坐标都不用写死。
// SourceSections 为空（单测直接构造的 Map）时退回已解析的 `TownMovableAreas`。
func currentSeriaRoomDoorTriggersInMapOrder(parsed worldmap.Map) []currentSeriaRoomDoorTrigger {
	if len(parsed.SourceSections) == 0 {
		out := make([]currentSeriaRoomDoorTrigger, 0, len(parsed.TownMovableAreas))
		for _, movable := range parsed.TownMovableAreas {
			out = append(out, currentSeriaRoomDoorTrigger{
				X:            movable.X,
				Y:            movable.Y,
				Width:        movable.Width,
				Height:       movable.Height,
				TargetTownID: movable.Params[0],
				TargetAreaID: movable.Params[1],
				Section:      "town_movable_area",
			})
		}
		return out
	}
	out := make([]currentSeriaRoomDoorTrigger, 0, 4)
	// inMovableArea 跟踪"当前这一节是不是带开门动画那一段里的 [movable area]"。
	// 逐节顺序扫描（而不是按作用域字符串分组），同一张地图里出现多段动画门也不会串味。
	// 注意 [town movable area] 这一节在真档里只到第一个子节为止（PVF 的每一行 `[名]`
	// 都关闭上一节），所以它天然只含六元组；嵌套的 [quest condition]、[area]、
	// [move town] 各自是独立小节，不会污染数值行。
	inMovableArea := false
	rect := [4]int64{}
	rectSet := false
	targetTownID, targetAreaID := int64(0), int64(0)
	targetSet := false
	emitAnimated := func() {
		if rectSet && targetSet {
			out = append(out, currentSeriaRoomDoorTrigger{
				X:            rect[0],
				Y:            rect[1],
				Width:        rect[2],
				Height:       rect[3],
				TargetTownID: targetTownID,
				TargetAreaID: targetAreaID,
				Section:      "town_movable_area_with_animation",
			})
		}
		rectSet = false
		targetSet = false
	}
	for _, section := range parsed.SourceSections {
		key := currentSeriaRoomDoorSectionKey(section.Name)
		switch key {
		case "movable area":
			emitAnimated()
			inMovableArea = currentSeriaRoomDoorScopeContains(section.Scope, "town movable area with animation")
		case "/movable area":
			emitAnimated()
			inMovableArea = false
		case "town movable area":
			values := currentSeriaRoomDoorInts(section)
			for index := 0; index+5 < len(values); index += 6 {
				out = append(out, currentSeriaRoomDoorTrigger{
					X:            values[index],
					Y:            values[index+1],
					Width:        values[index+2],
					Height:       values[index+3],
					TargetTownID: values[index+4],
					TargetAreaID: values[index+5],
					Section:      "town_movable_area",
				})
			}
		case "area":
			if !inMovableArea {
				continue
			}
			values := currentSeriaRoomDoorInts(section)
			if len(values) != 4 {
				continue
			}
			rect = [4]int64{values[0], values[1], values[2], values[3]}
			rectSet = true
			if targetSet {
				emitAnimated()
			}
		case "move town":
			if !inMovableArea {
				continue
			}
			values := currentSeriaRoomDoorInts(section)
			if len(values) != 2 {
				continue
			}
			targetTownID, targetAreaID = values[0], values[1]
			targetSet = true
			if rectSet {
				emitAnimated()
			}
		}
	}
	emitAnimated()
	return out
}

// currentSeriaRoomDoorInts 按 `parseMovableAreas` 的口径取一节里的全部整数
// （符号 token 忽略），这样 `[town movable area]` 那行 18 个整数能被切成 3 条六元组。
func currentSeriaRoomDoorInts(section worldmap.RawSection) []int64 {
	out := make([]int64, 0, len(section.Tokens))
	for _, token := range section.Tokens {
		if token.Kind == dnfpvf.TokenInt {
			out = append(out, token.Int)
		}
	}
	return out
}

func currentSeriaRoomDoorSectionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func currentSeriaRoomDoorScopeContains(scope []string, want string) bool {
	for _, entry := range scope {
		if currentSeriaRoomDoorSectionKey(entry) == want {
			return true
		}
	}
	return false
}

// currentTownAreaMapDocument 按运行时 PVF 的真实档案路径读一份城镇区域地图文档。
// 城镇文档里的地图路径是 `Cataclysm/Town/.../x.map`，PVF 里的档案路径是
// `map/cataclysm/town/.../x.map`（前缀 `map/` + 大小写不敏感）；两种写法都试一次。
func currentTownAreaMapDocument(
	archive interface {
		ReadText(string) (string, error)
	},
	area dnftown.Area,
) (worldmap.Map, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(area.MapPath), "\\", "/")
	for _, candidate := range []string{
		"map/" + normalized,
		"map/" + strings.ToLower(normalized),
	} {
		text, err := archive.ReadText(candidate)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		doc, parseErr := dnfpvf.Parse(candidate, text)
		if parseErr != nil {
			continue
		}
		return worldmap.ParseMap(area.ID, candidate, doc), true
	}
	return worldmap.Map{}, false
}
