package fatigue

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type guardFailCharacter struct{ repo.CharacterRepository }

func (f guardFailCharacter) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("injected character save failure after inventory staging")
}

type guardFailAssets struct{ repo.CharacterAssetUnitOfWork }

func (f guardFailAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return f.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return apply(guardFailCharacter{c}, i, e)
	})
}

func TestPotionSourceAndCommitGuards(t *testing.T) {
	for _, mode := range []string{"valid", "expiry_exact", "expiry_future", "expiry_extra", "locked", "pending_unlock", "unlock_elapsed", "foreign_owner", "missing_owner", "unbound", "session_changed_after_writes", "native_expiry", "save_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
			a := repo.AccountRecord{AccountID: "owner"}
			c := repo.CharacterRecord{CharacterID: "6", AccountID: a.AccountID, Level: 90, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
			Refresh(&c, a, now)
			stack := repo.ItemStack{ItemID: 416, Count: 2, Extra: map[string]string{}}
			checks := 0
			guard := PotionUseGuard{Expired: func(repo.ItemStack, time.Time) bool { return mode == "native_expiry" }, Revalidate: func() bool {
				checks++
				return mode != "unbound" && (mode != "session_changed_after_writes" || checks == 1)
			}}
			switch mode {
			case "expiry_exact":
				stack.ExpireAt = now
			case "expiry_future":
				stack.ExpireAt = now.Add(time.Second)
			case "expiry_extra":
				stack.Extra["expire_unix"] = strconv.FormatInt(now.Unix(), 10)
			case "locked":
				stack.Extra["equipment_lock_state"] = "locked"
			case "pending_unlock":
				stack.Extra["equipment_lock_state"] = "pending_unlock"
				stack.Extra["equipment_unlock_at"] = strconv.FormatInt(now.Add(time.Second).Unix(), 10)
			case "unlock_elapsed":
				stack.Extra["equipment_lock_state"] = "pending_unlock"
				stack.Extra["equipment_unlock_at"] = strconv.FormatInt(now.Unix(), 10)
			case "foreign_owner":
				c.AccountID = "other"
			case "missing_owner":
				c.AccountID = ""
			case "save_failure":
				g.CharacterAssets = guardFailAssets{g.CharacterAssets}
			}
			inv := repo.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]repo.ItemStack{"0:87": stack}}
			for _, e := range []error{g.Character.Save(ctx, c), g.Inventory.Save(ctx, inv)} {
				if e != nil {
					t.Fatal(e)
				}
			}
			beforeC, _, _ := g.Character.Load(ctx, c.CharacterID)
			beforeI, _, _ := g.Inventory.Load(ctx, c.CharacterID)
			d := PotionDefinition{ItemID: 416, Points: 50, DailyLimit: 3, CoolTimeMS: 1000, PVFPath: "stackable/416.stk"}
			result, err := RestorePotionWithGuard(ctx, g, a, c.CharacterID, 87, d, now, guard)
			afterC, _, _ := g.Character.Load(ctx, c.CharacterID)
			afterI, _, _ := g.Inventory.Load(ctx, c.CharacterID)
			success := mode == "valid" || mode == "expiry_future" || mode == "unlock_elapsed"
			if success {
				if err != nil || result.Remaining != 1 || afterI.Slots["0:87"].Count != 1 || afterC.Stats["fatigue"] != 50 || afterC.Stats["fatigue_potion_used_416"] != 1 {
					t.Fatalf("success: result=%+v error=%v", result, err)
				}
			} else if err == nil || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatalf("guard %s: err=%v changed=%t/%t", mode, err, !reflect.DeepEqual(beforeC, afterC), !reflect.DeepEqual(beforeI, afterI))
			}
			if mode == "session_changed_after_writes" && checks != 2 {
				t.Fatalf("did not reach post-write guard: %d", checks)
			}
		})
	}
}
