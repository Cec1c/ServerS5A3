package guild

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildMemberExpansionTo5000AndContractUpgrade(t *testing.T) {
	ctx := context.Background()
	repos, q, now := contractPurchaseFixture(t)
	cat := contractCatalog()
	cat.Contents[19] = Content{ID: 19, Type: 6, Value: 5000, Days: -1, Price: 2000000}
	cat.Contents[27] = Content{ID: 27, Type: 21, Value: 5000, Days: 1, Price: 10000000, Gold: true}

	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Mileage = 2800000
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}

	// Skip-tier directly to 5000 from 300 must be rejected.
	q.ContentID, q.RequestID = 19, fmt.Sprintf("%064x", 900)
	if _, err := PurchaseMemberExpansion(ctx, repos.Guild, q, cat, now); err != ErrMemberExpansionPrerequisite {
		t.Fatalf("skip tier 300->5000 err=%v want ErrMemberExpansionPrerequisite", err)
	}

	// Expand 300 -> 500 -> 700 -> 1000.
	for i, want := range []uint32{500, 700, 1000} {
		q.ContentID, q.RequestID = uint32(16+i), fmt.Sprintf("%064x", 910+i)
		got, err := PurchaseMemberExpansion(ctx, repos.Guild, q, cat, now)
		if err != nil || got.After != want {
			t.Fatalf("step %d got=%+v err=%v", i, got, err)
		}
	}

	// Buy an active 1000-tier contract before expanding to 5000.
	q.ContentID, q.RequestID = 26, fmt.Sprintf("%064x", 920)
	contract1000, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now)
	if err != nil || contract1000.Benefit.Value != 1000 {
		t.Fatalf("1000 contract got=%+v err=%v", contract1000, err)
	}

	// Expand 1000 -> 5000 and verify durable replay + validation on reload.
	q.ContentID, q.RequestID = 19, fmt.Sprintf("%064x", 930)
	got, err := PurchaseMemberExpansion(ctx, repos.Guild, q, cat, now)
	if err != nil || got.Before != 1000 || got.After != 5000 || got.Price != 2000000 {
		t.Fatalf("1000->5000 got=%+v err=%v", got, err)
	}
	replayed, err := PurchaseMemberExpansion(ctx, repos.Guild, q, cat, now.Add(time.Hour))
	if err != nil || !equalCargoPurchaseReceipt(replayed, got) {
		t.Fatalf("replay got=%+v err=%v", replayed, err)
	}

	reg, err = Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].MemberLimit != 5000 || reg.Guilds[1].Mileage != 0 {
		t.Fatalf("reloaded guild limit=%d mileage=%d err=%v", reg.Guilds[1].MemberLimit, reg.Guilds[1].Mileage, err)
	}
	// Existing 1000-tier contract stays active until extended by the 5000-tier contract.
	if !reg.Guilds[1].Benefits[21].Active(now) {
		t.Fatal("active 1000-tier contract became inactive after 5000 expansion")
	}

	// Buying 1000-tier contract after expanding to 5000 must be rejected.
	q.ContentID, q.RequestID = 26, fmt.Sprintf("%064x", 940)
	if _, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now); err != ErrContractMemberLimit {
		t.Fatalf("old tier contract err=%v want ErrContractMemberLimit", err)
	}

	// Buying 5000-tier contract extends existing expiry seamlessly for 10,000,000 gold.
	q.ContentID, q.RequestID = 27, fmt.Sprintf("%064x", 950)
	contract5000, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now)
	if err != nil || !contract5000.Gold || contract5000.Price != 10000000 || contract5000.Benefit.Value != 5000 {
		t.Fatalf("5000 contract got=%+v err=%v", contract5000, err)
	}
	if !contract5000.Benefit.ExpiresAt.Equal(contract1000.Benefit.ExpiresAt.Add(24 * time.Hour)) {
		t.Fatalf("contract extension=%v want %v", contract5000.Benefit.ExpiresAt, contract1000.Benefit.ExpiresAt.Add(24*time.Hour))
	}
	actor, _, _ := repos.Character.Load(ctx, "1")
	if actor.Stats["gold"] != 100000000-10000000-10000000 {
		t.Fatalf("actor gold=%d want 80000000", actor.Stats["gold"])
	}

	// Joining member #1001 succeeds under the expanded 5000 capacity.
	reg, err = Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	for i := 2; i <= 1000; i++ {
		id := fmt.Sprint(i)
		g.Members[id] = Member{CharacterID: id, AccountID: "a" + id, Name: "成员" + id, Grade: DefaultMemberGrade}
	}
	if _, err := reg.JoinDirect(1, r.CharacterRecord{CharacterID: "1001", AccountID: "a1001", Name: "新成员1001", Level: 90}, now); err != nil {
		t.Fatalf("join 1001 failed: %v", err)
	}
	if len(g.Members) != 1001 {
		t.Fatalf("members=%d want 1001", len(g.Members))
	}
}

func TestGuildReadSnapshotCachesRegistryAbove4MiB(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	g := reg.Guilds[1]
	g.MemberLimit = 5000
	memo := strings.Repeat("公会成员备忘信息", 25) // ~600 bytes per member so 5000 members exceed 4 MiB JSON.
	for i := 2; i <= 5000; i++ {
		id := fmt.Sprint(i)
		g.Members[id] = Member{
			CharacterID:  id,
			AccountID:    "a" + id,
			Name:         "成员" + id,
			Job:          "1",
			GrowType:     "33",
			Level:        90,
			Grade:        DefaultMemberGrade,
			Memo:         memo,
			JoinedAt:     now,
			AttendedAt:   now,
			LastOnlineAt: now,
		}
	}
	if err := Save(context.Background(), store, reg, now); err != nil {
		t.Fatal(err)
	}
	rawLen := len(store.record.Values[registryStateKey])
	if rawLen <= 4<<20 {
		t.Fatalf("fixture raw JSON len=%d must exceed 4 MiB to test large snapshot caching", rawLen)
	}
	for i := 0; i < 10; i++ {
		membership, found, err := ReadGuildMembership(context.Background(), store, owner.CharacterID)
		if err != nil || !found {
			t.Fatalf("iter=%d found=%v err=%v", i, found, err)
		}
		if ent, ok := membership.Entitlement(owner); !ok || ent.ID != 1 {
			t.Fatalf("iter=%d entitlement=%+v ok=%v", i, ent, ok)
		}
	}
	if store.loads != 1 {
		t.Fatalf("registry above 4 MiB (%d bytes) loaded %d times, want 1 cached load", rawLen, store.loads)
	}
}
