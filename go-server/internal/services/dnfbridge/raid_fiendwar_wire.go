// 超时空战况表投影。领域取自轮回源码；当前客户端通知编号及行宽重新核对。
package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"sort"
	"time"
)

// v143 的 b90cc0 注册本组回调，真实分发通过既有分类适配器允许九包。
// v144 将1483空回调接到模型重置和活动战况恢复；其余行宽保持原生契约。
const (
	currentFiendWarInit        uint16 = 1483
	currentFiendWarStateNotice uint16 = 1484
	currentFiendWarWarning     uint16 = 1485
	currentFiendWarUser        uint16 = 1486
	currentFiendWarMonster     uint16 = 1487
	currentFiendWarEnemy       uint16 = 1488
	currentFiendWarTimer       uint16 = 1489
	currentFiendWarMerchant    uint16 = 1490
	currentFiendWarSymbol      uint16 = 1491
)

type currentFiendWarProjection struct {
	RaidKey  uint32
	Battle   *fiendwar.FiendWarBattle
	Members  []runtimeRaidMemberState
	Deadline time.Time
}
type currentFiendWarWire struct {
	opcode uint16
	body   []byte
}

func currentFiendWarGroup(group byte) byte {
	if group == 0 {
		return 1
	}
	return group
}
func currentFiendWarSeconds(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	return uint32((d + time.Second - 1) / time.Second)
}
func currentFiendWarProjectionMember(s currentFiendWarProjection, id uint16) (runtimeRaidMemberState, bool) {
	for _, m := range s.Members {
		if m.CharID == id {
			return m, true
		}
	}
	return runtimeRaidMemberState{}, false
}
func runtimeFiendWarBoardEnemy(b *fiendwar.FiendWarBattle, id int) fiendwar.FiendWarUnit {
	if b == nil {
		return fiendwar.FiendWarUnit{}
	}
	a := b.Config.Areas[id]
	kind := runtimeFiendWarFixtureKind(a.Kind)
	if kind == 0 {
		return b.Areas[id].Enemy
	}
	if b.AreaOpen(id) || b.Areas[id].Group != 0 {
		return fiendwar.FiendWarUnit{ID: uint32(a.Dungeon), Type: kind}
	}
	return fiendwar.FiendWarUnit{}
}

func runtimeFiendWarFixtureKind(kind string) byte {
	return map[string]byte{"radar": 5, "battery": 6, "top outpost": 7, "mid outpost": 8, "bot outpost": 9, "top command center": 10, "mid command center": 11, "bot command center": 12, "top boss": 13, "bot boss": 13}[kind]
}

func runtimeFiendWarRadarRevealed(b *fiendwar.FiendWarBattle) bool {
	if b == nil {
		return false
	}
	return b.Value("radar state") == 1
}

func runtimeFiendWarRevealEnemy(b *fiendwar.FiendWarBattle, id int, unit fiendwar.FiendWarUnit) (fiendwar.FiendWarUnit, bool) {
	if unit.Type == 0 {
		return fiendwar.FiendWarUnit{}, false
	}
	scouted := runtimeFiendWarScoutedEnemy(b, id, unit)
	if !runtimeFiendWarRadarRevealed(b) {
		scouted.Type = byte(0)
	}
	return scouted, true
}

func runtimeFiendWarScoutedEnemy(b *fiendwar.FiendWarBattle, id int, unit fiendwar.FiendWarUnit) fiendwar.FiendWarUnit {
	a := b.Config.Areas[id]
	if runtimeFiendWarFixtureKind(a.Kind) != 0 {
		maze, _ := b.MazeForArea(id)
		if boss, ok := fiendwar.FiendWarFixtureBossID(a.Dungeon, maze); ok {
			return fiendwar.FiendWarUnit{ID: boss, Type: unit.Type}
		}
		return fiendwar.FiendWarUnit{Type: unit.Type}
	}
	// 普通作战区域：这里的 type 是兵种 1..4，它决定这只军团会走进哪张 maze
	// （maze = type-1），头像就用那张 maze 的领主。
	if boss, ok := fiendwar.FiendWarArmyBossID(unit.Type); ok {
		return fiendwar.FiendWarUnit{ID: boss, Type: unit.Type}
	}
	return fiendwar.FiendWarUnit{Type: unit.Type}
}

func runtimeFiendWarRevealMonster(b *fiendwar.FiendWarBattle, id int, unit fiendwar.FiendWarUnit) (fiendwar.FiendWarUnit, bool) {
	if unit.Type == 0 {
		return fiendwar.FiendWarUnit{}, false
	}
	scouted := fiendwar.FiendWarUnit{Type: unit.Type}
	if boss, ok := fiendwar.FiendWarOrcBossID(unit.Type); ok {
		scouted.ID = boss
	}
	if !runtimeFiendWarRadarRevealed(b) {
		scouted.Type = byte(0)
	}
	return scouted, true
}

func runtimeFiendWarAreaClockSeconds(b *fiendwar.FiendWarBattle, areaID int, now time.Time) (uint32, bool) {
	if b == nil {
		return 0, false
	}
	area := b.Areas[areaID]
	if area.Group == 0 || area.Deadline.IsZero() || !area.Deadline.After(now) {
		return 0, false
	}
	return currentFiendWarSeconds(area.Deadline.Sub(now)), true
}

func runtimeRaidGateCountdownTimerType(wireState uint32) (byte, bool) {
	switch wireState {
	case 0:
		return 1, true
	case 1, 3:
		return 2, true
	default:
		return 0, false
	}
}

func runtimeFiendWarViewerClockSeconds(state currentFiendWarProjection, viewer uint16, now time.Time) uint32 {
	raidClock := currentFiendWarSeconds(state.Deadline.Sub(now))
	b := state.Battle
	if b == nil || viewer == 0 {
		return raidClock
	}
	// 先看"这一位本人被记在哪一格"。MemberAreas 是进图/回城时逐人写的账本
	// （game-fiendwar-member-area-marked），它才是"这一个人在不在图里"的正解。
	if areaID, tracked := b.MemberAreas[viewer]; tracked {
		if seconds, live := runtimeFiendWarAreaClockSeconds(b, areaID, now); live {
			return seconds
		}
	}
	// 账本区域会滞后（2026-10-01「走不动」那轮的病根），所以再用**小队的区域**兜一次。
	// 两条都拿不到"有在跑的钟"就回落到攻坚倒计时 —— 战况表上、待机区、刚通关回城
	// 都是这一支。
	if member, ok := currentFiendWarProjectionMember(state, viewer); ok {
		group := currentFiendWarGroup(member.GroupIndex)
		if areaID, tracked := b.GroupAreas[group]; tracked {
			if seconds, live := runtimeFiendWarAreaClockSeconds(b, areaID, now); live {
				return seconds
			}
		}
	}
	return raidClock
}

func buildCurrentFiendWarBoard(state, previous currentFiendWarProjection, viewer uint16, now time.Time) ([]currentFiendWarWire, error) {
	b := state.Battle
	if b == nil || b.Config == nil {
		return nil, fmt.Errorf("超时空战场未初始化")
	}
	old := previous.Battle
	if previous.RaidKey != state.RaidKey {
		old = nil
	}
	var packets []currentFiendWarWire
	appendRows := func(op uint16, rows [][]byte) {
		for len(rows) > 0 {
			n := min(len(rows), 255)
			body := []byte{byte(n)}
			for _, r := range rows[:n] {
				body = append(body, r...)
			}
			packets = append(packets, currentFiendWarWire{op, body})
			rows = rows[n:]
		}
	}
	keys := make([]int, 0, len(b.Symbols))
	for key := range b.Symbols {
		keys = append(keys, int(key))
	}
	sort.Ints(keys)
	shared := map[uint32]uint32{}
	var symbols [][]byte
	for _, key := range keys {
		value := b.Symbols[uint32(key)]
		if key < 121 {
			shared[uint32(key)] = uint32(value)
		}
		if key < 81 || key > 125 || old != nil && old.Symbols[uint32(key)] == value || key >= 121 && (old == nil || value == 0) {
			continue
		}
		r := binary.LittleEndian.AppendUint32(nil, uint32(key))
		r = binary.LittleEndian.AppendUint32(r, uint32(value))
		symbols = append(symbols, r)
	}
	sharedBody, err := raid.BuildSymbolBody(shared)
	if err != nil {
		return nil, err
	}
	packets = append(packets, currentFiendWarWire{583, sharedBody})
	appendRows(currentFiendWarSymbol, symbols)
	ids := make([]int, 0, len(b.Config.Areas))
	for id := range b.Config.Areas {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var enemies, monsters, warnings, timers, users [][]byte
	unitRow := func(area int, u fiendwar.FiendWarUnit, op uint32, enemy bool) []byte {
		r := []byte{byte(area)}
		r = binary.LittleEndian.AppendUint32(r, u.ID)
		r = append(r, u.Type)
		r = binary.LittleEndian.AppendUint32(r, op)
		if enemy {
			r = binary.LittleEndian.AppendUint32(r, 2)
		}
		return r
	}
	for _, id := range ids {
		a := b.Areas[id]
		var prior fiendwar.FiendWarAreaState
		if old != nil {
			prior = old.Areas[id]
		}
		enemy, ok := runtimeFiendWarRevealEnemy(b, id, runtimeFiendWarBoardEnemy(b, id))
		var priorEnemy fiendwar.FiendWarUnit
		priorOK := false
		if old != nil {
			priorEnemy, priorOK = runtimeFiendWarRevealEnemy(old, id, runtimeFiendWarBoardEnemy(old, id))
		}
		if priorOK && (!ok || enemy.ID != priorEnemy.ID) {
			enemies = append(enemies, unitRow(id, priorEnemy, 2, true))
		}
		if ok && (!priorOK || enemy != priorEnemy) {
			enemies = append(enemies, unitRow(id, enemy, 1, true))
		}
		monster, mok := runtimeFiendWarRevealMonster(b, id, a.Monster)
		var priorMonster fiendwar.FiendWarUnit
		pmok := false
		if old != nil {
			priorMonster, pmok = runtimeFiendWarRevealMonster(old, id, prior.Monster)
		}
		if pmok && (!mok || monster.ID != priorMonster.ID) {
			monsters = append(monsters, unitRow(id, priorMonster, 2, false))
		}
		if mok && (!pmok || monster != priorMonster) {
			monsters = append(monsters, unitRow(id, monster, 1, false))
		}
		if a.Warning && !prior.Warning {
			warnings = append(warnings, []byte{byte(id), 1})
		}
		timerNames := make([]string, 0, len(fiendwar.FiendWarTimerKinds))
		for name := range fiendwar.FiendWarTimerKinds {
			timerNames = append(timerNames, name)
		}
		sort.Slice(timerNames, func(i, j int) bool {
			return fiendwar.FiendWarTimerKinds[timerNames[i]] < fiendwar.FiendWarTimerKinds[timerNames[j]]
		})
		for _, kindName := range timerNames {
			kind := fiendwar.FiendWarTimerKinds[kindName]
			timer, active := b.Timers[fiendwar.FiendWarTimerKey{Area: id, Kind: kindName}]
			if !active || !timer.Deadline.After(now) {
				if old != nil && !old.Timers[fiendwar.FiendWarTimerKey{Area: id, Kind: kindName}].Deadline.IsZero() {
					timers = append(timers, append([]byte{byte(id), kind}, make([]byte, 8)...))
				}
				continue
			}
			if old != nil && old.Timers[fiendwar.FiendWarTimerKey{Area: id, Kind: kindName}] == timer {
				continue
			}
			r := []byte{byte(id), kind}
			r = binary.LittleEndian.AppendUint32(r, uint32(timer.Deadline.Unix()))
			r = binary.LittleEndian.AppendUint32(r, currentFiendWarSeconds(timer.Deadline.Sub(timer.StartedAt)))
			timers = append(timers, r)
		}
		if old != nil && !prior.Deadline.IsZero() && a.Deadline.IsZero() {
			timers = append(timers, append([]byte{byte(id), 12}, make([]byte, 8)...))
		}
		if !a.Deadline.IsZero() && (old == nil || !a.Deadline.Equal(prior.Deadline)) {
			r := []byte{byte(id), 12}
			r = binary.LittleEndian.AppendUint32(r, uint32(a.Deadline.Unix()))
			r = binary.LittleEndian.AppendUint32(r, uint32(b.Config.Areas[id].LimitSeconds))
			timers = append(timers, r)
		}
	}
	recovery := func(b *fiendwar.FiendWarBattle, id uint16) uint32 {
		if b == nil || b.RecoveryUntil[id].IsZero() {
			return 0
		}
		return uint32(b.RecoveryUntil[id].Unix())
	}
	userRow := func(op uint32, m runtimeRaidMemberState, b *fiendwar.FiendWarBattle) []byte {
		r := binary.LittleEndian.AppendUint32(nil, op)
		r = append(r, byte(b.MemberArea(m.CharID, currentFiendWarGroup(m.GroupIndex))))
		r = binary.LittleEndian.AppendUint32(r, uint32(m.CharID))
		return binary.LittleEndian.AppendUint32(r, recovery(b, m.CharID))
	}
	for _, m := range previous.Members {
		if _, present := currentFiendWarProjectionMember(state, m.CharID); old != nil && !present {
			users = append(users, userRow(2, m, old))
		}
	}
	for _, m := range state.Members {
		pm, present := currentFiendWarProjectionMember(previous, m.CharID)
		if old == nil || !present {
			users = append(users, userRow(1, m, b))
			continue
		}
		if old.MemberArea(m.CharID, currentFiendWarGroup(pm.GroupIndex)) != b.MemberArea(m.CharID, currentFiendWarGroup(m.GroupIndex)) {
			users = append(users, userRow(2, pm, old), userRow(1, m, b))
		} else if recovery(old, m.CharID) != recovery(b, m.CharID) {
			users = append(users, userRow(1, m, b))
		}
	}
	appendRows(currentFiendWarEnemy, enemies)
	appendRows(currentFiendWarMonster, monsters)
	appendRows(currentFiendWarWarning, warnings)
	appendRows(currentFiendWarTimer, timers)
	appendRows(currentFiendWarUser, users)
	// Clear any stale merchant marker; purchasing this NPC is not activated.
	packets = append(packets, currentFiendWarWire{currentFiendWarMerchant, []byte{0}})
	var gates []raid.DungeonState
	for _, id := range ids {
		a := b.Config.Areas[id]
		if a.Dungeon <= 0 {
			continue
		}
		status := byte(2)
		if b.AreaOpen(id) || b.Areas[id].Group != 0 {
			status = 0
		}
		gates = append(gates, raid.DungeonState{ID: uint32(a.Dungeon), State: status})
	}
	for _, id := range ids {
		a := b.Config.Areas[id]
		live := b.Areas[id]
		var prior fiendwar.FiendWarAreaState
		if old != nil {
			prior = old.Areas[id]
		}
		if a.Dungeon > 0 && !live.Deadline.Equal(prior.Deadline) {
			end, duration := uint32(0), uint32(0)
			if !live.Deadline.IsZero() {
				end = uint32(live.Deadline.Unix())
				duration = uint32(a.LimitSeconds)
			}
			packets = append(packets, currentFiendWarWire{596, raid.BuildTimerBody(1, uint32(a.Dungeon), end, duration)})
		}
	}
	gateBody, err := raid.BuildDungeonStateBody(gates, 0)
	if err != nil {
		return nil, err
	}
	packets = append(packets, currentFiendWarWire{585, gateBody}, currentFiendWarWire{597, raid.BuildRemainingTimeBody(0, runtimeFiendWarViewerClockSeconds(state, viewer, now))})
	return packets, nil
}
