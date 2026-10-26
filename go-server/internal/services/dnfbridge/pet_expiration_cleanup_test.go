package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestR176HatchedPetExpirationUsesAtomicOwner(t *testing.T) {
	source := bridgePetCatalogFixture()
	source["monster/monster.lst"] = ""
	source["stackable/stackable.lst"] = ""
	source["equipment/creature/egg_faras.equ"] += "[usable period] 2\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	pets, err := dnfpet.NewPVFCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	runR176HatchedPetExpiry(t, catalog, pets, 63006)
}

func TestR176RealPVFHatchedPetExpiration(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !knownRepairPVFChecksum(a.Snapshot().Checksum) {
		t.Fatal("PVF identity")
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	pets, err := dnfpet.NewPVFCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	for id, ref := range catalog.itemRefs {
		if ref.kind == dungeonDropItemEquipment && strings.Contains(strings.ToLower(ref.path), "creature") {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		def, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		if def.UsablePeriodDays <= 0 || !def.ExpirationDate.IsZero() {
			continue
		}
		hatch, err := pets.ResolveHatch(int64(id))
		if err != nil {
			continue
		}
		t.Logf("actual PVF egg=%d output=%d periodDays=%d path=%s", id, hatch.HatchedItemID, def.UsablePeriodDays, def.PVFPath)
		runR176HatchedPetExpiry(t, catalog, pets, id)
		return
	}
	t.Fatal("no real timed hatchable egg found; synthetic fixture cannot establish real coverage")
}

func runR176HatchedPetExpiry(t *testing.T, catalog *pvfDungeonDropCatalog, pets *dnfpet.PVFCatalog, eggID uint32) {
	for _, mode := range []string{"due", "before", "wrong_account", "permanent", "mismatch", "rollback", "worn", "online", "online_retry"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
			s, repos, q := newDailyDeleteTestService(t, now)
			s.pvfItemCatalog = catalog
			s.petCatalog = pets
			def, err := catalog.ResolveItem(eggID)
			if err != nil {
				t.Fatal(err)
			}
			issued := now.Add(-time.Duration(def.UsablePeriodDays) * 24 * time.Hour)
			grant, err := currentPVFItemDefinitionForGrantAt(def, issued)
			if err != nil {
				t.Fatal(err)
			}
			egg, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: int64(eggID), Count: 1}, grant, issued)
			if egg.ExpireAt.IsZero() {
				t.Fatalf("fixture grant lacks deadline: %+v", def)
			}
			if mode == "permanent" {
				egg = dnfrepo.ItemStack{ItemID: int64(eggID), Count: 1}
			}
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			inv.Slots = make(map[string]dnfrepo.ItemStack)
			inv.Slots["7:5"] = egg
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfpet.NewOwner(repos, s.petCatalog)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Hatch(ctx, dnfpet.HatchCommand{SelectedCharacterID: 19, ListType: 7, SlotIndex: 5})
			if err != nil {
				t.Fatal(err)
			}
			inv, _, _ = repos.Inventory.Load(ctx, "19")
			stack := inv.Slots["7:5"]
			if strings.HasPrefix(mode, "online") {
				q.now = now.Add(-time.Second)
				conn := &relativeExpiryFailConn{fail: mode == "online_retry"}
				p := &gameSession{conn: conn, connID: "r176-expiry", accountID: "account-1", selectedCharacterID: 19, characterGeneration: 3, postStartMapPlayerStateSent: true}
				s.armCurrentDailyDeleteClock(p)
				task := q.task(t, 0)
				if task.delay != time.Second {
					t.Fatalf("hatched deadline not armed: %s", task.delay)
				}
				q.now = now
				q.fire(task, false)
				afterPet, _, _ := repos.Pet.Load(ctx, "19")
				if len(afterPet.Entries) != 0 {
					t.Fatal("online pet state not deleted")
				}
				if mode == "online_retry" {
					if p.pendingItemExpiryProjection == nil {
						t.Fatal("lost expiry retry")
					}
					conn.fail = false
					if err := s.sendCurrentExpiryProjection(p); err != nil {
						t.Fatal(err)
					}
				}
				if p.pendingItemExpiryProjection != nil {
					t.Fatal("expiry refresh not completed")
				}
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.MsgID != 14 || packet.Body[0] != 7 || binary.LittleEndian.Uint32(packet.Body[5:9]) != math.MaxUint32 {
					t.Fatal("missing precise pet slot clear")
				}
				packet, rest = splitGameServerUpperPacket(t, rest)
				if packet.Header.MsgID != 105 || len(rest) != 0 {
					t.Fatal("missing authoritative pet table refresh")
				}
				return
			}
			if mode != "permanent" && stack.Extra["expiration_source"] != egg.Extra["expiration_source"] {
				t.Fatal("hatch lost expiry provenance")
			}
			if mode == "before" {
				q.now = now.Add(-time.Second)
			}
			if mode == "mismatch" {
				p, _, _ := repos.Pet.Load(ctx, "19")
				e := p.Entries[result.PetKey]
				e.ItemID++
				p.Entries[result.PetKey] = e
				if err := repos.Pet.Save(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "worn" {
				stack.Extra["equipment_slot"] = "26"
				delete(inv.Slots, "7:5")
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				eq := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{"26": {SlotIndex: 26, ItemID: stack.ItemID, ExpireAt: stack.ExpireAt, Extra: stack.Extra}}}
				if err := repos.Equipment.Save(ctx, eq); err != nil {
					t.Fatal(err)
				}
				p, _, _ := repos.Pet.Load(ctx, "19")
				p.EquippedKey = result.PetKey
				if err := repos.Pet.Save(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
			beforePet, _, _ := repos.Pet.Load(ctx, "19")
			beforeEq, _, _ := repos.Equipment.Load(ctx, "19")
			account := "account-1"
			if mode == "wrong_account" {
				account = "wrong"
			}
			if mode == "rollback" {
				repos.AccountAssets = rejectPetExpiryCommit{repos.AccountAssets}
			}
			summary, removed, err := s.reconcileCurrentDailyDeleteWithRemoved(ctx, repos, "19", account, catalog)
			afterInv, _, _ := repos.Inventory.Load(ctx, "19")
			afterPet, _, _ := repos.Pet.Load(ctx, "19")
			afterEq, _, _ := repos.Equipment.Load(ctx, "19")
			if mode == "wrong_account" || mode == "mismatch" || mode == "rollback" {
				if err == nil || len(removed) != 0 || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforePet, afterPet) || !reflect.DeepEqual(beforeEq, afterEq) {
					t.Fatalf("failed expiry changed assets: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before" || mode == "permanent" {
				if summary.Total() != 0 || len(removed) != 0 || !reflect.DeepEqual(beforePet, afterPet) {
					t.Fatal("live/permanent pet removed")
				}
				return
			}
			if summary.Pets != 1 || len(afterPet.Entries) != 0 || afterPet.EquippedKey != "" || len(removed) != 1 {
				t.Fatalf("summary=%+v pets=%+v removed=%v", summary, afterPet, removed)
			}
			if mode == "worn" {
				if len(afterEq.Entries) != 0 || removed[0] != (currentDailyDeleteSlot{3, 26}) {
					t.Fatal("worn pet remains")
				}
			} else {
				if _, ok := afterInv.Slots["7:5"]; ok {
					t.Fatal("bag pet remains")
				}
			}
		})
	}
}

type rejectPetExpiryCommit struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (r rejectPetExpiryCommit) WithinAccountCharacterPetAssets(ctx context.Context, a, c string, fn func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.PetRepository) error) error {
	return r.AccountCharacterAssetUnitOfWork.(dnfrepo.AccountCharacterPetAssetUnitOfWork).WithinAccountCharacterPetAssets(ctx, a, c, func(ar dnfrepo.AccountInventoryRepository, cr dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository, p dnfrepo.PetRepository) error {
		if err := fn(ar, cr, i, e, p); err != nil {
			return err
		}
		return context.Canceled
	})
}
