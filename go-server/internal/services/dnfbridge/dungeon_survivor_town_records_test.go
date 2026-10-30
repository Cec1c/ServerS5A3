package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

var errDungeonSurvivorTownInjectedWrite = errors.New("injected survivor town write failure")

// Fail on the second frame while retaining the first successful frame. The
// survivor-town helper must return immediately instead of attempting its own
// record or any later record after a write error.
type dungeonSurvivorTownFailSecondWriteConn struct {
	*bufferConn
	attempts int
}

func (c *dungeonSurvivorTownFailSecondWriteConn) Write(data []byte) (int, error) {
	c.attempts++
	if c.attempts == 2 {
		return 0, errDungeonSurvivorTownInjectedWrite
	}
	return c.bufferConn.Write(data)
}

func newDungeonSurvivorTownRecordsFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	service, leader, _, _, _ := partyDungeonDeathFixture(t)
	service.options.currentClientVersion = "official-24.9"
	conn := leader.conn.(*bufferConn)
	conn.write.Reset()
	return service, leader, conn
}

func dungeonSurvivorTownOp9TownRecords(t *testing.T, data []byte) ([]uint16, []byte) {
	t.Helper()
	frames := splitUpperFrames(t, data)
	keys := make([]uint16, 0, len(frames))
	owners := make([]byte, 0, len(frames))
	for _, frame := range frames {
		if frame.msgID != 9 || len(frame.body) != 10 || frame.body[9] != currentSceneOp9ActorRemoveKind {
			t.Fatalf("unexpected survivor-town frame: msg=%d body=% X", frame.msgID, frame.body)
		}
		keys = append(keys, binary.LittleEndian.Uint16(frame.body[4:6]))
		owners = append(owners, frame.body[7])
	}
	return keys, owners
}

func TestDungeonSurvivorTownRecordsOrderDedupAndZeroKey(t *testing.T) {
	service, receiver, conn := newDungeonSurvivorTownRecordsFixture(t)
	t.Setenv(dungeonPartyRetiredRecordClearEnv, "on")
	receiver.channel.ID = 11
	receiver.townActorOwnerChannel = 0
	receiver.connectionTownActorOwnerChannel = 23

	members := []alignedcmd.PartyMemberState{
		{UserID: 100},
		{UserID: 101},
		{UserID: 100},
		{UserID: 99},
		{UserID: 0},
		{UserID: 101},
		{UserID: 99},
		{UserID: 0},
	}
	if err := service.clearDungeonSurvivorTownPartyRecords(receiver, members, "test_order"); err != nil {
		t.Fatal(err)
	}

	keys, owners := dungeonSurvivorTownOp9TownRecords(t, conn.write.Bytes())
	wantKeys := []uint16{100, 101, 99}
	if len(keys) != len(wantKeys) {
		t.Fatalf("town record keys=%v want %v", keys, wantKeys)
	}
	for index, want := range wantKeys {
		if keys[index] != want {
			t.Fatalf("town record keys=%v want %v: self must be last", keys, wantKeys)
		}
		if owners[index] != 11 {
			t.Fatalf("town record owner[%d]=%d want selected channel 11", index, owners[index])
		}
	}
}

func TestDungeonSurvivorTownRecordsUsesSelectedChannelWithoutTruncation(t *testing.T) {
	for _, test := range []struct {
		name       string
		channelID  int
		wantFrames int
		wantOwner  byte
	}{
		{name: "selected_channel_11_beats_town_zero", channelID: 11, wantFrames: 1, wantOwner: 11},
		{name: "u8_max_255_is_exact", channelID: 255, wantFrames: 1, wantOwner: 255},
		{name: "oversized_256_is_not_truncated_to_zero", channelID: 256, wantFrames: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, receiver, conn := newDungeonSurvivorTownRecordsFixture(t)
			t.Setenv(dungeonPartyRetiredRecordClearEnv, "on")
			receiver.channel.ID = test.channelID
			receiver.townActorOwnerChannel = 0
			receiver.connectionTownActorOwnerChannel = 200

			if err := service.clearDungeonSurvivorTownPartyRecords(receiver, []alignedcmd.PartyMemberState{{UserID: 99}}, "test_channel"); err != nil {
				t.Fatal(err)
			}
			if test.wantFrames == 0 {
				if conn.write.Len() != 0 {
					t.Fatalf("oversized channel wrote % X", conn.write.Bytes())
				}
				return
			}
			keys, owners := dungeonSurvivorTownOp9TownRecords(t, conn.write.Bytes())
			if len(keys) != 1 || keys[0] != 99 || len(owners) != 1 || owners[0] != test.wantOwner {
				t.Fatalf("town records keys=%v owners=%v want one key 99 owner %d", keys, owners, test.wantOwner)
			}
		})
	}
}

func TestDungeonSurvivorTownRecordsLegacyAndOffSendNothing(t *testing.T) {
	for _, value := range []string{"legacy", "off", " LEGACY ", "0", "false"} {
		t.Run(value, func(t *testing.T) {
			service, receiver, conn := newDungeonSurvivorTownRecordsFixture(t)
			t.Setenv(dungeonPartyRetiredRecordClearEnv, value)
			receiver.channel.ID = 11
			receiver.townActorOwnerChannel = 0

			if err := service.clearDungeonSurvivorTownPartyRecords(receiver, []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}}, "test_disabled"); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("%q retained mode wrote % X", value, conn.write.Bytes())
			}
		})
	}
}

func TestDungeonSurvivorTownRecordsWriteFailureStopsBeforeFollowingRecords(t *testing.T) {
	service, receiver, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
	service.options.currentClientVersion = "official-24.9"
	t.Setenv(dungeonPartyRetiredRecordClearEnv, "on")
	receiver.channel.ID = 11
	receiver.townActorOwnerChannel = 0
	baseConn := receiver.conn.(*bufferConn)
	baseConn.write.Reset()
	conn := &dungeonSurvivorTownFailSecondWriteConn{bufferConn: baseConn}
	receiver.conn = conn

	state := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100}},
	}
	// This helper runs after the departure has committed its retained roster.
	manager := service.runtimePartyManagerForService()
	if before, found := manager.SnapshotByUser(100, 0); found {
		result := manager.LeaveExpected(100, beforeMemberGeneration(before, 100), before.ID)
		if !result.OK {
			t.Fatal("fixture member departure failed")
		}
		state = result.Party.StateFor(99)
	}
	storeRuntimePartyState(receiver, state)
	err := service.sendMemberGiveupSurvivorRosterAfterDeparture(state, leaderRuntime.partyInstance, 100)
	if !errors.Is(err, errDungeonSurvivorTownInjectedWrite) {
		t.Fatalf("write error=%v want injected failure", err)
	}
	if conn.attempts != 2 {
		t.Fatalf("write attempts=%d want 2: helper continued after the failed frame", conn.attempts)
	}
	keys, _ := dungeonSurvivorTownOp9TownRecords(t, baseConn.write.Bytes())
	if len(keys) != 1 || keys[0] != 100 {
		t.Fatalf("successfully written town records=%v want only the departing first key 100", keys)
	}
}
