package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type spendTimeFailSettlement struct {
	delegate      dnfrepo.CharacterSettlementUnitOfWork
	calls, failAt int
	failure       error
}

func TestSpendTimeTickSendsCommittedItemsDespiteProgressGateOrLaterFailure(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		failAt   int
		throttle bool
	}{
		{"progress-throttled", 0, true}, {"next-day-failed", 3, true},
		{"before-commit-failed", 1, true}, {"normal-progress-and-reward", 0, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, err := range []error{
				repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}),
				repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "account-1", Stats: map[string]int64{"gold": 0}}),
				repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "7", Slots: map[string]dnfrepo.ItemStack{}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.failAt != 0 {
				repos.CharacterSettlement = &spendTimeFailSettlement{delegate: repos.CharacterSettlement, failAt: scenario.failAt, failure: errors.New("repository unavailable")}
			}
			const rewardID uint32 = 490006574
			boundary := time.Date(2026, 8, 4, 6, 0, 0, 0, dnfonlineevent.ChinaCalendar)
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = boundary.Add(5 * time.Second)
			service := &Service{gameplayTimers: queue, pvfItemCatalog: &pvfDungeonDropCatalog{source: bridgePVFSource{}, itemCache: map[uint32]dungeonDropItemDefinition{rewardID: {ItemID: rewardID, Kind: dungeonDropItemStackable, StackLimit: 1000, SlotStart: 9, SlotEnd: 56}}}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "committed-review-" + t.Name()}
			session.spendTime.characterID = 7
			session.spendTime.accountID = "account-1"
			session.spendTime.anchor = boundary.Add(-20 * time.Second)
			session.spendTime.catalog = &currentSpendTimeRuntimeCatalog{definition: dnfonlineevent.Definition{ID: "2347-test", Calendar: dnfonlineevent.ChinaCalendar, Stages: []dnfonlineevent.Stage{{ID: "stage-1", RequiredSeconds: 10, Items: []dnfonlineevent.ItemReward{{ItemID: int64(rewardID), Count: 1}}}}}}
			defer forgetSpendTimeProgress(session.connID)
			if scenario.throttle && !spendTimeProgressShouldSend(session.connID, 7, 0, 0) {
				t.Fatal("fixture progress mark failed")
			}
			service.runCurrentSpendTimeTick(session, 0)
			wire := conn.write.Bytes()
			inv, _, err := repos.Inventory.Load(ctx, "7")
			if scenario.failAt == 1 {
				if len(wire) != 0 || err != nil || len(inv.Slots) != 0 {
					t.Fatalf("failed transaction emitted reward: wire=%x inv=%+v err=%v", wire, inv, err)
				}
				return
			}
			if len(wire) == 0 {
				t.Fatal("committed inventory reward never refreshed")
			}
			if !scenario.throttle {
				progress, rest := splitGameServerUpperPacketWithHeader(t, wire, service.gameUpperHeaderSize())
				if progress.Header.MsgID != currentSpendTimeProgressMsgID {
					t.Fatalf("normal progress=%+v", progress.Header)
				}
				wire = rest
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, wire, service.gameUpperHeaderSize())
			if packet.Header.MsgID != 14 || len(rest) != 0 {
				t.Fatalf("wanted only committed op14 refresh, got %+v trailing=%d", packet.Header, len(rest))
			}
			if err != nil || inv.Slots["0:9"].Count != 1 {
				t.Fatalf("committed count=%v err=%v", inv.Slots, err)
			}
		})
	}
}

func (u *spendTimeFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	u.calls++
	if u.calls == u.failAt {
		return u.failure
	}
	return u.delegate.WithinCharacterSettlement(ctx, id, fn)
}

func TestSpendTimeRetainsCommittedRewardsWhenFollowingDaySettlementFails(t *testing.T) {
	for _, after := range []time.Duration{0, 10 * time.Second} {
		t.Run(after.String(), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, e := range []error{
				repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}),
				repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "account-1", Stats: map[string]int64{"gold": 0}}),
				repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "7", Slots: map[string]dnfrepo.ItemStack{}}),
			} {
				if e != nil {
					t.Fatal(e)
				}
			}
			failure := errors.New("next-day repository temporarily unavailable")
			faults := &spendTimeFailSettlement{delegate: repos.CharacterSettlement, failAt: 3, failure: failure}
			repos.CharacterSettlement = faults
			const rewardID uint32 = 490006574
			service := &Service{pvfItemCatalog: &pvfDungeonDropCatalog{source: bridgePVFSource{}, itemCache: map[uint32]dungeonDropItemDefinition{rewardID: {ItemID: rewardID, Kind: dungeonDropItemStackable, StackLimit: 1000, SlotStart: 9, SlotEnd: 56}}}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			boundary := time.Date(2026, 8, 4, 6, 0, 0, 0, dnfonlineevent.ChinaCalendar)
			session := &gameSession{}
			session.spendTime.characterID = 7
			session.spendTime.accountID = "account-1"
			session.spendTime.anchor = boundary.Add(-20 * time.Second)
			session.spendTime.catalog = &currentSpendTimeRuntimeCatalog{definition: dnfonlineevent.Definition{ID: "2347-test", Calendar: dnfonlineevent.ChinaCalendar, Stages: []dnfonlineevent.Stage{{ID: "stage-1", RequiredSeconds: 10, Items: []dnfonlineevent.ItemReward{{ItemID: int64(rewardID), Count: 1}}}}}}
			result, err := service.settleCurrentSpendTimeClockLocked(session, boundary.Add(after))
			if !errors.Is(err, failure) {
				t.Fatalf("expected failure, result=%+v err=%v calls=%d", result, err, faults.calls)
			}
			inventory, found, loadErr := repos.Inventory.Load(ctx, "7")
			if loadErr != nil || !found || inventory.Slots["0:9"].Count != 1 {
				t.Fatalf("reward not committed: %+v err=%v", inventory, loadErr)
			}
			if !result.changed || len(result.rewardItems) != 1 || result.rewardItems[0].PostCount != 1 {
				t.Fatalf("committed reward update discarded: %+v", result)
			}
			if !session.spendTime.anchor.Equal(boundary) {
				t.Fatalf("failed day advanced anchor: %v", session.spendTime.anchor)
			}
			retry, retryErr := service.settleCurrentSpendTimeClockLocked(session, boundary.Add(10*time.Second))
			if retryErr != nil || len(retry.rewardItems) != 1 || retry.rewardItems[0].PostCount != 2 {
				t.Fatalf("retry=%+v err=%v", retry, retryErr)
			}
		})
	}
}
