package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestDimensionPartyDropsUseEachRecipientsCareer(t *testing.T) {
	s, r, repos := prepareDimensionDungeonDropTest(t)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	rules := strings.Replace(currentDungeonGoldDropTestRuleText(), "1 200 10000 0 0 0 0", "1 200 0 0 10000 0 0", 1)
	rules = strings.Replace(rules, "[item drop ref table]\n1 0 3", "[item drop ref table]\n20 0 3", 1)
	source[dungeonCardDropRulePath] = rules
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	source[currentDungeonDimensionGatePath] = "[dimensiondungeon list]\n700 25 0\n[/dimensiondungeon list]"
	source[currentDungeonDimensionInfoPath] = "[dimension dungeon info]\n0 700\n[/dimension dungeon info]\n[dungeon difficulty condition info]\n1 70\n[/dungeon difficulty condition info]\n[drop item Info]\n0 0 3227\n[/drop item Info]"
	source[currentDungeonDimensionDropListPath] = "[chronicle grow type]\n0 1\n[normal chronicle list]\n9101\n[/normal chronicle list]\n[set chronicle list]\n\n[/set chronicle list]\n[/chronicle grow type]\n[chronicle grow type]\n1 1\n[normal chronicle list]\n9102\n[/normal chronicle list]\n[set chronicle list]\n\n[/set chronicle list]\n[/chronicle grow type]"
	source[currentDungeonDimensionDropExcludePath] = "[exclude list]\n[/exclude list]"
	source[currentDungeonClearRewardRulePath] = "[chronicle set reward rate]\n0 100 0\n[/chronicle set reward rate]"
	source[dungeonDropEquipmentList] = "9101 `chronicle/job0.equ`\n9102 `chronicle/job1.equ`"
	for _, p := range []string{"equipment/chronicle/job0.equ", "equipment/chronicle/job1.equ"} {
		source[p] = "[name]\n`职业装备`\n[durability]\n50\n[attach type]\n`[trade]`\n"
	}
	currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(s.dungeonMonsterTable)
	peerCharacter := r.Character
	peerCharacter.CharacterID = "100"
	peerCharacter.Job = "1"
	if err := repos.Character.Save(context.Background(), peerCharacter); err != nil {
		t.Fatal(err)
	}
	leader := &gameSession{selectedCharacterID: 99, accountID: "account-1"}
	peer := &gameSession{selectedCharacterID: 100, accountID: "account-1"}
	r.partyInstance = &dungeonPartyInstance{members: map[uint16]time.Time{99: time.Now(), 100: time.Now()}, sessions: map[uint16]*gameSession{99: leader, 100: peer}, nextObjectKey: r.NextObjectKey}
	monster := r.Room.Snapshot().Monsters[0]
	// 使用测试规则的BOSS档确保装备分支命中，隔离职业归属与随机未命中。
	monster.Spawn.Rank = "[boss]"
	wires, err := s.planPartyDungeonMonsterDrops(leader, r, monster, 99)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint16]uint32{}
	for _, wire := range wires {
		id := binary.LittleEndian.Uint32(wire.Item.data[2:6])
		if id == 9101 || id == 9102 {
			got[wire.OwnerActorObjectKey] = id
		}
	}
	if got[99] != 9101 || got[100] != 9102 {
		t.Fatalf("职业装备串号：%v", got)
	}
}
