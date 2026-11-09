package wedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func extensionFixture(t *testing.T) (Owner, PairRequest, *ExtensionRules) {
	t.Helper()
	o, req, _ := marriedLovePointFixture(t, 400)
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		s, _ := Read(c)
		s.MarriedAt = req.Now.Unix() - 100
		s.LastLoveDay, s.LastWeddingDay = 10, 11
		write(&c, s)
		if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
	}
	if e := o.Repositories.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:65": {ItemID: 10002766, Count: 1}, "0:66": {ItemID: 10002765, Count: 5}}}); e != nil {
		t.Fatal(e)
	}
	o.Rules.Furniture = map[int64]Furniture{100: {ItemID: 100, Points: 10, Positions: map[uint16]bool{0: true}}}
	rules := &ExtensionRules{Seeds: map[int64]int64{10002766: 1}, Trees: map[int64]TreeRule{1: {Index: 1, Days: 30, DailyWater: 2, MaximumGrowth: 30, Water: map[int64]int64{10002765: 10}, Rewards: []TreeReward{{ItemID: 10002901, Weight: 100000, Count: 2}}}}, Capsule: CapsuleRules{MaximumOwn: 10, MaximumTaken: 10, Gain: 2, MaximumGauge: 10, ItemID: 10002910}, DivorceGold: [2]int64{500000, 500000}, DivorceDays: 3}
	return o, req, rules
}
func extensionStamp(op uint16, sequence uint64) ExtensionReceipt {
	return ExtensionReceipt{Session: strings.Repeat("ab", 32), Sequence: sequence, Opcode: op, Fingerprint: fmt.Sprint(op, ":", sequence)}
}
func extensionTreeCommand(op uint16, sequence uint64) TreeCommand {
	return TreeCommand{Opcode: op, Day: 1, Receipt: extensionStamp(op, sequence), Slots: itemplacement.SlotPolicy{ListType: r.MainInventoryListType, Start: 65, End: 120}}
}
func saveExtensionTestRoom(ctx context.Context, settings r.SettingsRepository, scope string, room Room) error {
	raw, e := json.Marshal(room)
	if e != nil {
		return e
	}
	return settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"room": string(raw)}})
}

func TestWeddingExtensionsTreeQuotaHarvestMailAtomicAndReplay(t *testing.T) {
	o, req, rules := extensionFixture(t)
	ctx := context.Background()
	cmd := extensionTreeCommand(1048, 1)
	cmd.Slot, cmd.ItemID = 65, 10002766
	if _, e := o.MutateTree(ctx, req, rules, cmd); e != nil {
		t.Fatal(e)
	}
	cmd = extensionTreeCommand(1049, 2)
	cmd.Slot, cmd.ItemID = 66, 10002765
	for sequence := uint64(2); sequence <= 3; sequence++ {
		cmd.Receipt = extensionStamp(1049, sequence)
		if _, e := o.MutateTree(ctx, req, rules, cmd); e != nil {
			t.Fatal(e)
		}
	}
	cmd.Receipt = extensionStamp(1049, 4)
	if _, e := o.MutateTree(ctx, req, rules, cmd); e == nil {
		t.Fatal("daily quota bypass")
	}
	cmd.Day = 2
	cmd.Receipt = extensionStamp(1049, 5)
	if _, e := o.MutateTree(ctx, req, rules, cmd); e != nil {
		t.Fatal(e)
	}
	cmd = extensionTreeCommand(1050, 6)
	cmd.Day = 2
	cmd.Draw = func(n int64) (int64, error) { return 0, nil }
	cmd.Deliver = func(m r.MailboxRepository, c r.CharacterRecord, reward TreeReward, key string) (string, error) {
		return r.AppendSystemMail(ctx, m, r.SystemMailDelivery{RecipientCharacterID: c.CharacterID, Title: "tree", Body: "tree", Source: "wedding_tree_harvest", CompletionKey: key, CreatedAt: req.Now, Attachments: []r.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	result, e := o.MutateTree(ctx, req, rules, cmd)
	if e != nil {
		t.Fatal(e)
	}
	if result.Room.Tree.Growth != 0 || len(result.MailIDs) != 2 {
		t.Fatalf("harvest %+v", result)
	}
	for _, id := range []string{"1", "2"} {
		box, found, e := o.Repositories.Mailbox.Load(ctx, id)
		if e != nil || !found || len(box.Mails) != 1 {
			t.Fatalf("mail %s %+v %v", id, box, e)
		}
	}
	result, e = o.MutateTree(ctx, req, rules, cmd)
	if e != nil || !result.Replayed || len(result.MailIDs) != 0 {
		t.Fatalf("replay %+v %v", result, e)
	}
	for _, id := range []string{"1", "2"} {
		box, _, _ := o.Repositories.Mailbox.Load(ctx, id)
		if len(box.Mails) != 1 {
			t.Fatal("duplicate reward")
		}
	}
}

func TestWeddingExtensionsSecondMailboxFailureRollsBackHarvest(t *testing.T) {
	o, req, rules := extensionFixture(t)
	ctx := context.Background()
	a, _, _ := o.Repositories.Character.Load(ctx, "1")
	b, _, _ := o.Repositories.Character.Load(ctx, "2")
	scope, _ := r.WeddingPairScope([]string{"1", "2"})
	room := Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}, Tree: TreeState{Index: 1, Expires: req.Now.Add(24 * time.Hour).Unix(), Growth: 30, WaterDay: 1, PlantSerial: 1}}
	_ = a
	_ = b
	// Set the persisted shared tree through the same lifecycle UOW used live.
	u := o.Repositories.CharacterTrade.(r.WeddingLifecycleUnitOfWork)
	if e := u.WithinWeddingLifecycle(ctx, []string{"1", "2"}, scope, func(g r.Group) error { return saveExtensionTestRoom(ctx, g.Settings, scope, room) }); e != nil {
		t.Fatal(e)
	}
	cmd := extensionTreeCommand(1050, 1)
	cmd.Draw = func(int64) (int64, error) { return 0, nil }
	calls := 0
	cmd.Deliver = func(m r.MailboxRepository, c r.CharacterRecord, reward TreeReward, key string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("second mailbox failure")
		}
		return r.AppendSystemMail(ctx, m, r.SystemMailDelivery{RecipientCharacterID: c.CharacterID, Source: "tree", CreatedAt: req.Now, Attachments: []r.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	if _, e := o.MutateTree(ctx, req, rules, cmd); e == nil {
		t.Fatal("fault accepted")
	}
	a, _, _ = o.Repositories.Character.Load(ctx, "1")
	b, _, _ = o.Repositories.Character.Load(ctx, "2")
	loaded, e := o.ReadRoom(ctx, a, b)
	if e != nil || !reflect.DeepEqual(loaded, room) {
		t.Fatalf("tree rolled forward %+v %v", loaded, e)
	}
	for _, id := range []string{"1", "2"} {
		box, _, e := o.Repositories.Mailbox.Load(ctx, id)
		if e != nil || len(box.Mails) != 0 {
			t.Fatalf("partial mail %s %+v %v", id, box, e)
		}
	}
}

func TestWeddingExtensionsCapsuleMakeGiveTakeAndFailedGrant(t *testing.T) {
	o, req, rules := extensionFixture(t)
	ctx := context.Background()
	c, _, _ := o.Repositories.Character.Load(ctx, "1")
	for i := 0; i < 8; i++ {
		if e := AccumulateCapsuleClear(&c, rules.Capsule); e != nil {
			t.Fatal(e)
		}
	}
	state, _ := ReadCapsule(c, rules.Capsule)
	if state.Progress != 10 {
		t.Fatal(state)
	}
	if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldWedding); e != nil {
		t.Fatal(e)
	}
	makeCmd := CapsuleCommand{Opcode: 1052, Receipt: extensionStamp(1052, 1)}
	result, e := o.MutateCapsule(ctx, req, rules.Capsule, makeCmd)
	if e != nil || result.States[0].Own != 1 || result.States[0].Progress != 0 {
		t.Fatalf("make %+v %v", result, e)
	}
	result, e = o.MutateCapsule(ctx, req, rules.Capsule, makeCmd)
	if e != nil || !result.Replayed || result.States[0].Own != 1 {
		t.Fatalf("replay %+v %v", result, e)
	}
	result, e = o.MutateCapsule(ctx, req, rules.Capsule, CapsuleCommand{Opcode: 1053, Receipt: extensionStamp(1053, 2)})
	if e != nil || result.States[1].Taken != 1 || result.States[0].Own != 0 {
		t.Fatalf("give %+v %v", result, e)
	}
	req.Actor, req.Partner = req.Partner, req.Actor
	take := CapsuleCommand{Opcode: 1054, Receipt: extensionStamp(1054, 1), Grant: func(*r.InventoryRecord, int64, int64, time.Time) error { return errors.New("bag full") }}
	if _, e = o.MutateCapsule(ctx, req, rules.Capsule, take); e == nil {
		t.Fatal("failed grant accepted")
	}
	c, _, _ = o.Repositories.Character.Load(ctx, "2")
	state, _ = ReadCapsule(c, rules.Capsule)
	if state.Taken != 1 {
		t.Fatal("stock consumed before grant")
	}
	take.Grant = func(bag *r.InventoryRecord, id, count int64, at time.Time) error {
		if bag.Slots == nil {
			bag.Slots = map[string]r.ItemStack{}
		}
		bag.Slots["0:65"] = r.ItemStack{ItemID: id, Count: count}
		return nil
	}
	result, e = o.MutateCapsule(ctx, req, rules.Capsule, take)
	if e != nil || result.Granted != 1 || result.States[0].Taken != 0 {
		t.Fatalf("take %+v %v", result, e)
	}
}

func TestWeddingExtensionsDivorceAtomicPrivilegesAndFees(t *testing.T) {
	for _, op := range []byte{6, 7} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			o, req, rules := extensionFixture(t)
			ctx := context.Background()
			before, _, _ := o.Repositories.Character.Load(ctx, "1")
			scope, _ := r.WeddingPairScope([]string{"1", "2"})
			if e := o.Repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: "1", States: map[int64]r.QuestState{20737: {Status: "completed"}, 9: {Status: "completed"}}}); e != nil {
				t.Fatal(e)
			}
			result, e := o.Divorce(ctx, req, rules, op)
			if e != nil {
				t.Fatal(e)
			}
			x, _ := Read(result.Actor)
			y, _ := Read(result.Partner)
			if x.Phase != Single || y.Phase != Single || x.LovePoints != 0 || x.CooldownUntil != req.Now.Add(72*time.Hour).Unix() || x.LastLoveDay != 10 || x.LastWeddingDay != 11 {
				t.Fatalf("privileges %+v %+v", x, y)
			}
			if result.Actor.Stats["gold"] != before.Stats["gold"]-500000 {
				t.Fatal("actor fee")
			}
			wantPartner := int64(10000000)
			if op == 7 {
				wantPartner -= 500000
			}
			if result.Partner.Stats["gold"] != wantPartner {
				t.Fatal("partner fee")
			}
			q, _, _ := o.Repositories.Quest.Load(ctx, "1")
			if _, found := q.States[20737]; found || q.States[9].Status != "completed" {
				t.Fatal("quest clear escaped")
			}
			if _, e = o.Divorce(ctx, req, rules, op); e == nil {
				t.Fatal("divorce replay accepted")
			}
			after, _, _ := o.Repositories.Character.Load(ctx, "1")
			if !reflect.DeepEqual(after, result.Actor) {
				t.Fatal("second mutation")
			}
			settings, _, _ := o.Repositories.Settings.Load(ctx, scope)
			if len(settings.Values) != 1 {
				t.Fatalf("old pair settings %+v", settings)
			}
		})
	}
}

func TestWeddingExtensionsDivorceRejectsLowGoldAndOutstandingGuestDebt(t *testing.T) {
	for _, scenario := range []string{"actor_gold", "partner_gold", "guest_debt"} {
		t.Run(scenario, func(t *testing.T) {
			o, req, rules := extensionFixture(t)
			ctx := context.Background()
			op := byte(6)
			if scenario == "actor_gold" {
				c, _, _ := o.Repositories.Character.Load(ctx, "1")
				c.Stats["gold"] = 499999
				if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "partner_gold" {
				op = 7
				c, _, _ := o.Repositories.Character.Load(ctx, "2")
				c.Stats["gold"] = 499999
				if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); e != nil {
					t.Fatal(e)
				}
			}
			scope, _ := r.WeddingPairScope([]string{"1", "2"})
			if scenario == "guest_debt" {
				raw, _ := json.Marshal(GuestLedger{Version: 1, Invitations: map[string]GuestInvitation{"3": {GuestID: "3", Gold: 10000}}})
				if e := o.Repositories.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"guest_v1:1": string(raw), "unknown": "keep"}}); e != nil {
					t.Fatal(e)
				}
			}
			a, _, _ := o.Repositories.Character.Load(ctx, "1")
			b, _, _ := o.Repositories.Character.Load(ctx, "2")
			settings, found, _ := o.Repositories.Settings.Load(ctx, scope)
			if _, e := o.Divorce(ctx, req, rules, op); e == nil {
				t.Fatal("rejection accepted")
			}
			x, _, _ := o.Repositories.Character.Load(ctx, "1")
			y, _, _ := o.Repositories.Character.Load(ctx, "2")
			after, afterFound, _ := o.Repositories.Settings.Load(ctx, scope)
			if !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, y) || found != afterFound || !reflect.DeepEqual(settings, after) {
				t.Fatal("rejection partially changed relationship/assets/ledger")
			}
		})
	}
}

func TestCurrentPVFMarriageExtensions(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("paired PVF not supplied")
	}
	archive, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	rules, e := LoadExtensionRules(archive)
	if e != nil {
		t.Fatal(e)
	}
	if len(rules.Trees) != 3 || rules.Seeds[10002766] != 1 || rules.Seeds[10002768] != 2 || rules.Seeds[10002767] != 3 || rules.Capsule != (CapsuleRules{MaximumOwn: 10, MaximumTaken: 10, Gain: 2, MaximumGauge: 10, ItemID: 10002910}) || rules.DivorceGold != ([2]int64{500000, 500000}) {
		t.Fatalf("paired extensions %+v", rules)
	}
	for _, tree := range rules.Trees {
		sum := int64(0)
		for _, reward := range tree.Rewards {
			sum += reward.Weight
		}
		if tree.Days != 30 || tree.DailyWater != 2 || tree.MaximumGrowth != 30 || tree.Water[10002765] != 10 || tree.Water[10002905] != 20 || sum != 100000 {
			t.Fatalf("tree %+v", tree)
		}
	}
}
