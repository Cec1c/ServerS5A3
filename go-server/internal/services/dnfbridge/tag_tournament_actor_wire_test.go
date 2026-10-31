package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func tagActorWireFixture() currentTagTournamentActorWire {
	stats := make([]byte, currentMode1StatBlobWireSize)
	binary.LittleEndian.PutUint32(stats, 12000)
	binary.LittleEndian.PutUint32(stats[4:], 6000)
	return currentTagTournamentActorWire{level: 90, job: 14, grow: 0x21, base3A4: 3, base3A6: 4, base3A8: 0x11223344, stats: stats, extraSlots: 7, extraSlotFlag: true, cloneTitle: 12345, equipmentFinalState: 54321, skillObjectKey: 99, skillTree: 1, auxiliaryHandles: []uint32{0xabc001, 0xabc002, 0xabc003}, shieldDeck: [5]uint32{1, 2, 3, 4, 5}, creatureItem: 100200, creatureHandle: 765432}
}

func TestTagActorWireNativeReplayFixtures(t *testing.T) {
	var fixtures []map[string]any
	for _, appearance := range []int{0, 1, 14, 33} {
		for _, skillCount := range []int{0, 1, 100, 255} {
			a := tagActorWireFixture()
			for i := 0; i < appearance; i++ {
				a.appearance = append(a.appearance, repo.CharacterRosterEquipSummary{Slot: int64(i), ItemIDOrIcon: int64(100 + i), PackedFlags: int64(i % 2), AuxValue: int64(9 + i), AuxFlag: int64(i)})
			}
			for i := 0; i < skillCount; i++ {
				slot := byte(255)
				if i < 204 {
					slot = byte(i)
				}
				a.skills = append(a.skills, currentTagTournamentSkillWire{slot, uint16(i), byte(i%254 + 1)})
			}
			body, err := buildCurrentTagTournamentActorWire(a)
			if err != nil {
				t.Fatal(err)
			}
			fixtures = append(fixtures, map[string]any{"外观行": appearance, "技能行": skillCount, "正文hex": hex.EncodeToString(body)})
		}
	}
	// 仅显式设置隔离证据路径时导出机器夹具；普通回归无文件副作用。
	if name := os.Getenv("DNFBRIDGE_TAG_NATIVE_FIXTURES"); name != "" {
		b, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTagActorWireRejectsTruncationAndInvalidIdentity(t *testing.T) {
	for name, change := range map[string]func(*currentTagTournamentActorWire){
		"缺少属性":  func(a *currentTagTournamentActorWire) { a.stats = nil },
		"零生命上限": func(a *currentTagTournamentActorWire) { a.stats = make([]byte, 92) },
		"等级缺失":  func(a *currentTagTournamentActorWire) { a.level = 0 },
		"职业越界":  func(a *currentTagTournamentActorWire) { a.job = 16 },
		"技能页越界": func(a *currentTagTournamentActorWire) { a.skillTree = 255 },
		"技能槽越界": func(a *currentTagTournamentActorWire) { a.skills = []currentTagTournamentSkillWire{{204, 20, 1}} },
		"未学习技能": func(a *currentTagTournamentActorWire) { a.skills = []currentTagTournamentSkillWire{{1, 20, 0}} },
		"重复技能": func(a *currentTagTournamentActorWire) {
			a.skills = []currentTagTournamentSkillWire{{1, 20, 1}, {2, 20, 1}}
		},
		"重复技能槽": func(a *currentTagTournamentActorWire) {
			a.skills = []currentTagTournamentSkillWire{{1, 20, 1}, {1, 21, 1}}
		},
		"外观槽越界": func(a *currentTagTournamentActorWire) { a.appearance = []repo.CharacterRosterEquipSummary{{Slot: 33}} },
		"重复外观槽": func(a *currentTagTournamentActorWire) {
			a.appearance = []repo.CharacterRosterEquipSummary{{Slot: 1}, {Slot: 1}}
		},
		"宠物残缺": func(a *currentTagTournamentActorWire) { a.creatureItem = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			a := tagActorWireFixture()
			change(&a)
			if body, err := buildCurrentTagTournamentActorWire(a); err == nil || len(body) != 0 {
				t.Fatal(fmt.Sprintf("非法资料被编码: %x %v", body, err))
			}
		})
	}
}
