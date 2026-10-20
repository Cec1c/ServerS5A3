package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"
)

type reviewRaidMailUOW struct {
	repo.AccountCharacterAssetUnitOfWork
	mode string
	once sync.Once
}

func (u *reviewRaidMailUOW) WithinAccountCharacterMailboxAssets(ctx context.Context, a, c string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error) error {
	with := u.AccountCharacterAssetUnitOfWork.(repo.AccountCharacterMailboxAssetUnitOfWork)
	if u.mode == "unknown_commit" {
		e := with.WithinAccountCharacterMailboxAssets(ctx, a, c, apply)
		if e != nil {
			return e
		}
		unknown := false
		u.once.Do(func() { unknown = true })
		if unknown {
			return errors.New("commit acknowledgement unavailable after durable fixture commit")
		}
		return nil
	}
	return with.WithinAccountCharacterMailboxAssets(ctx, a, c, func(ai repo.AccountInventoryRepository, ch repo.CharacterRepository, iv repo.InventoryRepository, eq repo.EquipmentRepository, mb repo.MailboxRepository) error {
		if e := apply(ai, ch, iv, eq, mb); e != nil {
			return e
		}
		if u.mode == "late_abort" {
			return errors.New("late staged abort")
		}
		if u.mode == "mail_panic" {
			panic("post-project panic")
		}
		return nil
	})
}

type reviewRaidNoMailUOW struct {
	repo.AccountCharacterAssetUnitOfWork
}

func reviewRaidOverflowFixture(t *testing.T, catalog *pvfDungeonDropCatalog, config raid.AntonRewardConfiguration, id uint32) (repo.Group, currentAntonRewardPlan, raid.RewardCommand) {
	t.Helper()
	var choice raid.AntonRewardChoice
	for _, c := range config.Phases[0].Choices {
		if c.ContainerID == id {
			choice = c
		}
	}
	if choice.ContainerID == 0 {
		t.Fatal("actual choice absent", id)
	}
	now := time.Now().UTC()
	plan, e := resolveCurrentAntonConfiguredReward(catalog, choice, zeroAntonRoll, now)
	if e != nil || len(plan.Rewards) != 1 {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"}); e != nil {
		t.Fatal(e)
	}
	def := plan.Rewards[0].Definition
	slots := map[string]repo.ItemStack{}
	for slot := def.SlotStart; slot <= def.SlotEnd; slot++ {
		slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	for slot := currentDungeonPickupQuickSlotStart; slot <= currentDungeonPickupQuickSlotEnd; slot++ {
		slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: slots}); e != nil {
		t.Fatal(e)
	}
	cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 0, Kind: map[string]byte{"party_card": 1, "pcroom_card": 2, "squad_item": 3}[choice.Type], Card: 0, MinimumClears: config.Phases[0].MinimumClears, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: now}
	return g, plan, cmd
}
func reviewRaidOverflowMailCount(t *testing.T, g repo.Group, character string) (int, int64) {
	t.Helper()
	m, _, e := g.Mailbox.Load(context.Background(), character)
	if e != nil {
		t.Fatal(e)
	}
	var count int64
	for _, mail := range m.Mails {
		for _, a := range mail.Attachments {
			count += a.Count
		}
	}
	return len(m.Mails), count
}
func TestReviewActualRaidOverflowIdentityFailureReplayAndConcurrency(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	for _, mode := range []string{"success", "late_abort", "unknown_commit", "foreign_account", "foreign_character", "changed_fingerprint", "changed_card", "no_mail_capability", "cancelled", "concurrent_same", "concurrent_different"} {
		t.Run(mode, func(t *testing.T) {
			g, plan, cmd := reviewRaidOverflowFixture(t, catalog, config, 10304483)
			base := g.AccountAssets
			oldInv, _, _ := g.Inventory.Load(context.Background(), "31")
			switch mode {
			case "late_abort", "unknown_commit":
				g.AccountAssets = &reviewRaidMailUOW{AccountCharacterAssetUnitOfWork: base, mode: mode}
			case "foreign_account":
				cmd.AccountID = "other"
			case "foreign_character":
				cmd.CharacterID = "99"
			case "changed_fingerprint":
				cmd.Fingerprint[0] ^= 1
			case "no_mail_capability":
				g.AccountAssets = reviewRaidNoMailUOW{base}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if mode == "concurrent_same" || mode == "concurrent_different" {
				const n = 8
				var wg sync.WaitGroup
				results := make(chan error, n)
				replays := make(chan bool, n)
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						c := cmd
						if mode == "concurrent_different" && i%2 == 1 {
							c.Card = 1
						}
						r, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, c)
						results <- e
						replays <- r.Replayed
					}(i)
				}
				wg.Wait()
				close(results)
				close(replays)
				failures, commits := 0, 0
				for e := range results {
					if e != nil {
						if !errors.Is(e, raid.ErrRewardReceipt) {
							t.Fatal(e)
						}
						failures++
					}
				}
				for replay := range replays {
					if !replay {
						commits++
					}
				}
				mails, count := reviewRaidOverflowMailCount(t, g, "31")
				if mails != 1 || count != 1 || (mode == "concurrent_same" && (failures != 0 || commits != 1)) || (mode == "concurrent_different" && failures != 4) {
					t.Fatal("concurrent duplicate or owner conflict", mails, count, failures, commits)
				}
				return
			}
			result, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
			if mode == "changed_card" {
				if e != nil {
					t.Fatal(e)
				}
				cmd.Card = 1
				_, e = grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
				if !errors.Is(e, raid.ErrRewardReceipt) {
					t.Fatal("receipt card substitution", e)
				}
				m, c := reviewRaidOverflowMailCount(t, g, "31")
				if m != 1 || c != 1 {
					t.Fatal("changed card duplicated")
				}
				return
			}
			if mode == "success" || mode == "unknown_commit" {
				if mode == "success" && (e != nil || result.Replayed) {
					t.Fatal(e)
				}
				if mode == "unknown_commit" && e == nil {
					t.Fatal("unknown commit fixture not exercised")
				}
				m, c := reviewRaidOverflowMailCount(t, g, "31")
				if m != 1 || c != 1 {
					t.Fatal("durable fixture commit missing", m, c)
				}
				replay, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
				if e != nil || !replay.Replayed {
					t.Fatal("lost reply replay", e)
				}
				m, c = reviewRaidOverflowMailCount(t, g, "31")
				if m != 1 || c != 1 {
					t.Fatal("replay duplicated", m, c)
				}
				return
			}
			if e == nil {
				t.Fatal("invalid grant accepted", mode)
			}
			m, c := reviewRaidOverflowMailCount(t, g, "31")
			if m != 0 || c != 0 {
				t.Fatal("failed transaction leaked mail", m, c)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "31")
			character, _, _ := g.Character.Load(context.Background(), "31")
			if !reflect.DeepEqual(inv, oldInv) || len(character.Stats) != 0 {
				t.Fatal("failed transaction leaked asset or receipt")
			}
			if mode == "late_abort" {
				g.AccountAssets = base
				r, e := grantCurrentAntonRewardPlan(context.Background(), g, catalog, plan, cmd)
				if e != nil || r.Replayed {
					t.Fatal("rolled back grant cannot retry", e)
				}
				m, c = reviewRaidOverflowMailCount(t, g, "31")
				if m != 1 || c != 1 {
					t.Fatal("retry not exactly once")
				}
			}
		})
	}
}
func TestReviewActualRaidUncappedMergeRetainsNormalContract(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	g, plan, cmd := reviewRaidOverflowFixture(t, catalog, config, 10304474)
	r := plan.Rewards[0]
	def := r.Definition
	if _, shared := currentDisjointWarehouseFixedSlots[def.ItemID]; shared || r.Count != 30 || def.ItemID != 10304529 || def.StackLimit != 0 {
		t.Fatal("actual non-shared reward fixture invalid", r.Count, def.ItemID, def.StackLimit)
	}
	// Cera fillRange takes one unit at MaxInt32, then the existing pickup
	// owner can merge the remainder up to MaxInt64. Preserve this current
	// uncapped contract; it is NOT a full-page or positive PVF cap model.
	slot := int16(358)
	ctx := context.Background()
	warehouse := repo.ItemStack{ItemID: 10096296, Count: 1} // actual legacy foreign occupant cannot be evicted or merged.
	if e := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account", Slots: map[string]repo.ItemStack{repo.AccountSharedInventorySlotKey(slot): warehouse}}); e != nil {
		t.Fatal(e)
	}
	inv, _, _ := g.Inventory.Load(ctx, "31")
	partial := currentPVFGrantIdentity(def)
	partial.ItemID = int64(def.ItemID)
	partial.Count = math.MaxInt32 - 1
	inv.Slots[currentDungeonPickupMainSlotKey(def.SlotStart)] = partial
	if e := g.Inventory.Save(ctx, inv); e != nil {
		t.Fatal(e)
	}
	before := repo.CloneInventory(inv)
	result, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if e != nil || result.Replayed {
		t.Fatal(e)
	}
	after, _, _ := g.Inventory.Load(ctx, "31")
	account, _, _ := g.AccountInventory.Load(ctx, "account")
	m, count := reviewRaidOverflowMailCount(t, g, "31")
	key := currentDungeonPickupMainSlotKey(def.SlotStart)
	if after.Slots[key].Count != before.Slots[key].Count+int64(r.Count) || len(after.Slots) != len(before.Slots) || !reflect.DeepEqual(account.Slots[repo.AccountSharedInventorySlotKey(slot)], warehouse) || m != 0 || count != 0 {
		t.Fatal("character partial placement or unrelated foreign warehouse was changed", m, count, r.Count, account.Slots[repo.AccountSharedInventorySlotKey(slot)].Count)
	}
	replay, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if e != nil || !replay.Replayed {
		t.Fatal(e)
	}
	m, count = reviewRaidOverflowMailCount(t, g, "31")
	if m != 0 || count != 0 {
		t.Fatal("uncapped replay created mail")
	}
}

// These shared-item rows are private server-frozen projector fixtures built
// from real PVF definitions, not claims that Anton's configured plans contain
// these items. No definition or PVF stack limit is fabricated.
func TestReviewActualPVFSharedRewardPlacementAndExactOverflow(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	var positiveCap int
	for id, slot := range currentDisjointWarehouseFixedSlots {
		def, e := catalog.ResolveItem(id)
		if e != nil {
			t.Fatal(id, e)
		}
		t.Logf("actual shared item=%d cap=%d path=%s", id, def.StackLimit, def.PVFPath)
		placement, e := currentBoosterPlacement(def)
		if e != nil {
			t.Fatal(e)
		}
		row := currentBoosterReward{Definition: def, Count: 3, Placement: placement}
		for _, mode := range []string{"normal", "foreign", "partial_if_actual_capped"} {
			if mode == "partial_if_actual_capped" && def.StackLimit < 3 {
				continue
			}
			t.Run(fmt.Sprintf("%d/%s", id, mode), func(t *testing.T) {
				ctx := context.Background()
				g := memory.NewMemoryGroup()
				ai := repo.AccountInventoryRecord{AccountID: "account", Slots: map[string]repo.ItemStack{}}
				iv := repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}
				for n := def.SlotStart; n <= def.SlotEnd; n++ {
					iv.Slots[currentDungeonPickupMainSlotKey(n)] = repo.ItemStack{ItemID: 10096296, Count: 1}
				}
				for n := currentDungeonPickupQuickSlotStart; n <= currentDungeonPickupQuickSlotEnd; n++ {
					iv.Slots[currentDungeonPickupMainSlotKey(n)] = repo.ItemStack{ItemID: 10096296, Count: 1}
				}
				var wantMail int64
				key := repo.AccountSharedInventorySlotKey(slot)
				if mode == "foreign" {
					ai.Slots[key] = repo.ItemStack{ItemID: 10096296, Count: 1}
					wantMail = 3
				}
				if mode == "partial_if_actual_capped" {
					ai.Slots[key] = repo.ItemStack{ItemID: int64(id), Count: def.StackLimit - 2}
					wantMail = 1
					positiveCap++
				}
				old := repo.CloneInventory(iv)
				oldForeign := ai.Slots[key]
				cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", CompletedAt: time.Now().UTC()}
				_, changed, mailed, e := grantCurrentAntonRewardRow(ctx, &raid.RewardAssets{AccountInventory: &ai, Inventory: &iv, Mailbox: g.Mailbox}, catalog, row, cmd)
				if e != nil || !reflect.DeepEqual(iv, old) {
					t.Fatal("shared placement changed full character page", e)
				}
				m, count := reviewRaidOverflowMailCount(t, g, "31")
				if count != wantMail || (wantMail > 0 && (!mailed || m != 1)) || (wantMail == 0 && (mailed || m != 0)) {
					t.Fatal("inaccurate remaining", m, count, wantMail)
				}
				if mode == "foreign" {
					if changed || !reflect.DeepEqual(ai.Slots[key], oldForeign) {
						t.Fatal("foreign shared occupant overwritten")
					}
				} else {
					if !changed {
						t.Fatal("shared grant missing")
					}
					want := int64(3)
					if mode == "partial_if_actual_capped" {
						want = def.StackLimit
					}
					if ai.Slots[key].ItemID != int64(id) || ai.Slots[key].Count != want {
						t.Fatal("shared amount incorrect")
					}
				}
			})
		}
	}
	if positiveCap == 0 {
		t.Log("actual PVF has no shared definition with positive cap >=3; partial shared saturation is not reachable with these real definitions")
	}
}
