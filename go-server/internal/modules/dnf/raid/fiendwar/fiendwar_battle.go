package fiendwar

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

type FiendWarUnit struct {
	ID   uint32
	Type byte
}
type FiendWarAreaState struct {
	Enemy, Monster FiendWarUnit
	Group          byte
	Warning        bool
	Deadline       time.Time
}
type FiendWarTimerKey struct {
	Area int
	Kind string
}
type FiendWarTimer struct{ StartedAt, Deadline time.Time }
type FiendWarEvent struct {
	Kind string
	ID   int
	Name string
}

type FiendWarBattle struct {
	Config            *FiendWarBattleConfig
	Symbols           map[uint32]int
	Areas             map[int]FiendWarAreaState
	Timers            map[FiendWarTimerKey]FiendWarTimer
	DungeonStates     map[int]string
	GroupAreas        map[byte]int
	MemberAreas       map[uint16]int
	RecoveryUntil     map[uint16]time.Time
	ImprisonCounts    map[uint16]int
	MerchantArea      int
	MerchantKind      int
	MerchantDuration  time.Duration
	MerchantExpiresAt time.Time
	NextUnitID        uint32
	Complete, Failed  bool
	// BossWindowDone[side] = 这一侧已经就**本轮**破防窗口报过尾（kind=2 带上自己
	// 那一侧 BOSS 的剩余血量）。两侧都报齐之后才判定：都归零 = 通关，
	// 否则血量均摊、整轮重开。旧字段名叫 BossPatternDone，语义是"两侧都报了同一颗球"
	// —— 那个"锁步"模型是错的（见 fiendWarCloneStates 上面的说明）。
	BossWindowDone [2]bool
	// PickClone 只有测试会注入：官方这一版四颗球的出场顺序是随机的，
	// 但测试需要钉住序列。nil → rand.IntN。
	PickClone func(candidates []int) int
	Retreats  map[byte]int
	// FixtureMazes 是"这个设施区域的领主是哪一张图"，key = [AREA INDEX]。
	//
	// 它**不是抽签结果，是固定的**。操作员 2026-10-01 复核历史截图后定的口径：
	//
	//	「我所说的这些格子里的怪物是固定的。雷达打完前显示问号、服务端随机抽没问题；
	//	  雷达打完之后客户端就是这么画的，服务端也理应按照客户端画的去让怪物真正
	//	  百分百地出现 —— 换句话说，雷达打完前服务端随机抽签生效，打完后就不再抽签。」
	//
	// 客户端那张 kind→脸 的表是按格子位置排的（上哨=宾柏 / 中哨=薛丁恶 / 下哨=克鲁瑟，
	// 兵站本营同理），而战况表上的 type 也正是这个位置 kind。所以服务端只要把
	// "实际进的那张图"钉在同一位置上，表上画的那只与进图打的那只就是同一只，
	// 不需要（也不允许）靠随机去碰。见 fiendWarFixtureMazeForKind。
	//
	// 只有 maze 张数 > 1 的设施区域才登记；雷达/炮塔/传送站只有一张图，表里没有
	// 它们，取用时退回 0。
	FixtureMazes map[int]int
	pending      []FiendWarEvent
}

// fiendWarFixtureMazeForKind 把设施的位置翻译成它**固定**的那张 maze。
//
//	上哨 / 上兵营 → 0   中哨 / 中兵营 → 1   下哨 / 下兵营 → 2
//
// 顺序与 FiendWarFixtureBossIDs 一致（那张表是从 PVF 的 maze 顺序抄下来的）：
// 0 = 人类背叛者:宾柏 / 光之恶魔:欧格里，1 = 引力恶魔:薛丁恶 / 暗之恶魔:艾森，
// 2 = 宇宙粉碎机:克鲁瑟 / 重力恶魔:白斯。
func fiendWarFixtureMazeForKind(kind string) (int, bool) {
	switch kind {
	case "top outpost", "top command center":
		return 0, true
	case "mid outpost", "mid command center":
		return 1, true
	case "bot outpost", "bot command center":
		return 2, true
	}
	return 0, false
}

func NewFiendWarBattle(config *FiendWarBattleConfig) (*FiendWarBattle, error) {
	if config == nil || config.Raid == nil || len(config.Areas) == 0 || len(config.Rules) == 0 {
		return nil, fmt.Errorf("fiendwar battle configuration missing")
	}
	b := &FiendWarBattle{Config: config, Symbols: map[uint32]int{}, Areas: map[int]FiendWarAreaState{}, Timers: map[FiendWarTimerKey]FiendWarTimer{}, DungeonStates: map[int]string{}, GroupAreas: map[byte]int{}, Retreats: map[byte]int{}, FixtureMazes: map[int]int{}}
	b.MemberAreas, b.RecoveryUntil, b.ImprisonCounts = map[uint16]int{}, map[uint16]time.Time{}, map[uint16]int{}
	for _, id := range config.Symbols {
		b.Symbols[id] = 0
	}
	for id, area := range config.Areas {
		b.Areas[id] = FiendWarAreaState{}
		// 多图设施：按位置钉死是哪一张（不抽签）。
		if bosses := FiendWarFixtureBossIDs[area.Dungeon]; len(bosses) > 1 {
			if maze, ok := fiendWarFixtureMazeForKind(area.Kind); ok && maze < len(bosses) {
				b.FixtureMazes[id] = maze
			}
		}
	}
	return b, nil
}

// MazeForArea 返回这一格固定用的那张 maze。只有多图设施区域会登记；
// 未登记时 ok 为 false，调用方按自己的语义决定（单图副本恒为 0）。
func (b *FiendWarBattle) MazeForArea(area int) (int, bool) {
	if b == nil || b.FixtureMazes == nil {
		return 0, false
	}
	maze, ok := b.FixtureMazes[area]
	return maze, ok
}

func (b *FiendWarBattle) Clone() *FiendWarBattle {
	if b == nil {
		return nil
	}
	c := *b
	c.Symbols = maps.Clone(b.Symbols)
	c.Areas = maps.Clone(b.Areas)
	c.Timers = maps.Clone(b.Timers)
	c.DungeonStates = maps.Clone(b.DungeonStates)
	c.GroupAreas = maps.Clone(b.GroupAreas)
	c.MemberAreas = maps.Clone(b.MemberAreas)
	c.RecoveryUntil = maps.Clone(b.RecoveryUntil)
	c.ImprisonCounts = maps.Clone(b.ImprisonCounts)
	c.Retreats = maps.Clone(b.Retreats)
	c.FixtureMazes = maps.Clone(b.FixtureMazes)
	c.pending = append([]FiendWarEvent(nil), b.pending...)
	return &c
}

func (b *FiendWarBattle) Value(name string) int      { return b.Symbols[b.Config.Symbols[name]] }
func (b *FiendWarBattle) set(name string, value int) { b.setSymbol(b.Config.Symbols[name], value) }
func (b *FiendWarBattle) setSymbol(id uint32, value int) {
	if id == 0 || b.Symbols[id] == value {
		return
	}
	b.Symbols[id] = value
	b.pending = append(b.pending, FiendWarEvent{Kind: "on change symbol", ID: int(id)})
}

// Apply processes the PVF's paired rules in source order, then follows only
// actual symbol/area changes. A bounded queue rejects cyclic script updates.
func (b *FiendWarBattle) Apply(event FiendWarEvent, now time.Time) error {
	if b.Complete || b.Failed {
		return nil
	}
	b.pending = append(b.pending, event)
	for count := 0; len(b.pending) > 0; count++ {
		if count > 4096 {
			return fmt.Errorf("fiendwar trigger cycle")
		}
		e := b.pending[0]
		b.pending = b.pending[1:]
		for _, rule := range b.Config.Rules {
			matched := true
			for _, c := range rule.Conditions {
				if !b.matches(c, e) {
					matched = false
					break
				}
			}
			if matched {
				for _, a := range rule.Actions {
					b.act(a, now)
				}
			}
			if b.Complete || b.Failed {
				b.pending = nil
				return nil
			}
		}
	}
	return nil
}

func (b *FiendWarBattle) matches(c FiendWarInstruction, e FiendWarEvent) bool {
	arg := 0
	if len(c.Args) > 0 {
		arg = int(c.Args[0].Int)
	}
	switch c.Op {
	case "if":
		return b.Symbols[c.Symbol] == arg
	case "less":
		return b.Symbols[c.Symbol] < arg
	case "on change symbol":
		return e.Kind == c.Op && e.ID == int(c.Symbol)
	case "on change area state", "on giveup area", "on enter dungeon", "on enter map", "on giveup dungeon", "on change dungeon state":
		return e.Kind == c.Op && e.ID == arg
	case "check area state":
		return b.areaState(arg) == c.Args[1].Value
	case "check dungeon state":
		return b.DungeonStates[arg] == c.Args[1].Value
	case "check timer end":
		return e.Kind == c.Op && e.ID == int(c.Args[1].Int) && e.Name == c.Args[0].Value
	}
	return false
}

func (b *FiendWarBattle) areaState(id int) string {
	a := b.Areas[id]
	if a.Group != 0 {
		if a.Enemy.ID != 0 {
			return "user_enemy"
		}
		if a.Monster.ID != 0 {
			return "user_monster"
		}
	}
	if a.Enemy.ID != 0 && a.Monster.ID != 0 {
		return "enemy_monster"
	}
	if a.Enemy.ID != 0 {
		return "enemy"
	}
	if a.Monster.ID != 0 {
		return "monster"
	}
	return "empty"
}

func (b *FiendWarBattle) updateArea(id int, a FiendWarAreaState) {
	previous := b.areaState(id)
	b.Areas[id] = a
	current := b.areaState(id)
	if previous != current {
		// sub_382A400: empty, enemy, monster, user_enemy, user_monster, enemy_monster.
		b.Symbols[b.Config.Areas[id].Symbol] = map[string]int{"empty": 0, "enemy": 1, "monster": 2, "user_enemy": 3, "user_monster": 4, "enemy_monster": 5}[current]
		b.pending = append(b.pending, FiendWarEvent{Kind: "on change area state", ID: id})
	}
	count := 0
	for _, area := range b.Areas {
		if area.Monster.ID != 0 {
			count++
		}
	}
	b.set("alive monster count", count)
}

func (b *FiendWarBattle) act(a FiendWarInstruction, now time.Time) {
	id := 0
	if len(a.Args) > 0 {
		id = int(a.Args[0].Int)
	}
	switch a.Op {
	case "set":
		b.setSymbol(a.Symbol, id)
	case "set timer":
		key := FiendWarTimerKey{int(a.Args[1].Int), a.Args[0].Value}
		b.Timers[key] = FiendWarTimer{now, now.Add(time.Duration(a.Args[2].Int) * time.Second)}
	case "reset timer":
		delete(b.Timers, FiendWarTimerKey{int(a.Args[1].Int), a.Args[0].Value})
	case "create enemy":
		area := b.Areas[id]
		if area.Enemy.ID != 0 {
			return
		}
		total := 0
		for i := 1; i < len(b.Config.EnemyRates); i += 2 {
			total += b.Config.EnemyRates[i]
		}
		roll := rand.IntN(total)
		kind := 1
		for i := 0; i < len(b.Config.EnemyRates); i += 2 {
			roll -= b.Config.EnemyRates[i+1]
			if roll < 0 {
				kind = b.Config.EnemyRates[i]
				break
			}
		}
		b.NextUnitID++
		area.Enemy = FiendWarUnit{b.NextUnitID, byte(kind)}
		b.updateArea(id, area)
	case "move enemy":
		to := int(a.Args[1].Int)
		src, dst := b.Areas[id], b.Areas[to]
		if src.Enemy.ID == 0 || src.Group != 0 {
			return
		}
		if dst.Enemy.ID == 0 {
			dst.Enemy = src.Enemy
		}
		dst.Warning = false
		src.Enemy = FiendWarUnit{}
		src.Warning = false
		delete(b.Timers, FiendWarTimerKey{id, "move_enemy"})
		delete(b.Timers, FiendWarTimerKey{id, "warning_enemy"})
		b.updateArea(id, src)
		b.updateArea(to, dst)
	case "delete monster":
		area := b.Areas[id]
		area.Monster = FiendWarUnit{}
		b.updateArea(id, area)
	case "delete all enemy":
		for _, id := range b.areaIDs() {
			area := b.Areas[id]
			area.Enemy = FiendWarUnit{}
			area.Warning = false
			b.updateArea(id, area)
		}
		for key := range b.Timers {
			if strings.Contains(key.Kind, "enemy") {
				delete(b.Timers, key)
			}
		}
	case "warning enemy":
		area := b.Areas[id]
		area.Warning = true
		b.Areas[id] = area
	case "create monsters":
		count := 0
		var available []int
		for _, id := range b.areaIDs() {
			area := b.Areas[id]
			if area.Monster.ID != 0 {
				count++
			}
			// Orcs appear in empty areas. Enemy/monster combat starts when an
			// army reaches them, not when a spawn overlaps a stationary army.
			if b.Config.Areas[id].Kind == "" && b.Config.Areas[id].Dungeon > 0 && area.Group == 0 && area.Enemy.ID == 0 && area.Monster.ID == 0 {
				available = append(available, id)
			}
		}
		for ; count < 3 && len(available) > 0; count++ {
			i := rand.IntN(len(available))
			id := available[i]
			available = append(available[:i], available[i+1:]...)
			area := b.Areas[id]
			b.NextUnitID++
			area.Monster = FiendWarUnit{b.NextUnitID, byte(1 + rand.IntN(2))}
			b.updateArea(id, area)
		}
	case "create merchant":
		if len(b.Config.MerchantPositions) > 0 {
			b.MerchantArea = b.Config.MerchantPositions[rand.IntN(len(b.Config.MerchantPositions))]
		}
		if len(b.Config.MerchantRates) > 0 {
			total := 0
			for _, rate := range b.Config.MerchantRates {
				total += rate.Weight
			}
			if total > 0 {
				roll := rand.IntN(total)
				for _, rate := range b.Config.MerchantRates {
					roll -= rate.Weight
					if roll < 0 {
						b.MerchantKind = rate.Kind
						b.MerchantDuration = time.Duration(rate.DurationSeconds) * time.Second
						b.MerchantExpiresAt = now.Add(b.MerchantDuration)
						break
					}
				}
			}
		}
	case "boss pattern init":
		b.resetBossPattern()
		b.set("kick out character", 0)
	case "clear phase":
		b.Complete = true
	case "fail fiendwar":
		b.Failed = true
	}
}

func (b *FiendWarBattle) areaIDs() []int {
	ids := slices.Collect(maps.Keys(b.Config.Areas))
	slices.Sort(ids)
	return ids
}
func (b *FiendWarBattle) AreaForDungeon(dungeon int) (FiendWarArea, bool) {
	for _, a := range b.Config.Areas {
		if a.Dungeon == dungeon {
			return a, true
		}
	}
	return FiendWarArea{}, false
}
func (b *FiendWarBattle) AreaOpen(id int) bool {
	a, ok := b.Config.Areas[id]
	if !ok || b.Complete || b.Failed {
		return false
	}
	if strings.HasSuffix(a.Kind, "boss") {
		return b.Value(a.Kind+" state") == 1
	}
	if a.Kind == "radar" || a.Kind == "battery" {
		return b.Value(a.Kind+" state") == 0
	}
	if a.Kind != "" {
		return b.Value(a.Kind+" state") == 1
	}
	return a.Dungeon > 0 && (b.Areas[id].Enemy.ID != 0 || b.Areas[id].Monster.ID != 0)
}

func (b *FiendWarBattle) Move(group byte, target int) error {
	if group == 0 || b.Complete || b.Failed {
		return fmt.Errorf("fiendwar group inactive")
	}
	from, ok := b.GroupAreas[group]
	if !ok {
		from = b.Config.WaitingArea
	}
	if from == target {
		return nil
	}
	if !slices.Contains(b.Config.Areas[from].Next, target) {
		return fmt.Errorf("fiendwar areas are not adjacent")
	}
	// 超时空的区域是**共享战场**，不是小队的格子：
	//
	//   - 客户端点进已有同伴的区域时自己弹的是「移动后，会与处于当前城镇的同伴
	//     一同开始战斗。确认要移动吗？」—— 语义就是"进去一起打"；
	//   - PVF 的 [AREA INFO] 只有 [MEMBER MAX]（本 PVF 每区域 4 人），
	//     **没有"归属小队"这个概念**。
	//
	// 所以这里不能按小队独占。人数上限由 MoveMember 的 [MEMBER MAX] 管，
	// 死锁/占位交给上层（谁是这一区域的战斗单元由 encounter 决定）。
	// 原实现的两条判断（Areas[target].Group != 0 → "fiendwar area occupied"，
	// 以及 GroupAreas 扫描 → "fiendwar area occupied by another group"）
	// 正是真机上「9999 进了作战区域-01，礼白666 就进不去」的原因。
	// 原来这里还有一条"站在没打完的区域里不许再走"（"fiendwar source area
	// requires combat"）。它已经害过两次真机事故，2026-10-01 这次是第二次：
	//
	//	玩家在作战区域 A 里没打，直接走进了有怪的 B —— 客户端**照走不误**（它自己不
	//	认这条规则），服务端却在 town_move.go 里静默 return，于是出现两种后果：
	//	  1) 客户端的角色在 B，服务端的 MemberAreas 还记着 A —— 两个人明明站在同一格，
	//	     runtimeRaidFiendWarAreaParty 却看不到同伴，"同区域一起进图"直接落空；
	//	  2) 之后这个人再按进图（target 还是 B，from 仍是 A）又被同一条拒绝挡下，
	//	     而拒绝路径**一个包都不发** —— 礼白666 就是这样被卡死的。
	//
	// 客户端的行为就是官方规则的证据：它能走，说明官方允许走。地图的推进由
	// [AREA INFO] 的相邻关系 + 设施/BOSS 的 symbol 状态把关（下面那条 BOSS 门禁），
	// 不需要"踩到怪就必须打完"这条自造规则。
	if strings.HasSuffix(b.Config.Areas[target].Kind, "boss") && !b.AreaOpen(target) {
		return fmt.Errorf("fiendwar boss gate closed")
	}
	b.GroupAreas[group] = target
	return nil
}

func (b *FiendWarBattle) MemberArea(character uint16, group byte) int {
	if area, ok := b.MemberAreas[character]; ok {
		return area
	}
	if area, ok := b.GroupAreas[group]; ok {
		return area
	}
	return b.Config.WaitingArea
}

func (b *FiendWarBattle) MoveMember(character uint16, group byte, target int, now time.Time) error {
	area, found := b.Config.Areas[target]
	if character == 0 || !found {
		return fmt.Errorf("invalid fiendwar member or area")
	}
	if now.Before(b.RecoveryUntil[character]) {
		return fmt.Errorf("fiendwar member recovering")
	}
	occupants := 0
	for other, location := range b.MemberAreas {
		if other != character && location == target {
			occupants++
		}
	}
	if area.MemberMax > 0 && occupants >= area.MemberMax {
		return fmt.Errorf("fiendwar area member limit reached")
	}
	previous, exists := b.GroupAreas[group]
	b.GroupAreas[group] = b.MemberArea(character, group)
	err := b.Move(group, target)
	if exists {
		b.GroupAreas[group] = previous
	} else {
		delete(b.GroupAreas, group)
	}
	if err != nil {
		return err
	}
	if b.MemberAreas == nil {
		b.MemberAreas = make(map[uint16]int)
	}
	b.MemberAreas[character] = target
	return nil
}

func (b *FiendWarBattle) ImprisonMember(character uint16, now time.Time) {
	if b.MemberAreas == nil {
		b.MemberAreas = make(map[uint16]int)
	}
	if b.RecoveryUntil == nil {
		b.RecoveryUntil = make(map[uint16]time.Time)
	}
	if b.ImprisonCounts == nil {
		b.ImprisonCounts = make(map[uint16]int)
	}
	durations := b.Config.DieImprisonSeconds
	if len(durations) == 0 {
		return
	}
	seconds := durations[min(b.ImprisonCounts[character], len(durations)-1)]
	b.ImprisonCounts[character]++
	b.MemberAreas[character] = b.Config.WaitingArea
	b.RecoveryUntil[character] = now.Add(time.Duration(seconds) * time.Second)
}

func (b *FiendWarBattle) Enter(group byte, dungeon int, now time.Time) error {
	a, ok := b.AreaForDungeon(dungeon)
	if !ok || b.GroupAreas[group] != a.Index || !b.AreaOpen(a.Index) || b.Areas[a.Index].Group != 0 {
		return fmt.Errorf("fiendwar dungeon admission rejected")
	}
	area := b.Areas[a.Index]
	delete(b.Retreats, group)
	delete(b.DungeonStates, dungeon)
	if strings.HasSuffix(a.Kind, "boss") {
		b.set("kick out character", 0)
	}
	area.Group = group
	area.Deadline = now.Add(time.Duration(a.LimitSeconds) * time.Second)
	area.Warning = false
	b.updateArea(a.Index, area)
	delete(b.Timers, FiendWarTimerKey{a.Index, "move_enemy"})
	delete(b.Timers, FiendWarTimerKey{a.Index, "warning_enemy"})
	return b.Apply(FiendWarEvent{Kind: "on enter dungeon", ID: dungeon}, now)
}

func (b *FiendWarBattle) Clear(dungeon int, now time.Time) error {
	a, ok := b.AreaForDungeon(dungeon)
	if !ok || b.Areas[a.Index].Group == 0 {
		return fmt.Errorf("fiendwar dungeon not occupied")
	}
	// 每个 BOSS 房的门槛只看自己这一侧：PVF 的两条推进规则是独立的
	// （[ON CHANGE DUNGEON STATE] 4978 … [IF TOP BOSS STATE] 9 → 10，
	//   4953 那条同理），谁先倒下谁先记 10，两个都到 10 才 [CLEAR PHASE]。
	// 原来这里额外要求**双方**的 hp 都已归零，等于规定两个 BOSS 必须同时死，
	// 分队先后击破时先倒的那一侧永远记不上账。
	if strings.HasSuffix(a.Kind, "boss") && (b.Value(a.Kind+" state") != 9 || b.Value(a.Kind+" hp") != 0) {
		return fmt.Errorf("fiendwar boss mechanisms unfinished")
	}
	area := b.Areas[a.Index]
	area.Enemy = FiendWarUnit{}
	area.Monster = FiendWarUnit{}
	area.Group = 0
	area.Deadline = time.Time{}
	b.updateArea(a.Index, area)
	if a.Kind == "radar" || a.Kind == "battery" {
		b.set(a.Kind+" state", 1)
	} else if a.Kind != "" && !strings.HasSuffix(a.Kind, "boss") {
		b.set(a.Kind+" state", 0)
	}
	b.DungeonStates[dungeon] = "clear"
	return b.Apply(FiendWarEvent{Kind: "on change dungeon state", ID: dungeon}, now)
}

// ClearClientBossRoom is Clear for a BOSS room whose death the CLIENT proved.
//
// 背景（2026-10-01 真机，packet_log-20261001-007）：
//
//	这个客户端在 BOSS 房里只发一种 op1511 报文 —— kind=0（BOSS 血量百分比），
//	每 2 秒一条，value 恒为 100，直到 BOSS 死亡那一刻为止；全程 34 条，一条
//	kind=1（机制）都没有。而 PVF 的 BOSS 状态机只能靠 kind=1 从 5 走到 9
//	（BossReport case 1），所以 `topic boss state` 永远停在 2/4/5，
//	Clear 必然以 "fiendwar boss mechanisms unfinished" 失败，
//	通关记账整段被跳过 —— 玩家看到的就是「BOSS 打死了，什么都没发生」：
//	没有 1453{3,0}、没有结算、没有回城，两个人一直卡在 BOSS 房里。
//
// 结论：本客户端下 PVF 的 BOSS 机制握手是不可达的。副本通关本身已经是
// 权威事实（op39 BOSS 死亡 → op115/op117 → ordinary_final_clear_committed），
// 所以这里用它当作"这一侧 BOSS 已死"的证据，把该侧的机制收尾到 PVF 允许
// 通关的那个状态（state 9 + hp 0），再走原有的 Clear —— 于是
// [IF … BOSS STATE 9] → 10、两个 10 → [CLEAR PHASE] 全部照 PVF 走。
//
// 非 BOSS 区域逐字走 Clear；客户端真的报过 kind=1 的场合（state 已经是 9）
// 这里不改任何状态，行为与原来完全一致。
func (b *FiendWarBattle) ClearClientBossRoom(dungeon int, now time.Time) error {
	if b == nil {
		return fmt.Errorf("fiendwar battle missing")
	}
	a, ok := b.AreaForDungeon(dungeon)
	if !ok || !strings.HasSuffix(a.Kind, "boss") {
		return b.Clear(dungeon, now)
	}
	if b.Areas[a.Index].Group == 0 {
		return fmt.Errorf("fiendwar dungeon not occupied")
	}
	if b.Value(a.Kind+" state") != 9 {
		// 机制条归零、这一侧血量归零，然后把它推进到"伤害窗口已开"的 9。
		b.set("boss pattern state", 0)
		b.set(a.Kind+" hp", 0)
		b.set(a.Kind+" view hp", 0)
		b.set(a.Kind+" state", 9)
	}
	// 已确认的本体死亡也覆盖迟到的HP心跳；state9不代表最后一条血量报告已到。
	b.set(a.Kind+" hp", 0)
	b.set(a.Kind+" view hp", 0)
	return b.Clear(dungeon, now)
}

func (b *FiendWarBattle) Abandon(dungeon int, now time.Time) error {
	a, ok := b.AreaForDungeon(dungeon)
	if !ok {
		return fmt.Errorf("unknown fiendwar dungeon")
	}
	area := b.Areas[a.Index]
	group := area.Group
	if group == 0 {
		return nil
	}
	area.Group = 0
	area.Deadline = time.Time{}
	b.updateArea(a.Index, area)
	b.Retreats[group] = dungeon
	b.pending = append(b.pending, FiendWarEvent{Kind: "on giveup area", ID: a.Index})
	if err := b.Apply(FiendWarEvent{Kind: "on giveup dungeon", ID: dungeon}, now); err != nil {
		return err
	}
	if strings.HasSuffix(a.Kind, "boss") {
		// PVF requests both rooms to retreat when either boss run is abandoned.
		for _, other := range b.areaIDs() {
			if strings.HasSuffix(b.Config.Areas[other].Kind, "boss") && b.Areas[other].Group != 0 {
				if err := b.Abandon(b.Config.Areas[other].Dungeon, now); err != nil {
					return err
				}
			}
		}
		b.BossWindowDone = [2]bool{}
		b.set("boss pattern state", 0)
	}
	return nil
}

func (b *FiendWarBattle) Tick(now time.Time) error {
	if b.MerchantArea != 0 && !b.MerchantExpiresAt.IsZero() && !now.Before(b.MerchantExpiresAt) {
		b.MerchantArea = 0
		b.MerchantExpiresAt = time.Time{}
	}
	for _, id := range b.areaIDs() {
		a := b.Areas[id]
		if a.Group != 0 && !a.Deadline.IsZero() && !now.Before(a.Deadline) {
			if err := b.Abandon(b.Config.Areas[id].Dungeon, now); err != nil {
				return err
			}
		}
	}
	keys := slices.Collect(maps.Keys(b.Timers))
	slices.SortFunc(keys, func(a, c FiendWarTimerKey) int {
		if a.Area != c.Area {
			return a.Area - c.Area
		}
		return strings.Compare(a.Kind, c.Kind)
	})
	for _, key := range keys {
		timer, ok := b.Timers[key]
		if ok && !now.Before(timer.Deadline) {
			delete(b.Timers, key)
			if err := b.Apply(FiendWarEvent{Kind: "check timer end", ID: key.Area, Name: key.Kind}, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 罗什（宇宙恶魔）的四颗属性球
// ---------------------------------------------------------------------------
//
// 一切以**客户端自己的表**为准（`DNF.exe` 0x9D80A0 那张 if/else 链，反汇编见
// `_re_badge_20261001/boss_native.py`）：客户端把"我这一侧现在该打哪颗球"读成
// `top boss state` / `bot boss state`（PVF 符号 92/93），取值与分身怪物一一对应：
//
//	5 → 110996 cosmofiend_planet_crystal（宝石/虚无）
//	6 → 110997 cosmofiend_planet_fire   （火）
//	7 → 110998 cosmofiend_planet_water  （水/冰，动画是 cosmofiend_planet_ice.ani）
//	8 → 110999 cosmofiend_planet_wind   （风）
//
// 9 = 这一侧没有球可打了（本体出现）；10 = 这一侧已通关（PVF rule 137/139）。
//
// **球池是两队共享的**：`boss pattern state`（符号 94）的四个十进制位就是四颗球，
// 1 = 那颗球还在。客户端拿它画罗什身边那几颗球（自己正在打的那颗不画）。
// 所以两侧**不必打同一颗球** —— 官方攻略「超时空漩涡宇宙恶魔罗什攻略」（3DM/COLG）
// 的原话：
//
//	「罗什四属性球出现后开始随机选择：黑暗队伍视角第一个分身为虚无、
//	  纯白队伍视角第一个分身为火 … 当黑暗队伍消灭虚无分身：罗什选择了风分身、
//	  纯白队伍视角虚无球消失 … 当纯白队伍消灭冰分身 → 两队视角所有球消失本体出现」
//
// 也就是：每一侧**各拿一颗**（两颗不同），谁打掉自己那颗，那颗球从池子里永久消失、
// 该侧再从剩下的球里补一颗；四颗全清 → 两侧都到 9 → 本体破防（官方：14.8 秒，
// 本机实测正好 15 秒）。破防结束两队**血量均摊（向下取整）**，然后整轮重开。
var fiendWarCloneStates = [4]int{5, 6, 7, 8}

// fiendWarCloneWeights 是机制条 boss pattern state 的四个十进制位（1=球还在）。
// 位与分身的对应关系只影响客户端画哪一颗球灭掉，不影响判定。
var fiendWarCloneWeights = [4]int{1000, 100, 10, 1}

// cloneInPattern 判断某颗球还在不在机制条上。
func cloneInPattern(pattern, state int) bool {
	if state < 5 || state > 8 {
		return false
	}
	weight := fiendWarCloneWeights[state-5]
	return (pattern/weight)%10 == 1
}

func bossPatternSideName(side int) string {
	if side == 1 {
		return "bot boss"
	}
	return "top boss"
}

// resetBossPattern 重开一轮：四颗球复原、两侧各分一颗**不同**的球。
//
// 调用点有两个：PVF 的 `[BOSS PATTERN INIT]`（两队都进了 BOSS 房）和"破防窗口
// 两队没打完、血量均摊之后"。
func (b *FiendWarBattle) resetBossPattern() {
	if b == nil {
		return
	}
	b.set("boss pattern state", 1111)
	b.BossWindowDone = [2]bool{}
	// 先把两侧都清空，再各分一颗 —— 分配时会避开对面手上那颗。
	b.set("top boss state", 0)
	b.set("bot boss state", 0)
	b.assignBossPatternClone(0)
	b.assignBossPatternClone(1)
}

// assignBossPatternClone 从共享球池里给这一侧补一颗球。
//
// 池子 = 机制条上还亮着的球；**对面正拿着的那颗不给**（两侧同时各打一颗不同的）。
// 没有可给的了（池子空了，或只剩对面手上那颗）→ 该侧置 9：本体出现，
// 这一侧转入"等对面收尾"（官方攻略里的黄地板等待）。
func (b *FiendWarBattle) assignBossPatternClone(side int) {
	pattern := b.Value("boss pattern state")
	otherHeld := b.Value(bossPatternSideName(1-side) + " state")
	candidates := make([]int, 0, len(fiendWarCloneStates))
	for _, state := range fiendWarCloneStates {
		if !cloneInPattern(pattern, state) {
			continue
		}
		if state == otherHeld {
			continue
		}
		candidates = append(candidates, state)
	}
	if len(candidates) == 0 {
		b.set(bossPatternSideName(side)+" state", 9)
		return
	}
	pick := 0
	if b.PickClone != nil {
		pick = b.PickClone(candidates)
	} else {
		// 官方这一版（2018 超时空之战）四颗球的出场顺序是随机的：
		// 攻略原文「分身与BOSS的血量/抗性/机制一致，分身顺序也是随机」。
		pick = rand.IntN(len(candidates))
	}
	if pick < 0 || pick >= len(candidates) {
		pick = 0
	}
	b.set(bossPatternSideName(side)+" state", candidates[pick])
}

// Boss reports are accepted only after the bridge binds a live dungeon host.
// A zero HP report never grants a clear; the dungeon death owner must follow.
func (b *FiendWarBattle) BossReport(dungeon, kind, value int, now time.Time) error {
	a, ok := b.AreaForDungeon(dungeon)
	if !ok || !strings.HasSuffix(a.Kind, "boss") || b.Areas[a.Index].Group == 0 {
		return fmt.Errorf("fiendwar boss host missing")
	}
	side := 0
	if a.Kind == "bot boss" {
		side = 1
	}
	state := b.Value(a.Kind + " state")
	switch kind {
	case 0:
		// BOSS 血量是客户端自己的 HUD 事实，任何"这一侧 BOSS 房被占着"的时刻都
		// 应当被接收：原来只允许 5..9，结果这个客户端（只会发 kind=0）的每一条
		// 上报都被 "invalid fiendwar boss HP" 拒掉，战况表上的 BOSS 血量永远是
		// 初始值，与实际不符。上界 9 保留 —— 10 是"该侧已通关"。
		if state < 1 || state > 9 || value < 0 || value > 100 {
			return fmt.Errorf("invalid fiendwar boss HP")
		}
		if state == 9 && value < b.Value(a.Kind+" hp") {
			b.set(a.Kind+" hp", value)
		}
		b.set(a.Kind+" view hp", value)
	case 1:
		// **只认自己手上那颗**：客户端报的 value 就是它这一侧的 boss state。
		// 打掉之后那颗球从共享池里永久消失，这一侧再从池子里补一颗；
		// 池子空了（四颗都清）→ 两侧都到 9，本体破防开始。
		if state < 5 || state > 8 || value != state || !cloneInPattern(b.Value("boss pattern state"), state) {
			return fmt.Errorf("stale fiendwar boss mechanism")
		}
		b.set("boss pattern state", b.Value("boss pattern state")-fiendWarCloneWeights[state-5])
		b.assignBossPatternClone(side)
	case 2:
		if state != 9 || value < 0 || value > 100 {
			return fmt.Errorf("invalid fiendwar boss damage-window report")
		}
		b.set(a.Kind+" hp", min(value, b.Value(a.Kind+" hp")))
		b.BossWindowDone[side] = true
		if b.BossWindowDone[0] && b.BossWindowDone[1] {
			topHP, botHP := b.Value("top boss hp"), b.Value("bot boss hp")
			if topHP != 0 || botHP != 0 {
				// 官方攻略：「击杀所有属性分身后，全团拥有15秒输出时间，15秒内两队将
				// 各自的BOSS击杀即可顺利通关，如果未击杀成功，则会将BOSS剩余血量
				// **均摊给两个队伍**，同上机制重新再打一轮即可。」
				// 另一份攻略补了取整口径：「破防结束后两队会平衡血量，结果**向下取整**；
				// 相应也会出现一队0%另一队1%，从而直接把1%平衡掉的情况。」
				balanced := (topHP + botHP) / 2
				b.set("top boss hp", balanced)
				b.set("top boss view hp", balanced)
				b.set("bot boss hp", balanced)
				b.set("bot boss view hp", balanced)
				// 均摊之后还有血 → 整轮重开（球池复原、两侧重新分球）；
				// 均摊成 0（0% / 1% 那一档）→ 两边本体都算死了，不再重开，
				// 交给客户端证死那条路收尾。
				if balanced != 0 {
					b.resetBossPattern()
				}
			}
			b.BossWindowDone = [2]bool{}
		}
	default:
		return fmt.Errorf("unknown fiendwar boss operation")
	}
	return b.Apply(FiendWarEvent{}, now)
}
