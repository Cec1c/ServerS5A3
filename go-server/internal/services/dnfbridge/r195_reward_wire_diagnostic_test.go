package dnfbridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func r195RewardWireFixture(t *testing.T, packetLog bool, flow *currentAntonRewardFlow) (*raidStartGuardFixture, *gameSession, *bytes.Buffer) {
	t.Helper()
	f := newRaidStartGuardFixture(t, nil, nil)
	t.Cleanup(func() { _ = f.logger.Close() })
	if !packetLog {
		f.service.packetLog = nil
	}
	session := f.session()
	session.loginPacketSeen = true
	session.accountFromToken = true
	f.service.bindGameSessionCharacter(session, 19)
	f.conn.write.Reset()
	f.state.Started = true
	f.state.Battle = &runtimeRaidBattle{
		InstanceID:     "reward-wire-diag-instance",
		Mode:           1,
		Phase:          0,
		ChannelID:      session.channel.ID,
		ChannelType:    session.channel.Type,
		MemberAccounts: map[uint16]string{19: "account-1"},
	}
	if flow != nil {
		f.state.Battle.RewardFlow[0] = flow
	}
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	return f, session, &logs
}

func r195RewardWireEvent(t *testing.T, logs *bytes.Buffer, opcode uint16) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] == currentRaidRewardWireDiagnosticKind && event["opcode"] == float64(opcode) {
			return event
		}
	}
	t.Fatalf("missing %s opcode %d in %s", currentRaidRewardWireDiagnosticKind, opcode, logs.String())
	return nil
}

func TestR195RewardWireDiagnosticKeepsRealSendAndRedactsPayload(t *testing.T) {
	cards := []raid.RewardDisplayCard{
		{CharacterID: 19, CardType: 1, Items: []raid.RewardDisplayItem{{ObjectKind: 0, ItemID: 10304529, Quantity: 30}}},
		{CharacterID: 20, CardType: 2, Items: []raid.RewardDisplayItem{{ObjectKind: 0, ItemID: 10093974, Quantity: 19}, {ObjectKind: 0, ItemID: 10304475, Quantity: 1}}},
	}
	teamBody, err := raid.BuildRewardListBody(1, cards)
	if err != nil {
		t.Fatal(err)
	}
	packets := []currentAntonRewardPacket{
		{opcode: 587, class: 0, body: []byte{4, 0}},
		{opcode: 600, class: 0, body: teamBody},
	}

	base, baseSession, _ := r195RewardWireFixture(t, false, nil)
	if base.service.packetLog != nil {
		t.Fatal("base fixture did not disable packetLog")
	}
	if err := base.service.sendCurrentAntonRewardPackets(baseSession, cloneRuntimeRaidState(*base.state), packets, false); err != nil {
		t.Fatal(err)
	}
	withLog, loggedSession, logs := r195RewardWireFixture(t, true, nil)
	if err := withLog.service.sendCurrentAntonRewardPackets(loggedSession, cloneRuntimeRaidState(*withLog.state), packets, false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base.conn.write.Bytes(), withLog.conn.write.Bytes()) {
		t.Fatal("packetLog=nil changed the real reward wire")
	}

	state := r195RewardWireEvent(t, logs, 587)
	if state["char_id"] != float64(19) || state["instance_id"] != "reward-wire-diag-instance" ||
		state["mode"] != float64(1) || state["phase"] != float64(0) || state["class"] != float64(0) ||
		state["body_len"] != float64(2) || state["body_digest"] != currentProjectionBodyDigest(packets[0].body) ||
		state["written"] != true || state["layout_ok"] != true || state["state"] != float64(4) {
		t.Fatalf("587 diagnostic=%v", state)
	}
	team := r195RewardWireEvent(t, logs, 600)
	if team["class"] != float64(0) || team["body_len"] != float64(len(teamBody)) ||
		team["body_digest"] != currentProjectionBodyDigest(teamBody) || team["written"] != true ||
		team["layout_ok"] != true || team["card_kind"] != float64(1) || team["card_count"] != float64(2) ||
		team["item_count"] != float64(3) || team["owner_count"] != float64(2) || team["owner_ids"] != "19,20" {
		t.Fatalf("600 diagnostic=%v", team)
	}
	for _, key := range []string{"body", "body_hex", "account_id", "error", "item_id", "item_ids", "items"} {
		if _, exists := state[key]; exists {
			t.Fatalf("587 diagnostic leaked %s", key)
		}
		if _, exists := team[key]; exists {
			t.Fatalf("600 diagnostic leaked %s", key)
		}
	}
	if strings.Contains(logs.String(), "account-1") {
		t.Fatalf("reward diagnostic leaked account: %s", logs.String())
	}
}

func TestR195RewardWireDiagnosticRecordsSquadShownFilterWithoutChangingWire(t *testing.T) {
	flow := &currentAntonRewardFlow{SquadShownAt: time.Now()}
	f, session, logs := r195RewardWireFixture(t, false, flow)
	teamBody, err := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: 19, CardType: 1}})
	if err != nil {
		t.Fatal(err)
	}
	packets := []currentAntonRewardPacket{
		{opcode: 600, class: 0, body: teamBody},
		{opcode: 659, class: 1, body: []byte{1}},
	}
	if err := f.service.sendCurrentAntonRewardPackets(session, cloneRuntimeRaidState(*f.state), packets, false); err != nil {
		t.Fatal(err)
	}
	event := r195RewardWireEvent(t, logs, 600)
	if event["written"] != false || event["reason"] != "squad_shown_at_filtered" || event["opcode"] != float64(600) {
		t.Fatalf("filtered 600 diagnostic=%v", event)
	}
	stream := f.conn.write.Bytes()
	seen := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		seen++
		if packet.Header.MsgID == 600 {
			t.Fatal("SquadShownAt filter sent the class0 600 list")
		}
		if packet.Header.MsgID != 659 || packet.Header.Classification != 1 {
			t.Fatalf("unexpected retained packet: %+v", packet.Header)
		}
	}
	if seen != 1 {
		t.Fatalf("retained packets=%d, want one class1 ACK", seen)
	}
}

func TestR195RewardWire600LayoutValidationRejectsTruncation(t *testing.T) {
	if _, _, _, _, ok := currentRaidRewardWire600([]byte{1, 1, 19, 0, 1, 1}); ok {
		t.Fatal("truncated NOTI600 body accepted")
	}
	if _, _, _, _, ok := currentRaidRewardWire600([]byte{1, 1, 19}); ok {
		t.Fatal("missing card header accepted")
	}
}

func TestR195RewardWireDiagnosticRecordsUncommittedSquadBeforeSend(t *testing.T) {
	flow := &currentAntonRewardFlow{
		InstanceID:   "reward-wire-diag-instance",
		RaidKey:      23<<16 | 19,
		Phase:        0,
		Mode:         1,
		SquadShownAt: time.Now(),
		Recipients:   []currentAntonRewardRecipient{{CharacterID: 19}},
	}
	f, session, logs := r195RewardWireFixture(t, false, flow)
	session.raidProjection.resultSent = true
	session.raidProjection.resultIdentity = currentRaidProjectionIdentity{
		InstanceID: "reward-wire-diag-instance",
		Phase:      0,
		CharID:     19,
		Generation: session.characterGeneration,
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != raid.ErrRewardReceipt {
		t.Fatalf("uncommitted squad error=%v", err)
	}
	event := r195RewardWireEvent(t, logs, 600)
	if event["written"] != false || event["reason"] != "squad_card_uncommitted" || event["body_len"] != float64(0) {
		t.Fatalf("uncommitted squad diagnostic=%v", event)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("uncommitted squad diagnostic sent a packet")
	}
}
