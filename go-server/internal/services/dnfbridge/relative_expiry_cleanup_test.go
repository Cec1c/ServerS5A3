package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type rejectRelativeExpiryCommit struct {
	inner dnfrepo.AccountCharacterAssetUnitOfWork
}

func (r rejectRelativeExpiryCommit) WithinAccountCharacterAssets(ctx context.Context, account, character string, fn func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return r.inner.WithinAccountCharacterAssets(ctx, account, character, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if err := fn(a, c, i, e); err != nil {
			return err
		}
		return errors.New("injected relative expiry final commit failure")
	})
}

func TestRealPVFTrialRingExpiredInventoryCleanup(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(100322469)
	if err != nil || def.UsablePeriodDays != 2 || !def.ExpirationDate.IsZero() {
		t.Fatalf("trial ring: %+v %v", def, err)
	}
	issued := time.Date(2026, 10, 13, 14, 47, 1, 0, time.UTC)
	grant, err := currentPVFItemDefinitionForGrantAt(def, issued)
	if err != nil {
		t.Fatal(err)
	}
	expired, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: 100322469, Count: 1}, grant, issued)
	later, _ := currentPVFItemDefinitionForGrantAt(def, issued.Add(10*24*time.Hour))
	future, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: 100322469, Count: 1}, later, issued)
	for _, mode := range []string{"before", "deadline", "after", "wrong_owner", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, repos, q := newDailyDeleteTestService(t, grant.ExpirationDate)
			s.pvfItemCatalog = catalog
			if mode == "before" {
				q.now = q.now.Add(-time.Second)
			}
			if mode == "after" {
				q.now = q.now.Add(6 * 24 * time.Hour)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			inv.Slots = map[string]dnfrepo.ItemStack{}
			inv.Warehouse = map[string]dnfrepo.ItemStack{}
			inv.Slots["0:13"] = expired
			inv.Slots["0:14"] = future
			inv.Slots["0:15"] = dnfrepo.ItemStack{ItemID: 100322052, Count: 1}
			inv.Warehouse["2:4"] = expired
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			account.Slots = map[string]dnfrepo.ItemStack{}
			account.Slots["12:8"] = expired
			if err := repos.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			accountID := "account-1"
			if mode == "wrong_owner" {
				accountID = "other"
			}
			if mode == "rollback" {
				repos.AccountAssets = rejectRelativeExpiryCommit{repos.AccountAssets}
			}
			summary, removed, err := s.reconcileCurrentDailyDeleteWithRemoved(ctx, repos, "19", accountID, catalog)
			afterInv, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if mode == "wrong_owner" || mode == "rollback" {
				if err == nil || len(removed) != 0 || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatal("failed transaction changed assets", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before" {
				if summary.Total() != 0 || len(removed) != 0 || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatal("early cleanup or renewed deadline")
				}
				return
			}
			if summary.DeletedInventory != 1 || summary.DeletedWarehouse != 1 || summary.DeletedAccount != 1 || !reflect.DeepEqual(removed, []currentDailyDeleteSlot{{0, 13}, {2, 4}, {12, 8}}) {
				t.Fatalf("expired relative item retained: summary=%+v removed=%v", summary, removed)
			}
			if len(afterInv.Slots) != 2 || len(afterInv.Warehouse) != 0 || len(afterAccount.Slots) != 0 || !reflect.DeepEqual(afterInv.Slots["0:14"], future) || !reflect.DeepEqual(afterInv.Slots["0:15"], beforeInv.Slots["0:15"]) {
				t.Fatal("cleanup changed another item")
			}
			conn := &bufferConn{}
			if err := s.sendCurrentDailyDeleteClearedSlots(&gameSession{conn: conn}, removed); err != nil {
				t.Fatal(err)
			}
			frames := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			if len(frames) != 3 {
				t.Fatalf("clear frames=%d", len(frames))
			}
			for _, frame := range frames {
				if frame.Header.MsgID != 14 || frame.Header.Classification != 0 {
					t.Fatal("unexpected clear packet")
				}
			}
			summary, removed, err = s.reconcileCurrentDailyDeleteWithRemoved(ctx, repos, "19", "account-1", catalog)
			if err != nil || summary.Total() != 0 || len(removed) != 0 {
				t.Fatal("cleanup replay not empty", err)
			}
		})
	}
}

// Destructive cleanup must leave unrelated expiration owners and inconsistent
// historical instances untouched, even when a timestamp happens to be old.
func TestRelativeExpiryCleanupPreservesUnprovenInstances(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Hour)
	for _, name := range []string{"permanent", "absolute_event", "unmarked", "gm_override", "rental", "old_projection", "missing_days", "different_days", "missing_deadline", "conflicting_deadline", "future"} {
		t.Run(name, func(t *testing.T) {
			def := dungeonDropItemDefinition{UsablePeriodDays: 2}
			stack := dnfrepo.ItemStack{ExpireAt: deadline, Extra: map[string]string{"expiration_source": "runtime_pvf_usable_period_grant", "usable_period_days": "2", "expire_time": fmt.Sprint(deadline.Unix()), "expire_unix": fmt.Sprint(deadline.Unix())}}
			switch name {
			case "permanent":
				def.UsablePeriodDays = 0
			case "absolute_event":
				def.ExpirationDate = deadline
			case "unmarked":
				delete(stack.Extra, "expiration_source")
			case "gm_override":
				stack.Extra["expiration_source"] = "gm_override"
			case "rental":
				stack.Extra["expiration_source"] = "rental"
			case "old_projection":
				stack.Extra["expiration_source"] = "runtime_pvf"
			case "missing_days":
				delete(stack.Extra, "usable_period_days")
			case "different_days":
				stack.Extra["usable_period_days"] = "30"
			case "missing_deadline":
				stack.ExpireAt = time.Time{}
			case "conflicting_deadline":
				stack.Extra["expire_unix"] = "1"
			case "future":
				stack.ExpireAt = now.Add(time.Hour)
			}
			if currentPVFRelativeGrantExpired(stack, def, now) {
				t.Fatal("unproven instance selected for deletion")
			}
		})
	}
}
