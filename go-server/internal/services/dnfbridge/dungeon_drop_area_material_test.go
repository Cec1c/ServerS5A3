package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
)

func configureBirthBudDropTestSource(t *testing.T, s *Service) {
	t.Helper()
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonDropStackableList] += "\n10161610 `material/birth_bud.stk`\n"
	source["stackable/material/birth_bud.stk"] = "[name]\n`诞生之芽`\n[stackable type]\n`[material]` 0\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	if source[dungeonDropEquipmentList] == "" {
		source[dungeonDropEquipmentList] = "9001 `weapon/test.equ`\n"
		source["equipment/weapon/test.equ"] = "[name]\n`测试装备`\n[durability]\n50\n"
	}
	// 将通用概率范围移出测试怪物等级，仅保留原模板材料池与新增区域材料。
	source[dungeonCardDropRulePath] = strings.Replace(currentDungeonGoldDropTestRuleText(), "1 200 10000 0 0 0 0", "1 1 0 0 0 0 0", 1)
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBirthBudAreaMaterialRange(t *testing.T) {
	for id := int64(310); id <= 315; id++ {
		entry, ok := currentDungeonAreaMaterialPoolEntry(id)
		if !ok || entry.ItemID != 10161610 || entry.Weight != 100 {
			t.Fatalf("魔界普通图%d材料映射有误：%+v %t", id, entry, ok)
		}
	}
	for _, id := range []int64{0, 104, 144, 309, 316, 317, 5000, 5097, 7155} {
		if entry, ok := currentDungeonAreaMaterialPoolEntry(id); ok {
			t.Fatalf("范围外副本%d获得区域材料：%+v", id, entry)
		}
	}
}

func TestBirthBudPlanSharesTemplatePoolRollAndWeights(t *testing.T) {
	s, r, _ := prepareCurrentDungeonDropTest(t, 1, 3227)
	configureBirthBudDropTestSource(t, s)
	scene, _ := r.Session.Scene()
	visit, err := r.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	m := r.Room.Snapshot().Monsters[0]
	for _, id := range []int64{309, 310, 311, 312, 313, 314, 315, 316, 317, 5000} {
		for _, rank := range []string{"[normal]", "[champion]", "[super champion]", "[boss]"} {
			r.Dungeon.ID, m.Spawn.Rank = id, rank
			pool := []dungeonMonsterDropPoolEntry{{ItemID: 3227, Weight: 10}}
			if entry, ok := currentDungeonAreaMaterialPoolEntry(id); ok {
				pool = append(pool, entry)
			}
			for seed := uint32(0); seed < 64; seed++ {
				r.Request.Difficulty = byte(seed % 5)
				visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
				model := newCurrentDungeonDropLCG(seed)
				var want uint32
				if model.Next(currentDungeonDropDenominator) < currentDungeonExplicitPoolRate {
					entry, ok := selectDungeonMonsterDrop(pool, uint64(model.NextUint32()))
					if !ok {
						t.Fatal("测试材料池为空")
					}
					want = entry.ItemID
				}
				wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
				if err != nil {
					t.Fatal(err)
				}
				if (len(wires) != 0) != (want != 0) || len(wires) > 1 || visit.DropRNG != model.Seed() {
					t.Fatalf("共享材料池判定或随机流有误：图%d rank=%s seed=%d want=%d count=%d", id, rank, seed, want, len(wires))
				}
				if want != 0 && (binary.LittleEndian.Uint32(wires[0].Item.data[2:6]) != want || binary.LittleEndian.Uint32(wires[0].Item.data[6:10]) != 1) {
					t.Fatal("材料池未按权重选物，或单次数量不是1")
				}
			}
		}
	}
}

func TestBirthBudDeathReplayAndPickupOnce(t *testing.T) {
	s, leader, peer, r, peerRuntime := partyDungeonDeathFixture(t)
	configureBirthBudDropTestSource(t, s)
	for _, current := range []*runtimeDungeonState{r, peerRuntime} {
		current.Dungeon.ID = 310
		scene, _ := current.Session.Scene()
		visit, err := current.currentDungeonRoomVisit(scene)
		if err != nil {
			t.Fatal(err)
		}
		// 两名成员从同一房间随机流依次抽取；79让两次独立外层判定都命中。
		visit.DropRNG = 79
	}
	m := r.Room.Snapshot().Monsters[0]
	for _, member := range []*gameSession{leader, peer} {
		member.conn.(*bufferConn).write.Reset()
	}
	killDungeonActors(t, s, leader, m.ObjectKey)
	keys := map[uint16]uint32{}
	for _, member := range []*gameSession{leader, peer} {
		for _, body := range abyssOp38Bodies(t, member.conn.(*bufferConn).write.Bytes()) {
			for _, row := range abyssEpicFragmentDropRows(t, body) {
				if row.ItemID == currentDungeonBirthBudItemID {
					if keys[member.selectedCharacterID] != 0 {
						t.Fatal("单次死亡产生多个区域材料")
					}
					keys[member.selectedCharacterID] = row.ObjectKey
				}
			}
		}
		if keys[member.selectedCharacterID] == 0 {
			t.Fatalf("成员%d未收到区域材料", member.selectedCharacterID)
		}
	}
	if keys[leader.selectedCharacterID] == keys[peer.selectedCharacterID] {
		t.Fatal("队员共用了区域材料地面对象")
	}
	before := len(r.DropOwner.byObjectKey)
	killDungeonActors(t, s, peer, m.ObjectKey)
	if len(r.DropOwner.byObjectKey) != before {
		t.Fatal("重复死亡重新抽取了区域材料")
	}
	for _, member := range []*gameSession{leader, peer} {
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.handleCurrentDungeonPickup(member, currentDungeonPickupTestBody(keys[member.selectedCharacterID])); err != nil {
				t.Fatal(err)
			}
		}
		repos, _ := s.repositoryGroup()
		bag, found, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(member.selectedCharacterID)))
		if err != nil || !found {
			t.Fatal("读取隔离背包失败", err)
		}
		var amount int64
		for _, stack := range bag.Slots {
			if stack.ItemID == int64(currentDungeonBirthBudItemID) {
				amount += stack.Count
			}
		}
		if amount != 1 {
			t.Fatalf("重复拾取后区域材料数量%d", amount)
		}
	}
}
