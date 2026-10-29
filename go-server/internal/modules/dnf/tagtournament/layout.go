package tagtournament

import (
	"fmt"
	"reflect"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// RoundRoom把每日劲敌绑定到真实PVF房间。倍率保留为配置数值，不在这里
// 改写怪物属性；实际伤害/生命投影必须使用同一条记录。
type RoundRoom struct {
	Coordinate worldmap.RoomCoordinate
	Encounter  DailyEncounter
	BasisLevel uint32
}

type Layout struct {
	Topology *worldmap.DungeonTopology
	Rounds   []RoundRoom
}

// BuildLayout保留PVF的起始房、连接及Boss坐标，只在现有战斗房中按今日
// 名单换入完整地图。不能只改怪物ID而遗失对应地图的脚本、机关或出生点。
// 此方法没有仓储或扣费操作，也不修改Table中共享的副本/地图模板。
func (c *Catalog) BuildLayout(table *worldmap.Table, resolver *worldmap.Resolver, now time.Time, plan DailyPlan) (*Layout, error) {
	if c == nil || table == nil || resolver == nil || now.IsZero() {
		return nil, fmt.Errorf("帝国竞技场地图准备缺少已验证资源")
	}
	want, err := c.DailyPlan(now, plan.Mode)
	if err != nil || !reflect.DeepEqual(want, plan) {
		return nil, fmt.Errorf("帝国竞技场地图日计划已失效或被修改")
	}
	definition, found := c.Dungeon(plan.Mode)
	if !found {
		return nil, fmt.Errorf("帝国竞技场模式没有副本配置")
	}
	dungeon, found := table.FindDungeon(int64(plan.DungeonID))
	if !found || len(dungeon.Mazes) != 1 || !dungeon.Metadata.BasisLevel.Set || dungeon.Metadata.BasisLevel.Value != int64(definition.BasisLevel) {
		return nil, fmt.Errorf("帝国竞技场副本迷宫或基准等级与资源目录不符")
	}
	topology, err := worldmap.BuildDungeonTopology(resolver, dungeon.ID, 0)
	if err != nil {
		return nil, err
	}
	rooms := topology.Rooms()
	if topology.Start == nil || len(topology.Bosses) != 1 || len(rooms) != len(plan.Encounters)+1 {
		return nil, fmt.Errorf("帝国竞技场需要一个起始房及完整回合房间")
	}
	// 现用DTT为单向水平链。沿PVF真实邻接边求顺序，不按地图编号或
	// map迭代次序排列，也不凭空生成坐标。
	ordered := make([]worldmap.DungeonRoom, 0, len(rooms))
	current := *topology.Start
	seen := map[worldmap.RoomCoordinate]bool{}
	for len(ordered) < len(rooms) {
		room, ok := topology.Room(current)
		if !ok || seen[current] || room.Map == nil {
			return nil, fmt.Errorf("帝国竞技场存在循环或未解析房间")
		}
		seen[current] = true
		ordered = append(ordered, room)
		// 现用op29把X/Y写入261E6A0/261E6B0；1456300及14562C0
		// 直接以X作为回合数，供困难倍率与普通王房判定使用。仅单链
		// 不足以保证这一契约，竖直或反向地图必须在扣费前拒绝。
		if current.X != int64(len(ordered)-1) || current.Y != 0 {
			return nil, fmt.Errorf("帝国竞技场房间坐标不符合原生回合顺序")
		}
		var next []worldmap.RoomCoordinate
		for _, neighbor := range room.Neighbors {
			if !seen[neighbor.Coordinate] {
				next = append(next, neighbor.Coordinate)
			}
		}
		if len(ordered) == len(rooms) {
			if len(next) != 0 || current != topology.Bosses[0] {
				return nil, fmt.Errorf("帝国竞技场终点不在PVF首领房")
			}
			break
		}
		if len(next) != 1 {
			return nil, fmt.Errorf("帝国竞技场回合房间不是已验证的单链")
		}
		current = next[0]
	}
	for _, spawn := range ordered[0].Map.Map.Monsters {
		for _, encounter := range plan.Encounters {
			if spawn.MonsterID == int64(encounter.MonsterID) {
				return nil, fmt.Errorf("帝国竞技场起始房提前包含今日劲敌")
			}
		}
	}
	result := &Layout{Topology: topology}
	for i, encounter := range plan.Encounters {
		// 公开Table在加载阶段解析地图，缺失或无效地图仍在扣费前拒绝。
		m, ok := table.FindMap(int64(encounter.MapID))
		matches := 0
		for _, spawn := range m.Monsters {
			if spawn.MonsterID == int64(encounter.MonsterID) {
				matches++
			}
		}
		if !ok || matches != 1 {
			return nil, fmt.Errorf("帝国竞技场回合%d地图与今日劲敌不符", i+1)
		}
		coordinate := ordered[i+1].Coordinate
		if err := topology.OverrideRoomMap(coordinate, worldmap.ResolvedMap{Map: m, Source: "tag_tournament_daily_plan"}); err != nil {
			return nil, err
		}
		result.Rounds = append(result.Rounds, RoundRoom{Coordinate: coordinate, Encounter: encounter, BasisLevel: definition.BasisLevel})
	}
	return result, nil
}
