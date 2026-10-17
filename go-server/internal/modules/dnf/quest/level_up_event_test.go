package quest

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestFinishQuestRecordsRealLevelEventWithSettlementAndNotOnReplay(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	first, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := repos.Character.Load(ctx, "19")
	stamp := c.Stats[dnfrepo.LevelUpEventKey(2)]
	if first.NewLevel != 2 || stamp <= 0 {
		t.Fatalf("quest upgrade missing durable event=%+v %+v", first, c)
	}
	if _, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "19")
	if c.Stats[dnfrepo.LevelUpEventKey(2)] != stamp || c.Stats[dnfrepo.LevelUpEventKey(3)] != 0 {
		t.Fatal("quest replay recreated upgrade event")
	}
}
