package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFLukeSelectorReturnDefersWhileNewRunIsInstalled(t *testing.T) {
	f, member, _ := lukeSelectorReturnWaitingFixture(t)
	owner := member.selectorReturnPresence
	before := member.conn.(*bufferConn).write.Len()
	// The prepared-follower tests exercise installation and rollback. This
	// interleaving is the late town confirmation while that new run is installed.
	member.dungeon.mu.Lock()
	member.dungeon.runtime = &runtimeDungeonState{}
	member.dungeon.mu.Unlock()
	if err := f.service.ensureCurrentConfirmedDungeonReturnPlayerState(member, "late_town_during_prepared_entry"); err != nil {
		t.Fatal(err)
	}
	if member.conn.(*bufferConn).write.Len() != before || member.selectorReturnPresence != owner || !member.confirmedDungeonReturnStatePending {
		t.Fatal("installed run consumed or sent the old town return")
	}
	member.dungeon.mu.Lock()
	member.dungeon.runtime = nil
	member.dungeon.mu.Unlock()
	if err := f.service.ensureCurrentConfirmedDungeonReturnPlayerState(member, "town_confirm_after_rollback"); err != nil {
		t.Fatal(err)
	}
	if _, found := f.service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); !found {
		t.Fatal("rolled-back entry lost its pending return")
	}
}

type lukeReturnFailingCharacterRepository struct {
	dnfrepo.CharacterRepository
	fail atomic.Bool
}

func (r *lukeReturnFailingCharacterRepository) Load(ctx context.Context, key string) (dnfrepo.CharacterRecord, bool, error) {
	if r.fail.Load() {
		return dnfrepo.CharacterRecord{}, false, errors.New("transient character read failure")
	}
	return r.CharacterRepository.Load(ctx, key)
}

type lukeReturnAfterPlacementConn struct {
	net.Conn
	repository *lukeReturnFailingCharacterRepository
}

func (c *lukeReturnAfterPlacementConn) Write(body []byte) (int, error) {
	n, err := c.Conn.Write(body)
	if err == nil && n == len(body) && len(body) >= 3 && binary.LittleEndian.Uint16(body[1:3]) == 120 {
		c.repository.fail.Store(true)
	}
	return n, err
}

func TestRealPVFLukeSelectorReturnRetriesPresenceWithoutRebuildingActor(t *testing.T) {
	f, member, _ := lukeSelectorReturnWaitingFixture(t)
	s := f.service
	repos := f.repos
	fault := &lukeReturnFailingCharacterRepository{CharacterRepository: repos.Character}
	repos.Character = fault
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	conn := member.conn
	member.conn = &lukeReturnAfterPlacementConn{Conn: conn, repository: fault}
	if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(member, "actor_written_then_repository_unavailable"); err != nil {
		t.Fatal(err)
	}
	if !fault.fail.Load() {
		t.Fatal("fixture did not reach the final actor placement write")
	}
	if member.confirmedDungeonReturnStatePending || member.selectorReturnPresence == nil {
		t.Fatal("completed actor state lost its deferred presence owner")
	}
	if _, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); found {
		t.Fatal("failed character read published presence")
	}
	generation := member.townPostTransition.generation
	if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(member, "repository_still_unavailable"); err != nil {
		t.Fatal(err)
	}
	if member.selectorReturnPresence == nil || generation != member.townPostTransition.generation {
		t.Fatal("transient failure discarded the retry or replayed the actor")
	}
	fault.fail.Store(false)
	member.conn = conn
	if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(member, "repository_recovered"); err != nil {
		t.Fatal(err)
	}
	if p, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); !found || p.Session != member {
		t.Fatal("recovered character read did not restore waiting presence")
	}
	if member.selectorReturnPresence != nil || generation != member.townPostTransition.generation {
		t.Fatal("presence-only retry replayed or retained the actor generation")
	}
}
