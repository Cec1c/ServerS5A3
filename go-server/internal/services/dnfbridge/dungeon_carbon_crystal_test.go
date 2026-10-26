package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func configureCarbonCrystalTestSource(t *testing.T, s *Service) {
	t.Helper()
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	if source[dungeonDropEquipmentList] == "" {
		source[dungeonDropEquipmentList] = "9001 `weapon/test_drop.equ`\n"
		source["equipment/weapon/test_drop.equ"] = "[name]\n`测试装备`\n[durability]\n50\n[attach type]\n`[trade]`\n"
	}
	source[dungeonDropStackableList] += "10163135 `material/carbon_crystal.stk`\n"
	source["stackable/material/carbon_crystal.stk"] = "[name]\n`碳结晶体`\n[grade]\n1\n[rarity]\n2\n[stackable type]\n`[material]` 0\n[attach type]\n`[trade]`\n"
	source[dungeonCardDropRulePath] = strings.Replace(currentDungeonGoldDropTestRuleText(), "1 200 10000 0 0 0 0", "1 200 0 0 0 0 0", 1)
	var gold strings.Builder
	gold.WriteString("[gold drop ref table]\n")
	for level := 1; level <= 200; level++ {
		gold.WriteString(strconv.Itoa(level) + " 100 0\n")
	}
	source[dungeonCardGoldReferencePath] = gold.String()
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.dungeonMonsterTable.DropCatalog(); err != nil {
		t.Fatalf("碳结晶体测试掉落目录不完整：%v", err)
	}
}

func prepareCarbonCrystalTest(t *testing.T) (*Service, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	s, r, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
	configureCarbonCrystalTestSource(t, s)
	r.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Set: true, Value: 55}
	r.Room.monsters[0].Spawn.Rank = "[boss]"
	return s, r, repos
}

func carbonCrystalTestWires(wires []currentDungeonDeathDropWire) []currentDungeonDeathDropWire {
	var result []currentDungeonDeathDropWire
	for _, wire := range wires {
		if binary.LittleEndian.Uint32(wire.Item.data[2:6]) == currentDungeonCarbonCrystalItemID {
			result = append(result, wire)
		}
	}
	return result
}

func TestCarbonCrystalDungeonLevelFatigueAndBossBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*runtimeDungeonState, *runtimeDungeonMonster)
		want   int
	}{
		{"55级领主", nil, 1},
		{"54级领主", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) { r.Dungeon.Metadata.BasisLevel.Value = 54 }, 0},
		{"90级领主", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) { r.Dungeon.Metadata.BasisLevel.Value = 90 }, 1},
		{"地下城等级缺失", func(r *runtimeDungeonState, m *runtimeDungeonMonster) {
			r.Dungeon.Metadata.BasisLevel.Set = false
			m.Spawn.Level, m.Spawn.AutoLevel = 0, 20
		}, 0},
		{"不消耗疲劳", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) { r.Dungeon.Metadata.NoFatigue = true }, 0},
		{"训练场疲劳豁免", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) {
			r.Dungeon.ID = 5000
			r.Dungeon.Path = "dungeon/PoongjinTrainingRoom/PoongjinTrainingRoom.dgn"
		}, 0},
		{"普通怪", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) { m.Spawn.Rank = "[normal]" }, 0},
		{"绿名不等于领主", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) {
			m.Spawn.Rank = "[normal]"
			// 公开怪物结构保留源段落；从实际绿名标记解析夹具，避免依赖本地扩展字段。
			source := bridgeDungeonPVF(false)
			source["monster/test.gob"] += "[is named monster]\n1\n"
			catalog, err := newPVFDungeonMonsterCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			definition, found, err := catalog.Find(m.Spawn.MonsterID)
			if err != nil || !found {
				t.Fatalf("绿名夹具解析失败：%v", err)
			}
			m.Definition = definition
		}, 0},
		{"冠军不等于领主", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) { m.Spawn.Rank = "[champion]" }, 0},
		{"超级冠军不等于领主", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) { m.Spawn.Rank = "[super champion]" }, 0},
		{"后缀声明领主", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) {
			m.Spawn.Rank = "[dummy]"
			m.Spawn.SuffixMarker = "[boss]"
		}, 1},
		{"怪物模板声明领主", func(_ *runtimeDungeonState, m *runtimeDungeonMonster) {
			m.Spawn.Rank = "[normal]"
			m.Definition.Rank = "[boss]"
		}, 1},
		{"耗尽最后疲劳仍有产出", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) { r.Character.Stats["fatigue"] = 0 }, 1},
		{"高等级角色不抬高低级副本", func(r *runtimeDungeonState, _ *runtimeDungeonMonster) {
			r.Character.Level = 90
			r.Dungeon.Metadata.BasisLevel.Value = 54
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _ := prepareCarbonCrystalTest(t)
			m := r.Room.Snapshot().Monsters[0]
			if tc.change != nil {
				tc.change(r, &m)
			}
			wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(carbonCrystalTestWires(wires)); got != tc.want {
				t.Fatalf("碳结晶体地面条目%d，期望%d", got, tc.want)
			}
		})
	}
}

func TestCarbonCrystalGuaranteedAcrossDifficultyAndRandomSeeds(t *testing.T) {
	s, r, _ := prepareCarbonCrystalTest(t)
	m := r.Room.Snapshot().Monsters[0]
	scene, _ := r.Session.Scene()
	visit, err := r.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	for difficulty := byte(0); difficulty < 5; difficulty++ {
		r.Request.Difficulty = difficulty
		for seed := uint32(0); seed < 16; seed++ {
			visit.DropRNG = seed
			wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
			if err != nil {
				t.Fatal(err)
			}
			rows := carbonCrystalTestWires(wires)
			if len(rows) != 1 || binary.LittleEndian.Uint32(rows[0].Item.data[6:10]) != 1 {
				t.Fatalf("难度%d 种子%d 未固定掉落一个碳结晶体：%+v", difficulty, seed, rows)
			}
			endSeed := visit.DropRNG
			// 同样的普通掉落在疲劳豁免副本运行，新增固定项不能消耗随机数。
			r.Dungeon.Metadata.NoFatigue = true
			visit.DropRNG = seed
			_, err = s.planCurrentDungeonMonsterDrops(r, m, 99)
			r.Dungeon.Metadata.NoFatigue = false
			if err != nil || visit.DropRNG != endSeed {
				t.Fatalf("固定产出改变随机流：%v", err)
			}
		}
	}
}

func TestCarbonCrystalAbsentFromLegacyPVFKeepsOriginalDrops(t *testing.T) {
	s, r, _ := prepareCarbonCrystalTest(t)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n"
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := s.planCurrentDungeonMonsterDrops(r, r.Room.Snapshot().Monsters[0], 99)
	if err != nil || len(carbonCrystalTestWires(wires)) != 0 {
		t.Fatalf("旧PVF缺少碳结晶体仍新增产出或中断原掉落：%v", err)
	}
}

func TestCarbonCrystalPartyDeathReplayAndPickupOnce(t *testing.T) {
	s, leader, peer, r, peerRuntime := partyDungeonDeathFixture(t)
	configureCarbonCrystalTestSource(t, s)
	for _, current := range []*runtimeDungeonState{r, peerRuntime} {
		current.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Set: true, Value: 55}
	}
	r.Room.monsters[0].Spawn.Rank = "[boss]"
	m := r.Room.Snapshot().Monsters[0]
	for _, member := range []*gameSession{leader, peer} {
		member.conn.(*bufferConn).write.Reset()
	}
	killDungeonActors(t, s, leader, m.ObjectKey)
	keys := map[uint16]uint32{}
	for _, member := range []*gameSession{leader, peer} {
		count := 0
		for _, body := range abyssOp38Bodies(t, member.conn.(*bufferConn).write.Bytes()) {
			for i, row := range abyssEpicFragmentDropRows(t, body) {
				if row.ItemID == currentDungeonCarbonCrystalItemID {
					count++
					keys[member.selectedCharacterID] = row.ObjectKey
					if binary.LittleEndian.Uint16(body[abyssDropRowOwnerOffset(i):]) != currentSceneActorObjectKey(member.selectedCharacterID) {
						t.Fatal("碳结晶体归属错误")
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("成员%d收到%d个碳结晶体条目", member.selectedCharacterID, count)
		}
	}
	if keys[99] == keys[100] {
		t.Fatal("两名成员共用了地面物品编号")
	}
	scene, _ := r.Session.Scene()
	if _, err := r.DropOwner.owned(keys[100], runtimeDungeonRoomKeyFromScene(scene), 99); !errors.Is(err, errDungeonDropActorMismatch) {
		t.Fatalf("允许拾取他人碳结晶体：%v", err)
	}
	before := len(r.DropOwner.byObjectKey)
	killDungeonActors(t, s, peer, m.ObjectKey)
	if len(r.DropOwner.byObjectKey) != before {
		t.Fatal("重复领主死亡增加了掉落")
	}
	for _, member := range []*gameSession{leader, peer} {
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.handleCurrentDungeonPickup(member, currentDungeonPickupTestBody(keys[member.selectedCharacterID])); err != nil {
				t.Fatal(err)
			}
		}
		repos, _ := s.repositoryGroup()
		inventory, found, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(member.selectedCharacterID)))
		if err != nil || !found {
			t.Fatalf("背包读取失败：%v", err)
		}
		var count int64
		for _, stack := range inventory.Slots {
			if stack.ItemID == int64(currentDungeonCarbonCrystalItemID) {
				count += stack.Count
			}
		}
		if count != 1 {
			t.Fatalf("成员%d重复拾取后数量%d", member.selectedCharacterID, count)
		}
	}
}

func TestCarbonCrystalRiftBossKeepsOriginalRewards(t *testing.T) {
	s, r, _ := prepareCarbonCrystalTest(t)
	drops, err := s.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r.riftLoot = &currentRiftLootDefinition{DropCatalog: drops, MonsterCatalog: s.dungeonMonsterTable, Pieces: []uint32{3227}, Equipment: []uint32{9001}, FragmentCount: 2, EpicRate: 0}
	scene, _ := r.Session.Scene()
	for _, noFatigue := range []bool{false, true} {
		r.Dungeon.Metadata.NoFatigue = noFatigue
		wires, err := s.planCurrentRiftBossDrops(&gameSession{selectedCharacterID: 99}, r, scene)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if noFatigue {
			want = 0
		}
		if len(carbonCrystalTestWires(wires)) != want || len(wires) != 2+want {
			t.Fatalf("裂缝原奖励或碳结晶体有误：豁免%t 条目%d", noFatigue, len(wires))
		}
	}
}

func TestCarbonCrystalEpicEntryModeExcluded(t *testing.T) {
	s, r, _ := prepareCarbonCrystalTest(t)
	// Epic Road 6063/6064/6065 are ticket-entry modes. Their runtime does not
	// debit room fatigue, despite DGN lacking [no fatigue], and their boss path
	// invokes both ordinary and Epic Road loot planning. Exclude both paths.
	r.epicEntry = &currentEpicEntry{}
	m := r.Room.Snapshot().Monsters[0]
	wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(carbonCrystalTestWires(wires)); got != 0 {
		t.Fatalf("票券免疲劳模式普通路径产出碳结晶体%d个", got)
	}

	drops, err := s.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r.riftLoot = &currentRiftLootDefinition{DropCatalog: drops, MonsterCatalog: s.dungeonMonsterTable, Pieces: []uint32{3227}, Equipment: []uint32{9001}, FragmentCount: 2, EpicRate: 0}
	scene, _ := r.Session.Scene()
	wires, err = s.planCurrentRiftBossDrops(&gameSession{selectedCharacterID: 99}, r, scene)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(carbonCrystalTestWires(wires)); got != 0 {
		t.Fatalf("票券免疲劳模式专项路径产出碳结晶体%d个", got)
	}
	if len(wires) != 2 {
		t.Fatalf("票券免疲劳模式原裂缝奖励改变：%d", len(wires))
	}
}
