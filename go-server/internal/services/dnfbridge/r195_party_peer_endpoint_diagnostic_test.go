package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// The reported incident has packet logging disabled. These tests exercise the
// three real op11 send points and keep the diagnostics independent of the
// packet logger.
func TestR195PartyPeerEndpointDiagnosticsCoverFormationRepeatAndDungeonRemotes(t *testing.T) {
	service, leader, _ := vendorRelayFixture(t)
	leader.connID = "game-r195-peer-endpoint"
	state := runtimePartyStateSnapshot(leader)
	if service.packetLog != nil {
		t.Fatal("test requires packet logging to be disabled")
	}

	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	initialBody, err := service.sendRuntimePartyPeerEndpointInfo(leader, state, service.partyFormationOrder())
	if err != nil {
		t.Fatal(err)
	}
	wireBody := r195PartyPeerEndpointOp11Body(t, r195PartyPeerEndpointDrainFrames(t, leader))
	if !bytes.Equal(initialBody, wireBody) {
		t.Fatalf("initial op11 returned body=% X wire=% X", initialBody, wireBody)
	}
	initial := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	assertR195PeerEndpointSendEvent(t, initial, string(partyPeerEndpointStageInitial), "party_snapshot_before_roster", state, leader, initialBody)

	logs.Reset()
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := service.sendRuntimePartyPeerEndpointsAfterRoster(leader, state, initialBody, partyPeerEndpointsAfterRosterRepeat); err != nil {
		t.Fatal(err)
	}
	repeatBody := r195PartyPeerEndpointOp11Body(t, r195PartyPeerEndpointDrainFrames(t, leader))
	if !bytes.Equal(initialBody, repeatBody) {
		t.Fatalf("repeat changed op11 bytes: initial=% X repeat=% X", initialBody, repeatBody)
	}
	repeat := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	assertR195PeerEndpointSendEvent(t, repeat, string(partyPeerEndpointStageAfterRosterRepeat), "party_formation_after_roster_repeat", state, leader, repeatBody)
	if repeat["source"] == initial["source"] {
		t.Fatalf("roster repeat retained phase-one source: %v", repeat)
	}

	logs.Reset()
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	sent, err := service.sendDungeonEntryPeerEndpointRemotes(leader, "r195_dungeon_entry")
	if err != nil || !sent {
		t.Fatalf("dungeon remotes sent=%t err=%v", sent, err)
	}
	remotesBody := r195PartyPeerEndpointOp11Body(t, r195PartyPeerEndpointDrainFrames(t, leader))
	if !bytes.Equal(initialBody, remotesBody) {
		t.Fatalf("dungeon remotes changed op11 bytes: initial=% X remotes=% X", initialBody, remotesBody)
	}
	remotes := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	assertR195PeerEndpointSendEvent(t, remotes, string(partyPeerEndpointStageDungeonEntryRemotes), "dungeon_entry_remotes", state, leader, remotesBody)
	r195PartyPeerEndpointAssertNoPrivateFields(t, logs.String(), "PRIVATE_PEER_ENDPOINT")
	if service.packetLog != nil {
		t.Fatal("diagnostic enabled packet logging")
	}
}

func TestR195PartyPeerEndpointDiagnosticsRecordRealSendFailures(t *testing.T) {
	const privateWriteError = "PRIVATE_PEER_ENDPOINT_WRITE_FAILURE"
	cases := []struct {
		name   string
		stage  partyPeerEndpointStage
		source string
		send   func(*Service, *gameSession, alignedcmd.PartyState, []byte) error
	}{
		{
			name:   "initial_formation",
			stage:  partyPeerEndpointStageInitial,
			source: "party_snapshot_before_roster",
			send: func(service *Service, session *gameSession, state alignedcmd.PartyState, _ []byte) error {
				_, err := service.sendRuntimePartyPeerEndpointInfo(session, state, service.partyFormationOrder())
				return err
			},
		},
		{
			name:   "after_roster_repeat",
			stage:  partyPeerEndpointStageAfterRosterRepeat,
			source: "party_formation_after_roster_repeat",
			send: func(service *Service, session *gameSession, state alignedcmd.PartyState, bootstrap []byte) error {
				return service.sendRuntimePartyPeerEndpointsAfterRoster(session, state, bootstrap, partyPeerEndpointsAfterRosterRepeat)
			},
		},
		{
			name:   "dungeon_entry_remotes",
			stage:  partyPeerEndpointStageDungeonEntryRemotes,
			source: "dungeon_entry_remotes",
			send: func(service *Service, session *gameSession, _ alignedcmd.PartyState, _ []byte) error {
				sent, err := service.sendDungeonEntryPeerEndpointRemotes(session, "r195_dungeon_entry_failure")
				if !sent && err == nil {
					return errors.New("dungeon remotes reported skipped instead of attempted")
				}
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, leader, member := vendorRelayFixture(t)
			leader.connID = "game-r195-peer-endpoint-failure"
			state := runtimePartyStateSnapshot(leader)
			service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 0x13C7))
			bootstrap := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
			writeErr := errors.New(privateWriteError)
			leader.conn = &doc104RefusalSendConn{
				Conn:     leader.conn,
				writeErr: writeErr,
			}
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if err := tc.send(service, leader, state, bootstrap); !errors.Is(err, writeErr) {
				t.Fatalf("injected write failure returned %v, want original %v", err, writeErr)
			}
			event := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
			if event["stage"] != string(tc.stage) || event["source"] != tc.source {
				t.Fatalf("failure stage/source = %v/%v want %s/%s", event["stage"], event["source"], tc.stage, tc.source)
			}
			if event["send_call_ok"] != false || event["send_call_result"] != "returned_error" {
				t.Fatalf("failed send diagnostic = %v", event)
			}
			if event["body_digest"] == nil || event["body_len"] == nil {
				t.Fatalf("failed send lost body evidence: %v", event)
			}
			r195PartyPeerEndpointAssertNoPrivateFields(t, logs.String(), privateWriteError)
		})
	}
}

func TestR195PartyPeerEndpointDiagnosticSuppressedSendDoesNotClaimWireDelivery(t *testing.T) {
	service, leader, _ := vendorRelayFixture(t)
	_ = r195PartyPeerEndpointDrainFrames(t, leader)
	resetUpperSendFilterCache()
	defer resetUpperSendFilterCache()
	t.Setenv(upperSendFilterEnv, "0:11")
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := service.sendRuntimePartyPeerEndpointInfo(leader, runtimePartyStateSnapshot(leader), service.partyFormationOrder()); err != nil {
		t.Fatal(err)
	}
	if frames := r195PartyPeerEndpointDrainFrames(t, leader); len(frames) != 0 {
		t.Fatalf("suppressed op11 wrote frames: %v", frameMessageIDs(frames))
	}
	event := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	if event["send_call_ok"] != true || event["send_call_result"] != "returned_nil" || event["body_scope"] != "send_call_input" || event["sent"] != nil || event["success"] != nil {
		t.Fatalf("suppressed send diagnostic overstates wire delivery: %v", event)
	}
}

func TestR195PartyPeerEndpointRejectedRegistrationReachesNormalLog(t *testing.T) {
	service := &Service{}
	session := &gameSession{connID: "game-r195-malformed-op2", selectedCharacterID: 1104}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if service.captureCurrentPartyPeerEndpointRegistration(session, []byte("PRIVATE_MALFORMED_OP2")) {
		t.Fatal("malformed registration changed endpoint")
	}
	event := r195DiagnosticEvent(t, &logs, "game-party-peer-endpoint-registration-rejected")
	if event["body_len"] != float64(len("PRIVATE_MALFORMED_OP2")) || event["reason"] != "current_exe_op2_31_byte_layout_mismatch" || event["error"] != nil {
		t.Fatalf("malformed op2 diagnostic = %v", event)
	}
	r195PartyPeerEndpointAssertNoPrivateFields(t, logs.String(), "PRIVATE_MALFORMED_OP2")
}

func TestR195PartyDungeonLoadingSkippedPlacementDoesNotClaimSuccess(t *testing.T) {
	service := &Service{}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	service.logGameEvent(&gameSession{connID: "game-r195-skipped"}, "game-party-dungeon-post-start-map-placement-skipped",
		"char_id", uint16(1104), "reason", "deferred_scene_unavailable_at_loading_barrier_release")
	event := r195DiagnosticEvent(t, &logs, "game-party-dungeon-post-start-map-placement-skipped")
	if event["event_phase"] != "placement_skipped" || event["success"] != nil || event["call_ok"] != nil {
		t.Fatalf("skipped placement diagnostic = %v", event)
	}
}

func TestR195SilentPeerKickNormalDiagnosticsWithoutPacketLog(t *testing.T) {
	service, _, member, _, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterRepeat)
	service.packetLog = nil
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if !service.kickSilentPartyMember(partyID, member.selectedCharacterID) {
		t.Fatal("silent kick failed")
	}
	event := r195DiagnosticEvent(t, &logs, "party-udp-relay-silent-peer-kick")
	if event["ordering"] != "op11_then_op9_then_op153_then_op11_after_roster" || event["after_roster"] != "repeat" || event["member_id"] != float64(member.selectedCharacterID) {
		t.Fatalf("normal-log kick evidence = %v", event)
	}
	r195PartyPeerEndpointAssertNoPrivateFields(t, logs.String(), "PRIVATE_KICK_ERROR")
}

func TestR195PartyPeerEndpointRegistrationDiagnosticIsWhitelisted(t *testing.T) {
	service := &Service{}
	session := &gameSession{connID: "game-r195-op2", selectedCharacterID: 1020}
	session.channel.ID = 23
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if service.packetLog != nil {
		t.Fatal("test requires packet logging to be disabled")
	}
	if changed := service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 0x13C7)); !changed {
		t.Fatal("first op2 registration should change the port")
	}
	event := r195DiagnosticEvent(t, &logs, partyPeerEndpointRegistrationDiagnosticKind)
	if event["port"] != float64(5063) || event["port_endian"] != "be" ||
		event["character_attribute"] != float64(5) || event["mtu"] != float64(1472) ||
		event["advertised_mtu"] != float64(1472) || event["port_changed"] != true || event["acc_stamped"] != true {
		t.Fatalf("op2 whitelist summary = %v", event)
	}
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "kind": true,
		"conn_id": true, "channel_id": true, "char_id": true,
		"port": true, "port_endian": true, "character_attribute": true,
		"mtu": true, "advertised_mtu": true, "port_changed": true, "acc_stamped": true,
	}
	for key := range event {
		if !allowed[key] {
			t.Fatalf("op2 diagnostic forwarded non-whitelisted field %q: %v", key, event)
		}
	}
	r195PartyPeerEndpointAssertNoPrivateFields(t, logs.String(), "00ff211ef879")
}

func TestR195PartyPeerEndpointDiagnosticBoundsRowsAtFour(t *testing.T) {
	service := &Service{}
	session := &gameSession{connID: "game-r195-four-row-bound", selectedCharacterID: 1020}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	body := make([]byte, 1+6*currentPartyPeerEndpointSize)
	body[0] = 6
	for index := 0; index < 6; index++ {
		start := 1 + index*currentPartyPeerEndpointSize
		binary.LittleEndian.PutUint16(body[start:start+2], uint16(1100+index))
		copy(body[start+2:start+6], []byte{10, 0, 0, byte(index + 1)})
		copy(body[start+6:start+10], []byte{10, 0, 0, byte(index + 1)})
		binary.BigEndian.PutUint16(body[start+10:start+12], uint16(30000+index))
		body[start+16] = 1
		binary.LittleEndian.PutUint32(body[start+17:start+21], 512)
		body[start+21] = 0x64
	}
	service.logPartyPeerEndpointSendDiagnostic(session, alignedcmd.PartyState{PartyID: 7}, partyPeerEndpointStageInitial, body, nil)
	event := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	if event["peer_count"] != float64(6) || event["peer_rows_logged"] != float64(4) ||
		event["peer_rows_truncated"] != true || event["parse_reason"] != "row_limit" {
		t.Fatalf("bounded row summary = %v", event)
	}
	if event["peer_4_char_id"] != float64(1103) || event["peer_5_char_id"] != nil {
		t.Fatalf("row cap leaked past four peers: %v", event)
	}
	if event["peer_1_inner_class"] != "private" || event["peer_1_inner_equals_outer"] != true {
		t.Fatalf("address summary = %v", event)
	}
	if event["peer_1_port"] != float64(30000) {
		t.Fatalf("peer port offset = %v want 30000", event)
	}
}

func TestR195PartyPeerEndpointDiagnosticUsesVendorPortOffsetWithNontrivialAccount(t *testing.T) {
	const accountID uint32 = 0x71A2B3C4
	body := buildCurrentPartyPeerEndpointInfoRows([]currentPartyPeerEndpointRow{{
		userID:    1104,
		inner:     [4]byte{10, 1, 2, 3},
		outer:     [4]byte{203, 0, 113, 9},
		port:      30002,
		accountID: accountID,
		nat:       1,
		mtu:       512,
		charAttr:  0x64,
	}})
	if len(body) != 1+currentPartyPeerEndpointSize {
		t.Fatalf("builder body=% X", body)
	}
	row := body[1:]
	if binary.LittleEndian.Uint32(row[12:16]) != accountID {
		t.Fatalf("builder account bytes = % X want %08x", row[12:16], accountID)
	}
	if got := binary.BigEndian.Uint16(row[10:12]); got != 30002 {
		t.Fatalf("builder port bytes = % X read %d want 30002", row[10:12], got)
	}

	service := &Service{}
	session := &gameSession{connID: "game-r195-vendor-port-offset", selectedCharacterID: 1020}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	service.logPartyPeerEndpointSendDiagnostic(
		session,
		alignedcmd.PartyState{PartyID: 7},
		partyPeerEndpointStageDungeonEntryRemotes,
		body,
		nil,
	)
	event := r195DiagnosticEvent(t, &logs, partyPeerEndpointDiagnosticKind)
	if event["peer_1_char_id"] != float64(1104) || event["peer_1_port"] != float64(30002) ||
		event["peer_1_nat"] != float64(1) || event["peer_1_mtu"] != float64(512) ||
		event["peer_1_attr"] != float64(0x64) || event["source"] != "dungeon_entry_remotes" {
		t.Fatalf("real builder diagnostic = %v body=% X", event, body)
	}
}

func TestR195PartyDungeonLoadingDiagnosticsSurviveDisabledPacketLog(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		fields []any
		failed bool
		want   map[string]any
	}{
		{
			name: "loading_ready",
			kind: "game-party-dungeon-loading-ready",
			fields: []any{
				"conn_id", "game-loading-1020", "channel_id", byte(23),
				"party_id", uint16(77), "char_id", uint16(1020), "slot", byte(1),
				"cycle", uint32(4), "first", true, "release", false, "member_count", 2,
				"reason", "PRIVATE_LOADING_REASON",
			},
			want: map[string]any{
				"party_id": float64(77), "char_id": float64(1020), "slot": float64(1),
				"cycle": float64(4), "first": true, "release": false, "member_count": float64(2),
			},
		},
		{
			name: "loading_released_with_tail_failure",
			kind: "game-party-dungeon-loading-released",
			fields: []any{
				"conn_id", "game-loading-1020", "channel_id", byte(23),
				"party_id", uint16(77), "char_id", uint16(1020), "local_slot", byte(1),
				"trigger", "last_member_op37", "release_tail", true,
				"packet_order", "PRIVATE_PACKET_ORDER", "reason", "PRIVATE_LOADING_REASON",
				"error", errors.New("PRIVATE_LOADING_ERROR"),
			},
			failed: true,
			want: map[string]any{
				"party_id": float64(77), "char_id": float64(1020), "local_slot": float64(1),
				"trigger": "last_member_op37", "release_tail": true,
			},
		},
		{
			name: "placement_deferred",
			kind: "game-party-dungeon-post-start-map-placement-deferred",
			fields: []any{
				"conn_id", "game-loading-1020", "channel_id", byte(23),
				"source", "deferred_entry", "char_id", uint16(1020), "party_id", uint16(77),
				"barrier", "PRIVATE_BARRIER", "deferred_until", "PRIVATE_DEFERRED_UNTIL",
				"reason", "PRIVATE_LOADING_REASON",
			},
			want: map[string]any{
				"source": "deferred_entry", "char_id": float64(1020), "party_id": float64(77),
			},
		},
		{
			name: "placement_released",
			kind: "game-party-dungeon-post-start-map-placement-released",
			fields: []any{
				"conn_id", "game-loading-1020", "channel_id", byte(23),
				"source", "party_dungeon_shared_loading_barrier", "char_id", uint16(1020),
				"deferred_source", "post_start_map", "barrier", "PRIVATE_BARRIER",
			},
			want: map[string]any{
				"source": "party_dungeon_shared_loading_barrier", "deferred_source": "post_start_map",
			},
		},
	}
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "kind": true,
		"conn_id": true, "channel_id": true, "char_id": true, "party_id": true,
		"slot": true, "local_slot": true, "cycle": true, "member_count": true,
		"first": true, "release": true, "trigger": true, "release_tail": true,
		"source": true, "deferred_source": true, "phase": true, "event_phase": true, "call_ok": true,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{}
			session := &gameSession{connID: "game-loading-1020", selectedCharacterID: 1020}
			session.channel.ID = 23
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if service.packetLog != nil {
				t.Fatal("test requires packet logging to be disabled")
			}
			service.logGameEvent(session, tc.kind, tc.fields...)
			event := r195DiagnosticEvent(t, &logs, tc.kind)
			for key := range event {
				if !allowed[key] {
					t.Fatalf("%s forwarded non-whitelisted field %q: %v", tc.kind, key, event)
				}
			}
			if tc.kind == "game-party-dungeon-loading-released" {
				if event["call_ok"] != !tc.failed {
					t.Fatalf("release call status = %v", event)
				}
			} else if event["call_ok"] != nil || event["success"] != nil {
				t.Fatalf("pre-send event must not claim success: %v", event)
			}
			if event["event_phase"] == nil {
				t.Fatalf("missing event phase: %v", event)
			}
			for key, want := range tc.want {
				if event[key] != want {
					t.Fatalf("%s field %s=%v want %v: %v", tc.kind, key, event[key], want, event)
				}
			}
			for _, forbidden := range []string{
				"PRIVATE_LOADING_ERROR", "PRIVATE_LOADING_REASON", "PRIVATE_PACKET_ORDER",
				"PRIVATE_BARRIER", "PRIVATE_DEFERRED_UNTIL", `"error"`, `"reason"`,
				`"packet_order"`, `"barrier"`, `"deferred_until"`,
			} {
				if strings.Contains(logs.String(), forbidden) {
					t.Fatalf("%s leaked %q: %s", tc.kind, forbidden, logs.String())
				}
			}
		})
	}
}

func TestR195PartyDungeonLoadingBarrierEventsReachNormalLogWithPacketLogOff(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()

	service, leader, follower, _, _, _ := partyDungeonSyncFixture(t)
	service.packetLog = nil
	var concurrentLogs r195LogBuffer
	service.logger = slog.New(slog.NewJSONHandler(&concurrentLogs, nil))
	runPartyDungeonSyncEntry(t, service, leader)
	// This synthetic map fixture has no runtime skill PVF. Reuse the existing
	// prepared-skill tail setup: exercise the real shared barrier and op37/op30
	// release without claiming to validate full skill hydration here.
	r195PrepareLoadingTail(leader, follower)
	if err := service.sendFinishLoadingStatus(leader, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatal(err)
	}
	if err := service.sendFinishLoadingStatus(follower, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatal(err)
	}
	logs := bytes.NewBufferString(concurrentLogs.String())
	for _, kind := range []string{
		"game-party-dungeon-loading-ready",
		"game-party-dungeon-loading-released",
		"game-party-dungeon-post-start-map-placement-deferred",
		"game-party-dungeon-post-start-map-placement-released",
	} {
		event := r195DiagnosticEvent(t, logs, kind)
		if kind == "game-party-dungeon-loading-released" && event["call_ok"] != true {
			t.Fatalf("release call failed: %v", event)
		}
		if kind == "game-party-dungeon-post-start-map-placement-released" &&
			(event["event_phase"] != "before_placement_send" || event["call_ok"] != nil || event["success"] != nil) {
			t.Fatalf("placement event incorrectly claims completed send: %v", event)
		}
		if _, found := event["error"]; found {
			t.Fatalf("%s forwarded an error field: %v", kind, event)
		}
	}
	if service.packetLog != nil {
		t.Fatal("test unexpectedly enabled packet logging")
	}
}

func assertR195PeerEndpointSendEvent(
	t *testing.T,
	event map[string]any,
	stage string,
	source string,
	state alignedcmd.PartyState,
	session *gameSession,
	body []byte,
) {
	t.Helper()
	if event["stage"] != stage || event["source"] != source {
		t.Fatalf("stage/source = %v/%v want %s/%s", event["stage"], event["source"], stage, source)
	}
	if event["conn_id"] != session.connID || event["channel_id"] != float64(session.channel.ID) ||
		event["receiver_char_id"] != float64(session.selectedCharacterID) || event["party_id"] != float64(state.PartyID) {
		t.Fatalf("send identity fields = %v", event)
	}
	if event["send_call_ok"] != true || event["send_call_result"] != "returned_nil" {
		t.Fatalf("send result = %v", event)
	}
	if event["body_len"] != float64(len(body)) || event["body_digest"] != currentProjectionBodyDigest(body) || event["parse_ok"] != true {
		t.Fatalf("body evidence = %v want len=%d digest=%s", event, len(body), currentProjectionBodyDigest(body))
	}
	if len(body) < 1+currentPartyPeerEndpointSize {
		t.Fatalf("op11 body too short: % X", body)
	}
	row := body[1:]
	uid := binary.LittleEndian.Uint16(row[0:2])
	port := binary.BigEndian.Uint16(row[10:12])
	mtu := binary.LittleEndian.Uint32(row[17:21])
	if event["peer_ids"] != "1002" || event["peer_1_char_id"] != float64(uid) ||
		event["peer_1_port"] != float64(port) || event["peer_1_port_endian"] != "be" ||
		event["peer_1_nat"] != float64(row[16]) || event["peer_1_mtu"] != float64(mtu) ||
		event["peer_1_attr"] != float64(row[21]) {
		t.Fatalf("peer row evidence = %v body=% X", event, body)
	}
	if event["peer_1_relation"] == nil || event["peer_1_inner_class"] == nil || event["peer_1_outer_class"] == nil {
		t.Fatalf("address relation summary missing: %v", event)
	}
	r195PartyPeerEndpointAssertNoPrivateFields(t, mustJSONLine(t, event), "PRIVATE_PEER_ENDPOINT")
}

func r195PartyPeerEndpointDrainFrames(t *testing.T, session *gameSession) []upperFrame {
	t.Helper()
	var data []byte
	switch conn := session.conn.(type) {
	case *bufferConn:
		data = append([]byte(nil), conn.write.Bytes()...)
		conn.write.Reset()
	case *tcpAddrBufferConn:
		data = append([]byte(nil), conn.write.Bytes()...)
		conn.write.Reset()
	default:
		t.Fatalf("unsupported session conn %T", session.conn)
	}
	return splitUpperFrames(t, data)
}

func r195PartyPeerEndpointOp11Body(t *testing.T, frames []upperFrame) []byte {
	t.Helper()
	var body []byte
	for _, frame := range frames {
		if frame.msgID == 0x000b {
			body = frame.body
		}
	}
	if body == nil {
		t.Fatal("no op11 frame was written")
	}
	return body
}

func r195PartyPeerEndpointAssertNoPrivateFields(t *testing.T, logged string, privateError string) {
	t.Helper()
	for _, secret := range []string{
		privateError,
		"192.168.0.101",
		"00ff211ef879",
		`"account"`,
		`"account_id"`,
		`"token"`,
		`"inner_ipv4"`,
		`"outer_ipv4"`,
		`"peer_row_acc"`,
		`"error"`,
		`"body"`,
		`"packet"`,
	} {
		if secret != "" && strings.Contains(logged, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, logged)
		}
	}
}

func mustJSONLine(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
