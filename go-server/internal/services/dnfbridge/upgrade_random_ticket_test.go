package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/pvf"
	"strings"
	"testing"
)

const randomArmorTicketFixture = "[enchant random]\n[er_grade]\n0 1 2 3 5 6\n[er_slot]\n2\n[er_seal]\n2\n[er_enchant]\n8 6 15285 7 30805 8 32150 9 11201 10 8600 11 1440 12 437 13 82\n[/er_enchant]\n[/enchant random]\n"

func TestUpgradeRandomTicketPVF(t *testing.T) {
	catalog, source := newUpgradeTicketTestCatalog(t)
	source["stackable/ticket/random.stk"] = "[stackable type] `[etc]` 1\n" + randomArmorTicketFixture
	source["equipment/weapon/test_sword.equ"] = "[equipment type] `[coat]` 21\n[rarity] 1\n[minimum level] 90\n"
	r, err := resolveCurrentUpgradeTicketMetadata(catalog, source, 9102, 700)
	if err != nil || r.RandomTicket == nil || !r.TicketRandom || r.TicketMode != "reinforce" || r.TargetMinimumLevel != 90 || r.TargetRarity != 1 || r.RandomTicket.Seal != 2 || r.RandomTicket.Slot != 2 || len(r.RandomTicket.Chances) != 8 || r.RandomTicket.Chances[7].Level != 13 || r.RandomTicket.Chances[7].Weight != 82 {
		t.Fatalf("resolution=%+v err=%v", r, err)
	}
}

func TestUpgradeRandomTicketMalformedPVF(t *testing.T) {
	for _, pair := range [][2]string{{"8 6 15285", "7 6 15285"}, {"6 15285", "32 15285"}, {"6 15285", "6 -1"}, {"6 15285", "6 100000"}, {"6 15285", "6 `15285`"}, {"[er_seal]\n2", "[er_seal]\n3"}, {"[er_slot]\n2", "[er_slot]\n4"}, {"0 1 2 3 5 6", "0 1 2 3 5 7"}, {"8 6 15285", ""}} {
		d, err := pvf.Parse("ticket.stk", strings.Replace(randomArmorTicketFixture, pair[0], pair[1], 1))
		if err != nil {
			t.Fatal(err)
		}
		if parseUpgradeRandomTicket(d) != nil {
			t.Fatalf("accepted malformed %q", pair)
		}
	}
}

func TestFixedUpgradeTicketEquipmentAllowlistAndBaseLevel(t *testing.T) {
	catalog, source := newUpgradeTicketTestCatalog(t)
	source["stackable/ticket/plus10.stk"] = "[stackable type] `[etc]` 1\n[equipment reinforcement ticket]\n11 100 `fixed` -1\n[reinforcement usable item list]\n700 701\n[/reinforcement usable item list]\n[check usable itemlevel]\n1 90\n"
	source["equipment/weapon/test_sword.equ"] = "[equipment type] `[weapon]`\n[minimum level] 90\n"
	r, err := resolveCurrentUpgradeTicketMetadata(catalog, source, 9100, 700)
	if err != nil || r.TicketMode != "reinforce" || r.TargetLevel != 11 || r.SuccessWeight != 100000 || len(r.UsableItemIDs) != 2 || !r.MaterialUsableItemLevelSet || r.MaterialUsableItemLevelMax != 90 || r.TargetMinimumLevel != 90 {
		t.Fatalf("resolution=%+v err=%v", r, err)
	}
}
