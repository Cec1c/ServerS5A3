package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This fixture uses the real shared-party adoption path and the matching PVF
// quest definition. Only combat content is the existing one-room test dungeon.
func weddingClearPartyFixture(t *testing.T) (*Service, []*gameSession, []*runtimeDungeonState, r.Group) {
	t.Helper()
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("matching PVF not supplied")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prepare := func(s *Service, a, b *gameSession) {
		s.initialEquipmentArchive = archive
		clock := newFakeCurrentDungeonDeathTimerQueue()
		clock.now = time.Date(2026, 11, 2, 14, 20, 22, 0, time.UTC)
		s.gameplayTimers = clock
		g, _ := s.repositoryGroup()
		for i, p := range []*gameSession{a, b} {
			id := fmt.Sprint(p.selectedCharacterID)
			c, found, err := g.Character.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("load character %s: found=%t err=%v", id, found, err)
			}
			c.AccountID, c.Level = "wedding-account-"+id, 90
			if c.Name == "" {
				c.Name = "wedding" + id
			}
			c.Job = []string{"0", "11"}[i]
			p.accountID = c.AccountID
			if err := g.Account.Save(ctx, r.AccountRecord{AccountID: c.AccountID}); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: id, Entries: map[string]r.EquipmentEntry{}}); err != nil {
				t.Fatal(err)
			}
		}
		identityA, _ := s.boundGameSessionCharacterSnapshot(a)
		identityB, _ := s.boundGameSessionCharacterSnapshot(b)
		memberA, okA := s.runtimePartyMemberForIdentity(identityA)
		memberB, okB := s.runtimePartyMemberForIdentity(identityB)
		if !okA || !okB {
			t.Fatal("party actors are not current")
		}
		manager := s.runtimePartyManagerForService()
		created := manager.Create(memberA, alignedcmd.PartyState{})
		joined := manager.Join(created.Party.ID, memberB)
		if !created.OK || !joined.OK || len(joined.Party.Members) != 2 {
			t.Fatalf("managed party create=%+v join=%+v", created, joined)
		}
		s.publishRuntimePartySnapshot(joined.Party)
		marriage := wedding.Owner{Repositories: g, Rules: &wedding.Rules{MinimumLevel: 30}}
		acceptedAt := clock.Now().Add(-time.Minute)
		if _, err := marriage.Engage(ctx, wedding.PairRequest{
			Actor:   wedding.Actor{AccountID: a.accountID, CharacterID: fmt.Sprint(a.selectedCharacterID)},
			Partner: wedding.Actor{AccountID: b.accountID, CharacterID: fmt.Sprint(b.selectedCharacterID)},
			Now:     acceptedAt,
		}); err != nil {
			t.Fatal(err)
		}
		catalog, err := s.loadQuestCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := quest.NewOwner(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []*gameSession{a, b} {
			result, err := owner.ApplyAcceptAt(ctx, catalog, quest.CharacterEligibility{Level: 90}, quest.Command{
				AccountID: p.accountID, SelectedCharacterID: p.selectedCharacterID, QuestID: 20737,
			}, acceptedAt)
			if err != nil || result.InitTrigger != 5 {
				t.Fatalf("real wedding quest accept char=%d result=%+v err=%v", p.selectedCharacterID, result, err)
			}
		}
	}
	s, a, b, ra, rb := partyDungeonDeathFixture(t, prepare)
	g, _ := s.repositoryGroup()
	return s, []*gameSession{a, b}, []*runtimeDungeonState{ra, rb}, g
}

func TestWeddingCurrentPVFPartyOrdinaryClearAdvancesBothAndReplaysOnce(t *testing.T) {
	for _, mode := range []string{"canonical", "historic-party-generation", "registered-auxiliary"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, runs, repos := weddingClearPartyFixture(t)
			switch mode {
			case "historic-party-generation":
				manager := s.runtimePartyManagerForService()
				for _, p := range peers {
					identity, _ := s.boundGameSessionCharacterSnapshot(p)
					member, _ := s.runtimePartyMemberForIdentity(identity)
					member.SessionGeneration += 1000
					if result := manager.RebindSession(identity.character, identity.generation, member); !result.OK {
						t.Fatal("cannot set historic membership fixture", result.Reason)
					}
				}
			case "registered-auxiliary":
				for i, p := range peers {
					aux := &gameSession{conn: &bufferConn{}, connID: fmt.Sprint("wedding-aux-", p.selectedCharacterID),
						accountID: p.accountID, selectedCharacterID: p.selectedCharacterID, characterGeneration: p.characterGeneration + 100,
						channel: p.channel, residentChannel: p.residentChannel}
					storeRuntimePartyState(aux, runtimePartyStateSnapshot(p))
					aux.dungeon.runtime, p.dungeon.runtime = runs[i], nil
					s.registerLiveGameSession(aux)
					instance := runs[i].partyInstance
					instance.mu.Lock()
					instance.sessions[p.selectedCharacterID] = aux
					instance.mu.Unlock()
					peers[i] = aux
				}
			}
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			assertWeddingPartyOrdinaryClear(t, s, peers, runs, repos)
			if !strings.Contains(logs.String(), `"remaining_before":5,"remaining_after":4,"outcome":"persisted"`) ||
				strings.Contains(logs.String(), "wedding-account-") {
				t.Fatalf("packet-log-off diagnostic missing or exposed accounts: %s", logs.String())
			}
		})
	}
}

func TestWeddingCurrentPVFClearRejectsRetiredOrMismatchedRunAttachments(t *testing.T) {
	s, peers, runs, _ := weddingClearPartyFixture(t)
	instance := runs[0].partyInstance
	peerID := peers[1].selectedCharacterID
	for _, test := range []struct {
		name   string
		change func() func()
	}{
		{"source-account", func() func() {
			old := peers[0].accountID
			peers[0].accountID = "foreign"
			return func() { peers[0].accountID = old }
		}},
		{"peer-account", func() func() {
			old := peers[1].accountID
			peers[1].accountID = "foreign"
			return func() { peers[1].accountID = old }
		}},
		{"revoked-source", func() func() { peers[0].loginRevoked.Store(true); return func() { peers[0].loginRevoked.Store(false) } }},
		{"peer-scene", func() func() {
			instance.mu.Lock()
			old := instance.scenes[peerID]
			instance.scenes[peerID] = runs[0].Session
			instance.mu.Unlock()
			return func() { instance.mu.Lock(); instance.scenes[peerID] = old; instance.mu.Unlock() }
		}},
		{"peer-runtime-detached", func() func() {
			instance.mu.Lock()
			old := instance.memberRuntimes[peerID]
			delete(instance.memberRuntimes, peerID)
			instance.mu.Unlock()
			return func() { instance.mu.Lock(); instance.memberRuntimes[peerID] = old; instance.mu.Unlock() }
		}},
		{"peer-session-replaced", func() func() {
			instance.mu.Lock()
			old := instance.sessions[peerID]
			instance.sessions[peerID] = &gameSession{}
			instance.mu.Unlock()
			return func() { instance.mu.Lock(); instance.sessions[peerID] = old; instance.mu.Unlock() }
		}},
		{"retired-instance-registry", func() func() {
			registry := s.partyDungeonInstanceRegistryForService()
			registry.mu.Lock()
			delete(registry.byKey, instance.key)
			registry.mu.Unlock()
			return func() { registry.mu.Lock(); registry.byKey[instance.key] = instance; registry.mu.Unlock() }
		}},
		{"source-runtime-replaced", func() func() {
			peers[0].dungeon.runtime = &runtimeDungeonState{}
			return func() { peers[0].dungeon.runtime = runs[0] }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			restore := test.change()
			defer restore()
			if proof := s.currentWeddingClearPartnerProof(peers[0], runs[0]); proof.partner != "" {
				t.Fatalf("invalid run attachment qualified: %+v", proof)
			}
		})
	}
	if got := s.currentWeddingClearPartner(peers[0], runs[0]); got != fmt.Sprint(peerID) {
		t.Fatalf("valid current run lost after guard cases: %q", got)
	}
	// A captured transport proof cannot survive a canonical generation change.
	pin, valid := s.currentWeddingClearTransport(peers[0], runs[0].AccountID)
	if !valid {
		t.Fatal("valid source pin unavailable")
	}
	peers[0].characterGeneration++
	if s.currentWeddingClearTransportCurrent(pin) {
		t.Fatal("retired canonical generation reused its captured proof")
	}
	peers[0].characterGeneration--
	aux := &gameSession{conn: &bufferConn{}, connID: "wedding-guard-aux", accountID: peers[0].accountID,
		selectedCharacterID: peers[0].selectedCharacterID, characterGeneration: peers[0].characterGeneration + 100}
	aux.dungeon.runtime = runs[0]
	peers[0].dungeon.runtime = nil
	instance.mu.Lock()
	instance.sessions[aux.selectedCharacterID] = aux
	instance.mu.Unlock()
	defer func() {
		peers[0].dungeon.runtime = runs[0]
		instance.mu.Lock()
		instance.sessions[aux.selectedCharacterID] = peers[0]
		instance.mu.Unlock()
	}()
	if got := s.currentWeddingClearPartner(aux, runs[0]); got != "" {
		t.Fatal("unregistered auxiliary transport qualified")
	}
	s.registerLiveGameSession(aux)
	defer s.unregisterLiveGameSession(aux)
	if got := s.currentWeddingClearPartner(aux, runs[0]); got != fmt.Sprint(peerID) {
		t.Fatalf("registered same-owner auxiliary did not qualify: %q", got)
	}
	auxPin, valid := s.currentWeddingClearTransport(aux, runs[0].AccountID)
	if !valid {
		t.Fatal("registered auxiliary pin unavailable")
	}
	aux.characterGeneration++
	if s.currentWeddingClearTransportCurrent(auxPin) {
		t.Fatal("changed auxiliary generation reused its captured proof")
	}
	aux.characterGeneration--
	aux.accountID = "foreign"
	if got := s.currentWeddingClearPartner(aux, runs[0]); got != "" {
		t.Fatal("same-character auxiliary with a foreign account qualified")
	}
}

func assertWeddingPartyOrdinaryClear(t *testing.T, s *Service, peers []*gameSession, runs []*runtimeDungeonState, repos r.Group) {
	t.Helper()
	for i, p := range peers {
		want := fmt.Sprint(peers[1-i].selectedCharacterID)
		if got := s.currentWeddingClearPartner(p, runs[i]); got != want {
			t.Fatalf("char=%d clear partner=%q want=%q entry=%d", p.selectedCharacterID, got, want, runs[i].entryPartyCount)
		}
	}
	target := runs[0].Room.Snapshot().Monsters[0].ObjectKey
	if _, cleared, err := runs[0].Room.CommitActorDeathReport(target, runs[0].Session); err != nil || !cleared {
		t.Fatalf("authoritative final actor cleared=%t err=%v", cleared, err)
	}
	if _, err := runs[1].Session.MarkHostileDefeated(target); err != nil {
		t.Fatal(err)
	}
	for i, p := range peers {
		p.dungeon.mu.Lock()
		scene, _ := runs[i].Session.Scene()
		err := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(p, runs[i], scene, target)
		p.dungeon.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !runs[i].clearMapCompletionPhaseAPersisted {
			t.Fatalf("ordinary no-op117 completion did not close PhaseA char=%d", p.selectedCharacterID)
		}
		record, found, err := repos.Quest.Load(context.Background(), fmt.Sprint(p.selectedCharacterID))
		if err != nil || !found || record.States[20737].ProgressValue != 4 {
			t.Fatalf("real wedding clear char=%d found=%t row=%+v err=%v", p.selectedCharacterID, found, record.States[20737], err)
		}
		if record.States[20737].Extra["reward_state"] == "pending" || len(runs[i].clearMapCompletionPendingQuestIDs) != 0 {
			t.Fatal("partial wedding progress was marked reward-pending")
		}
		assertNonoraActiveQuestSnapshotProgress(t, p.conn.(*bufferConn).write.Bytes(), map[uint16]uint32{20737: 4})
		key := runs[i].clearMapCompletionKey
		if key == "" || record.States[20737].Extra["completion_key"] != key {
			t.Fatal("real clear missing stable receipt")
		}
		for extraKey := range record.States[20737].Extra {
			if len(extraKey) > 128 {
				t.Fatalf("clear metadata key exceeds SQL column: length=%d", len(extraKey))
			}
		}
		// Re-enter the quest persistence owner with the same accepted run receipt.
		runs[i].clearMapCompletionPhaseAPersisted = false
		p.dungeon.mu.Lock()
		err = s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), p, runs[i], runs[i].Session.Snapshot())
		p.dungeon.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		after, _, _ := repos.Quest.Load(context.Background(), fmt.Sprint(p.selectedCharacterID))
		if after.States[20737].ProgressValue != 4 || runs[i].clearMapCompletionKey != key {
			t.Fatal("same run counted more than once")
		}
	}
}
