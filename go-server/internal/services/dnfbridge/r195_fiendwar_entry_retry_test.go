package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	r195FiendWarSourceArea    = 10
	r195FiendWarTargetArea    = 9
	r195FiendWarTargetDungeon = 4957
)

type r195LogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *r195LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *r195LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type r195SessionGate struct {
	release chan struct{}
	done    chan error
	once    sync.Once
}

func (g *r195SessionGate) open() {
	if g != nil {
		g.once.Do(func() { close(g.release) })
	}
}

// This is the live-shape regression for both members entering one FiendWar
// encounter from the same source area. It deliberately goes through the real
// op36 handler and its automatic queue; it does not force b.Dungeons or any
// other point-in-time cache into an artificial state.
func TestR195FiendWarSameSourceDuoEntryRetry(t *testing.T) {
	f, peers := r134N1Fixture(t)
	r195FiendWarSameSourceEntry(t, f, peers, len(peers))
}

func TestR195FiendWarSameSourceFollowerStillOutside(t *testing.T) {
	f, peers := r134N1Fixture(t)
	r195FiendWarSameSourceEntry(t, f, peers, 1)
}

// The same path with a one-member roster is the control. A failure specific to
// the two-member roster is the routing/party-shape regression, not a general
// FiendWar entry refusal.
func TestR195FiendWarSameSourceSoloControl(t *testing.T) {
	f, peers := r134N1Fixture(t)
	if len(peers) < 2 || len(f.state.Members) < 2 {
		t.Fatal("fixture must start with two members")
	}
	removed := peers[1].selectedCharacterID
	removedGroup := f.state.Battle.MemberGroups[removed]
	f.state.Members = f.state.Members[:1]
	delete(f.state.Battle.MemberAccounts, removed)
	delete(f.state.Battle.MemberGroups, removed)
	delete(f.state.Battle.FiendWar.MemberAreas, removed)
	delete(f.state.Battle.FiendWar.GroupAreas, removedGroup)
	f.service.onlinePlayers.LeaveArea(removed)
	r195FiendWarSameSourceEntry(t, f, peers[:1], 1)
}

func r195FiendWarSameSourceEntry(t *testing.T, f *raidStartGuardFixture, peers []*gameSession, moving int) {
	t.Helper()
	if len(peers) == 0 {
		t.Fatal("fixture has no peers")
	}
	s, b := f.service, f.state.Battle
	area := r195FiendWarTargetArea
	target, found := b.FiendWar.Config.Areas[area]
	if !found || target.Dungeon != r195FiendWarTargetDungeon || target.WorldMap != 119 || target.MemberMax < 2 || target.Kind != "" {
		t.Fatalf("real PVF target mismatch for area %d: %+v", area, target)
	}
	if !b.FiendWar.AreaOpen(area) {
		// The initial PVF enemies spawn at 14/16/18. Area 14's move_enemy timer
		// fires at +120s and moves that existing enemy to area 9. Advance the
		// ordinary battle clock instead of inventing a unit ID or a row state.
		if b.StartedAt.IsZero() {
			t.Fatal("fixture battle has no start clock")
		}
		if b.FiendWar.Areas[14].Enemy.ID == 0 {
			t.Fatal("normal PVF start did not create the area-14 enemy")
		}
		if err := b.FiendWar.Tick(b.StartedAt.Add(180 * time.Second)); err != nil {
			t.Fatal(err)
		}
		syncCurrentFiendWarDungeons(b)
	}
	if !b.FiendWar.AreaOpen(area) {
		t.Fatalf("normal-clock enemy route did not open area %d: %+v", area, b.FiendWar.Areas[area])
	}
	if b.FiendWar.Areas[area].Enemy.ID == 0 {
		t.Fatalf("area %d is not backed by the PVF area-14 enemy route: %+v", area, b.FiendWar.Areas[area])
	}
	const source = r195FiendWarSourceArea
	if _, ok := b.FiendWar.Config.Areas[source]; !ok {
		t.Fatalf("real area %d is absent", source)
	}
	var logs r195LogBuffer
	previousLogger := s.logger
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	defer func() { s.logger = previousLogger }()

	// Prepare every source first. Only then start the real session loops and
	// block them while all op36 requests update their position and pending move.
	targetArea, found := s.townArea(int64(b.FiendWar.Config.WaitingTown), int64(area))
	if !found || len(targetArea.NeedQuests) == 0 {
		t.Fatalf("real town area %d has no quest requirement: found=%t area=%+v", area, found, targetArea)
	}
	has8641 := false
	for _, questID := range targetArea.NeedQuests {
		has8641 = has8641 || questID == 8641
	}
	if !has8641 {
		t.Fatalf("real town area %d quests=%v, want 8641", area, targetArea.NeedQuests)
	}
	// Production preloads the real skill catalog before accepting players;
	// resident characters have completed login initialization. Do that before
	// the bounded entry event too, rather than timing a cold fixture PVF parse.
	if err := s.preloadSkillCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers {
		r134N1Presence(t, f, peer, source)
		character, found, err := f.repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil || !found {
			t.Fatalf("load persisted character char%d found=%t err=%v", peer.selectedCharacterID, found, err)
		}
		// op36 requires the current persisted area state even when it is the
		// legal zero value. r134N1Presence restores the town/position fields but
		// does not supply this independent character statistic.
		character.Stats["area_state"] = 0
		if err := f.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatalf("persist area_state char%d: %v", peer.selectedCharacterID, err)
		}
		if _, err := s.ensureCharacterInitializationSnapshot(context.Background(), f.repos, character); err != nil {
			t.Fatalf("prepare logged-in character %d: %v", peer.selectedCharacterID, err)
		}
		quests, _, err := f.repos.Quest.Load(context.Background(), character.CharacterID)
		if err != nil {
			t.Fatalf("load quests char%d: %v", peer.selectedCharacterID, err)
		}
		if quests.States == nil {
			quests.States = make(map[int64]dnfrepo.QuestState, len(targetArea.NeedQuests))
		}
		quests.CharacterID = character.CharacterID
		for _, questID := range targetArea.NeedQuests {
			quests.States[questID] = dnfrepo.QuestState{Status: "completed"}
		}
		if err := f.repos.Quest.Save(context.Background(), quests); err != nil {
			t.Fatalf("persist real target quests char%d: %v", peer.selectedCharacterID, err)
		}
	}
	for _, peer := range peers {
		if err := s.startGameSessionEvents(context.Background(), peer); err != nil {
			t.Fatalf("start session events char%d: %v", peer.selectedCharacterID, err)
		}
		peer := peer
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			_ = s.stopGameSessionEvents(ctx, peer)
		})
	}
	// Run each real movement on its own session mailbox. Holding both mailboxes
	// before op36 (the old fixture) expired the automatic queue's 3s context
	// while the test itself was still doing movement/projection work.
	gates := make([]*r195SessionGate, moving)
	arrived := make(chan error, moving)
	defer func() {
		for _, gate := range gates {
			gate.open()
		}
	}()
	for i, peer := range peers[:moving] {
		gate := &r195SessionGate{release: make(chan struct{}), done: make(chan error, 1)}
		gates[i] = gate
		go func(peer *gameSession, gate *r195SessionGate) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			gate.done <- s.callGameSession(ctx, peer, "r195-real-op36", func() error {
				peer.townMu.Lock()
				x, y := peer.townPositionSnapshot.PositionX, peer.townPositionSnapshot.PositionY
				peer.townMu.Unlock()
				body := buildTownMoveRequest(98, byte(area), int16(x), int16(y), 5)
				err := s.handleTownSetUserArea(peer, body)
				if err == nil {
					peer.townMu.Lock()
					pending := peer.fiendWarPendingMove
					if pending == nil || pending.from != source || pending.to != area {
						err = fmt.Errorf("op36 pending char%d=%+v want %d->%d", peer.selectedCharacterID, pending, source, area)
					}
					peer.townMu.Unlock()
				}
				arrived <- err
				select {
				case <-gate.release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return err
			})
		}(peer, gate)
	}
	for range gates {
		select {
		case err := <-arrived:
			if err != nil {
				t.Fatalf("real op36: %v\n%s", err, r195RelevantLogs(logs.String()))
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("real op36 did not finish\n%s", r195RelevantLogs(logs.String()))
		}
	}
	for _, gate := range gates {
		gate.open()
	}
	for _, gate := range gates {
		select {
		case err := <-gate.done:
			if err != nil {
				t.Fatalf("op36 mailbox: %v", err)
			}
		case <-time.After(createWriteTimeout):
			t.Fatal("op36 mailbox did not finish")
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		// Both runtime pointers become visible before the leader drives each
		// follower through its op28 entry callback. That callback applies the
		// already-frozen FiendWar return origin. Observe the automatic queue's
		// completed attempt, not an intermediate runtime-installation state.
		allCommitted := strings.Contains(logs.String(), `"reason":"entry_committed"`)
		var missing []uint16
		for _, peer := range peers {
			peer.dungeon.mu.Lock()
			runtime := peer.dungeon.runtime
			peer.dungeon.mu.Unlock()
			if runtime == nil {
				allCommitted = false
				missing = append(missing, peer.selectedCharacterID)
				continue
			}
			if runtime.Dungeon.ID != r195FiendWarTargetDungeon {
				t.Fatalf("char%d entered dungeon %d want %d\n%s", peer.selectedCharacterID, runtime.Dungeon.ID, r195FiendWarTargetDungeon, r195RelevantLogs(logs.String()))
			}
		}
		if allCommitted {
			var runID string
			for _, peer := range peers {
				peer.dungeon.mu.Lock()
				runtime := peer.dungeon.runtime
				entry := runtime.raidEntry
				origin := runtime.townReturnOrigin
				peer.dungeon.mu.Unlock()
				if entry == nil || entry.RunID == "" || len(entry.Participants) != len(peers) {
					t.Fatalf("committed encounter has incomplete frozen participants: %+v", entry)
				}
				if runID != "" && entry.RunID != runID {
					t.Fatal("same-source members entered different encounter runs")
				}
				runID = entry.RunID
				if origin.CharacterID != peer.selectedCharacterID || !origin.PositionValid || origin.TownID != 98 || int(origin.AreaID) != area {
					t.Fatalf("wrong frozen FiendWar return origin: %+v", origin)
				}
				frames := peer.conn.(*partyDungeonSyncConn).frames()
				for _, op := range []uint16{28, 29} {
					if n := countUpperFramesByMsgID(frames, op); n != 1 {
						t.Fatalf("char%d entry op%d count=%d, want one completed entry", peer.selectedCharacterID, op, n)
					}
				}
				persisted, found, err := f.repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
				if err != nil || !found || persisted.Stats["area_id"] != source {
					t.Fatalf("battlefield projection rewrote persisted location: found=%t err=%v stats=%v", found, err, persisted.Stats)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic same-source entry did not commit char(s)=%v target=%+v dungeons=%+v member_areas=%v member_groups=%v member_accounts=%v\n%s\n%s",
				missing, b.FiendWar.Areas[area], b.Dungeons, b.FiendWar.MemberAreas, b.MemberGroups, b.MemberAccounts, r195RelevantLogs(logs.String()), r195RelevantLogs(f.logText(t)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func r195FailTownMoveFixture(
	t *testing.T,
	f *raidStartGuardFixture,
	logs *r195LogBuffer,
	label string,
	peer *gameSession,
	request currentTownSetUserAreaRequest,
	preflight *currentFiendWarMovePlan,
	preflightErr error,
) {
	t.Helper()
	t.Fatalf("%s; char_id=%d prepare_plan=%+v prepare_error=%v request=%+v packet_log=%s\n%s",
		label, peer.selectedCharacterID, preflight, preflightErr, request, f.logText(t), r195RelevantLogs(logs.String()))
}

func r195RelevantLogs(raw string) string {
	const maxLines = 200
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, maxLines)
	for _, line := range lines {
		if !strings.Contains(line, "fiendwar") &&
			!strings.Contains(line, "dungeon-select") &&
			!strings.Contains(line, "party-dungeon-entry") &&
			!strings.Contains(line, "超时空区域遭遇战") {
			continue
		}
		kept = append(kept, line)
		if len(kept) == maxLines {
			kept = append(kept, "... log capture truncated")
			break
		}
	}
	if len(kept) == 0 {
		return "fixture logs: no matching fixture/admission lines"
	}
	return "fixture logs:\n" + strings.Join(kept, "\n")
}
