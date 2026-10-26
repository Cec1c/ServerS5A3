package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// r195ClosedEnergyReturnFixture keeps the end-of-room flags and settlement
// presentation as explicit fixtures. The dungeon/map graph, ownership binding,
// participation capture, clear reducer, op72 handler and exit release all use
// their real production paths.
type r195ClosedEnergyReturnFixture struct {
	service *Service
	repos   repo.Group
	session *gameSession
	runtime *runtimeDungeonState
	conn    *bufferConn
	runID   string
	before  r195ClosedEnergyProgressSnapshot
	assets  r195ClosedEnergyAssetSnapshot
}

type r195ClosedEnergyProgressSnapshot struct {
	ClearedRuns      map[string]currentRaidClearedRun
	MemberClears     map[uint16]uint32
	Dungeons         []raid.DungeonState
	PhaseCompleted   bool
	PhaseCompletedAt time.Time
	RewardFlow       [2]*currentAntonRewardFlow
	PhaseCoins       [2]uint32
	RunCoins         map[string]uint32
	DomainClearCount uint32
	AntonSymbols     map[string]uint32
	AntonTimers      map[raid.AntonTimerKey]raid.AntonTimer
}

type r195ClosedEnergyAssetSnapshot struct {
	Inventory      repo.InventoryRecord
	InventoryFound bool
	Equipment      repo.EquipmentRecord
	EquipmentFound bool
}

func r195SnapshotClosedEnergyProgress(b *runtimeRaidBattle) r195ClosedEnergyProgressSnapshot {
	snapshot := r195ClosedEnergyProgressSnapshot{
		ClearedRuns:      make(map[string]currentRaidClearedRun, len(b.ClearedRuns)),
		MemberClears:     make(map[uint16]uint32, len(b.MemberClears)),
		Dungeons:         append([]raid.DungeonState(nil), b.Dungeons...),
		PhaseCompleted:   b.PhaseCompleted,
		PhaseCompletedAt: b.PhaseCompletedAt,
		RewardFlow:       b.RewardFlow,
		PhaseCoins:       b.PhaseCoins,
		RunCoins:         make(map[string]uint32, len(b.RunCoins)),
	}
	for run, entry := range b.ClearedRuns {
		entry.Participants = append([]uint16(nil), entry.Participants...)
		snapshot.ClearedRuns[run] = entry
	}
	for id, clears := range b.MemberClears {
		snapshot.MemberClears[id] = clears
	}
	for run, coins := range b.RunCoins {
		snapshot.RunCoins[run] = coins
	}
	if b.Anton != nil {
		snapshot.DomainClearCount = b.Anton.ClearCount(5063)
		snapshot.AntonSymbols = make(map[string]uint32)
		for name, value := range b.Anton.Symbols() {
			snapshot.AntonSymbols[name] = value
		}
		snapshot.AntonTimers = make(map[raid.AntonTimerKey]raid.AntonTimer)
		for key, timer := range b.Anton.Timers() {
			snapshot.AntonTimers[key] = timer
		}
	}
	return snapshot
}

func r195LoadClosedEnergyAssets(t *testing.T, repositories repo.Group) r195ClosedEnergyAssetSnapshot {
	t.Helper()
	if repositories.Inventory == nil || repositories.Equipment == nil {
		t.Fatal("real inventory/equipment repositories are unavailable")
	}
	ctx := context.Background()
	inventory, inventoryFound, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load real inventory: %v", err)
	}
	equipment, equipmentFound, err := repositories.Equipment.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load real equipment: %v", err)
	}
	return r195ClosedEnergyAssetSnapshot{
		Inventory:      repo.CloneInventory(inventory),
		InventoryFound: inventoryFound,
		Equipment:      repo.CloneEquipment(equipment),
		EquipmentFound: equipmentFound,
	}
}

func r195ClosedEnergyDungeonState(state runtimeRaidState, dungeonID uint32) (byte, bool) {
	if state.Battle == nil {
		return 0, false
	}
	for _, dungeon := range state.Battle.Dungeons {
		if dungeon.ID == dungeonID {
			return dungeon.State, true
		}
	}
	return 0, false
}

func r195AdvanceLukeEnergyControlClosed(t *testing.T, fixture *raidStartGuardFixture, at time.Time) {
	t.Helper()
	b := fixture.state.Battle
	if b == nil || b.Anton == nil {
		t.Fatal("real Luke battle graph is missing")
	}
	if state, found := r195ClosedEnergyDungeonState(*fixture.state, 5063); !found || state != 0 {
		t.Fatalf("real PVF fixture did not open energy control: state=%d found=%t", state, found)
	}
	if state, found := r195ClosedEnergyDungeonState(*fixture.state, 5313); !found || state == 0 {
		t.Fatalf("fixture did not advance the 5313 altar leg: state=%d found=%t", state, found)
	}

	progress := &raid.LukeProgression{AntonProgression: b.Anton}
	for index, leg := range []struct {
		id  uint32
		run string
	}{
		{5057, "r195-closed-energy-5057"},
		{5058, "r195-closed-energy-5058"},
		{5059, "r195-closed-energy-5059"},
		{5060, "r195-closed-energy-5060"},
	} {
		enterAt := at.Add(time.Duration(index*3+1) * time.Second)
		next, _, err := progress.EnterDungeon(leg.run, leg.id, progress.Epoch(), enterAt)
		if err != nil {
			t.Fatalf("real domain enter %d: %v", leg.id, err)
		}
		progress = next
		next, _, err = progress.ClearDungeon(leg.run, leg.id, progress.Epoch(), enterAt.Add(time.Second))
		if err != nil {
			t.Fatalf("real domain clear %d: %v", leg.id, err)
		}
		progress = next
	}
	b.Anton, b.Dungeons = progress.AntonProgression, progress.Dungeons()

	for _, dungeonID := range []uint32{5057, 5058, 5059, 5060, 5063} {
		if state, found := r195ClosedEnergyDungeonState(*fixture.state, dungeonID); !found || state == 0 {
			t.Fatalf("real graph did not close %d after the altar chain: state=%d found=%t", dungeonID, state, found)
		}
	}
}

func r195CompleteLukeEnergyLocally(t *testing.T, runtime *runtimeDungeonState) {
	t.Helper()
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("complete local real-PVF room: %v", err)
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		if err := runtime.Session.Complete(); err != nil {
			t.Fatalf("complete local real-PVF run: %v", err)
		}
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("local room completion did not produce a completed run")
	}
	// These are the boss/settlement gate observations normally produced by the
	// room event sequence. They are deliberately fixture-only in this test.
	runtime.bossDieCheckAccepted = true
	runtime.bossDieCheckResponseSent = true
	runtime.settlementEntrySent = true
}

func newR195ClosedEnergyReturnFixture(t *testing.T, recordParticipation bool) *r195ClosedEnergyReturnFixture {
	t.Helper()
	f, session, runtime, _, at := reviewActualLukeCoinFixtureForDungeon(t, false, 5063)
	if f == nil || session == nil || runtime == nil || runtime.raidEntry == nil {
		t.Fatal("real Luke energy fixture incomplete")
	}
	conn := f.conn
	if conn == nil {
		t.Fatal("real Luke energy fixture connection is missing")
	}
	runID := runtime.raidEntry.RunID
	service := f.service
	// Coin fixtures bypass login. Complete the real missing-only initialization
	// before measuring assets so town reconstruction cannot become first login.
	if _, err := service.ensureCharacterInitializationSnapshot(context.Background(), f.repos, runtime.Character); err != nil {
		t.Fatalf("initialize existing character snapshot: %v", err)
	}
	if recordParticipation {
		service.recordCurrentRaidParticipation(session, runtime)
	} else {
		service.raidMu.Lock()
		delete(f.state.Battle.Participation, session.selectedCharacterID)
		service.raidMu.Unlock()
	}
	if !(&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(runID, 5063) {
		t.Fatal("real local energy run was not entered before global closure")
	}

	r195AdvanceLukeEnergyControlClosed(t, f, at)
	if !(&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(runID, 5063) {
		t.Fatal("global altar progression erased the old local runtime entry")
	}
	r195CompleteLukeEnergyLocally(t, runtime)
	if f.state.Battle.Phase != 1 || f.state.Battle.PhaseCompleted {
		t.Fatalf("altar closure changed the result phase: phase=%d completed=%t", f.state.Battle.Phase, f.state.Battle.PhaseCompleted)
	}

	assetsBefore := r195LoadClosedEnergyAssets(t, f.repos)
	before := r195SnapshotClosedEnergyProgress(f.state.Battle)
	_, _, err := service.reduceCurrentRaidDungeonClearLocked(session, runtime, at.Add(20*time.Second))
	if err == nil {
		t.Fatal("closed energy control was counted as a clear")
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "not open") && !strings.Contains(message, "closed") {
		t.Fatalf("closed energy reducer returned an unrelated error: %v", err)
	}
	after := r195SnapshotClosedEnergyProgress(f.state.Battle)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("closed energy reducer changed progress/reward state:\nbefore=%+v\nafter=%+v", before, after)
	}
	if _, counted := after.ClearedRuns[runID]; counted {
		t.Fatal("closed energy reducer wrote the local run into ClearedRuns")
	}
	if after.DomainClearCount != 0 {
		t.Fatalf("closed energy reducer credited domain clear count=%d", after.DomainClearCount)
	}
	if assetsAfter := r195LoadClosedEnergyAssets(t, f.repos); !reflect.DeepEqual(assetsBefore, assetsAfter) {
		t.Fatalf("closed energy reducer changed repository assets:\nbefore=%+v\nafter=%+v", assetsBefore, assetsAfter)
	}
	if session.dungeon.runtime != runtime || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("closed energy reducer detached or completed a different runtime")
	}
	if !(&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(runID, 5063) {
		t.Fatal("reducer should retain the old local entry until result exit")
	}
	conn.write.Reset()
	return &r195ClosedEnergyReturnFixture{
		service: service, repos: f.repos, session: session, runtime: runtime, conn: conn,
		runID: runID, before: before, assets: assetsBefore,
	}
}

func r195RunClosedEnergyOp72(t *testing.T, fixture *r195ClosedEnergyReturnFixture, body []byte, repeats int) []byte {
	t.Helper()
	return r195RunClosedEnergyCommand(t, fixture, uint16(dnfenum.CmdPacketEplpCommand), body, repeats)
}

func r195RunClosedEnergyCommand(t *testing.T, fixture *r195ClosedEnergyReturnFixture, opcode uint16, body []byte, repeats int) []byte {
	t.Helper()
	fixture.conn.write.Reset()
	for i := 0; i < repeats; i++ {
		if opcode == uint16(dnfenum.CmdPacketBack2Village) {
			frame, err := dnfproto.BuildChannelPacket(opcode, body, 0, dnfproto.DefaultChannelClassification)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.handleGameUpper(fixture.session, frame); err != nil {
				t.Fatalf("real upper op%d: %v", opcode, err)
			}
			continue
		}
		if err := fixture.service.handleGameCommand(
			fixture.session,
			byte(dnfenum.GameCmdCommand),
			opcode,
			body,
		); err != nil {
			t.Fatalf("real op%d %v: %v", opcode, body, err)
		}
	}
	return append([]byte(nil), fixture.conn.write.Bytes()...)
}

func r195CountClosedEnergyPackets(t *testing.T, raw []byte, msgID uint16, classification byte, body []byte) int {
	t.Helper()
	count := 0
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		if packet.Header.MsgID == msgID && packet.Header.Classification == classification &&
			(body == nil || bytes.Equal(packet.Body, body)) {
			count++
		}
	}
	return count
}

func r195AssertClosedEnergyStillOwned(t *testing.T, fixture *r195ClosedEnergyReturnFixture) {
	t.Helper()
	if fixture.session.dungeon.runtime != fixture.runtime {
		t.Fatal("negative closed-energy action detached the owned runtime")
	}
	state, found := fixture.service.runtimeRaidForMember(fixture.session.selectedCharacterID)
	if !found {
		t.Fatal("negative closed-energy action erased raid membership")
	}
	if !(&raid.LukeProgression{AntonProgression: state.Battle.Anton}).HasEnteredRun(fixture.runID, 5063) {
		t.Fatal("negative closed-energy action released the old run")
	}
}

func TestR195LukeClosedEnergyRealPVFReducerAndResultRoutes(t *testing.T) {
	t.Run("reducer_reports_closed_without_progress_or_reward", func(t *testing.T) {
		newR195ClosedEnergyReturnFixture(t, true)
	})

	t.Run("return_town_and_repeat", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		body := buildCurrentDungeonOp72SuccessBody(1, 2)
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 2)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, body); got != 1 {
			t.Fatalf("closed energy return ACK count=%d want 1", got)
		}
		if fixture.session.dungeon.runtime != nil || !fixture.runtime.townReturnOp24Sent {
			t.Fatalf("closed energy return did not commit: runtime=%p typed24=%t", fixture.session.dungeon.runtime, fixture.runtime.townReturnOp24Sent)
		}
		state, found := fixture.service.runtimeRaidForMember(fixture.session.selectedCharacterID)
		if !found {
			t.Fatal("closed energy return erased raid membership")
		}
		if (&raid.LukeProgression{AntonProgression: state.Battle.Anton}).HasEnteredRun(fixture.runID, 5063) {
			t.Fatal("closed energy return did not release HasEnteredRun")
		}
		if after := r195SnapshotClosedEnergyProgress(state.Battle); !reflect.DeepEqual(fixture.before, after) {
			t.Fatal("closed energy return changed clear or reward progress")
		}
		if after := r195LoadClosedEnergyAssets(t, fixture.repos); !reflect.DeepEqual(fixture.assets, after) {
			t.Fatalf("closed energy return changed repository assets: before=%+v after=%+v", fixture.assets, after)
		}
	})

	t.Run("select_other_and_repeat", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		body := buildCurrentDungeonOp72SuccessBody(1, 1)
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 1}, 2)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, body); got != 1 {
			t.Fatalf("closed energy select-other ACK count=%d want 1", got)
		}
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEnterSelectDungeon), 0, nil) + r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEnterSelectDungeon), 1, nil); got == 0 {
			t.Fatal("closed energy select-other emitted no native selector packet")
		}
		if fixture.session.dungeon.runtime != nil {
			t.Fatal("closed energy select-other retained the completed runtime")
		}
		state, found := fixture.service.runtimeRaidForMember(fixture.session.selectedCharacterID)
		if !found {
			t.Fatal("closed energy select-other erased raid membership")
		}
		if (&raid.LukeProgression{AntonProgression: state.Battle.Anton}).HasEnteredRun(fixture.runID, 5063) {
			t.Fatal("closed energy select-other did not release HasEnteredRun")
		}
		if after := r195SnapshotClosedEnergyProgress(state.Battle); !reflect.DeepEqual(fixture.before, after) {
			t.Fatal("closed energy select-other changed clear or reward progress")
		}
		if after := r195LoadClosedEnergyAssets(t, fixture.repos); !reflect.DeepEqual(fixture.assets, after) {
			t.Fatal("closed energy select-other changed repository assets")
		}
	})

	t.Run("system_back_to_village_op132", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		_ = r195RunClosedEnergyCommand(t, fixture, uint16(dnfenum.CmdPacketBack2Village), nil, 1)
		if fixture.session.dungeon.runtime != nil || !fixture.runtime.townReturnOp24Sent {
			t.Fatalf("op132 closed energy return did not commit: runtime=%p typed24=%t", fixture.session.dungeon.runtime, fixture.runtime.townReturnOp24Sent)
		}
		state, found := fixture.service.runtimeRaidForMember(fixture.session.selectedCharacterID)
		if !found {
			t.Fatal("op132 closed energy return erased raid membership")
		}
		if (&raid.LukeProgression{AntonProgression: state.Battle.Anton}).HasEnteredRun(fixture.runID, 5063) {
			t.Fatal("op132 closed energy return did not release HasEnteredRun")
		}
		if after := r195SnapshotClosedEnergyProgress(state.Battle); !reflect.DeepEqual(fixture.before, after) {
			t.Fatal("op132 closed energy return changed clear, domain graph, or reward progress")
		}
		if after := r195LoadClosedEnergyAssets(t, fixture.repos); !reflect.DeepEqual(fixture.assets, after) {
			t.Fatal("op132 closed energy return changed repository assets")
		}
	})

	t.Run("retry_same_closed_map_is_not_relaxed", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 0}, 2)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, []byte{1, 0}); got != 0 {
			t.Fatalf("closed energy retry was acknowledged %d times", got)
		}
		if fixture.runtime.settlementCardExitAckSent {
			t.Fatal("closed energy retry consumed the exit ACK gate")
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})
}

func TestR195LukeClosedEnergyRealPVFDisconnectRelease(t *testing.T) {
	fixture := newR195ClosedEnergyReturnFixture(t, true)
	fixture.service.releaseCurrentRaidParticipationSession(fixture.session)
	fixture.service.releaseCurrentRaidParticipationSession(fixture.session)

	state, found := fixture.service.runtimeRaidForMember(fixture.session.selectedCharacterID)
	if !found {
		t.Fatal("disconnect release erased raid membership")
	}
	if _, present := state.Battle.Participation[fixture.session.selectedCharacterID]; present {
		t.Fatal("disconnect release retained participation")
	}
	if (&raid.LukeProgression{AntonProgression: state.Battle.Anton}).HasEnteredRun(fixture.runID, 5063) {
		t.Fatal("single-member disconnect did not release HasEnteredRun")
	}
	if after := r195SnapshotClosedEnergyProgress(state.Battle); !reflect.DeepEqual(fixture.before, after) {
		t.Fatal("disconnect release changed clear, domain graph, or reward progress")
	}
	if after := r195LoadClosedEnergyAssets(t, fixture.repos); !reflect.DeepEqual(fixture.assets, after) {
		t.Fatal("disconnect release changed repository assets")
	}
}

func TestR195LukeClosedEnergyRealPVFDualMemberLastLeaveReleasesRun(t *testing.T) {
	f, local, localRuntime, peer, at := reviewActualLukeCoinFixtureForDungeon(t, true, 5063)
	if f == nil || local == nil || localRuntime == nil || peer == nil || peer.dungeon.runtime == nil || localRuntime.raidEntry == nil {
		t.Fatal("real dual-member Luke energy fixture incomplete")
	}
	peerRuntime := peer.dungeon.runtime
	if len(localRuntime.raidEntry.Participants) != 2 {
		t.Fatalf("frozen participants=%v want two members", localRuntime.raidEntry.Participants)
	}
	service := f.service
	service.recordCurrentRaidParticipation(local, localRuntime)
	service.recordCurrentRaidParticipation(peer, peerRuntime)
	r195AdvanceLukeEnergyControlClosed(t, f, at)
	r195CompleteLukeEnergyLocally(t, localRuntime)
	r195CompleteLukeEnergyLocally(t, peerRuntime)

	assetsBefore := r195LoadClosedEnergyAssets(t, f.repos)
	before := r195SnapshotClosedEnergyProgress(f.state.Battle)
	for _, member := range []struct {
		session *gameSession
		runtime *runtimeDungeonState
	}{{local, localRuntime}, {peer, peerRuntime}} {
		if _, _, err := service.reduceCurrentRaidDungeonClearLocked(member.session, member.runtime, at.Add(20*time.Second)); err == nil {
			t.Fatalf("closed dual energy map accepted a clear for %d", member.session.selectedCharacterID)
		}
	}
	if after := r195SnapshotClosedEnergyProgress(f.state.Battle); !reflect.DeepEqual(before, after) {
		t.Fatal("dual closed energy reducer changed clear, domain graph, or reward progress")
	}
	if after := r195LoadClosedEnergyAssets(t, f.repos); !reflect.DeepEqual(assetsBefore, after) {
		t.Fatal("dual closed energy reducer changed repository assets")
	}
	if !(&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(localRuntime.raidEntry.RunID, 5063) {
		t.Fatal("dual closed energy run was not still entered before departures")
	}

	service.releaseCurrentRaidParticipationSession(local)
	if !(&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(localRuntime.raidEntry.RunID, 5063) {
		t.Fatal("first member release cleared a dual-member run")
	}
	if _, present := f.state.Battle.Participation[peer.selectedCharacterID]; !present {
		t.Fatal("first member release removed the remaining participant")
	}
	service.releaseCurrentRaidParticipationSession(peer)
	if (&raid.LukeProgression{AntonProgression: f.state.Battle.Anton}).HasEnteredRun(localRuntime.raidEntry.RunID, 5063) {
		t.Fatal("last member release did not clear HasEnteredRun")
	}
	if len(f.state.Battle.Participation) != 0 {
		t.Fatalf("dual closed energy participation remains: %+v", f.state.Battle.Participation)
	}
	if after := r195SnapshotClosedEnergyProgress(f.state.Battle); !reflect.DeepEqual(before, after) {
		t.Fatal("last-member release changed clear, domain graph, or reward progress")
	}
	if after := r195LoadClosedEnergyAssets(t, f.repos); !reflect.DeepEqual(assetsBefore, after) {
		t.Fatal("last-member release changed repository assets")
	}
}

func TestR195LukeClosedEnergyRealPVFOwnershipNegatives(t *testing.T) {
	t.Run("retired_socket", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		replacementConn := &bufferConn{}
		replacementSession := &gameSession{
			conn:                replacementConn,
			connID:              "r195-closed-energy-replacement",
			channel:             fixture.session.channel,
			accountID:           fixture.session.accountID,
			selectedCharacterID: fixture.session.selectedCharacterID,
			loginPacketSeen:     true,
			accountFromToken:    true,
		}
		fixture.service.bindGameSessionCharacter(replacementSession, fixture.session.selectedCharacterID)
		fixture.service.promoteResidentGameSession(replacementSession, fixture.session.selectedCharacterID)
		if fixture.service.currentRaidAuthenticated(fixture.session) || !fixture.service.currentRaidAuthenticated(replacementSession) {
			t.Fatal("replacement did not become the sole authenticated owner")
		}
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 1)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, nil); got != 0 {
			t.Fatalf("retired socket received %d op72 ACKs", got)
		}
		if replacementConn.write.Len() != 0 {
			t.Fatal("retired socket action emitted on the replacement transport")
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})

	t.Run("wrong_account", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		fixture.runtime.AccountID = "foreign-account"
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 1)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, nil); got != 0 {
			t.Fatalf("wrong-account closed energy received %d op72 ACKs", got)
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})

	t.Run("wrong_epoch", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		fixture.runtime.raidEntry.Attempt++
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 1)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, nil); got != 0 {
			t.Fatalf("wrong-epoch closed energy received %d op72 ACKs", got)
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})

	t.Run("wrong_generation", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, true)
		fixture.session.characterGeneration++
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 1)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, nil); got != 0 {
			t.Fatalf("wrong-generation closed energy received %d op72 ACKs", got)
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})

	t.Run("missing_recorded_participation", func(t *testing.T) {
		fixture := newR195ClosedEnergyReturnFixture(t, false)
		raw := r195RunClosedEnergyOp72(t, fixture, []byte{1, 2}, 1)
		if got := r195CountClosedEnergyPackets(t, raw, uint16(dnfenum.CmdPacketEplpCommand), currentDungeonCardResponseClass, nil); got != 0 {
			t.Fatalf("unrecorded closed energy received %d op72 ACKs", got)
		}
		r195AssertClosedEnergyStillOwned(t, fixture)
	})
}
