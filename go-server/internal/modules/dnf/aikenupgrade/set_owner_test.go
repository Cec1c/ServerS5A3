package aikenupgrade

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func setOwnerFixture(t *testing.T) (repo.Group, *Owner, SetCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	slots := map[string]repo.ItemStack{}
	for _, slot := range []int{9, 10, 11} {
		slots["0:"+strconv.Itoa(slot)] = repo.ItemStack{ItemID: int64(1000 + slot), Count: 1, RawEntry: []byte{1}, Extra: map[string]string{"native_buff_instance": strconv.Itoa(slot)}}
	}
	slots["0:121"] = repo.ItemStack{ItemID: 2000, Count: 7}
	slots["0:122"] = repo.ItemStack{ItemID: 2000, Count: 8}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	r := Rules{
		Validate: func(s repo.ItemStack) error {
			if s.ItemID < 1009 || s.ItemID > 1011 {
				return ErrInvalid
			}
			return nil
		},
		SourcePolicy: func(repo.ItemStack) (itemplacement.SlotPolicy, error) {
			return itemplacement.SlotPolicy{Start: 9, End: 64}, nil
		},
		CostPolicy: func(id int64) (itemplacement.SlotPolicy, error) {
			if id != 2000 {
				return itemplacement.SlotPolicy{}, ErrInvalid
			}
			return itemplacement.SlotPolicy{Start: 121, End: 176}, nil
		},
		Expired:  func(s repo.ItemStack, now time.Time) bool { return !s.ExpireAt.IsZero() && !now.Before(s.ExpireAt) },
		SetCount: func(s repo.ItemStack, n int64) (repo.ItemStack, error) { s.Count = n; return s, nil },
	}
	c := SetCommand{AccountID: "acct", CharacterID: "77", Now: time.Unix(1800000000, 0), Request: SetRequest{Options: []byte{34}, Targets: []uint16{9, 10, 11}}}
	c.Rules = SetRules{Item: r, Plan: func(items []repo.ItemStack, options []byte) (SetPlan, error) {
		if len(items) != 3 || len(options) != 1 || options[0] != 34 {
			return SetPlan{}, ErrInvalid
		}
		return SetPlan{Costs: []MaterialCost{{ItemID: 2000, Count: 10}}, Record: Record{Record0: 1, Record1: 3, Slots: [5]byte{34}}}, nil
	}, Write: func(s repo.ItemStack, slot int16, r Record) (repo.ItemStack, error) {
		s.RawEntry = append(s.RawEntry, r.Record0)
		s.Extra["set_test"] = "saved"
		return s, nil
	}}
	return g, o, c
}

func setOwnerLoad(t *testing.T, g repo.Group) repo.InventoryRecord {
	t.Helper()
	v, ok, e := g.Inventory.Load(context.Background(), "77")
	if e != nil || !ok {
		t.Fatalf("load: %v %v", ok, e)
	}
	return v
}

func TestSetOwnerAtomicTargetsAndMaterials(t *testing.T) {
	g, o, c := setOwnerFixture(t)
	before := setOwnerLoad(t, g)
	r, e := o.ApplySet(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	after := setOwnerLoad(t, g)
	if len(r.Changed) != 5 || after.Slots["0:121"].Count != 0 || after.Slots["0:122"].Count != 5 {
		t.Fatalf("bad result %+v %+v", r, after)
	}
	for _, slot := range []string{"0:9", "0:10", "0:11"} {
		if after.Slots[slot].Extra["set_test"] != "saved" || before.Slots[slot].Extra["set_test"] != "" {
			t.Fatalf("missing persisted state or alias leak %s", slot)
		}
	}
	if !after.UpdatedAt.Equal(c.Now) {
		t.Fatal("timestamp")
	}
}

func TestSetOwnerRejectsWithoutMutation(t *testing.T) {
	for _, name := range []string{"foreign-owner", "duplicate-target", "duplicate-option", "zero-option", "target-overflow", "missing-target", "endpoint-alias", "material-alias", "material-short", "expired-target", "expired-material", "duplicate-identity", "invalid-template", "write-failure", "plan-failure", "negative-cost", "duplicate-cost", "target-is-material", "mutating-plan", "mutating-write"} {
		t.Run(name, func(t *testing.T) {
			g, o, c := setOwnerFixture(t)
			inv := setOwnerLoad(t, g)
			switch name {
			case "foreign-owner":
				c.AccountID = "other"
			case "duplicate-target":
				c.Request.Targets[2] = 9
			case "duplicate-option":
				c.Request.Options = []byte{34, 34}
			case "zero-option":
				c.Request.Options = []byte{0}
			case "target-overflow":
				c.Request.Targets[2] = 65535
			case "missing-target":
				delete(inv.Slots, "0:11")
			case "endpoint-alias":
				inv.Slots["00:09"] = repo.ItemStack{ItemID: 9999, Count: 1}
			case "material-alias":
				inv.Slots["00:122"] = repo.ItemStack{ItemID: 9999, Count: 1}
			case "material-short":
				delete(inv.Slots, "0:122")
			case "expired-target":
				s := inv.Slots["0:9"]
				s.ExpireAt = c.Now
				inv.Slots["0:9"] = s
			case "expired-material":
				s := inv.Slots["0:122"]
				s.ExpireAt = c.Now
				inv.Slots["0:122"] = s
			case "duplicate-identity":
				inv.Warehouse = map[string]repo.ItemStack{"1": clone(inv.Slots["0:9"])}
			case "invalid-template":
				s := inv.Slots["0:11"]
				s.ItemID = 9000
				inv.Slots["0:11"] = s
			case "write-failure":
				c.Rules.Write = func(s repo.ItemStack, slot int16, r Record) (repo.ItemStack, error) {
					s.Extra["bad"] = "mutated"
					return s, errors.New("write failed")
				}
			case "plan-failure":
				c.Rules.Plan = func(s []repo.ItemStack, b []byte) (SetPlan, error) { return SetPlan{}, errors.New("plan failed") }
			case "negative-cost", "duplicate-cost", "target-is-material":
				old := c.Rules.Plan
				c.Rules.Plan = func(s []repo.ItemStack, b []byte) (SetPlan, error) {
					p, e := old(s, b)
					switch name {
					case "negative-cost":
						p.Costs[0].Count = -1
					case "duplicate-cost":
						p.Costs = append(p.Costs, p.Costs[0])
					case "target-is-material":
						p.Costs[0].ItemID = s[0].ItemID
					}
					return p, e
				}
			case "mutating-plan":
				c.Rules.Plan = func(s []repo.ItemStack, b []byte) (SetPlan, error) {
					s[0].Extra["bad"] = "mutated"
					s[0].RawEntry[0] = 255
					return SetPlan{}, ErrInvalid
				}
			case "mutating-write":
				c.Rules.Write = func(s repo.ItemStack, slot int16, r Record) (repo.ItemStack, error) { s.ItemID++; return s, nil }
			}
			if err := g.Inventory.Save(context.Background(), inv); err != nil {
				t.Fatal(err)
			}
			before := setOwnerLoad(t, g)
			r, e := o.ApplySet(context.Background(), c)
			if e == nil || r.Changed != nil || r.Record != (Record{}) {
				t.Fatalf("accepted invalid request: %+v %v", r, e)
			}
			if !reflect.DeepEqual(before, setOwnerLoad(t, g)) {
				t.Fatal("rejected request changed assets")
			}
		})
	}
}

type setFaultUOW struct {
	repo.AccountCharacterAssetUnitOfWork
	failSave bool
}
type setFaultInventory struct{ repo.InventoryRepository }

func (setFaultInventory) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("save failed")
}
func (u setFaultUOW) WithinAccountCharacterAssets(ctx context.Context, a, c string, f func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository) error {
		if u.failSave {
			ir = setFaultInventory{ir}
		}
		if err := f(ar, cr, ir, er); err != nil {
			return err
		}
		return errors.New("commit failed")
	})
}

func TestSetOwnerSaveAndCommitRollback(t *testing.T) {
	for _, save := range []bool{true, false} {
		t.Run(strconv.FormatBool(save), func(t *testing.T) {
			g, _, c := setOwnerFixture(t)
			before := setOwnerLoad(t, g)
			g.AccountAssets = setFaultUOW{g.AccountAssets, save}
			o, e := NewOwner(g)
			if e != nil {
				t.Fatal(e)
			}
			r, e := o.ApplySet(context.Background(), c)
			if e == nil || r.Changed != nil {
				t.Fatal("failure did not propagate")
			}
			if !reflect.DeepEqual(before, setOwnerLoad(t, g)) {
				t.Fatal("failure did not rollback")
			}
		})
	}
}

func TestSetOwnerReplacesPriorMembersAcrossContainers(t *testing.T) {
	g, o, c := setOwnerFixture(t)
	ctx := context.Background()
	old := Record{Record0: 1, Record1: 3, Slots: [5]byte{34}, Record18: 3}
	inv := setOwnerLoad(t, g)
	inv.Slots["0:12"] = repo.ItemStack{ItemID: 1012, Count: 1, Extra: map[string]string{"old": "1"}}
	inv.Warehouse = map[string]repo.ItemStack{"2:0": {ItemID: 1013, Count: 1, Extra: map[string]string{"old": "1"}}}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "77", Entries: map[string]repo.EquipmentEntry{"15": {ItemID: 1014, SlotIndex: 15, Extra: map[string]string{"old": "1"}}}}); err != nil {
		t.Fatal(err)
	}
	c.Rules.Existing = func(s repo.ItemStack) (Record, bool, error) { return old, s.Extra["old"] == "1", nil }
	c.Rules.Replan = func(items []repo.ItemStack, options []byte, previous []Record) (SetPlan, error) {
		if len(previous) != 1 || previous[0] != old {
			t.Fatalf("deduplicated previous=%+v", previous)
		}
		return SetPlan{Costs: []MaterialCost{{ItemID: 2000, Count: 1}}, Record: Record{Record0: 1, Record1: 3, Slots: [5]byte{35}, Record18: 3}}, nil
	}
	write := c.Rules.Write
	c.Rules.Write = func(s repo.ItemStack, slot int16, r Record) (repo.ItemStack, error) {
		if r.Record0 == 0 {
			delete(s.Extra, "old")
			return s, nil
		}
		return write(s, slot, r)
	}
	r, err := o.ApplySet(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.WarehouseChanged || !r.EquipmentChanged {
		t.Fatalf("missing changed owners %+v", r)
	}
	after := setOwnerLoad(t, g)
	worn, _, err := g.Equipment.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if after.Slots["0:12"].Extra["old"] != "" || after.Warehouse["2:0"].Extra["old"] != "" || worn.Entries["15"].Extra["old"] != "" {
		t.Fatal("prior set membership retained")
	}
	if after.Slots["0:121"].Count != 6 {
		t.Fatal("replan cost ignored")
	}
}
