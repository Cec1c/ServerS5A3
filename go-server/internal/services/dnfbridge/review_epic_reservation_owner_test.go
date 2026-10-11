package dnfbridge

import (
	"context"
	"errors"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type epicReservationPausedAssets struct {
	repo.CharacterAssetUnitOfWork
	paid   chan struct{}
	resume chan struct{}
}

func (u *epicReservationPausedAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	e := u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, fn)
	if e != nil {
		return e
	}
	close(u.paid)
	<-u.resume
	return nil
}
func TestReviewActual6063PaidReservationBlocksCanonicalReplacementUntilInstall(t *testing.T) {
	s, a, r, g, identity := prepareAuditActual6063Owner(t)
	pause := &epicReservationPausedAssets{g.CharacterAssets, make(chan struct{}), make(chan struct{})}
	modified := g
	modified.CharacterAssets = pause
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	entryDone := make(chan error, 1)
	go func() { entryDone <- s.commitDungeonRuntime(a, r) }()
	select {
	case <-pause.paid:
	case <-time.After(time.Second):
		t.Fatal("actual physical paid point not reached")
	}
	bag, _, e := g.Inventory.Load(context.Background(), "99")
	if e != nil || bag.Slots["0:1"].Count != 0 {
		t.Fatal("physical debit not proven", e)
	}
	if a.dungeon.runtime == r {
		t.Fatal("post-physical/pre-install pause not reached")
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		t.Fatal("binding changed while paid reservation owns install")
	}
	readerDone := make(chan bool, 1)
	go func() { readerDone <- s.boundGameSessionCharacterCurrent(identity) }()
	select {
	case yes := <-readerDone:
		if !yes {
			t.Fatal("read-only binding rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("paid reservation blocks read-only validation")
	}
	replacement := &gameSession{selectedCharacterID: 99, characterGeneration: identity.generation + 1, accountID: a.accountID}
	replaced := make(chan struct{})
	go func() { s.promoteResidentGameSession(replacement, 99); close(replaced) }()
	select {
	case <-replaced:
		t.Fatal("canonical writer bypassed paid reservation")
	case <-time.After(30 * time.Millisecond):
	}
	other := &gameSession{selectedCharacterID: 100, characterGeneration: 1, accountID: "other"}
	otherDone := make(chan struct{})
	go func() { s.promoteResidentGameSession(other, 100); close(otherDone) }()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("per-character reservation blocked unrelated character")
	}
	close(pause.resume)
	select {
	case e = <-entryDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("paid runtime not installed")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("writer not released after paid install")
	}
	if a.dungeon.runtime != r {
		t.Fatal("paid old-owner runtime never installed")
	}
	if current, ok := s.onlineGameSessionCharacterSnapshot(99); !ok || current.session != replacement {
		t.Fatal("canonical replacement did not resume")
	}
	s.mu.Lock()
	pending := len(s.characterAssetReservations)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatal("reservation leaked after success")
	}
}

type epicReservationAbortedAssets struct {
	repo.CharacterAssetUnitOfWork
	kind   string
	cancel context.CancelFunc
}

func (u epicReservationAbortedAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(c, i, e); err != nil {
			return err
		}
		if u.cancel != nil {
			u.cancel()
		}
		if u.kind == "panic" {
			panic("epic-reservation-test-abort")
		}
		return errors.New("epic-reservation-test-abort")
	})
}
func TestReviewActual6063ReservationReleasesOnAbortAndSamePreparedRetry(t *testing.T) {
	for _, kind := range []string{"error", "cancel", "panic"} {
		t.Run(kind, func(t *testing.T) {
			s, a, r, g, _ := prepareAuditActual6063Owner(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.ctx = ctx
			fault := epicReservationAbortedAssets{CharacterAssetUnitOfWork: g.CharacterAssets, kind: kind}
			if kind == "cancel" {
				fault.cancel = cancel
			}
			modified := g
			modified.CharacterAssets = fault
			s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			failed := false
			func() {
				defer func() {
					if recover() != nil {
						failed = true
					}
				}()
				failed = s.commitDungeonRuntime(a, r) != nil
			}()
			if !failed {
				t.Fatal("abort fixture not reached")
			}
			bag, _, e := g.Inventory.Load(context.Background(), "99")
			if e != nil || bag.Slots["0:1"].Count != 30 || a.dungeon.runtime == r {
				t.Fatal("aborted admission escaped", e)
			}
			s.mu.Lock()
			n := len(s.characterAssetReservations)
			s.mu.Unlock()
			if n != 0 {
				t.Fatal("abort leaked paid reservation")
			}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			a.ctx = context.Background()
			if e = s.commitDungeonRuntime(a, r); e != nil {
				t.Fatal("same prepared retry", e)
			}
			bag, _, _ = g.Inventory.Load(context.Background(), "99")
			ch, _, _ := g.Character.Load(context.Background(), "99")
			if bag.Slots["0:1"].Count != 0 || ch.Stats["epic_entry_sequence"] != 1 || a.dungeon.runtime != r {
				t.Fatal("retry did not debit/install exactly once")
			}
		})
	}
}

type epicReservationUnknownAfterCommit struct{ repo.CharacterAssetUnitOfWork }

func (u epicReservationUnknownAfterCommit) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	if e := u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, fn); e != nil {
		return e
	}
	return errors.New("simulated-unknown-after-durable-epic-commit")
}
func TestReviewActual6063UnknownCommitKeepsReceiptForSamePreparedRetry(t *testing.T) {
	s, a, r, g, _ := prepareAuditActual6063Owner(t)
	modified := g
	modified.CharacterAssets = epicReservationUnknownAfterCommit{g.CharacterAssets}
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	if e := s.commitDungeonRuntime(a, r); e == nil {
		t.Fatal("unknown result not injected")
	}
	bag, _, _ := g.Inventory.Load(context.Background(), "99")
	c, _, _ := g.Character.Load(context.Background(), "99")
	if bag.Slots["0:1"].Count != 0 || c.Stats["epic_entry_sequence"] != 1 || a.dungeon.runtime == r {
		t.Fatal("unknown fixture not durable before install")
	}
	s.mu.Lock()
	pending := len(s.characterAssetReservations)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatal("unknown return leaked reservation")
	}
	nonce := r.epicEntry.command.Nonce
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	if e := s.commitDungeonRuntime(a, r); e != nil {
		t.Fatal("exact prepared receipt retry", e)
	}
	final, _, _ := g.Character.Load(context.Background(), "99")
	if final.Stats["epic_entry_sequence"] != 1 || final.Stats["epic_entry_nonce"] != nonce || a.dungeon.runtime != r {
		t.Fatal("unknown retry replaced/charged receipt")
	}
	// This is a Memory durable-success/unknown-result simulation, not a live SQL
	// network test and not recovery for a fresh op16 with a newly generated nonce.
}
func TestReviewActual6063CancelAndDisconnectAfterPaidStillInstallBeforeCleanup(t *testing.T) {
	s, a, r, g, _ := prepareAuditActual6063Owner(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	pause := &epicReservationPausedAssets{g.CharacterAssets, make(chan struct{}), make(chan struct{})}
	modified := g
	modified.CharacterAssets = pause
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	entryDone := make(chan error, 1)
	go func() { entryDone <- s.commitDungeonRuntime(a, r) }()
	select {
	case <-pause.paid:
	case <-time.After(time.Second):
		t.Fatal("paid point missing")
	}
	cancel()
	closed := make(chan struct{})
	go func() { s.unbindGameSession(a); close(closed) }()
	select {
	case <-closed:
		t.Fatal("disconnect changed owner before paid installation")
	case <-time.After(30 * time.Millisecond):
	}
	close(pause.resume)
	select {
	case e := <-entryDone:
		if e != nil {
			t.Fatal("paid install wrongly cancelled", e)
		}
	case <-time.After(time.Second):
		t.Fatal("paid install stuck against cleanup")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cleanup stuck after install")
	}
	if a.dungeon.runtime != r {
		t.Fatal("durable paid run not installed")
	}
	bag, _, _ := g.Inventory.Load(context.Background(), "99")
	c, _, _ := g.Character.Load(context.Background(), "99")
	if bag.Slots["0:1"].Count != 0 || c.Stats["epic_entry_sequence"] != 1 {
		t.Fatal("postpaid cleanup refunded/duplicated assets")
	}
}

type epicReservationSlowConn struct {
	bufferConn
	started chan struct{}
	resume  chan struct{}
	first   bool
}

func (c *epicReservationSlowConn) Write(b []byte) (int, error) {
	if !c.first {
		c.first = true
		close(c.started)
		<-c.resume
	}
	return c.bufferConn.Write(b)
}
func TestReviewActual6063ReservationEndsBeforeSlowSocketProjection(t *testing.T) {
	s, a, r, _, identity := prepareAuditActual6063Owner(t)
	slow := &epicReservationSlowConn{started: make(chan struct{}), resume: make(chan struct{})}
	a.conn = slow
	done := make(chan error, 1)
	go func() { done <- s.commitDungeonRuntime(a, r) }()
	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("postinstall socket projection not reached")
	}
	if a.dungeon.runtime != r {
		t.Fatal("socket began before paid install")
	}
	s.mu.Lock()
	n := len(s.characterAssetReservations)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("slow socket retained reservation")
	}
	replacement := &gameSession{selectedCharacterID: 99, characterGeneration: identity.generation + 1, accountID: a.accountID}
	promoted := make(chan struct{})
	go func() { s.promoteResidentGameSession(replacement, 99); close(promoted) }()
	select {
	case <-promoted:
	case <-time.After(time.Second):
		t.Fatal("canonical replacement blocked by postinstall slow socket")
	}
	close(slow.resume)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("socket continuation did not finish entry")
	}
}
func TestReviewActual6063CanonicalWritersWaitOnlyUntilPaidInstall(t *testing.T) {
	for _, kind := range []string{"bind", "handover", "reset", "token"} {
		t.Run(kind, func(t *testing.T) {
			s, a, r, g, identity := prepareAuditActual6063Owner(t)
			token := ""
			if kind == "token" {
				a.accountFromToken = false
				var e error
				token, e = newLoginToken()
				if e != nil {
					t.Fatal(e)
				}
				s.loginTokens.remember(token, "other-authorized-token-account", time.Now().Add(time.Hour))
			}
			replacement := &gameSession{connID: "reservation-next", conn: &bufferConn{}, channel: a.channel, residentChannel: a.residentChannel, selectedCharacterID: 99, characterGeneration: identity.generation + 1, accountID: a.accountID}
			if kind == "handover" {
				a.connID = "reservation-old"
				s.registerLiveGameSession(a)
				s.registerLiveGameSession(replacement)
			}
			pause := &epicReservationPausedAssets{g.CharacterAssets, make(chan struct{}), make(chan struct{})}
			modified := g
			modified.CharacterAssets = pause
			s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			entryDone := make(chan error, 1)
			go func() { entryDone <- s.commitDungeonRuntime(a, r) }()
			select {
			case <-pause.paid:
			case <-time.After(time.Second):
				t.Fatal("paid boundary missing")
			}
			writerDone := make(chan bool, 1)
			go func() {
				switch kind {
				case "bind":
					s.bindGameSessionCharacter(replacement, 99)
					writerDone <- true
				case "handover":
					writerDone <- s.handoverRuntimePartyToLiveSession(a, "reservation-test")
				case "reset":
					s.resetGameSessionForCharacterSelect(a)
					writerDone <- true
				case "token":
					writerDone <- s.bindGameSessionAccountFromLoginToken(a, proto.DefaultChannelClassification, buildClientLoginBody(token))
				}
			}()
			select {
			case <-writerDone:
				t.Fatal("canonical writer crossed paid reservation")
			case <-time.After(30 * time.Millisecond):
			}
			if !s.boundGameSessionCharacterCurrent(identity) {
				t.Fatal("owner changed before paid install")
			}
			close(pause.resume)
			select {
			case e := <-entryDone:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("entry/writer lock inversion")
			}
			select {
			case ok := <-writerDone:
				if !ok {
					t.Fatal("real canonical mutation not resumed")
				}
			case <-time.After(time.Second):
				t.Fatal("writer remained blocked")
			}
			c, _, _ := g.Character.Load(context.Background(), "99")
			if c.Stats["epic_entry_sequence"] != 1 {
				t.Fatal("writer invalidated paid receipt")
			}
			s.mu.Lock()
			n := len(s.characterAssetReservations)
			s.mu.Unlock()
			if n != 0 {
				t.Fatal("writer sequence leaked reservation")
			}
		})
	}
}
