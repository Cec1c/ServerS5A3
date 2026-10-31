package dnfbridge

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const petNameBoundaryCustomLong = "甲乙丙丁戊己庚辛壬癸子丑寅卯"
const petNameBoundaryDefaultLong = "合成宠物名称 (测 1)-尾"
const petNameBoundaryDefaultProjected = "合成宠物名称(测1)-尾"
const petNameBoundaryRuneLong = "🙂🙂🙂🙂🙂🙂尾"
const petNameBoundaryRuneProjected = "🙂🙂🙂🙂🙂🙂"

type petNameBoundaryCatalogSource map[string]string

func (source petNameBoundaryCatalogSource) ReadText(path string) (string, error) {
	value, found := source[path]
	if !found {
		return "", fmt.Errorf("missing synthetic pet catalog path %s", path)
	}
	return value, nil
}

func newPetNameBoundaryCatalog(t *testing.T) *dnfpet.PVFCatalog {
	t.Helper()
	catalog, err := dnfpet.NewPVFCatalog(petNameBoundaryCatalogSource{
		"equipment/equipment.lst": "63000 `creature/synthetic_default.equ`\n",
		"equipment/creature/synthetic_default.equ": "[name] `" + petNameBoundaryDefaultLong + "`\n" +
			"[equipment type] `[creature]`\n[minimum level] 1\n",
		"creature/creature.lst": "1 `synthetic/synthetic.cre`\n",
		"creature/synthetic/synthetic.cre": "[evolution quest] 0\n" +
			"[evolution creature id] 0\n[evolution level] 0\n",
		"creature/exptable.tbl": "2 5 9 15 23 33 44 57 71 88 106 126 148 171 196 223 252 283 315 350 " +
			"388 429 472 517 565 615 668 725 785 849 918 993 1073 1158 1245 1335 1430 1532 1642 1759 " +
			"1879 2011 2151 2301 2464 2639 2829 3049 3294 5579\n",
	})
	if err != nil {
		t.Fatalf("load synthetic pet catalog: %v", err)
	}
	return catalog
}

func petNameBoundaryGB(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(value))
	if err != nil {
		t.Fatalf("encode synthetic pet name %q: %v", value, err)
	}
	return encoded
}

func TestPetNameBoundaryCreatureStateProjectsRawAndFallbackNames(t *testing.T) {
	catalog := newPetNameBoundaryCatalog(t)
	service := &Service{petCatalog: catalog}
	rawBoundary := petNameBoundaryGB(t, petNameBoundaryRuneLong)
	input := []dnfrepo.PetEntry{
		{PetKey: "11", CreatureKey: 11, ItemID: 63000, Name: petNameBoundaryCustomLong, Level: 3, Exp: 4, Satiety: 5},
		{PetKey: "12", CreatureKey: 12, ItemID: 63000, Name: "不应采用的合成备用名", NameRaw: rawBoundary, Level: 6, Exp: 7, Satiety: 8},
		{PetKey: "13", CreatureKey: 13, ItemID: 63000, Name: "合成非法原始名回退", NameRaw: []byte{0xff}, Level: 9, Exp: 10, Satiety: 11},
		{PetKey: "14", CreatureKey: 14, ItemID: 63000, Level: 12, Exp: 13, Satiety: 14},
	}
	before := append([]dnfrepo.PetEntry(nil), input...)
	for index := range before {
		before[index].NameRaw = append([]byte(nil), input[index].NameRaw...)
	}

	wire, projected := service.currentCreatureStateEntriesForWire(input)
	if projected != 3 {
		t.Fatalf("projected name count=%d want=3", projected)
	}
	wantCustom := petNameBoundaryGB(t, "甲乙丙丁戊己庚辛壬癸子丑")
	wantRawBoundary := petNameBoundaryGB(t, petNameBoundaryRuneProjected)
	wantInvalidFallback := petNameBoundaryGB(t, "合成非法原始名回退")
	wantDefault := petNameBoundaryGB(t, petNameBoundaryDefaultProjected)
	for index, want := range [][]byte{wantCustom, wantRawBoundary, wantInvalidFallback, wantDefault} {
		if !bytes.Equal(wire[index].NameRaw, want) {
			t.Fatalf("entry %d raw=% X want=% X", index, wire[index].NameRaw, want)
		}
	}
	if wire[1].Name != input[1].Name {
		t.Fatalf("existing raw had lower priority than custom name: %+v", wire[1])
	}
	if _, err := dnfpet.BuildCreatureListBody(wire); err != nil {
		t.Fatalf("build projected synthetic op105 body: %v", err)
	}
	for index := range input {
		if !bytes.Equal(input[index].NameRaw, before[index].NameRaw) ||
			input[index].Name != before[index].Name || input[index].Level != before[index].Level ||
			input[index].Exp != before[index].Exp || input[index].Satiety != before[index].Satiety {
			t.Fatalf("durable input entry %d mutated: got=%+v before=%+v", index, input[index], before[index])
		}
	}
}

func TestPetNameBoundaryMode0ProjectsDefaultAndCustomNamesForTwoAndFourReceivers(t *testing.T) {
	catalog := newPetNameBoundaryCatalog(t)
	service := &Service{petCatalog: catalog}
	defaultCreature := currentEquippedCreatureSnapshot{itemID: 63000, serialOrHandle: 21, aliveState: 1}
	service.currentEquippedCreatureEnrichNameFromPVF(&defaultCreature)
	if defaultCreature.name != petNameBoundaryDefaultLong || !defaultCreature.nameFromPVF {
		t.Fatalf("synthetic PVF creature=%+v", defaultCreature)
	}
	customCreature := currentEquippedCreatureSnapshot{
		itemID: 63001, serialOrHandle: 22, name: petNameBoundaryCustomLong, aliveState: 1,
	}
	creatures := []struct {
		name  string
		value currentEquippedCreatureSnapshot
		want  []byte
	}{
		{name: "pvf-default", value: defaultCreature, want: petNameBoundaryGB(t, petNameBoundaryDefaultProjected)},
		{name: "custom", value: customCreature, want: petNameBoundaryGB(t, "甲乙丙丁戊己庚辛壬癸子丑")},
	}

	for _, receiverCount := range []int{2, 4} {
		for receiver := 1; receiver <= receiverCount; receiver++ {
			characterName := fmt.Sprintf("合成角色%02d%s", receiver, strings.Repeat("甲", 18))
			adventureName := fmt.Sprintf("合成冒险团%02d%s", receiver, strings.Repeat("乙", 18))
			character := dnfrepo.CharacterRecord{
				CharacterID: fmt.Sprintf("%d", 700+receiver),
				Name:        characterName,
				Job:         "11",
				Level:       90,
			}
			for _, creature := range creatures {
				body := buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(
					uint16(100+receiver), character, true, characterName, creature.value, adventureName,
					byte(receiver), 0,
				)
				itemID, name, alive := currentMode0CreatureForTest(t, body)
				if itemID != creature.value.itemID || !bytes.Equal(name, creature.want) || alive != 1 {
					t.Fatalf("%d-receiver %s item=%d name=% X alive=%d want=% X",
						receiverCount, creature.name, itemID, name, alive, creature.want)
				}
				if !bytes.Contains(body, rosterNameBytes(characterName)) {
					t.Fatalf("%d-receiver %s changed character name bytes", receiverCount, creature.name)
				}
				if !bytes.Contains(body, rosterNameBytes(adventureName)) {
					t.Fatalf("%d-receiver %s changed organization name bytes", receiverCount, creature.name)
				}
			}
		}
	}
}

func TestPetNameBoundaryHonorsCurrentUTF16LimitAndNULTerminator(t *testing.T) {
	if got := dnfpet.CreatureNameForWire(petNameBoundaryCustomLong); got != "甲乙丙丁戊己庚辛壬癸子丑" {
		t.Fatalf("custom long name=%q", got)
	}
	if got := dnfpet.DefaultCreatureNameForWire(petNameBoundaryDefaultLong); got != petNameBoundaryDefaultProjected {
		t.Fatalf("default long name=%q want=%q", got, petNameBoundaryDefaultProjected)
	}
	if got := dnfpet.CreatureNameForWire(petNameBoundaryRuneLong); got != petNameBoundaryRuneProjected {
		t.Fatalf("UTF16 rune boundary=%q want=%q", got, petNameBoundaryRuneProjected)
	}
	if got := dnfpet.CreatureNameForWire("合成\x00不应出现"); got != "合成" {
		t.Fatalf("NUL terminator name=%q", got)
	}
	if units := len(utf16.Encode([]rune(dnfpet.CreatureNameForWire(petNameBoundaryRuneLong)))); units != dnfpet.CurrentCreatureNameUTF16Limit {
		t.Fatalf("projected UTF16 units=%d want=%d", units, dnfpet.CurrentCreatureNameUTF16Limit)
	}
}
