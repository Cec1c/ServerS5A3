package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func placementFixture(t *testing.T) (r.Group, PlacementRequest, time.Time) {
	t.Helper()
	repos, base, _, now := merchantPurchaseFixture(t)
	inv, _, err := repos.Inventory.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:42"] = r.ItemStack{ItemID: 10156101, Count: 3, ExpireAt: now.Add(time.Hour)}
	if err = repos.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	q := PlacementRequest{Owner: base.Owner, GuildID: base.GuildID, RequestID: base.RequestID, Slot: 42, X: 902, Y: 276, Book: placementTestBook(3, 3), Map: placementTestMap(t), BuildSeconds: 180, Revalidate: func() bool { return true }, CanInteract: func(r.CharacterRecord, *Guild) bool { return true }}
	return repos, q, now
}

func TestGuildPlacementCommitReloadReplay(t *testing.T) {
	ctx := context.Background()
	repos, q, now := placementFixture(t)
	r1, err := ApplyPlacement(ctx, repos.Guild, q, now)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Placement.ID != 1 || r1.Placement.ObjectID != 71021 || len(r1.Placement.Cells) != 9 || !r1.Placement.ReadyAt.Equal(now.Add(180*time.Second)) {
		t.Fatalf("receipt %+v", r1)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Guilds[q.GuildID].Placements.Rows) != 1 || reg.Guilds[q.GuildID].Essence.AP != 1000 {
		t.Fatal("placement or existing balance")
	}
	r2, err := ApplyPlacement(ctx, repos.Guild, q, now.Add(2*time.Hour))
	if err != nil || !reflect.DeepEqual(r1, r2) {
		t.Fatalf("durable replay %+v %v", r2, err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	if inv.Slots["0:42"].Count != 2 {
		t.Fatal("duplicate debit")
	}
	q.X += 36
	if _, err = ApplyPlacement(ctx, repos.Guild, q, now); !errors.Is(err, ErrPlacementIdentity) {
		t.Fatalf("receipt collision %v", err)
	}
}

func TestGuildPlacementRejectionsPreserveInventoryAndRegistry(t *testing.T) {
	for _, mode := range []string{"overlap", "expired", "wrong-item", "wrong-owner", "wrong-guild", "bad-position", "stale-scene", "stale-generation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos, q, now := placementFixture(t)
			if mode == "overlap" {
				if _, err := ApplyPlacement(ctx, repos.Guild, q, now); err != nil {
					t.Fatal(err)
				}
				q.RequestID = fmt.Sprintf("%064x", 2)
			}
			if mode == "expired" {
				now = now.Add(time.Hour)
			}
			if mode == "wrong-item" {
				q.Book.ItemID++
			}
			if mode == "wrong-owner" {
				q.Owner.AccountID = "elsewhere"
			}
			if mode == "wrong-guild" {
				q.GuildID++
			}
			if mode == "bad-position" {
				q.X++
			}
			if mode == "stale-scene" {
				q.CanInteract = func(r.CharacterRecord, *Guild) bool { return false }
			}
			if mode == "stale-generation" {
				q.Revalidate = func() bool { return false }
			}
			before, _, _ := repos.Inventory.Load(ctx, "1")
			regBefore, _ := Load(ctx, repos.Settings)
			if _, err := ApplyPlacement(ctx, repos.Guild, q, now); err == nil {
				t.Fatal("accepted invalid placement")
			}
			after, _, _ := repos.Inventory.Load(ctx, "1")
			regAfter, _ := Load(ctx, repos.Settings)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(regBefore, regAfter) {
				t.Fatal("rejection mutated state")
			}
		})
	}
}

func TestGuildPlacementConcurrentSameCellAndReplay(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			repos, q, now := placementFixture(t)
			var wg sync.WaitGroup
			errs := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					request := q
					if !sameKey {
						request.RequestID = fmt.Sprintf("%064x", i+1)
					}
					_, err := ApplyPlacement(context.Background(), repos.Guild, request, now)
					errs <- err
				}(i)
			}
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrPlacementPosition) {
					t.Fatal(err)
				}
			}
			if (sameKey && success != 12) || (!sameKey && success != 1) {
				t.Fatalf("success=%d", success)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "1")
			reg, err := Load(context.Background(), repos.Settings)
			if err != nil || inv.Slots["0:42"].Count != 2 || len(reg.Guilds[q.GuildID].Placements.Rows) != 1 {
				t.Fatal("concurrent debit or placement")
			}
		})
	}
}

func TestGuildPlacementRollbackAfterInventoryWrite(t *testing.T) {
	repos, q, now := placementFixture(t)
	ctx := context.Background()
	before, _, _ := repos.Inventory.Load(ctx, "1")
	regBefore, _ := Load(ctx, repos.Settings)
	calls := 0
	q.Revalidate = func() bool { calls++; return calls < 3 }
	if _, err := ApplyPlacement(ctx, repos.Guild, q, now); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatalf("rollback %v", err)
	}
	after, _, _ := repos.Inventory.Load(ctx, "1")
	regAfter, _ := Load(ctx, repos.Settings)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(regBefore, regAfter) {
		t.Fatal("rolled back write leaked")
	}
}

func TestGuildPlacementMaintenanceIsNotAnInventedAreaCap(t *testing.T) {
	repos, q, now := placementFixture(t)
	q.Book.Maintenance = 200
	if _, err := ApplyPlacement(context.Background(), repos.Guild, q, now); err != nil {
		t.Fatal(err)
	}
	q.RequestID = fmt.Sprintf("%064x", 2)
	q.X += 108
	if _, err := ApplyPlacement(context.Background(), repos.Guild, q, now); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil || len(reg.Guilds[1].Placements.Rows) != 2 {
		t.Fatal("area sum rejected", err)
	}
}
