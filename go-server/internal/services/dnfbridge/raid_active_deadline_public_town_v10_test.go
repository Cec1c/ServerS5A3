package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Network-only fixture. No production reducer, owner, fee or scene helper is stubbed.
type deadlineAuditV10Conn struct {
	bufferConn
	mu     sync.Mutex
	fail24 bool
}

func (c *deadlineAuditV10Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail24 && len(p) >= 3 && binary.LittleEndian.Uint16(p[1:3]) == 24 {
		return 0, errors.New("audit typed24 write failure")
	}
	return c.bufferConn.Write(p)
}
func (c *deadlineAuditV10Conn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.write.Bytes()...)
}
func (c *deadlineAuditV10Conn) reset()             { c.mu.Lock(); defer c.mu.Unlock(); c.write.Reset() }
func (c *deadlineAuditV10Conn) failTyped24(v bool) { c.mu.Lock(); defer c.mu.Unlock(); c.fail24 = v }

// Lobby membership, persisted characters/materials and their town position are explicit
// in-memory fixtures. Battle admission/config/charge are ONLY normal public666; phase,
// epoch, RunID and accepted clear state are never directly assigned by this test.
func deadlineAuditV10Admit(t *testing.T, a *platformpvf.Archive, solo bool) (*raidStartGuardFixture, []*gameSession, []*deadlineAuditV10Conn, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	rule, err := raid.LoadAdmissionRule(a, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	n := rule.MinimumMembers
	if solo {
		n = 1
	}
	if n < 1 || n > 20 {
		t.Fatalf("actual member minimum unsupported by bounded fixture: %d", n)
	}
	f := newRaidStartGuardFixture(t, a, map[string]repo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count * 3}})
	t.Cleanup(func() { _ = f.logger.Close() })
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	// Exact observed r93 process startup env default: JOUST_EVENT_ENABLED=0.
	// optionsFromConfig stores its negation. No persisted activity2365 override
	// was queried; this fixture explicitly scopes the startup default only.
	// Actual enabled-Joust resource mismatch remains frozen in V6 evidence.
	s.options.joustEventDisabled = true
	s.onlinePlayers = newOnlinePlayerManager()
	// Match the normal connection cancellation lifetime; no packet-wide 3s budget.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Exact Service.Start loader order, before binding or incoming character packets.
	preloads := []struct {
		name string
		load func(context.Context) error
	}{
		{"initial equipment index", s.preloadInitialEquipmentIndex},
		{"item catalog", s.preloadPVFItemCatalog},
		{"expert job catalog", s.preloadExpertJobCatalog},
		{"adventure group table", s.preloadAdventureGroupTable},
		{"honor table", s.preloadHonorTable},
		{"town catalog", s.preloadTownCatalog},
		{"skill catalog", s.preloadSkillCatalog},
		{"profession profiles", s.preloadProfessionProfiles},
		{"quest catalog", s.preloadQuestCatalog},
		{"dungeon world map", s.preloadDungeonWorldMap},
		{"character stat table", s.preloadCharacterStatTable},
		{"equipment stat index", s.preloadEquipmentStatIndex},
		{"combat power affix catalog", s.preloadCombatPowerAffixCatalog},
	}
	for _, preload := range preloads {
		if err := preload.load(ctx); err != nil {
			t.Fatalf("exact production preload %s: %v", preload.name, err)
		}
	}
	// Deterministic valid resource choices are a test fixture, not a new daily policy.
	s.dungeonChoice = func(limit int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	f.state.CurrentLayout = true
	f.state.TailFlag = 0
	f.state.CurrentUIFlags = [3]byte{}
	if solo {
		f.state.TailFlag = 1
		f.state.CurrentUIFlags[0] = 1
	}
	f.state.Members = nil
	sessions := make([]*gameSession, 0, n)
	conns := make([]*deadlineAuditV10Conn, 0, n)
	for i := 0; i < n; i++ {
		id := uint16(19 + i)
		account := "account-1"
		if i > 0 {
			account = "account-" + strconv.Itoa(int(id))
		}
		group, slot := byte(1), byte(i)
		if i >= 2 {
			group = byte(2 + (i-2)/4)
			slot = byte((i - 2) % 4)
		}
		f.state.Members = append(f.state.Members, runtimeRaidMemberState{CharID: id, GroupIndex: group, SlotOrder: slot})
		// Same persisted town fixture used by the actual public sanctum regression.
		ch := repo.CharacterRecord{CharacterID: strconv.Itoa(int(id)), AccountID: account, Level: 90, Stats: map[string]int64{"fatigue": 156, "town_id": 50, "area_id": 0, "pos_x": 580, "pos_y": 243, "direction": 0, "area_state": 0}}
		if err = f.repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err = f.repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]repo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count * 3}}}); err != nil {
			t.Fatal(err)
		}
		c := &deadlineAuditV10Conn{}
		p := f.session()
		p.conn = c
		// handleGameConn installs this exact live connection context on construction.
		p.ctx = ctx
		p.connID = fmt.Sprintf("deadline-audit-%d", id)
		p.accountID = account
		p.loginPacketSeen = true
		p.accountFromToken = true
		s.bindGameSessionCharacter(p, id)
		if err = s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, p)
		conns = append(conns, c)
	}
	// Cleanup LIFO: stop below runs before this exact logger/owner capture.
	t.Cleanup(func() { deadlineAuditV10SaveDiagnostics(t, f, sessions, conns) })
	t.Cleanup(func() {
		for _, p := range sessions {
			stopctx, stop := context.WithTimeout(context.Background(), createWriteTimeout)
			if err := s.stopGameSessionEvents(stopctx, p); err != nil {
				t.Logf("deadline diagnostic stop: %v", err)
			}
			stop()
		}
	})
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = time.Now()
	s.gameplayTimers = q
	// V6 separate fixture: use actual normal op4 selection and op143 progress36.
	// Level90 without tutorial marker follows the existing legacy-established
	// character policy; no tutorial-completed/ready/presence flags are assigned.
	// Existing per-account records have Slot0; the real resolver owns selection.
	for _, peer := range sessions {
		deadlineAuditV10NormalTown(t, f, peer)
	}
	if !solo {
		if !s.onlinePlayers.PeerInSameArea(sessions[0].selectedCharacterID, sessions[1].selectedCharacterID) ||
			!s.onlinePlayers.PeerInSameArea(sessions[1].selectedCharacterID, sessions[0].selectedCharacterID) {
			t.Fatal("normal selected-town/op36 did not establish mutual shared waiting-area presence")
		}
	}
	deadlineAuditV10Event(t, s, sessions[0], func() error { return s.handleOnlineStartRaid(sessions[0], 666, nil) })
	if !f.state.Started || f.state.Battle == nil || f.state.Battle.TimedOut || f.state.Battle.PhaseCompleted {
		t.Fatal("public666 did not admit actual battle")
	}
	if f.state.Battle.Single != solo {
		t.Fatal("actual admission selected wrong mode")
	}
	for _, p := range sessions {
		bag, ok, e := f.repos.Inventory.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		if e != nil || !ok || bag.Slots["0:50"].Count != rule.Count*2 {
			t.Fatalf("public admission fee not exact: %d %+v %v", p.selectedCharacterID, bag, e)
		}
	}
	// Entry currently samples time.Now, not gameplay queue. Honor the real configured
	// 3s delay instead of changing EnterAfter/phase eligibility or faking an old battle.
	delay := time.Until(f.state.Battle.EnterAfter)
	if delay > 5*time.Second {
		t.Fatalf("actual start delay exceeds narrow fixture allowance: %v", delay)
	}
	if delay > 0 {
		time.Sleep(delay + time.Millisecond)
	}
	q.mu.Lock()
	q.now = time.Now()
	q.mu.Unlock()
	return f, sessions, conns, q
}

// Diagnostics only: read each mutable owner under its existing lock. A failed stop
// does not justify an unlocked peek; no eligibility or production state is changed.
// Exact packet log is closed before copying, with no arbitrary repository dump.
func deadlineAuditV10SaveDiagnostics(t *testing.T, f *raidStartGuardFixture, peers []*gameSession, conns []*deadlineAuditV10Conn) {
	t.Helper()
	out := os.Getenv("DNFBRIDGE_DEADLINE_AUDIT_OUTPUT")
	if out == "" {
		t.Log("deadline diagnostics output directory absent; no persistence attempted")
		return
	}
	if err := f.logger.Close(); err != nil {
		t.Logf("deadline diagnostic logger close: %v", err)
		return
	}
	name := strings.ReplaceAll(t.Name(), "/", "_")
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Logf("deadline diagnostic directory: %v", err)
		return
	}
	if raw, err := os.ReadFile(f.logPath); err == nil {
		if err = os.WriteFile(filepath.Join(out, name+".packet.log"), raw, 0600); err != nil {
			t.Logf("deadline diagnostic log save: %v", err)
		}
	} else {
		t.Logf("deadline diagnostic log read: %v", err)
	}
	s := f.service
	d := map[string]any{"test": t.Name(), "snapshot_after_stop_attempt_and_logger_close": true}
	s.raidMu.Lock()
	d["started"] = f.state.Started
	if b := f.state.Battle; b != nil {
		d["battle"] = map[string]any{"instance": b.InstanceID, "phase": b.Phase, "timeout": b.TimedOut, "phase_completed": b.PhaseCompleted, "participation_count": len(b.Participation), "accepted_runs": len(b.ClearedRuns)}
	}
	s.raidMu.Unlock()
	owners := make([]map[string]any, 0, len(peers))
	for i, peer := range peers {
		identity, canonical := s.boundGameSessionCharacterSnapshot(peer)
		row := map[string]any{"character": identity.character, "generation": identity.generation, "canonical": canonical}
		peer.dungeon.mu.Lock()
		if r := peer.dungeon.runtime; r != nil {
			row["runtime"] = true
			row["dungeon"] = r.Dungeon.ID
			row["town_return_pending"] = r.townReturnPending
			row["town_return_24_sent"] = r.townReturnOp24Sent
			if r.Session != nil {
				snap := r.Session.Snapshot()
				if r.raidEntry != nil {
					row["run_id"] = r.raidEntry.RunID
				}
				row["run_status"] = snap.Run.Status
			}
		} else {
			row["runtime"] = false
		}
		peer.dungeon.mu.Unlock()
		peer.pendingPostStartMapMu.Lock()
		row["pending_placement"] = peer.pendingPostStartMapPlacement != nil
		peer.pendingPostStartMapMu.Unlock()
		peer.raidProjection.mu.Lock()
		row["projection_initialized"] = peer.raidProjection.initialized
		row["projection_failed"] = peer.raidProjection.failed
		row["start_transition_sent"] = peer.raidProjection.startTransitionSent
		peer.raidProjection.mu.Unlock()
		wire := conns[i].bytes()
		row["wire_bytes"] = len(wire)
		headers := make([]map[string]any, 0)
		for len(wire) >= 13 {
			n := int(binary.LittleEndian.Uint32(wire[3:7]))
			if n < 13 || n > len(wire) {
				row["partial_envelope"] = true
				break
			}
			op := binary.LittleEndian.Uint16(wire[1:3])
			if op == 666 || op == 16 || op == 24 || op == 601 || op == 587 {
				headers = append(headers, map[string]any{"class": wire[0], "opcode": op, "length": n})
			}
			wire = wire[n:]
		}
		row["whitelisted_headers"] = headers
		owners = append(owners, row)
	}
	d["owners"] = owners
	if raw, err := json.MarshalIndent(d, "", "  "); err == nil {
		if err = os.WriteFile(filepath.Join(out, name+".owner.json"), raw, 0600); err != nil {
			t.Logf("deadline diagnostic owner save: %v", err)
		}
	} else {
		t.Logf("deadline diagnostic owner marshal: %v", err)
	}
}

// Only normal source-proven public request producers establish the scene.
// Actual archive town19/area2 movable rectangle supplies a reported-position fixture.
// This is a server public-method composition; no TCP/physical-client claim.
func deadlineAuditV10NormalTown(t *testing.T, f *raidStartGuardFixture, p *gameSession) {
	t.Helper()
	s := f.service
	want := p.selectedCharacterID
	c, found, err := f.repos.Character.Load(p.ctx, strconv.Itoa(int(want)))
	if err != nil || !found {
		t.Fatalf("normal selection persisted character absent: found=%v err=%v", found, err)
	}
	if c.Level != 90 || selectedCharacterStartsInTutorial(c, true) {
		t.Fatal("existing level90/no-marker tutorial policy did not select normal town")
	}
	// Exact current plain op4 slot0 body, per game_select_request_parse.go63.
	deadlineAuditV10Event(t, s, p, func() error { return s.sendUpperCSharpSelectInit(p, make([]byte, 16)) })
	if p.selectedCharacterID != want {
		t.Fatalf("real op4 selected foreign character: got=%d want=%d", p.selectedCharacterID, want)
	}
	progress := make([]byte, 6)
	progress[0] = currentDungeonTutorialFinalPrefix
	binary.LittleEndian.PutUint32(progress[1:5], currentInitialTownProgress)
	progress[5] = currentDungeonTutorialFinalCommit
	deadlineAuditV10Event(t, s, p, func() error { return s.handleDungeonTutorialFlag(p, progress) })
	p.townMu.Lock()
	ready := p.townSceneReadyCharacterID == want
	landing := p.townPositionSnapshot
	p.townMu.Unlock()
	if !ready || landing.CharacterID != want || landing.TownID != 19 {
		t.Fatalf("normal selected-town route not committed: ready=%v landing=%+v", ready, landing)
	}
	area, ok := s.townArea(19, 2)
	if !ok || !s.currentAntonWaitingArea(19, 2) {
		t.Fatal("actual Anton waiting area/dungeon-gate contract absent")
	}
	// Area2 is `[dungeon gate]27`, not an ordinary `[gate]x y` landing.
	// This is an explicit reported-position fixture from the actual map's first
	// virtual movable rectangle, NOT official spawn or physical portal evidence.
	mapDoc, mapFound := currentTownAreaMapDocument(s.initialEquipmentArchive, area)
	if !mapFound || len(mapDoc.VirtualMovableArea) < 4 ||
		!reflect.DeepEqual(mapDoc.VirtualMovableArea[:4], []int64{20, 298, 1300, 140}) {
		t.Fatal("actual369d waiting-area movable-region contract absent or changed")
	}
	rect := mapDoc.VirtualMovableArea[:4]
	x, y := rect[0]+rect[2]/2, rect[1]+rect[3]/2
	if rect[2] <= 0 || rect[3] <= 0 || x < 0 || x > 32767 || y < 0 || y > 32767 {
		t.Fatal("actual movable-region midpoint cannot fit current op36 coordinate fields")
	}
	// Same-town op36 is the production public area request; lobby membership
	// already exists and production scope checking remains enabled.
	areaBody := make([]byte, currentTownSetUserAreaBodySize)
	areaBody[0], areaBody[1] = 19, 2
	binary.LittleEndian.PutUint16(areaBody[2:4], uint16(x))
	binary.LittleEndian.PutUint16(areaBody[4:6], uint16(y))
	deadlineAuditV10Event(t, s, p, func() error { return s.handleTownSetUserArea(p, areaBody) })
	// Actual op36 establishes a new scene owner with PositionValid=false.
	// Normal op35 is the client-reported position owner (town_position.go);
	// retain opaque trailing fields as zero, without assigning ready/presence.
	positionBody := make([]byte, currentTownSetUserPositionBodySize)
	binary.LittleEndian.PutUint16(positionBody[0:2], uint16(x))
	binary.LittleEndian.PutUint16(positionBody[2:4], uint16(y))
	deadlineAuditV10Event(t, s, p, func() error { return s.handleTownSetUserPosition(p, positionBody) })
	if ready, reason := s.currentTownEnterSelectReady(p); !ready {
		t.Fatalf("normal op36 did not own selector readiness: %s", reason)
	}
	info, present := s.onlinePlayers.PlayerForCharacter(want)
	if !present || info.Session != p || info.TownID != 19 || info.AreaID != 2 ||
		info.ChannelID != currentTownPresenceChannelID(p) || info.Instance != f.state.RaidKey {
		t.Fatalf("normal op36 exact lobby presence missing: present=%v info=%+v", present, info)
	}
}

func deadlineAuditV10Event(t *testing.T, s *Service, p *gameSession, fn func() error) {
	t.Helper()
	ctx := p.ctx
	if ctx == nil {
		t.Fatal("public connection context missing")
	}
	if err := s.callGameSession(ctx, p, "deadline-audit-public", fn); err != nil {
		t.Fatal(err)
	}
}
func deadlineAuditV10Wait(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState, sent bool) {
	t.Helper()
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		p.dungeon.mu.Lock()
		got := r.townReturnOp24Sent
		p.dungeon.mu.Unlock()
		s.raidMu.Lock()
		queued := s.currentRaidDeadlineQueued[r]
		s.raidMu.Unlock()
		if !queued && got == sent {
			deadlineAuditV10Event(t, s, p, func() error { return nil })
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("bounded deadline worker did not finish: wanted24=%v", sent)
}

// This top level opens/loads actual369d once. All subcases then use independent
// real admission owners/repos/session eventloops; no PVF mutation or fake clear.
func TestRealPVFRaidDeadlineActivePublicLoadedTownChain(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	a := catalog.source.(*platformpvf.Archive)
	for _, kind := range []string{"solo", "party_leader_scene_first", "party_member_scene_first", "typed24_write_failure", "explicit_active_leader_baseline"} {
		t.Run(kind, func(t *testing.T) {
			solo := kind == "solo" || kind == "typed24_write_failure"
			f, owners, conns, q := deadlineAuditV10Admit(t, a, solo)
			s := f.service
			b := f.state.Battle
			target := uint32(210)
			if solo {
				if len(b.SingleSequence) == 0 {
					t.Fatal("actual single catalog empty")
				}
				target = b.SingleSequence[0]
			}
			request := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
			deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonSelectUpper(owners[0], request) })
			active := 1
			if !solo {
				active = 2
			}
			runs := make([]*runtimeDungeonState, active)
			for i := 0; i < active; i++ {
				p := owners[i]
				r := p.dungeon.runtime
				if r == nil || r.raidEntry == nil || r.Dungeon.ID != int64(target) {
					t.Fatalf("public op16 follower/runtime not installed: %d", i)
				}
				runs[i] = r
				deadlineAuditV10Event(t, s, p, func() error { return s.sendFinishLoadingStatus(p, make([]byte, 8)) })
			}
			// Last op37 schedules the actual loading-release goroutine; its own
			// request returning is not proof that peer eventloop callbacks finished.
			// Observe exact production journal owners under raidMu, within the
			// existing worker3s budget. No flush/extra request/manual qualification.
			placementUntil := time.Now().Add(createWriteTimeout)
			for time.Now().Before(placementUntil) {
				ready := true
				s.raidMu.Lock()
				for i := 0; i < active; i++ {
					e := b.Participation[owners[i].selectedCharacterID]
					if e.Runtime != runs[i] || e.Session != owners[i] {
						ready = false
					}
				}
				s.raidMu.Unlock()
				if ready {
					break
				}
				time.Sleep(time.Millisecond)
			}
			for i := 0; i < active; i++ {
				s.raidMu.Lock()
				e := b.Participation[owners[i].selectedCharacterID]
				s.raidMu.Unlock()
				if e.Runtime != runs[i] || e.Session != owners[i] {
					t.Fatalf("actual placement failed Participation owner %d; no manual qualification permitted", i)
				}
			}
			party, _ := s.runtimePartyManagerForService().SnapshotByUser(owners[0].selectedCharacterID, 0)
			beforeInstance := runs[0].partyInstance
			for i := 0; i < active; i++ {
				conns[i].reset()
			}
			if kind == "explicit_active_leader_baseline" {
				deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonGiveupGame(owners[0], nil) })
			} else {
				if kind == "typed24_write_failure" {
					conns[0].failTyped24(true)
				}
				// Advance only the existing gameplay clock to the admitted actual deadline.
				// The production advance reducer sets TimedOut/gates; no direct field mutation.
				q.mu.Lock()
				q.now = b.Deadline
				q.mu.Unlock()
				states, errs := s.advanceCurrentRaidClocks(q.Now())
				if len(errs) != 0 || len(states) != 1 || !b.TimedOut || b.PhaseCompleted {
					t.Fatalf("real deadline reducer failed: %v %d", errs, len(states))
				}
				s.queueCurrentRaidDeadlineReturns(q.Now())
				for i := 0; i < active; i++ {
					deadlineAuditV10Wait(t, s, owners[i], runs[i], kind != "typed24_write_failure")
				}
				if kind == "typed24_write_failure" {
					if runs[0].Session.Snapshot().Run.Status != worldmap.DungeonRunActive || owners[0].dungeon.runtime != runs[0] {
						t.Fatal("failed24 abandoned run")
					}
					conns[0].failTyped24(false)
					s.queueCurrentRaidDeadlineReturns(q.Now())
					deadlineAuditV10Wait(t, s, owners[0], runs[0], true)
				}
			}
			for i := 0; i < active; i++ {
				if owners[i].dungeon.runtime != runs[i] || runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
					t.Fatal("socket24 alone abandoned/detached runtime")
				}
			}
			if kind != "explicit_active_leader_baseline" {
				for i := 0; i < active; i++ {
					stream := conns[i].bytes()
					failureAt, returnAt, index := -1, -1, 0
					for len(stream) > 0 {
						packet, rest := splitGameServerUpperPacket(t, stream)
						if packet.Header.Classification == 0 && packet.Header.MsgID == 601 && failureAt < 0 {
							failureAt = index
						}
						if packet.Header.Classification == 0 && packet.Header.MsgID == 24 && returnAt < 0 {
							returnAt = index
						}
						index++
						stream = rest
					}
					if failureAt < 0 || returnAt <= failureAt {
						t.Fatalf("failure601 must precede typed24: owner%d failure%d return%d", i, failureAt, returnAt)
					}
				}
			}
			// Every confirmation is the normal town-side op15 callback, not direct Abandon.
			order := []int{0}
			if active == 2 {
				order = []int{0, 1}
				if kind == "party_member_scene_first" {
					order = []int{1, 0}
				}
			}
			if kind == "explicit_active_leader_baseline" {
				order = []int{0}
			}
			for n, i := range order {
				deadlineAuditV10Event(t, s, owners[i], func() error { return s.sendEnterSelectDungeonState(owners[i], "upper_15", false, true) })
				if owners[i].dungeon.runtime != nil || runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
					t.Fatalf("positive scene did not retire own run %d", i)
				}
				if _, exists := b.Participation[owners[i].selectedCharacterID]; exists {
					t.Fatal("confirmed own Participation retained")
				}
				if active == 2 && n == 0 {
					other := 1 - i
					if owners[other].dungeon.runtime != runs[other] || runs[other].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
						t.Fatal("first scene changed surviving peer runtime")
					}
				}
			}
			current, exists := s.runtimePartyManagerForService().SnapshotByUser(owners[0].selectedCharacterID, 0)
			if kind == "explicit_active_leader_baseline" {
				if exists {
					t.Fatal("ordinary active leader return no longer disbands")
				}
			} else if !solo {
				if !exists || !reflect.DeepEqual(current, party) {
					t.Fatal("automatic deadline changed ordinary party owner")
				}
				if beforeInstance == nil {
					t.Fatal("two-member run had no actual shared instance")
				}
			}
			if len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 || b.PhaseCompleted {
				t.Fatal("deadline return manufactured accepted clear/reward")
			}
		})
	}
}
