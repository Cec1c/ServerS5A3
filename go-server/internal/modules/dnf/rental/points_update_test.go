package rental

import (
	"context"
	"longheng.io/server/internal/modules/dnf/pvf"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"math"
	"sync"
	"testing"
)

func TestUpdatePointsBoundsAndPreservesAssets(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	for _, value := range []int64{-1, 401, math.MaxInt64} {
		if _, err := owner.UpdatePoints(ctx, UpdatePointsCommand{AccountID: "dnf:1", CharacterID: "77", Value: &value, Limit: 400}); err == nil {
			t.Fatalf("accepted %d", value)
		}
	}
	value := int64(400)
	result, err := owner.UpdatePoints(ctx, UpdatePointsCommand{AccountID: "dnf:1", CharacterID: "77", Value: &value, Limit: 400})
	if err != nil || result.Points != 400 {
		t.Fatalf("%+v %v", result, err)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 1000 {
		t.Fatal("gold changed")
	}
	for _, amount := range []int64{1, math.MaxInt64, math.MinInt64, -401} {
		if _, err := owner.UpdatePoints(ctx, UpdatePointsCommand{AccountID: "dnf:1", CharacterID: "77", Amount: &amount, Limit: 400}); err == nil {
			t.Fatalf("accepted delta %d", amount)
		}
	}
	account, _, _ := repos.Account.Load(ctx, "dnf:1")
	if points, _ := Points(account); points != 400 {
		t.Fatal("rejection changed points")
	}
}
func TestUpdatePointsConcurrentDeltas(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := int64(1)
			if _, err := owner.UpdatePoints(ctx, UpdatePointsCommand{AccountID: "dnf:1", CharacterID: "77", Amount: &a, Limit: 400}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	account, _, _ := repos.Account.Load(ctx, "dnf:1")
	points, _ := Points(account)
	if points != 35 {
		t.Fatalf("lost updates: %d", points)
	}
}
func TestPointLimitRejectsMissingDuplicateAndMalformed(t *testing.T) {
	for _, text := range []string{"", "[limit point] 0", "[limit point] 400 999", "[limit point] 4294967296"} {
		doc, err := pvf.Parse(SystemPVFPath, text)
		if err == nil {
			if _, err = PointLimit(doc); err == nil {
				t.Fatalf("accepted %q", text)
			}
		}
	}
	doc, _ := pvf.Parse(SystemPVFPath, "[limit point] 400")
	if limit, err := PointLimit(doc); limit != 400 || err != nil {
		t.Fatalf("%d %v", limit, err)
	}
}
