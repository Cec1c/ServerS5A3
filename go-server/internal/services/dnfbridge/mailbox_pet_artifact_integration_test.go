package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Resolve through the bridge's real claim resolver, then execute the mail owner
// transaction. A correct family classifier alone did not prevent this incident:
// the mailbox adapter discarded its PetArtifact result.
func TestMailboxPetArtifactClaimRoutesAndPreservesAssets(t *testing.T) {
	for _, color := range []string{"red", "blue", "green"} {
		for _, fullPetBag := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pet_full=%t", color, fullPetBag), func(t *testing.T) {
				ctx := context.Background()
				catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
					"monster/monster.lst": "", "stackable/stackable.lst": "",
					"equipment/equipment.lst":     "9001 `creature/test.equ`\n",
					"equipment/creature/test.equ": fmt.Sprintf("[name] `Artifact`\n[equipment type] `[artifact %s]`\n", color),
				})
				if err != nil {
					t.Fatal(err)
				}
				service := &Service{pvfItemCatalog: catalog}
				resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
				if err != nil {
					t.Fatal(err)
				}
				repos := dnfrepomemory.NewMemoryGroup()
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "test", Stats: map[string]int64{"gold": 10}}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{}
				// Main equipment/quick slots are full in the success case, empty
				// in the failure case: neither can override the pet segment.
				if !fullPetBag {
					for slot := 3; slot <= 64; slot++ {
						slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 8000, Count: 1}
					}
				} else {
					for slot := 140; slot <= 188; slot++ {
						slots[fmt.Sprintf("7:%d", slot)] = dnfrepo.ItemStack{ItemID: 8000, Count: 1}
					}
				}
				before := dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}
				if err := repos.Inventory.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				letter := dnfrepo.MailRecord{MailID: "1", Gold: 100, Attachments: []dnfrepo.MailAttachment{{ItemID: 9001, Count: 1, Extra: map[string]string{"quality_seed": "42"}}}}
				if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": letter}}); err != nil {
					t.Fatal(err)
				}
				request := alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: resolver, Body: mailIDListBody(1)}
				for attempt := 0; attempt < 2; attempt++ {
					if _, err := dnfmail.NewHandler().Handle(ctx, request); err != nil {
						t.Fatal(err)
					}
				}
				inventory, _, err := repos.Inventory.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				character, _, err := repos.Character.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				box, _, err := repos.Mailbox.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				if fullPetBag {
					remaining := box.Mails["1"]
					if !reflect.DeepEqual(inventory.Slots, slots) || character.Stats["gold"] != 10 || remaining.Claimed || remaining.Deleted || remaining.Gold != 100 || !reflect.DeepEqual(remaining.Attachments, letter.Attachments) {
						t.Fatal("full pet bag changed inventory, gold or the unclaimed attachment")
					}
					return
				}
				got, ok := inventory.Slots["7:140"]
				if !ok || got.ItemID != 9001 || got.Count != 1 || got.Extra["quality_seed"] != "42" || got.Extra["equipment_type"] != "[artifact "+color+"]" || character.Stats["gold"] != 110 {
					t.Fatalf("claimed artifact or gold: %+v / %d", got, character.Stats["gold"])
				}
				delete(inventory.Slots, "7:140")
				if !reflect.DeepEqual(inventory.Slots, slots) {
					t.Fatal("claim changed another slot or delivered twice")
				}
			})
		}
	}
}
