package dnfbridge

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/party"
)

func newFiendWarPartyAdmissionDiagnosticFixture(t *testing.T) (*Service, *gameSession, *gameSession, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	service := &Service{
		options: options{
			currentClientVersion:     "official-24.9",
			gameUpperHeader:          gameUpperHeaderChannel13,
			gameUpperBodyCodec:       gameUpperBodyCodecPlain,
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
		},
		logger:              slog.New(slog.NewTextHandler(logs, nil)),
		runtimePartyManager: party.NewRuntimePartyManager(),
		// packetLog intentionally nil: this is the deployment shape where the
		// bounded diagnostic must remain visible.
	}
	channel := channelcatalog.Channel{ID: 244, Type: 42}
	self := &gameSession{conn: &bufferConn{}, selectedCharacterID: 26, characterGeneration: 1, channel: channel}
	peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 22, characterGeneration: 1, channel: channel}
	self.dungeon.runtime = &runtimeDungeonState{}
	peer.dungeon.runtime = &runtimeDungeonState{}
	service.gameSessions = map[uint16]*gameSession{26: self, 22: peer}
	storeRuntimePartyState(self, alignedcmd.PartyState{
		PartyID: 11, UserID: 26, IsLeader: true,
		Members: []alignedcmd.PartyMemberState{{UserID: 26, Slot: 0}},
	})
	storeRuntimePartyState(peer, alignedcmd.PartyState{
		PartyID: 12, UserID: 22, IsLeader: true,
		Members: []alignedcmd.PartyMemberState{{UserID: 22, Slot: 0}},
	})
	return service, self, peer, logs
}

func TestFiendWarPartyAdmissionDiagnosticSurvivesDisabledPacketLog(t *testing.T) {
	service, self, peer, logs := newFiendWarPartyAdmissionDiagnosticFixture(t)
	if service.packetLog != nil {
		t.Fatal("fixture must exercise the disabled packet-log path")
	}

	entry := &currentRaidDungeonEntry{
		RaidKey: 7, InstanceID: "inst-7", RunID: "run-7",
		Participants: []uint16{26, 22},
	}
	instance := &dungeonPartyInstance{id: 77}
	self.dungeon.mu.Lock()
	self.dungeon.runtime = &runtimeDungeonState{raidEntry: entry, partyInstance: instance}
	self.postStartMapPlayerStateSent = true
	self.currentFinishLoadingCompletionSent = true
	self.postFinishLoadingPlayerStateSent = true
	self.dungeon.mu.Unlock()
	peer.dungeon.mu.Lock()
	peer.dungeon.runtime = &runtimeDungeonState{raidEntry: entry, partyInstance: instance}
	peer.postStartMapPlayerStateSent = true
	peer.currentFinishLoadingCompletionSent = true
	peer.postFinishLoadingPlayerStateSent = true
	peer.dungeon.mu.Unlock()

	selfKey := dungeonPartyRunKey{
		PartyID: 11, RaidKey: 7, RaidInstanceID: "inst-7", RaidRunID: "run-7",
		DungeonID: 310, MazeIndex: 0, RoomX: 1, RoomY: 2, MapID: 36751, Seed: 9,
	}
	peerKey := selfKey
	peerKey.PartyID = 12
	self.dungeonPartyRun, self.dungeonPartyRunValid = selfKey, true
	peer.dungeonPartyRun, peer.dungeonPartyRunValid = peerKey, true

	handled, err := service.rejectCurrentFiendWarActivePartyRequest(self, 10, party.RequestPeerRequest{
		TargetID: 22, Mode: 0,
	})
	if err != nil || !handled {
		t.Fatalf("op10 diagnostic path handled=%t err=%v", handled, err)
	}
	text := logs.String()
	for _, want := range []string{
		"kind=game-fiendwar-party-admission",
		"phase=request",
		"mode=0",
		"reason=no_shared_encounter_party",
		"char_id=26",
		"peer_char_id=22",
		"party_id=11",
		"peer_party_id=12",
		"raid_key=7",
		"raid_instance_id=inst-7",
		"run_id=run-7",
		"participants=22,26",
		"run_key=true",
		"post_start=true",
		"finish=true",
		"post_finish=true",
		"ready=false",
		"peer_ready=false",
		"party_instance_id=77",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("disabled packet log omitted %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"account_id=", "conn_id=", "channel_id=", "payload=", "remote="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("admission diagnostic leaked %q:\n%s", forbidden, text)
		}
	}

	logs.Reset()
	handled, err = service.acknowledgeCurrentFiendWarPartyAccept(self, peer, 11, party.ResponsePeerRequest{
		TargetID: 22, Mode: 12, Value: 0,
	}, false)
	if err != nil || handled {
		t.Fatalf("op11 diagnostic path handled=%t err=%v", handled, err)
	}
	text = logs.String()
	for _, want := range []string{
		"phase=accept",
		"mode=12",
		"had_invite=false",
		"reason=no_existing_shared_encounter_party",
		"char_id=26",
		"peer_char_id=22",
		"run_id=run-7",
		"participants=22,26",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("op11 disabled packet log omitted %q:\n%s", want, text)
		}
	}
}

// The field that matters most for a solo runtime is the raw scene. It must not
// disappear merely because the managed-party run key is absent.
func TestFiendWarPartyAdmissionSceneKeyUsesRawSoloRuntime(t *testing.T) {
	service, leader, _, _, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)

	leader.dungeon.mu.Lock()
	runtime := leader.dungeon.runtime
	if runtime == nil || runtime.Session == nil {
		leader.dungeon.mu.Unlock()
		t.Fatal("fixture did not enter a real dungeon runtime")
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		leader.dungeon.mu.Unlock()
		t.Fatal("fixture runtime has no raw scene")
	}
	wantScene := fmt.Sprintf("%d/%d/%d/%d/%d/%d",
		runtime.Dungeon.ID, runtime.MazeIndex,
		scene.Coordinate.X, scene.Coordinate.Y,
		scene.Map.Map.ID, runtime.Seed)
	runtime.raidEntry = &currentRaidDungeonEntry{
		RaidKey: 7, InstanceID: "solo-inst", RunID: "solo-run",
		Participants: []uint16{99, 100},
	}
	runtime.partyInstance = &dungeonPartyInstance{id: 77}
	leader.postStartMapPlayerStateSent = true
	leader.currentFinishLoadingCompletionSent = true
	leader.postFinishLoadingPlayerStateSent = true
	leader.dungeon.mu.Unlock()

	// This is the solo shape: no managed-party run key.
	leader.dungeonPartyRunValid = false
	service.packetLog = nil
	logs := &bytes.Buffer{}
	service.logger = slog.New(slog.NewTextHandler(logs, nil))
	service.logFiendWarPartyAdmission("request", leader, 100, nil, 0, "solo_scene_probe")

	text := logs.String()
	for _, want := range []string{
		"kind=game-fiendwar-party-admission",
		"char_id=99",
		"peer_char_id=100",
		"scene_key=" + wantScene,
		"run_key=false",
		"post_start=true",
		"finish=true",
		"post_finish=true",
		"ready=true",
		"party_instance_id=77",
		"run_id=solo-run",
		"participants=99,100",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("solo raw-scene diagnostic omitted %q:\n%s", want, text)
		}
	}
}

func TestFiendWarPartyAdmissionDiagnosticDropsUnboundedFields(t *testing.T) {
	service, _, _, logs := newFiendWarPartyAdmissionDiagnosticFixture(t)
	service.logPacketEvent(fiendWarPartyAdmissionDiagnosticKind,
		"phase", "request",
		"char_id", 26,
		"account_id", "dnf:secret",
		"conn_id", "game-secret",
		"remote", "10.0.0.1:1234",
		"body", []byte{0xde, 0xad},
		"error", "secret transport error",
	)
	text := logs.String()
	if !strings.Contains(text, "char_id=26") {
		t.Fatalf("allowlisted admission field missing:\n%s", text)
	}
	for _, forbidden := range []string{"dnf:secret", "game-secret", "10.0.0.1:1234", "0xde", "transport error"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unbounded field %q reached the limited log:\n%s", forbidden, text)
		}
	}
}

func TestFiendWarPartyAdmissionDiagnosticAddsNoOrdinaryDungeonNoise(t *testing.T) {
	service, _, _, logs := newFiendWarPartyAdmissionDiagnosticFixture(t)
	ordinary := &gameSession{
		conn:                &bufferConn{},
		selectedCharacterID: 30,
		characterGeneration: 1,
		channel:             channelcatalog.Channel{ID: 244, Type: 1},
	}
	ordinary.dungeon.runtime = &runtimeDungeonState{}
	service.gameSessions[30] = ordinary

	handled, err := service.rejectCurrentFiendWarActivePartyRequest(ordinary, 10, party.RequestPeerRequest{
		TargetID: 22, Mode: 0,
	})
	if err != nil || handled {
		t.Fatalf("ordinary dungeon request handled=%t err=%v", handled, err)
	}
	handled, err = service.acknowledgeCurrentFiendWarPartyAccept(ordinary, nil, 11, party.ResponsePeerRequest{
		TargetID: 22, Mode: 0, Value: 0,
	}, false)
	if err != nil || handled {
		t.Fatalf("ordinary dungeon response handled=%t err=%v", handled, err)
	}
	if text := logs.String(); strings.Contains(text, fiendWarPartyAdmissionDiagnosticKind) {
		t.Fatalf("ordinary dungeon emitted FiendWar admission diagnostic:\n%s", text)
	}
}
