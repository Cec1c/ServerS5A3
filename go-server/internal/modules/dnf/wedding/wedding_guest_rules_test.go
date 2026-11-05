package wedding

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingGuestRulesRepeatedPVFSectionsAndGiftTierBoundaries(t *testing.T) {
	text := `[money gift limit]
10000 400000000
[/money gift limit]
[money gift charge]
50
[/money gift charge]
[ceremony]
0 500000 0 10 1 2000000 1 20 2 0 2 30
[/ceremony]
[guest gift]
99999 0 10002622 3 1 10002622 3 2 10002622 3
[/guest gift]
[guest gift]
400000000 0 10002623 10 1 10002623 10 2 10002623 10
[/guest gift]`
	rules, err := LoadGuestRules(lovePointTestSource{SystemPath: text})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Gifts) != 2 || rules.Charge != 50 || rules.MinimumGold != 10000 || rules.MaximumGold != 400000000 || rules.Capacity[0] != 10 || rules.Capacity[1] != 20 || rules.Capacity[2] != 30 {
		t.Fatalf("rules %+v", rules)
	}
	for _, row := range []struct{ gold, id int64 }{{10000, 10002622}, {99999, 10002622}, {100000, 10002623}, {400000000, 10002623}} {
		for _, kind := range []byte{0, 1, 2} {
			gift, err := rules.GiftFor(row.gold, kind)
			if err != nil || len(gift) != 1 || gift[0].ItemID != row.id {
				t.Fatalf("tier gold=%d kind=%d %+v/%v", row.gold, kind, gift, err)
			}
		}
	}
	for _, gold := range []int64{9999, 400000001} {
		if _, err := rules.GiftFor(gold, 0); err == nil {
			t.Fatal("out-of-range gift", gold)
		}
	}
}

func TestWeddingGuestGiftWishAndEscrowCommitToOneBookRow(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	guestPay(t, o, req, rules, ledger, 10000)
	rows, err := o.LoadGuestComments(context.Background(), CommentRequest{Actor: req.Actor, Key: ledger.Key, Now: req.Now})
	if err != nil || len(rows) != 1 || rows[0].AuthorID != "3" || rows[0].Text != "新婚快乐" {
		t.Fatalf("paid wish was not visible %+v/%v", rows, err)
	}
	if _, err := o.GiveWeddingGold(context.Background(), rules, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: 10000, Wish: "replay", Now: req.Now}); err == nil {
		t.Fatal("gift replay replaced the committed wish")
	}
	rows, err = o.LoadGuestComments(context.Background(), CommentRequest{Actor: req.Actor, Key: ledger.Key, Now: req.Now})
	if err != nil || len(rows) != 1 || rows[0].Text != "新婚快乐" {
		t.Fatalf("wish replay %+v/%v", rows, err)
	}
	// A corrupt book must roll back a prospective debit rather than leaving
	// paid escrow without the user's visible message.
	o2, req2, rules2, ledger2 := guestOwnerFixture(t)
	if _, err := o2.InviteWeddingGuests(context.Background(), req2, rules2, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	scope, _ := r.WeddingPairScope(ledger2.Hosts[:])
	record, _, _ := o2.Repositories.Settings.Load(context.Background(), scope)
	record.Values["comments_v1:"+strconv.FormatUint(uint64(ledger2.Key), 10)] = "invalid json"
	if err := o2.Repositories.Settings.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := o2.GiveWeddingGold(context.Background(), rules2, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger2.Key, Gold: 10000, Wish: "hello", Now: req2.Now}); err == nil {
		t.Fatal("corrupt book did not stop gift")
	}
	c, _, _ := o2.Repositories.Character.Load(context.Background(), "3")
	if c.Stats["gold"] != 10000000 {
		t.Fatal("failed wish left a partial debit")
	}
	stored, _, _ := o2.ReadGuestLedger(context.Background(), ledger2.Hosts[:], ledger2.At)
	if stored.Invitations["3"].Gold != 0 {
		t.Fatal("failed wish left partial escrow")
	}
}

func TestWeddingGuestLegacyReservationAndAdmittedBootstrapOnlyChangesTokenMetadata(t *testing.T) {
	for _, phase := range []byte{Reserved, InCeremony} {
		t.Run(strconv.Itoa(int(phase)), func(t *testing.T) {
			o, now := ownerFixture(t)
			req := pairRequest(now)
			at := now.Unix() - 300
			for _, id := range []string{"1", "2"} {
				c, _, _ := o.Repositories.Character.Load(context.Background(), id)
				partner := int64(1)
				if id == "1" {
					partner = 2
				}
				state := State{PartnerID: partner, Phase: phase, CeremonyAt: at, CeremonyChannel: 10, CeremonyArea: 8}
				if phase == InCeremony {
					state.CeremonyReadyAt = at
				}
				write(&c, state)
				if err := r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
				if err := o.Repositories.Quest.Save(context.Background(), r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}}}); err != nil {
					t.Fatal(err)
				}
			}
			general, err := o.EnsureRelationshipToken(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			oldToken := general.Actor.Stats["wedding_v1_relation_token"]
			quests := map[string]r.QuestRecord{}
			bags := map[string]r.InventoryRecord{}
			for _, id := range []string{"1", "2"} {
				quests[id], _, _ = o.Repositories.Quest.Load(context.Background(), id)
				bags[id], _, _ = o.Repositories.Inventory.Load(context.Background(), id)
			}
			ledger, err := o.EnsureGuestWedding(context.Background(), req, &GuestRules{Capacity: map[byte]int{0: 10}})
			if err != nil {
				t.Fatal(err)
			}
			if ledger.At != at || ledger.Key == uint32(oldToken) || len(ledger.Invitations) != 0 || ledger.ReservationPayment.PayerID != "" {
				t.Fatalf("bootstrap minted or reused debt %+v", ledger)
			}
			for _, id := range []string{"1", "2"} {
				c, _, _ := o.Repositories.Character.Load(context.Background(), id)
				state, _ := Read(c)
				if state.RelationToken != int64(ledger.Key) || state.Phase != phase || state.CeremonyAt != at || c.Stats["gold"] != 10000000 {
					t.Fatalf("legacy gameplay state changed %+v", state)
				}
				q, _, _ := o.Repositories.Quest.Load(context.Background(), id)
				bag, _, _ := o.Repositories.Inventory.Load(context.Background(), id)
				if !reflect.DeepEqual(q, quests[id]) || !reflect.DeepEqual(bag, bags[id]) {
					t.Fatal("legacy bootstrap changed quests or items")
				}
			}
			if _, err := o.GiveWeddingGold(context.Background(), &GuestRules{MinimumGold: 10000, MaximumGold: 400000000}, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: 10000, Now: time.Unix(at+400, 0)}); err == nil {
				t.Fatal("bootstrap authorized an uninvited legacy gift")
			}
		})
	}
}
