package town

import (
	"errors"
	"sort"
	"strings"
)

const DefaultList = "town/town.lst"

var (
	ErrSourceRequired = errors.New("dnf town pvf source is required")
	ErrListEmpty      = errors.New("dnf town list is empty")
	ErrDocumentEmpty  = errors.New("dnf town document is empty")
)

type Source interface {
	ReadText(string) (string, error)
}

type Options struct {
	ListPath string
}

type Gate struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

type Area struct {
	ID          int64   `json:"id"`
	MapPath     string  `json:"map_path"`
	Kind        string  `json:"kind,omitempty"`
	Gate        *Gate   `json:"gate,omitempty"`
	DungeonGate *int64  `json:"dungeon_gate,omitempty"`
	MinLevel    int64   `json:"min_level,omitempty"`
	NeedQuests  []int64 `json:"need_quests,omitempty"`
	// DefaultPosition is the optional coordinate pair a town document may declare
	// directly after the `[normal]` marker (for example
	// `[minimap rect]` 258 149 271 157 `[/minimap rect]` `[normal]` 594 234 in
	// town/180201_Wildforest.twn). Runtime PVF evidence only; it is used as a
	// landing fallback for an area that declares no `[gate]`, and it is never
	// synthesised when the document omits it.
	DefaultPosition *Gate `json:"default_position,omitempty"`
}

// boundaryGateMapNamePrefix 是 PVF 城镇图文件名里"城镇边界/门图"的**命名约定**前缀。
//
// 真档 Script.pvf（sha256 cc517a6e2b7c77a4ba0f30a1869a3ab234e281fe6251e35d60f0f04a82aacee1，
// 2026-09-27 全量扫描，53 个城镇）：15 个城镇的 area 0 地图文件名以 `Gate_` 开头
// （Gate_CentralPark.map / Gate_MetroCenter.map / Gate_SilverCrown.map …），
// 它们的 area 0 **同时**带 PVF `[gate]` 入口坐标标记，两者在这 15 个城镇上完全一致。
// 现场证据（packet_log.txt 2026-09-27，conn_id=game-000028/000033）：
// `game-town-teleport-potion-committed town_id=31 area_id=0 map_path=Town/CentralPark/Gate_CentralPark.map`
// 之后客户端假死（角色模型消失、能交互 NPC、不能移动），而同一份日志里该城镇的主街是
// area 1 `Town/CentralPark/CentralPark.map`。
const boundaryGateMapNamePrefix = "gate_"

// HasPVFGateMarker 表示该区域的 PVF 段落里声明了 `[gate] <x> <y>`
// （parse.go 只在读到两个整数时才置 Kind="gate"）。
//
// 注意：它**不是**"边界图"的判据。真档 25 个带该标记的区域里，7 个不是 `Gate_*` 图，
// 而且带标记的地图反而是城镇主图：
//
//	town/ReturneeCare.twn (town 33)   area 0 `Town/Returnee/ReaturneeTown0.map`
//	                                  `[minimap rect]` 258 149 271 157 `[/minimap rect]` `[gate]` 594 234
//	town/chn_knightofround.twn (103)  area 0 `chn_knight_of_round_vil/chn_knight_of_round_vil.map`
//	                                  `[minimap rect]` 258 149 271 157 `[/minimap rect]` `[gate]` 594 234
//	town/new_Elvengard.twn (38)       area 1 `Cataclysm/Town/Elvengard/new_seria_room.map` `[gate]` 450 234（赛丽亚房间）
//
// 这三处的 `[minimap rect]` + 入口坐标形状与真档主城图 town/180201_Wildforest.twn
// area 0 的 `[normal]` 594 234 同形（见 default_position_test.go），说明 `[gate] <x> <y>`
// 描述的是"该区域的入口坐标"，不是"这张图是城镇边界"。所以边界图只按命名约定判定；
// 这个访问器只用于日志取证与测试交叉核对。
func (a Area) HasPVFGateMarker() bool { return a.Kind == "gate" && a.Gate != nil }

// MapNameLooksLikeBoundaryGate 是**命名约定**判据：地图文件名以 `Gate_` 开头。
// 真档统计见 boundaryGateMapNamePrefix 的注释（15/53 个城镇的 area 0 命中）。
func (a Area) MapNameLooksLikeBoundaryGate() bool {
	name := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(a.MapPath, "\\", "/")))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.HasPrefix(name, boundaryGateMapNamePrefix)
}

// IsBoundaryArea 判断该区域是不是城镇的**边界/门图**：玩家从城镇走出去、或走门进城镇时
// 用的那张小图，不是玩家平时站着的城镇主图。判据 = 文件名命名约定（见上）。
func (a Area) IsBoundaryArea() bool {
	return a.MapNameLooksLikeBoundaryGate()
}

// BoundarySignals 返回判定该区域是边界图时命中的信号（用于事件日志取证）：
// `gate_map_name` 是判据本身；`pvf_gate_marker` 是同一区域内同时出现的 PVF 入口坐标
// 标记 —— 真档 15/15 个边界图都带它，作为命名约定的佐证一起记进日志。
func (a Area) BoundarySignals() string {
	signals := make([]string, 0, 2)
	if a.MapNameLooksLikeBoundaryGate() {
		signals = append(signals, "gate_map_name")
	}
	if a.HasPVFGateMarker() {
		signals = append(signals, "pvf_gate_marker")
	}
	return strings.Join(signals, "+")
}

type Town struct {
	ID    int64  `json:"id"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	Areas []Area `json:"areas"`
	// Presence marker in the actual town document. Event towns such as
	// chn_daydungeon.twn must not replace the character's normal return town.
	DisableSavePoint bool `json:"disable_save_point,omitempty"`
}

type Table struct {
	towns map[int64]Town
}

type Snapshot struct {
	Towns int `json:"towns"`
	Areas int `json:"areas"`
}

func (t *Table) Find(townID int64) (Town, bool) {
	if t == nil {
		return Town{}, false
	}
	value, ok := t.towns[townID]
	return cloneTown(value), ok
}

// Towns returns a deterministic cloned snapshot of every loaded PVF town.
func (t *Table) Towns() []Town {
	if t == nil || len(t.towns) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(t.towns))
	for id := range t.towns {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Town, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneTown(t.towns[id]))
	}
	return out
}

func (t *Table) FindArea(townID, areaID int64) (Area, bool) {
	town, ok := t.Find(townID)
	if !ok {
		return Area{}, false
	}
	for _, area := range town.Areas {
		if area.ID == areaID {
			return cloneArea(area), true
		}
	}
	return Area{}, false
}

// FindGateArea returns the first gate area declared by the requested town.
// Gate coordinates and area ownership come from the runtime PVF catalog.
func (t *Table) FindGateArea(townID int64) (Area, bool) {
	town, ok := t.Find(townID)
	if !ok {
		return Area{}, false
	}
	for _, area := range town.Areas {
		if area.Kind == "gate" && area.Gate != nil {
			return cloneArea(area), true
		}
	}
	return Area{}, false
}

// FindPrincipalArea 返回该城镇的**主城区**（玩家平时所在的城镇主图）。
//
// 判据（全部来自城镇文档本身，不硬编码 area id）：
//
//  1. 文档顺序里 id==0 的区域，只要它不是城镇边界门图（Area.IsBoundaryArea，命名约定
//     `Gate_*`）就是主城区 —— 真档 53 个城镇里 38 个走这一条，area 0 就是主图
//     （new_Elvengard.map、new_Hendon_Main.map、WestCoast.map、StormPass.map …）。
//  2. area 0 是边界门图时（真档 15 个城镇：town 31 中央公园 area 0 =
//     `Town/CentralPark/Gate_CentralPark.map`、town 30 魔界营地 area 0 =
//     `Town/MetroCenter/Gate_MetroCenter.map` …），主城区是**文档顺序里第一个非边界图、
//     且有地图的区域**；先取带 `[normal]` 标记的，再退到任意非边界图。
//     真档 15/15 都命中该城镇的主街图（CentralPark.map / Metro_Time.map /
//     BattleShip_Main.map / Sewer_Main.map / SilverCrown_MainStreet.map …）。
//  3. 该城镇除了边界图没有别的图（真档里不存在，15 个边界图城镇都至少还有一张图）时，
//     原样返回 area 0 —— 调用方据此逐字节回落到改动前的选择。
//
// 刻意**不**把 PVF `[gate] <x> <y>` 标记当成边界图判据：真档里 town 33 area 0
// (`Town/Returnee/ReaturneeTown0.map`)、town 103 area 0
// (`chn_knight_of_round_vil/chn_knight_of_round_vil.map`)、town 38 area 1（赛丽亚房间）
// 都带这个标记，但它们是（或属于）该城镇的主图 / 私房，不是 `Gate_*` 边界图。
func (t *Table) FindPrincipalArea(townID int64) (Area, bool) {
	town, ok := t.Find(townID)
	if !ok || len(town.Areas) == 0 {
		return Area{}, false
	}
	var zero *Area
	for idx := range town.Areas {
		if town.Areas[idx].ID == 0 {
			candidate := town.Areas[idx]
			zero = &candidate
			break
		}
	}
	if zero != nil && !zero.IsBoundaryArea() {
		return cloneArea(*zero), true
	}
	for _, area := range town.Areas {
		if area.IsBoundaryArea() || strings.TrimSpace(area.MapPath) == "" {
			continue
		}
		if area.Kind == "normal" {
			return cloneArea(area), true
		}
	}
	for _, area := range town.Areas {
		if area.IsBoundaryArea() || strings.TrimSpace(area.MapPath) == "" {
			continue
		}
		return cloneArea(area), true
	}
	if zero != nil {
		return cloneArea(*zero), true
	}
	return Area{}, false
}

func (t *Table) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{Towns: len(t.towns)}
	for _, town := range t.towns {
		snapshot.Areas += len(town.Areas)
	}
	return snapshot
}

func cloneTown(value Town) Town {
	value.Areas = append([]Area(nil), value.Areas...)
	for idx := range value.Areas {
		value.Areas[idx] = cloneArea(value.Areas[idx])
	}
	return value
}

func cloneArea(value Area) Area {
	value.NeedQuests = append([]int64(nil), value.NeedQuests...)
	if value.Gate != nil {
		gate := *value.Gate
		value.Gate = &gate
	}
	if value.DefaultPosition != nil {
		defaultPosition := *value.DefaultPosition
		value.DefaultPosition = &defaultPosition
	}
	if value.DungeonGate != nil {
		dungeonGate := *value.DungeonGate
		value.DungeonGate = &dungeonGate
	}
	return value
}
