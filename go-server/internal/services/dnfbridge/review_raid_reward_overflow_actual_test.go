package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestReviewActualAllAntonAndLukeConfiguredRewardsOverflow(t *testing.T) {
	catalog, anton := actualAntonRewardFixture(t)
	luke, e := raid.LoadLukeRewardConfiguration(catalog.source)
	if e != nil {
		t.Fatal(e)
	}
	fiend, e := raid.LoadFiendWarRewardConfiguration(catalog.source)
	if e != nil {
		t.Fatal(e)
	}
	for mode, cfg := range map[byte]raid.AntonRewardConfiguration{1: anton, 2: luke, 3: fiend} {
		for phase, p := range cfg.Phases {
			for _, choice := range p.Choices {
				if choice.Type != "party_card" && choice.Type != "pcroom_card" && choice.Type != "squad_item" {
					continue
				}
				t.Run(fmt.Sprintf("mode%d/phase%d/container%d/type%s", mode, phase, choice.ContainerID, choice.Type), func(t *testing.T) {
					now := time.Now().UTC()
					plan, e := resolveCurrentAntonConfiguredReward(catalog, choice, zeroAntonRoll, now)
					if e != nil {
						t.Fatal(e)
					}
					g := memory.NewMemoryGroup()
					ctx := context.Background()
					if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"}); e != nil {
						t.Fatal(e)
					}
					iv := repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}
					var want int64
					wantMails := 0
					shared := map[uint32]uint32{}
					for _, r := range plan.Rewards {
						if _, ok := currentDisjointWarehouseFixedSlots[r.Definition.ItemID]; ok {
							shared[r.Definition.ItemID] += r.Count
							continue
						}
						wantMails++
						for slot := r.Definition.SlotStart; slot <= r.Definition.SlotEnd; slot++ {
							iv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
						}
						want += int64(r.Count)
						if _, ok := currentDisjointWarehouseFixedSlots[r.Definition.ItemID]; ok {
							t.Fatal("configured shared reward needs separate expectation", r.Definition.ItemID)
						}
					}
					for slot := currentDungeonPickupQuickSlotStart; slot <= currentDungeonPickupQuickSlotEnd; slot++ {
						iv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
					}
					if e := g.Inventory.Save(ctx, iv); e != nil {
						t.Fatal(e)
					}
					cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: mode, Phase: byte(phase), Kind: map[string]byte{"party_card": 1, "pcroom_card": 2, "squad_item": 3}[choice.Type], MinimumClears: p.MinimumClears, OwnedClears: p.MinimumClears, Fingerprint: plan.Fingerprint, CompletedAt: now}
					result, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
					if e != nil || result.Replayed {
						t.Fatal(e)
					}
					after, _, _ := g.Inventory.Load(ctx, "31")
					if !reflect.DeepEqual(after, iv) {
						t.Fatal("overflow changed occupied page")
					}
					m, count := reviewRaidOverflowMailCount(t, g, "31")
					if m != wantMails || count != want {
						t.Fatal("wrong attachment total", m, count, want)
					}
					account, _, _ := g.AccountInventory.Load(ctx, "account")
					for id, n := range shared {
						slot := currentDisjointWarehouseFixedSlots[id]
						if account.Slots[repo.AccountSharedInventorySlotKey(slot)].Count != int64(n) {
							t.Fatal("shared final reward missing", id, n)
						}
					}
					mb, _, _ := g.Mailbox.Load(ctx, "31")
					for _, mail := range mb.Mails {
						for _, a := range mail.Attachments {
							if len(a.RawEntry) != currentItemListEntryWireSize || a.Extra["source"] != "raid_reward" || a.Extra["pvf_path"] == "" {
								t.Fatal("attachment projection differs", a.ItemID, len(a.RawEntry), a.Extra)
							}
						}
					}
					result, e = grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
					if e != nil || !result.Replayed {
						t.Fatal(e)
					}
					m, count = reviewRaidOverflowMailCount(t, g, "31")
					if m != wantMails || count != want {
						t.Fatal("configured replay duplicated")
					}
				})
			}
		}
	}
}

// An explicit private server-frozen unit plan, not a configured Anton reward:
// two copies of the actual raid equipment100050518 exercise sequential placement, where one
// empty equipment slot accepts the first instance before the second is full.
func TestReviewActualPVFEquipmentPartialPlacementRollback(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	def, e := catalog.ResolveItem(100050518)
	if e != nil || def.Kind != dungeonDropItemEquipment {
		t.Fatal("actual equipment fixture", def.Kind, e)
	}
	placement, e := currentBoosterPlacement(def)
	if e != nil || placement != currentBoosterRewardMain {
		t.Fatal("fixture must use ordinary raid equipment", placement, e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"})
	iv := repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}
	for slot := def.SlotStart; slot <= def.SlotEnd; slot++ {
		iv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	// Ordinary equipment also falls back to quick slots. Occupy that route
	// before leaving exactly one equipment slot for the partial-placement case.
	for slot := currentDungeonPickupQuickSlotStart; slot <= currentDungeonPickupQuickSlotEnd; slot++ {
		iv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	delete(iv.Slots, currentDungeonPickupMainSlotKey(def.SlotStart))
	g.Inventory.Save(ctx, iv)
	row := currentBoosterReward{Definition: def, Count: 2, Placement: placement}
	// First verify the old projector actually performs a partial mutation.
	old := repo.CloneInventory(iv)
	if _, e := grantCurrentBoosterReward(&old, catalog.source, row); e == nil || reflect.DeepEqual(old, iv) {
		t.Fatal("partial placement fixture did not reach actual full after first item", e)
	}
	now := time.Now().UTC()
	plan := currentAntonRewardPlan{Choice: raid.AntonRewardChoice{ContainerID: 100050518, Type: "squad_item", Weight: 1}, Rewards: []currentBoosterReward{row}}
	encoded, e := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{plan.Choice, plan.Rewards})
	if e != nil {
		t.Fatal(e)
	}
	plan.Fingerprint = sha256.Sum256(encoded)
	cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 0, Kind: 3, MinimumClears: 1, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: now}
	if _, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd); e != nil {
		t.Fatal(e)
	}
	after, _, _ := g.Inventory.Load(ctx, "31")
	m, count := reviewRaidOverflowMailCount(t, g, "31")
	if !reflect.DeepEqual(after, iv) || m != 1 || count != 2 {
		t.Fatal("partial success not restored before complete mail", m, count)
	}
	mb, _, _ := g.Mailbox.Load(ctx, "31")
	for _, mail := range mb.Mails {
		if len(mail.Attachments) != 2 {
			t.Fatal("equipment instances flattened")
		}
		for _, a := range mail.Attachments {
			if a.Count != 1 || a.ItemID != 100050518 || len(a.RawEntry) != currentItemListEntryWireSize {
				t.Fatal("equipment projection lost")
			}
		}
	}
}
func TestReviewActualRaidOverflowMailClaimUsesExistingOwner(t *testing.T) {
	catalog, cfg := actualAntonRewardFixture(t)
	g, plan, cmd := reviewRaidOverflowFixture(t, catalog, cfg, 10304483)
	ctx := context.Background()
	if _, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd); err != nil {
		t.Fatal(err)
	}
	iv, _, _ := g.Inventory.Load(ctx, "31")
	for _, r := range plan.Rewards {
		delete(iv.Slots, currentDungeonPickupMainSlotKey(r.Definition.SlotStart))
	}
	if err := g.Inventory.Save(ctx, iv); err != nil {
		t.Fatal(err)
	}
	mb, _, _ := g.Mailbox.Load(ctx, "31")
	var mailIDs []uint32
	for _, mail := range mb.Mails {
		id, err := strconv.ParseUint(mail.MailID, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		mailIDs = append(mailIDs, uint32(id))
	}
	if len(mailIDs) != 2 {
		t.Fatal("final rows not mailed", mailIDs)
	}
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{conn: &bufferConn{}, channel: channelcatalog.Channel{ID: 38, Type: 1}, selectedCharacterID: 31}
	s.bindGameSessionCharacter(session, 31)
	for attempt := 0; attempt < 2; attempt++ {
		handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(mailIDs...))
		if err != nil || !handled {
			t.Fatal(err)
		}
		after, _, _ := g.Inventory.Load(ctx, "31")
		for _, r := range plan.Rewards {
			var count int64
			for _, stack := range after.Slots {
				if stack.ItemID == int64(r.Definition.ItemID) {
					count += stack.Count
					if len(stack.RawEntry) != currentItemListEntryWireSize {
						t.Fatal("claimed metadata lost")
					}
				}
			}
			if count != int64(r.Count) {
				t.Fatal("claim missing or duplicated", attempt, r.Definition.ItemID, count, r.Count)
			}
		}
	}
	replay, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if err != nil || !replay.Replayed {
		t.Fatal("claimed receipt not retained", err)
	}
}
