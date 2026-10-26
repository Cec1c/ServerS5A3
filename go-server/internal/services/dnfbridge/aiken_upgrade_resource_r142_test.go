package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type aikenR142ChangedSource struct {
	pvf.Source
	path   string
	change func(string) string
}

func (s aikenR142ChangedSource) ReadText(path string) (string, error) {
	v, e := s.Source.ReadText(path)
	if e == nil && strings.EqualFold(path, s.path) {
		return s.change(v), nil
	}
	return v, e
}
func TestAikenR142ChangedResourceRejects(t *testing.T) {
	info := "etc/customability/individualcustominfo.etc"
	option := "etc/customability/options/group1_physical_attack.etc"
	for _, tc := range []struct {
		name, path string
		change     func(string) string
	}{
		{"new_upgrade_fee", info, func(s string) string {
			return strings.Replace(s, "[assign material]", "[upgrade gold]\n1\n[assign material]", 1)
		}},
		{"price_changed", info, func(s string) string { return strings.Replace(s, "10161536 18", "10161536 19", 1) }},
		{"max_changed", info, func(s string) string { return regexp.MustCompile(`(\[upgrade max\]\s*)4`).ReplaceAllString(s, "${1}5") }},
		{"unknown_section", info, func(s string) string { return s + "\n[upgrade rate]\n100\n" }},
		{"option_group_changed", info, func(s string) string { return strings.Replace(s, "1 2 3 4 5 6 7", "1 2 3 4 5 6 8", 1) }},
		{"invalid_grade", option, func(s string) string { return strings.Replace(s, "`D`", "`Z`", 1) }},
		{"unknown_option_stat", option, func(s string) string { return s + "\n[unknown upgrade effect]\n1\n" }},
		{"negative_parameter", option, func(s string) string { return strings.Replace(s, "12 ", "-1 ", 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			a := aikenR142Archive(t)
			raw, e := a.ReadText(tc.path)
			if e != nil {
				t.Fatal(e)
			}
			changed := tc.change(raw)
			if changed == raw {
				t.Fatal("resource mutation did not occur")
			}
			catalog, e := newPVFDungeonDropCatalog(aikenR142ChangedSource{a, tc.path, tc.change})
			if e != nil {
				t.Fatal(e)
			}
			s.pvfItemCatalog = catalog
			before := aikenR142Load(t, g)
			if e := aikenR142Request(t, s, session, 9); e != nil {
				t.Fatal(e)
			}
			aikenR142Outcome(t, session, false)
			if !reflect.DeepEqual(before, aikenR142Load(t, g)) {
				t.Fatal("changed resource mutated assets")
			}
		})
	}
}
func TestAikenR142PendingUnlockUsesGameplayClock(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, material := range []bool{false, true} {
		for _, delta := range []int{1, 0, -1} {
			t.Run(fmt.Sprintf("material%v/deadline%+d", material, delta), func(t *testing.T) {
				s, session, g, _, _ := aikenR142Fixture(t, "")
				s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
				before := aikenR142Load(t, g)
				key := "0:9"
				if material {
					key = "0:121"
				}
				x := before.Slots[key]
				if x.Extra == nil {
					x.Extra = map[string]string{}
				}
				x.Extra["equipment_lock_state"] = "2"
				x.Extra["equipment_unlock_at"] = fmt.Sprint(now.Unix() + int64(delta))
				before.Slots[key] = x
				aikenR142Save(t, g, before)
				if e := aikenR142Request(t, s, session, 9); e != nil {
					t.Fatal(e)
				}
				aikenR142Outcome(t, session, delta <= 0)
				if delta > 0 && !reflect.DeepEqual(before, aikenR142Load(t, g)) {
					t.Fatal("pending lock mutated assets")
				}
			})
		}
	}
}
func TestAikenR142EntryGuards(t *testing.T) {
	s, _, _, _, _ := aikenR142Fixture(t, "")
	if e := s.handleCurrentAikenUpgrade(nil, nil); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"empty_body", "extra_field", "wrong_class0", "wrong_class2", "no_selected_character", "dungeon"} {
		t.Run(name, func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			before := aikenR142Load(t, g)
			b := []byte{9, 0}
			class := byte(1)
			switch name {
			case "empty_body":
				b = nil
			case "extra_field":
				b = append(b, 1)
			case "wrong_class0":
				class = 0
			case "wrong_class2":
				class = 2
			case "no_selected_character":
				session.selectedCharacterID = 0
			case "dungeon":
				session.dungeon.runtime = &runtimeDungeonState{}
			}
			frame, e := proto.BuildChannelPacket(1342, b, 1, class)
			if e != nil {
				t.Fatal(e)
			}
			// BuildChannelPacket defaults class0 to1; restore actual adversarial header.
			frame[0] = class
			if e = s.handleGameUpper(session, frame); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, aikenR142Load(t, g)) {
				t.Fatal("invalid entry mutated assets")
			}
			if class == 1 {
				aikenR142Outcome(t, session, false)
			}
		})
	}
}
func TestAikenR142NoSharedOrDuplicateInstanceFabrication(t *testing.T) {
	s, session, g, _, _ := aikenR142Fixture(t, "")
	ctx := context.Background()
	before := aikenR142Load(t, g)
	x := before.Slots["0:9"]
	before.Slots["0:10"] = repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": x}}).Slots["x"]
	aikenR142Save(t, g, before)
	// Same raw/quality is not a persistent unique ID; a separate slot stays intact.
	if e := aikenR142Request(t, s, session, 9); e != nil {
		t.Fatal(e)
	}
	aikenR142Outcome(t, session, true)
	after, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before.Slots["0:10"], after.Slots["0:10"]) {
		t.Fatal("neighbor same-template changed")
	}
	tail, _ := hex.DecodeString(after.Slots["0:9"].Extra["mode1_raw_e_hex"])
	if len(tail) != 5 {
		t.Fatal("missing explicit login state")
	}
}
