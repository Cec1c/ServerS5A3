package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"sort"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFGuardianGemUniquenessAcrossFamiliesAndSockets(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed a56 Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	groups := make(map[string][]currentGuardianGemDefinition)
	ids := make([]uint32, 0, 52)
	for itemID, ref := range catalog.itemRefs {
		if ref.kind != dungeonDropItemStackable || !strings.HasPrefix(strings.ToLower(ref.path), "flaggem/") {
			continue
		}
		gem, err := resolveCurrentGuardianGem(catalog, itemID)
		if err != nil {
			t.Fatal(err)
		}
		_, text, err := readDungeonDropText(catalog.source, "stackable", gem.PVFPath)
		if err != nil || !strings.Contains(text, "相同属性无法重复装备") {
			t.Fatalf("item=%d duplicate rule missing: %v", itemID, err)
		}
		groups[gem.EnchantFamily] = append(groups[gem.EnchantFamily], gem)
		ids = append(ids, gem.ItemID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	t.Logf("active_guardian_gem_ids=%v", ids)
	if len(groups) != 13 {
		t.Fatalf("attribute families=%d want13", len(groups))
	}
	families := make([]string, 0, len(groups))
	for family, gems := range groups {
		if len(gems) != 4 {
			t.Fatalf("family=%s grades=%d want4", family, len(gems))
		}
		sort.Slice(gems, func(i, j int) bool { return gems[i].Grade < gems[j].Grade })
		for grade, gem := range gems {
			if int(gem.Grade) != grade {
				t.Fatalf("family=%s missing grade%d", family, grade)
			}
		}
		groups[family] = gems
		families = append(families, family)
	}
	sort.Strings(families)
	if _, err := resolveCurrentGuardianGemMedal(catalog, 100380017); err != nil {
		t.Fatalf("real medal fixture: %v", err)
	}
	for _, mode := range []string{"same-attribute-other-socket", "same-socket-replacement", "different-attribute"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 0
			for familyIndex, family := range families {
				for _, selected := range groups[family] {
					old := groups[family][(int(selected.Grade)+1)%4]
					for target := 0; target < 4; target++ {
						values := [4]uint16{}
						oldSocket := (target + 1) % 4
						if mode == "same-socket-replacement" {
							oldSocket = target
						}
						if mode == "different-attribute" {
							old = groups[families[(familyIndex+1)%len(families)]][selected.Grade]
						}
						value, err := currentGuardianGemSocketValue(old.ItemID)
						if err != nil {
							t.Fatal(err)
						}
						values[oldSocket] = value
						service, repos := newGuardianGemUniquenessService(t)
						service.pvfItemCatalog = catalog
						before := seedGuardianGemUniquenessTarget(t, repos, values, selected.ItemID, 2)
						request := currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: selected.ItemID, SocketIndex: byte(target)}
						_, err = service.commitCurrentGuardianGemUse(&gameSession{selectedCharacterID: 77}, request)
						inv, found, loadErr := repos.Inventory.Load(context.Background(), "77")
						if loadErr != nil || !found {
							t.Fatalf("inventory found=%t err=%v", found, loadErr)
						}
						after := loadGuardianGemUniquenessMedal(t, repos)
						if mode == "same-attribute-other-socket" {
							if err == nil || inv.Slots["38:49"].Count != 2 || !bytes.Equal(before, after) {
								t.Fatalf("family=%s item=%d target=%d duplicate mutated assets err=%v", family, selected.ItemID, target, err)
							}
						} else {
							if err != nil || inv.Slots["38:49"].Count != 1 {
								t.Fatalf("family=%s item=%d target=%d legitimate insertion err=%v count=%d", family, selected.ItemID, target, err, inv.Slots["38:49"].Count)
							}
							want := append([]byte(nil), before...)
							v, _ := currentGuardianGemSocketValue(selected.ItemID)
							binary.LittleEndian.PutUint16(want[101+2*target:103+2*target], v)
							if !bytes.Equal(want, after) {
								t.Fatalf("family=%s item=%d target=%d changed unrelated raw bytes", family, selected.ItemID, target)
							}
						}
						attempts++
					}
				}
			}
			if attempts != 208 {
				t.Fatalf("attempts=%d want208", attempts)
			}
			t.Logf("verified %d formal transactions over all 52 gems and four destination sockets", attempts)
		})
	}
}

func TestCurrentGuardianGemUnknownOtherSocketRejectsWithoutConsumption(t *testing.T) {
	service, repos := newGuardianGemUniquenessService(t)
	before := seedGuardianGemUniquenessTarget(t, repos, [4]uint16{0xffff}, 90012, 1)
	_, err := service.commitCurrentGuardianGemUse(&gameSession{selectedCharacterID: 77}, currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90012, SocketIndex: 1})
	if err == nil {
		t.Fatal("unresolvable other socket must reject rather than guess its attribute")
	}
	inv, found, loadErr := repos.Inventory.Load(context.Background(), "77")
	if loadErr != nil || !found || inv.Slots["38:49"].Count != 1 || !bytes.Equal(before, loadGuardianGemUniquenessMedal(t, repos)) {
		t.Fatal("unknown socket rejection changed assets")
	}
}
