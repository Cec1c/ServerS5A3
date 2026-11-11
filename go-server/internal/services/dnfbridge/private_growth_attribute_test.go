package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestPrivateGrowthAttributesMatchRealPVFAndClientProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	config, err := currentAmplifyItemConfigFromSource(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, pvfItemCatalog: catalog}
	stack := repo.ItemStack{ItemID: 990033101, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment"}}
	if err := s.initializePrivateGrowthGrant(&stack); err != nil {
		t.Fatal(err)
	}
	if stack.RawEntry[0x13] != 0 || stack.RawEntry[0x14] != 0 {
		t.Fatal("purchase granted free book effect")
	}
	for _, tc := range []struct {
		option string
		attr   byte
	}{{"[physical defense]", 1}, {"[magical defense]", 2}, {"[physical attack]", 3}, {"[magical attack]", 4}} {
		if config.OptionMapping[tc.option] != tc.attr {
			t.Fatal("client/PVF enum mismatch", tc)
		}
		converted := cloneOnlineItemTradeStack(stack)
		converted.RawEntry[0x13] = tc.attr
		converted.RawEntry[0x14] = 7
		for _, key := range []string{"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"} {
			converted.Extra[key] = string(rune('0' + tc.attr))
		}
		before := cloneOnlineItemTradeStack(converted)
		mode, attr, ok := inventory.GrowthTicketMode(converted)
		if !ok || mode != "amplify" || attr != tc.attr {
			t.Fatal("converted single ID has wrong mode")
		}
		for _, list := range []byte{0, 1, 2} {
			entry := currentItemListEntryFromStack(list, 9, converted)
			if entry.data[0x13] != tc.attr {
				t.Fatal("projection reset real attribute", tc.attr, list, entry.data[0x13])
			}
		}
		if !reflect.DeepEqual(converted, before) {
			t.Fatal("projection mutated instance")
		}
		target := repo.ItemStack{ItemID: 101040493, Count: 1}
		def, value, err := s.growthTargetDefinition(converted, target)
		if err != nil || def.MinimumLevel < 55 || value == 0 {
			t.Fatalf("converted white target failed: %+v %d %v", def, value, err)
		}
	}
}
