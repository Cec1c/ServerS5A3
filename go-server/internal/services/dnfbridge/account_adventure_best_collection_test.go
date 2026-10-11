package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentAdventureHighestProficiencyUsesOnlyEligibleSavedCollectionChoices(t *testing.T) {
	character := func(id, job, name string, slot, level int, grow, honor int64) dnfrepo.CharacterRecord {
		return dnfrepo.CharacterRecord{CharacterID: id, Job: job, Name: name, Slot: slot, Level: level,
			Stats: map[string]int64{"grow_type": grow, currentHonorExpertLevelStatKey: honor}}
	}
	rows := []dnfrepo.CharacterRecord{
		character("31", "0", "剑神", 0, 90, 0x21, 0),
		character("32", "11", "剑帝", 1, 90, 0x22, 7),
		character("33", "0", "未选高熟练度", 2, 90, 0x21, 99),
		character("34", "1", "不满级", 3, 89, 0x21, 100),
		character("35", "2", "未二觉", 4, 90, 0x11, 101),
		character("36", "3", "无转职", 5, 90, 0x20, 102),
		character("37", "4", "另一已选", 6, 90, 0x23, 8),
		character("38", "5", "同熟练度后位", 7, 90, 0x24, 8),
		character("39", "6", "损坏旧数值", 8, 90, 0x21, -1),
	}
	// Repository return order must not change the chosen card on equal honor.
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	tests := []struct {
		name      string
		choices   map[string]string
		job, grow byte
		honor     uint32
	}{
		{"no selection", nil, 0, 0, 0},
		{"legitimate zero proficiency", map[string]string{"0_1": "31"}, 0, 1, 0},
		{"selected maximum ignores nonselected higher same transfer", map[string]string{"0_1": "31", "11_2": "32"}, 11, 2, 7},
		{"multiple selected maximum", map[string]string{"11_2": "32", "4_3": "37"}, 4, 3, 8},
		{"stable tie follows collection slot order", map[string]string{"5_4": "38", "4_3": "37"}, 4, 3, 8},
		{"deleted choice does not choose replacement", map[string]string{"0_1": "999"}, 0, 0, 0},
		{"saved wrong transfer does not select", map[string]string{"0_1": "32"}, 0, 0, 0},
		{"ineligible stale choices do not select", map[string]string{"1_1": "34", "2_1": "35", "3_0": "36"}, 0, 0, 0},
		{"invalid old honor follows existing neutral projection", map[string]string{"6_1": "39"}, 6, 1, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := make([]dnfrepo.CharacterRecord, len(rows))
			copy(before, rows)
			for index, row := range rows {
				before[index].Stats = make(map[string]int64, len(row.Stats))
				for key, value := range row.Stats {
					before[index].Stats[key] = value
				}
			}
			body := buildCurrentAdventureInfoBodyWithState(31, adventuregroup.Summary{}, rows, "", 0,
				adventuregroup.Projection{CollectionSelections: test.choices})
			raw := body[18 : 18+currentAdventureInfoRawLength]
			if raw[44] != test.job || raw[45] != test.grow || binary.LittleEndian.Uint32(raw[48:52]) != test.honor {
				t.Fatalf("highest selected collection header=%x, want job=%d grow=%d honor=%d", raw[40:56], test.job, test.grow, test.honor)
			}
			if !bytes.Equal(raw[24:44], make([]byte, 20)) || !bytes.Equal(raw[46:48], []byte{0, 0}) || !bytes.Equal(raw[52:56], make([]byte, 4)) {
				t.Fatalf("unknown header fields changed: %x", raw[24:56])
			}
			if !reflect.DeepEqual(rows, before) {
				t.Fatal("collection projection changed source characters")
			}
		})
	}
}
