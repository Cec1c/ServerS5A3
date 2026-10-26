package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type r191FailingConn struct{ *bufferConn }

func (r191FailingConn) Write([]byte) (int, error) {
	return 0, errors.New("r191 injected write failure")
}

func r191DrainBuffSnapshots(t *testing.T, conn *bufferConn) int {
	t.Helper()
	count := 0
	for _, packet := range drainUpperPackets(t, conn) {
		if packet.msgID == currentBuffSwapSnapshotID || packet.msgID == currentBuffSwapRowsID || packet.msgID == nativeBuffSwapSnapshotID {
			count++
		}
	}
	return count
}

func r191CacheFixture(t *testing.T) (*Service, *gameSession, *bufferConn, repo.Group) {
	t.Helper()
	t.Setenv(buffSwapReapplyEnv, "on")
	repos := newSkillSwitchInventoryRepos(t)
	if err := repos.Inventory.Save(context.Background(), repo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	service, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
	service.options.currentClientVersion = "official-24.9"
	return service, session, conn, repos
}

// Explicit registration and clear go through sendBuffSwapReapply, the same
// recorder used by the committed-wear path. They must update the cache; a
// direct send that bypassed the recorder could leave the client on B while the
// cache still said A and a later move resolving to A was wrongly skipped.
func TestR191BuffReplayCacheDirectRegistrationAndClear(t *testing.T) {
	service, session, conn, repos := r191CacheFixture(t)
	ctx := context.Background()
	register := func(skill uint16, list byte, index uint16) {
		t.Helper()
		if err := service.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(skill, 12, list, index)); err != nil {
			t.Fatal(err)
		}
	}
	register(38, 0, 54)
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("registration snapshots=%d", got)
	}
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 0 {
		t.Fatalf("unchanged registration replay=%d", got)
	}

	// ParseCurrent accepts the current wire sentinel 45/0xffff and normalizes
	// it to the internal 44 clear value. Sending 44/0xffff on the wire is
	// rejected before any Buff output.
	register(38, 45, 0xffff)
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("clear snapshots=%d", got)
	}
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 0 {
		t.Fatalf("clear was not recorded; later replay=%d", got)
	}

	register(38, 0, 54)
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("re-registration snapshots=%d", got)
	}
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 0 {
		t.Fatalf("re-registration was not recorded; later replay=%d", got)
	}
	if state, found, err := buffswap.Load(ctx, repos, "1", "dnf:10001"); err != nil || !found || state.Slots[12].Index != 54 {
		t.Fatalf("registration state=%+v found=%t err=%v", state, found, err)
	}
}

func TestR191BuffReplayCacheOwnerAndFailureBoundaries(t *testing.T) {
	service, session, conn, repos := r191CacheFixture(t)
	ctx := context.Background()
	if err := service.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
		t.Fatal(err)
	}
	drainUpperPackets(t, conn)

	// A reconnect is a different session pointer. It must not inherit a cache
	// entry even when character/account and the projected body are identical.
	reconnected, reSession, reConn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
	reconnected.options.currentClientVersion = "official-24.9"
	if err := reconnected.sendBuffSwapReapplyAfterNonAppearanceWear(reSession, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, reConn); got != 1 {
		t.Fatalf("reconnect inherited cache: snapshots=%d", got)
	}

	// A new character generation on the same connection is also a new owner.
	service.gameSessions = map[uint16]*gameSession{1: session}
	session.characterGeneration++
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("generation change inherited cache: snapshots=%d", got)
	}

	// A failed write must not replace the last successful snapshot. Change the
	// durable state to an empty registration, fail that send, then a move that
	// resolves to the empty body must still be sent.
	failing, failSession, failConn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
	failing.options.currentClientVersion = "official-24.9"
	if err := failing.sendBuffSwapReapply(failSession, "last_success"); err != nil {
		t.Fatal(err)
	}
	drainUpperPackets(t, failConn)
	if err := buffswap.ApplyCurrent(ctx, repos, "1", "dnf:10001", buffswap.Command{Skill: 38, Slot: 12, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	failSession.conn = r191FailingConn{failConn}
	if err := failing.sendBuffSwapReapply(failSession, "failed_clear"); err == nil {
		t.Fatal("injected write failure was accepted")
	}
	failSession.conn = failConn
	if err := failing.sendBuffSwapReapplyAfterNonAppearanceWear(failSession, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, failConn); got != 1 {
		t.Fatalf("failed send polluted cache: snapshots=%d", got)
	}

	// A stale async owner must not match a newer generation or character.
	body := []byte{1, 2, 3}
	stale := buffOwnedSendIdentity{session: session, character: 1, generation: 3, accountID: "dnf:10001"}
	service.rememberBuffReplay(session, stale, currentBuffSwapSnapshotID, body)
	fresh := stale
	fresh.generation = 4
	if service.buffReplayUnchanged(session, fresh, currentBuffSwapSnapshotID, body) {
		t.Fatal("stale generation matched replay cache")
	}
	fresh.generation = 3
	fresh.character = 2
	if service.buffReplayUnchanged(session, fresh, currentBuffSwapSnapshotID, body) {
		t.Fatal("stale character matched replay cache")
	}
}

func TestR191BuffReplayCacheLegacyNeverDedupes(t *testing.T) {
	service, session, conn, repos := r191CacheFixture(t)
	service.options.currentClientVersion = ""
	ctx := context.Background()
	if err := buffswap.Apply(ctx, repos, "1", "dnf:10001", buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	if err := service.sendBuffSwapReapply(session, "legacy_registration"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("legacy registration snapshots=%d", got)
	}
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "aligned_move_itemspace"); err != nil {
		t.Fatal(err)
	}
	if got := r191DrainBuffSnapshots(t, conn); got != 1 {
		t.Fatalf("legacy 1369 path was deduped: snapshots=%d", got)
	}
}
