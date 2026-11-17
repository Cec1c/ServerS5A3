package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func eventNPCFavorPolicy(t *testing.T, request *EventItemRequest, npc uint32, points, maximum int64) string {
	t.Helper()
	request.Resolve = func(_ repo.CharacterRecord, source, target repo.ItemStack) (EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
		if source.ItemID != 100 {
			return EventItemPolicy{}, source, target, ErrEventItem
		}
		return EventItemPolicy{
			Kind:         EventNPCFavor,
			SourceSlots:  itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120},
			MinimumLevel: 1,
			FavorNPC:     npc,
			FavorPoints:  points,
			FavorMaximum: maximum,
		}, source, target, nil
	}
	return fmt.Sprintf("npc_favor_%d_points", npc)
}

func eventNPCFavorSetPoint(t *testing.T, group repo.Group, key string, value int64) {
	t.Helper()
	ctx := context.Background()
	character, found, err := group.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats[key] = value
	if err := group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
}

func TestEventNPCFavorConsumesLetterAndAddsPoints(t *testing.T) {
	group, request, now := eventItemFixture(t, EventNPCFavor)
	key := eventNPCFavorPolicy(t, &request, 12345, 120, 500)
	eventNPCFavorSetPoint(t, group, key, 25)
	ctx := context.Background()
	settingsBefore, found, err := group.Settings.Load(ctx, repo.CharacterContainerStateScope("1"))
	if err != nil || !found {
		t.Fatalf("load settings: found=%t err=%v", found, err)
	}

	result, err := ApplyEventItem(ctx, group, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceItemID != 100 || result.Remaining != 0 || result.Character.Stats[key] != 145 {
		t.Fatalf("result=%+v", result)
	}
	bag, found, err := group.Inventory.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, err)
	}
	if _, exists := bag.Slots["0:70"]; exists {
		t.Fatal("favor letter was not consumed")
	}
	character, found, err := group.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	if character.Stats[key] != 145 || !character.UpdatedAt.Equal(now) {
		t.Fatalf("character=%+v", character)
	}
	settingsAfter, found, err := group.Settings.Load(ctx, repo.CharacterContainerStateScope("1"))
	if err != nil || !found || !reflect.DeepEqual(settingsBefore, settingsAfter) {
		t.Fatalf("favor use changed container settings: before=%+v after=%+v err=%v", settingsBefore, settingsAfter, err)
	}
}

func TestEventNPCFavorCapsAtMaximumAndRejectsFull(t *testing.T) {
	t.Run("below maximum caps", func(t *testing.T) {
		group, request, now := eventItemFixture(t, EventNPCFavor)
		key := eventNPCFavorPolicy(t, &request, 12345, 50, 500)
		eventNPCFavorSetPoint(t, group, key, 480)
		result, err := ApplyEventItem(context.Background(), group, request, now)
		if err != nil || result.Remaining != 0 || result.Character.Stats[key] != 500 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("at maximum rejects without consuming", func(t *testing.T) {
		group, request, now := eventItemFixture(t, EventNPCFavor)
		key := eventNPCFavorPolicy(t, &request, 12345, 10, 500)
		eventNPCFavorSetPoint(t, group, key, 500)
		before := eventItemSnapshot(t, group)
		if _, err := ApplyEventItem(context.Background(), group, request, now); !errors.Is(err, ErrEventItem) {
			t.Fatalf("full favor error=%v", err)
		}
		if !reflect.DeepEqual(before, eventItemSnapshot(t, group)) {
			t.Fatal("full favor rejection changed assets")
		}
	})
}

func TestEventNPCFavorTransactionFailureRollsBack(t *testing.T) {
	group, request, now := eventItemFixture(t, EventNPCFavor)
	key := eventNPCFavorPolicy(t, &request, 12345, 30, 500)
	eventNPCFavorSetPoint(t, group, key, 20)
	before := eventItemSnapshot(t, group)
	base := group.CeraShopAssets
	group.CeraShopAssets = eventItemFailUOW{base}
	if _, err := ApplyEventItem(context.Background(), group, request, now); err == nil {
		t.Fatal("forced transaction abort was not returned")
	}
	if !reflect.DeepEqual(before, eventItemSnapshot(t, group)) {
		t.Fatal("transaction abort retained favor or consumed letter")
	}
}

func TestEventNPCFavorStaleRevalidationRollsBack(t *testing.T) {
	group, request, now := eventItemFixture(t, EventNPCFavor)
	key := eventNPCFavorPolicy(t, &request, 12345, 30, 500)
	eventNPCFavorSetPoint(t, group, key, 20)
	calls := 0
	request.Revalidate = func() bool {
		calls++
		return calls == 1
	}
	before := eventItemSnapshot(t, group)
	if _, err := ApplyEventItem(context.Background(), group, request, now); !errors.Is(err, ErrEventItem) {
		t.Fatalf("stale revalidation error=%v", err)
	}
	if calls != 2 {
		t.Fatalf("revalidation calls=%d, want initial and post-write checks", calls)
	}
	if !reflect.DeepEqual(before, eventItemSnapshot(t, group)) {
		t.Fatal("stale revalidation retained staged favor or inventory changes")
	}
}

func TestEventNPCFavorConcurrentLetterConsumedOnce(t *testing.T) {
	group, request, now := eventItemFixture(t, EventNPCFavor)
	key := eventNPCFavorPolicy(t, &request, 12345, 10, 500)
	start := make(chan struct{})
	results := make(chan error, 16)
	var wait sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := ApplyEventItem(context.Background(), group, request, now)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes=%d, want exactly one", successes)
	}
	bag, found, err := group.Inventory.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, err)
	}
	if _, exists := bag.Slots["0:70"]; exists {
		t.Fatal("successful favor use did not consume the only letter")
	}
	character, found, err := group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	if character.Stats[key] != 10 {
		t.Fatalf("favor=%d, want one ten-point application", character.Stats[key])
	}
}

func TestEventNPCFavorRejectsInvalidPolicyWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		npc     uint32
		points  int64
		maximum int64
	}{
		{name: "zero npc", npc: 0, points: 10, maximum: 500},
		{name: "zero points", npc: 12345, points: 0, maximum: 500},
		{name: "negative points", npc: 12345, points: -1, maximum: 500},
		{name: "points overflow", npc: 12345, points: int64(math.MaxInt32) + 1, maximum: 500},
		{name: "zero maximum", npc: 12345, points: 10, maximum: 0},
		{name: "negative maximum", npc: 12345, points: 10, maximum: -1},
		{name: "maximum overflow", npc: 12345, points: 10, maximum: int64(math.MaxInt32) + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			group, request, now := eventItemFixture(t, EventNPCFavor)
			eventNPCFavorPolicy(t, &request, test.npc, test.points, test.maximum)
			before := eventItemSnapshot(t, group)
			if _, err := ApplyEventItem(context.Background(), group, request, now); !errors.Is(err, ErrEventItem) {
				t.Fatalf("invalid favor policy error=%v", err)
			}
			if !reflect.DeepEqual(before, eventItemSnapshot(t, group)) {
				t.Fatal("invalid favor policy changed assets")
			}
		})
	}
}
