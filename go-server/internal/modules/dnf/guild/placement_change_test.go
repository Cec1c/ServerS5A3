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

func placementChangeFixture(t *testing.T) (r.Group, PlacementChangeRequest, time.Time) {
	t.Helper()
	repos, create, now := placementFixture(t)
	placed, err := ApplyPlacement(context.Background(), repos.Guild, create, now)
	if err != nil {
		t.Fatal(err)
	}
	q := PlacementChangeRequest{Owner: create.Owner, GuildID: create.GuildID, RequestID: fmt.Sprintf("%064x", 2), Change: PlacementChange{Action: PlacementMove, ID: placed.Placement.ID, ObjectID: create.Book.ObjectID, Area: create.Map.Area, X: create.X + 36, Y: create.Y}, Books: map[uint32]PlacementBook{create.Book.ItemID: create.Book}, Map: create.Map, Revalidate: create.Revalidate, CanInteract: create.CanInteract}
	return repos, q, now
}

func TestGuildPlacementChangeDurableReplayAndConstruction(t *testing.T) {
	ctx := context.Background()
	repos, q, now := placementChangeFixture(t)
	original, _ := Load(ctx, repos.Settings)
	before, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	first, err := ApplyPlacementChange(ctx, repos.Guild, q, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.Placement.X != int16(q.Change.X) || !first.Placement.ReadyAt.Equal(original.Guilds[1].Placements.Rows[0].ReadyAt) {
		t.Fatal("changed construction")
	}
	next := q
	next.RequestID = fmt.Sprintf("%064x", 3)
	next.Change.X += 36
	if _, err = ApplyPlacementChange(ctx, repos.Guild, next, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyPlacementChange(ctx, repos.Guild, q, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	if reg.Guilds[1].Placements.Rows[0].X != int16(next.Change.X) {
		t.Fatal("old move undid later move")
	}
	remove := next
	remove.RequestID = fmt.Sprintf("%064x", 4)
	remove.Change.Action = PlacementRemove
	if _, err = ApplyPlacementChange(ctx, repos.Guild, remove, now); err != nil {
		t.Fatal(err)
	}
	for _, req := range []PlacementChangeRequest{q, next, remove} {
		if _, err = ApplyPlacementChange(ctx, repos.Guild, req, now); err != nil {
			t.Fatal(err)
		}
	}
	reg, _ = Load(ctx, repos.Settings)
	if len(reg.Guilds[1].Placements.Rows) != 0 || reg.Guilds[1].Placements.NextID != 2 {
		t.Fatal("resurrection or reused key")
	}
	after, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed inventory")
	}
	collision := q
	collision.Change.Action = PlacementRemove
	if _, err = ApplyPlacementChange(ctx, repos.Guild, collision, now); !errors.Is(err, ErrPlacementIdentity) {
		t.Fatal("receipt collision", err)
	}
}

func TestGuildPlacementChangeRejectsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"foreign-owner", "foreign-guild", "object", "key", "area", "position", "overlap", "remove-stale", "permission", "scene", "generation", "after-save"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos, q, now := placementChangeFixture(t)
			switch mode {
			case "foreign-owner":
				q.Owner.AccountID = "elsewhere"
			case "foreign-guild":
				q.GuildID++
			case "object":
				q.Change.ObjectID++
			case "key":
				q.Change.ID++
			case "area":
				q.Change.Area++
			case "position":
				q.Change.X++
			case "overlap":
				reg, _ := Load(ctx, repos.Settings)
				p := reg.Guilds[1].Placements.Rows[0]
				p.ID = 2
				p.X += 108
				p.Cells, _ = q.Map.CellsAt(q.Books[p.ItemID], int(p.X), int(p.Y))
				reg.Guilds[1].Placements.Rows = append(reg.Guilds[1].Placements.Rows, p)
				reg.Guilds[1].Placements.NextID = 3
				if err := Save(ctx, repos.Settings, reg, now); err != nil {
					t.Fatal(err)
				}
				q.Change.X = int(p.X)
			case "remove-stale":
				q.Change.Action = PlacementRemove
			case "permission":
				reg, _ := Load(ctx, repos.Settings)
				reg.Guilds[1].Placements.Rows[0].CreatorID = 99
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
				if err := Save(ctx, repos.Settings, reg, now); err != nil {
					t.Fatal(err)
				}
			case "scene":
				q.CanInteract = func(r.CharacterRecord, *Guild) bool { return false }
			case "generation":
				q.Revalidate = func() bool { return false }
			case "after-save":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			before, _ := Load(ctx, repos.Settings)
			if _, err := ApplyPlacementChange(ctx, repos.Guild, q, now); err == nil {
				t.Fatal("accepted", mode)
			}
			after, _ := Load(ctx, repos.Settings)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected mutation leaked")
			}
		})
	}
}

func TestGuildPlacementChangePermissionAndParallelReplay(t *testing.T) {
	for _, creator := range []bool{true, false} {
		t.Run(fmt.Sprint(creator), func(t *testing.T) {
			ctx := context.Background()
			repos, q, now := placementChangeFixture(t)
			reg, _ := Load(ctx, repos.Settings)
			if creator {
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
			} else {
				reg.Guilds[1].Placements.Rows[0].CreatorID = 99
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: 1 << 16}
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 10)
			for range 10 {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := ApplyPlacementChange(ctx, repos.Guild, q, now); errs <- err }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil || len(reg.PlacementChangeReceipts) != 1 || reg.Guilds[1].Placements.Rows[0].X != int16(q.Change.X) {
				t.Fatal("parallel mutation", err)
			}
		})
	}
}

func TestGuildPlacementChangeConcurrentDestinationAndFreedCells(t *testing.T) {
	ctx := context.Background()
	repos, q, now := placementChangeFixture(t)
	reg, _ := Load(ctx, repos.Settings)
	second := reg.Guilds[1].Placements.Rows[0]
	second.ID = 2
	second.X += 216
	second.Cells, _ = q.Map.CellsAt(q.Books[second.ItemID], int(second.X), int(second.Y))
	reg.Guilds[1].Placements.Rows = append(reg.Guilds[1].Placements.Rows, second)
	reg.Guilds[1].Placements.NextID = 3
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.Change.X = 1010
	other := q
	other.RequestID = fmt.Sprintf("%064x", 3)
	other.Change.ID = 2
	type outcome struct {
		q   PlacementChangeRequest
		err error
	}
	results := make(chan outcome, 2)
	for _, request := range []PlacementChangeRequest{q, other} {
		go func() {
			_, err := ApplyPlacementChange(ctx, repos.Guild, request, now)
			results <- outcome{request, err}
		}()
	}
	success := 0
	var winner PlacementChangeRequest
	for range 2 {
		got := <-results
		if got.err == nil {
			success++
			winner = got.q
		} else if !errors.Is(got.err, ErrPlacementPosition) {
			t.Fatal(got.err)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d conflicting destinations", success)
	}
	winner.Change.Action = PlacementRemove
	winner.RequestID = fmt.Sprintf("%064x", 4)
	if _, err := ApplyPlacementChange(ctx, repos.Guild, winner, now); err != nil {
		t.Fatal(err)
	}
	loser := q
	if q.Change.ID == winner.Change.ID {
		loser = other
	}
	loser.RequestID = fmt.Sprintf("%064x", 5)
	if _, err := ApplyPlacementChange(ctx, repos.Guild, loser, now); err != nil {
		t.Fatalf("removed cells not reusable: %v", err)
	}
}
