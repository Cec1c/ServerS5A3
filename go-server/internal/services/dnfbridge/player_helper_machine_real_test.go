package dnfbridge

import (
	"context"
	expert "longheng.io/server/internal/modules/dnf/expertjob"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestPlayerUtilityRealMachineFeeEnduranceAndFailure(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, e := pvf.LoadArchive(pvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	jobs, e := expert.Load(context.Background(), source)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"visitor", "self", "personal", "closed", "insufficient"} {
		t.Run(mode, func(t *testing.T) {
			s, p, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:10": {ItemID: 100300212, Count: 1}, "0:11": {ItemID: 100300212, Count: 1}})
			ctx := context.Background()
			s.expertJobCatalog = jobs
			s.pvfItemCatalog = items
			for _, char := range []string{"19", "20"} {
				account := "account-1"
				if char == "20" {
					account = "owner"
				}
				_ = g.Account.Save(ctx, repo.AccountRecord{AccountID: account, Metadata: map[string]string{}})
				_ = g.Character.Save(ctx, repo.CharacterRecord{CharacterID: char, AccountID: account, Stats: map[string]int64{"gold": 10000, "expert_job_type": 3, "expert_job_exp": 10, currentExpertJobMachineGradeStat: 1, currentExpertJobMachineEnduranceStat: 300}})
				if char == "20" {
					_ = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: char, Slots: map[string]repo.ItemStack{}})
				}
				_ = g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: account, Slots: map[string]repo.ItemStack{}})
			}
			owner := uint16(20)
			ownerSession := &gameSession{conn: &bufferConn{}, connID: "owner", selectedCharacterID: 20, accountID: "owner"}
			if mode == "self" || mode == "personal" {
				owner = 19
				ownerSession = p
			}
			machine := &currentExpertJobStore{OwnerCharacterID: owner, OwnerSession: ownerSession, Kind: currentExpertJobDisjointStoreKind, MachineGrade: 1, Endurance: 300, Cost: 100}
			s.onlinePlayers = newOnlinePlayerManager()
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: p})
			s.expertJobStores = map[uint16]*currentExpertJobStore{owner: machine}
			s.expertJobVisitors = map[*gameSession]uint16{p: owner}
			if mode == "personal" {
				s.expertJobStores = nil
				s.expertJobVisitors = nil
			}
			v := utilityPreview(t, s, id, "disjoint", 127, 0)
			if v.Count != 2 || id.session.helperUtility.Plan.MachineFingerprint == "system" {
				t.Fatal("wrong machine", v)
			}
			if mode == "closed" {
				s.removeCurrentExpertJobStore(owner, ownerSession)
			}
			if mode == "insufficient" {
				c, _, _ := g.Character.Load(ctx, "19")
				c.Stats["gold"] = 150
				_ = g.Character.Save(ctx, c)
			}
			before, _, _ := g.Inventory.Load(ctx, "19")
			cBefore, _, _ := g.Character.Load(ctx, "19")
			oBefore, _, _ := g.Character.Load(ctx, "20")
			q := utilityCommit(id, v)
			_, e := s.applyHelperUtility(ctx, id, q)
			if mode == "closed" || mode == "insufficient" {
				if e == nil {
					t.Fatal("failure fell back to system")
				}
				after, _, _ := g.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed batch consumed")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			c, _, _ := g.Character.Load(ctx, "19")
			o, _, _ := g.Character.Load(ctx, "20")
			after, _, _ := g.Inventory.Load(ctx, "19")
			if _, ok := after.Slots["0:10"]; ok {
				t.Fatal("source retained")
			}
			target := o
			if mode == "self" || mode == "personal" {
				target = c
			}
			if target.Stats[currentExpertJobMachineEnduranceStat] >= 300 {
				t.Fatal("endurance not spent")
			}
			if mode == "visitor" && (c.Stats["gold"] != cBefore.Stats["gold"]-200 || o.Stats["gold"] != oBefore.Stats["gold"]+200) {
				t.Fatal("fee not settled", c.Stats, o.Stats)
			}
			if _, e := s.applyHelperUtility(ctx, id, q); e != nil {
				t.Fatal(e)
			}
			again, _, _ := g.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(after, again) {
				t.Fatal("replayed reward")
			}
		})
	}
}
func TestPlayerUtilityRealRepairPreviewAndCommit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, e := pvf.LoadArchive(pvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	s, _, g, id := utilityFixture(t, nil)
	s.options.pvfPath = path
	s.initialEquipmentArchive = source
	s.pvfItemCatalog, e = newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	// Quickbar item uses the same real repair resolver as worn equipment.
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "19")
	if inv.Slots == nil {
		inv.Slots = map[string]repo.ItemStack{}
	}
	inv.Slots["0:4"] = repo.ItemStack{ItemID: 10018, Count: 1, Extra: map[string]string{"durability": "1"}}
	_ = g.Inventory.Save(ctx, inv)
	v := utilityPreview(t, s, id, "repair", 0, 0)
	if v.Gold >= 0 || v.Count != 1 {
		t.Fatal(v)
	}
	before, _, _ := g.Character.Load(ctx, "19")
	still, _, _ := g.Inventory.Load(ctx, "19")
	if still.Slots["0:4"].Extra["durability"] != "1" {
		t.Fatal("preview repaired")
	}
	if _, e := s.applyHelperUtility(ctx, id, utilityCommit(id, v)); e != nil {
		t.Fatal(e)
	}
	after, _, _ := g.Character.Load(ctx, "19")
	fixed, _, _ := g.Inventory.Load(ctx, "19")
	if after.Stats["gold"] != before.Stats["gold"]+v.Gold || fixed.Slots["0:4"].Extra["durability"] == "1" {
		t.Fatal("repair not committed", after.Stats, fixed)
	}
}
