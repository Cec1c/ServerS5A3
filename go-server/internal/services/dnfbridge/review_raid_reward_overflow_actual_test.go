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
	"testing"
	"time"
)

func TestReviewActualAllAntonAndLukeConfiguredRewardsOverflow(t *testing.T) {
	catalog, anton := actualAntonRewardFixture(t)
	luke, e := raid.LoadLukeRewardConfiguration(catalog.source)
	if e != nil {
		t.Fatal(e)
	}
	for mode, cfg := range map[byte]raid.AntonRewardConfiguration{1: anton, 2: luke} {
		for phase, p := range cfg.Phases {
			for _, choice := range p.Choices {
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
					for _, r := range plan.Rewards {
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
					if m != len(plan.Rewards) || count != want {
						t.Fatal("wrong attachment total", m, count, want)
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
					if m != len(plan.Rewards) || count != want {
						t.Fatal("configured replay duplicated")
					}
				})
			}
		}
	}
}

// An explicit private server-frozen unit plan, not a configured Anton reward:
// two copies of real PVF equipment35381 exercise sequential placement, where one
// empty equipment slot accepts the first instance before the second is full.
func TestReviewActualPVFEquipmentPartialPlacementRollback(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	def, e := catalog.ResolveItem(35381)
	if e != nil || def.Kind != dungeonDropItemEquipment {
		t.Fatal("actual equipment fixture", def.Kind, e)
	}
	placement, e := currentBoosterPlacement(def)
	if e != nil {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"})
	iv := repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}
	for slot := def.SlotStart; slot <= def.SlotEnd; slot++ {
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
	plan := currentAntonRewardPlan{Choice: raid.AntonRewardChoice{ContainerID: 35381, Type: "squad_item", Weight: 1}, Rewards: []currentBoosterReward{row}}
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
			if a.Count != 1 || a.ItemID != 35381 || len(a.RawEntry) != currentItemListEntryWireSize {
				t.Fatal("equipment projection lost")
			}
		}
	}
}
func TestReviewActualRaidOverflowMailClaimUsesExistingOwner(t *testing.T) {
	catalog, cfg := actualAntonRewardFixture(t)
	g, plan, cmd := reviewRaidOverflowFixture(t, catalog, cfg, 10304483)
	ctx := context.Background()
	if _, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd); e != nil {
		t.Fatal(e)
	}
	iv, _, _ := g.Inventory.Load(ctx, "31")
	delete(iv.Slots, currentDungeonPickupMainSlotKey(plan.Rewards[0].Definition.SlotStart))
	g.Inventory.Save(ctx, iv)
	mb, _, _ := g.Mailbox.Load(ctx, "31")
	var mailID uint32
	for _, mail := range mb.Mails {
		if mail.MailID != "1" {
			t.Fatal("unexpected first mail ID", mail.MailID)
		}
		mailID = 1
	}
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{conn: &bufferConn{}, channel: channelcatalog.Channel{ID: 38, Type: 1}, selectedCharacterID: 31}
	s.bindGameSessionCharacter(session, 31)
	handled, e := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(mailID))
	if e != nil || !handled {
		t.Fatal(e)
	}
	after, _, _ := g.Inventory.Load(ctx, "31")
	var amount int64
	for _, stack := range after.Slots {
		if stack.ItemID == 10304483 {
			amount += stack.Count
			if len(stack.RawEntry) != currentItemListEntryWireSize {
				t.Fatal("claimed raw lost", len(stack.RawEntry))
			}
		}
	}
	if amount != 1 {
		t.Fatal("overflow mail cannot be claimed", amount)
	}
	s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(mailID))
	after, _, _ = g.Inventory.Load(ctx, "31")
	amount = 0
	for _, stack := range after.Slots {
		if stack.ItemID == 10304483 {
			amount += stack.Count
		}
	}
	if amount != 1 {
		t.Fatal("mail claim replay duplicated")
	}
	result, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if e != nil || !result.Replayed {
		t.Fatal("claimed mail receipt no longer replays", e)
	}
}
