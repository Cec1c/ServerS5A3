package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func r195RaidSessionRestoreFlags(session *gameSession) (pending, initialized, failed bool) {
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	return session.raidProjection.sessionRestorePending,
		session.raidProjection.initialized,
		session.raidProjection.failed
}

func r195AssertRaidSessionRestoreWireOrder(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	object := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		return p.Header.Classification == 1 && p.Header.MsgID == 1359
	})
	roster := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(p)
		return ok && mode == 3
	})
	metadata := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(p)
		return ok && mode == 2
	})
	firstProgress := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		return p.Header.MsgID == 583 || p.Header.MsgID == 584 || p.Header.MsgID == 585
	})
	if object < 0 || roster < 0 || metadata < 0 || firstProgress < 0 ||
		!(object < roster && roster < metadata && metadata < firstProgress) {
		t.Fatalf("restore object/roster/metadata/progress order: %s", r195UpperPacketIDs(packets))
	}
}

func TestRealPVFRaidSessionRestoreGatesBackgroundUntilOwnObject(t *testing.T) {
	for _, mode := range []string{"luke", "anton"} {
		t.Run(mode, func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			if mode == "anton" {
				f.session.channel.Type = dnfenum.RaidChannelType
				f.state.RouteOrRaidType = 0
				f.state.Battle.Mode = 1
				f.state.Battle.ChannelType = dnfenum.RaidChannelType
			}
			if !f.session.raidProjection.sessionRestorePending {
				t.Fatal("replacement published without restore gate")
			}
			state, _ := f.service.runtimeRaidForMember(f.session.selectedCharacterID)
			if err := f.service.sendCurrentRaidProgressProjection(f.session, state, false, false); err != nil {
				t.Fatal(err)
			}
			if err := f.service.sendCurrentRaidBattleState(f.session, state); err != nil {
				t.Fatal(err)
			}
			if f.conn.write.Len() != 0 || f.session.raidProjection.initialized {
				t.Fatal("background clock sent battle state before own object")
			}
			if err := f.service.restoreCurrentRaidSessionProjection(f.session, "test_scene_loading"); err != nil {
				t.Fatal(err)
			}
			packets := r195ParseUpperFrames(t, f.conn)
			object := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
				return p.Header.Classification == 1 && p.Header.MsgID == 1359 && len(p.Body) > 2 && p.Body[0] == 1 && p.Body[1] == 2
			})
			roster := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
				m, ok := r195RaidRefreshMode(p)
				return ok && m == 3
			})
			progress := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
				return p.Header.MsgID == 585
			})
			if object < 0 || roster <= object || progress <= roster {
				t.Fatalf("object/roster/progress order: %s", r195UpperPacketIDs(packets))
			}
			for _, p := range packets {
				if p.Header.MsgID == 587 || p.Header.MsgID == 668 {
					t.Fatal("restore replayed start or rejoin")
				}
			}
			if f.session.raidProjection.sessionRestorePending || !f.session.raidProjection.initialized {
				t.Fatal("successful restore failed to release clock")
			}
			f.conn.write.Reset()
			if err := f.service.restoreCurrentRaidSessionProjection(f.session, "repeat"); err != nil {
				t.Fatal(err)
			}
			if f.conn.write.Len() != 0 {
				t.Fatal("restored native object sent again")
			}
		})
	}
}

func TestRealPVFRaidSessionRestoreRejectsUnverifiedContexts(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *raidSelectorRestoreR195Fixture) *gameSession
	}{
		{
			name: "foreign_identity",
			run: func(_ *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.state.Battle.MemberAccounts[f.session.selectedCharacterID] = "foreign"
				return f.session
			},
		},
		{
			name: "wrong_channel",
			run: func(_ *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.session.channel.ID++
				return f.session
			},
		},
		{
			name: "old_session",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				t.Helper()
				f.old.raidProjection.mu.Lock()
				f.old.raidProjection.sessionRestorePending = true
				f.old.raidProjection.mu.Unlock()
				f.old.conn.(*bufferConn).write.Reset()
				return f.old
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			session := tc.run(t, f)
			if err := f.service.restoreCurrentRaidSessionProjection(session, "test_rejected_context"); err != nil {
				t.Fatalf("rejected context returned error: %v", err)
			}
			conn := session.conn.(*bufferConn)
			if conn.write.Len() != 0 {
				t.Fatalf("rejected context wrote %d bytes", conn.write.Len())
			}
			pending, initialized, failed := r195RaidSessionRestoreFlags(session)
			if !pending || initialized || failed {
				t.Fatalf("rejected context pending=%t initialized=%t failed=%t", pending, initialized, failed)
			}
		})
	}
}

type raidSessionRestoreR195ObjectFailConn struct {
	bufferConn
}

func (c *raidSessionRestoreR195ObjectFailConn) Write(data []byte) (int, error) {
	if len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == 1359 {
		return 0, errors.New("injected raid session restore object write failure")
	}
	return c.bufferConn.Write(data)
}

func TestRealPVFRaidSessionRestoreObjectWriteFailureKeepsRetryGate(t *testing.T) {
	f := newRaidSelectorRestoreR195Fixture(t)
	if pending, _, _ := r195RaidSessionRestoreFlags(f.session); !pending {
		t.Fatal("replacement published without restore gate")
	}
	failing := &raidSessionRestoreR195ObjectFailConn{}
	f.session.conn = failing
	if err := f.service.restoreCurrentRaidSessionProjection(f.session, "test_object_failure"); err == nil {
		t.Fatal("object write failure was reported as success")
	}
	if object := r195FindUpperPacket(r195ParseUpperFrames(t, &failing.bufferConn), func(p dnfproto.ChannelPacket) bool {
		return p.Header.Classification == 1 && p.Header.MsgID == 1359
	}); object >= 0 {
		t.Fatal("failed object packet was committed to the wire")
	}
	pending, initialized, failed := r195RaidSessionRestoreFlags(f.session)
	if !pending || initialized || failed {
		t.Fatalf("object failure pending=%t initialized=%t failed=%t", pending, initialized, failed)
	}

	f.conn.write.Reset()
	f.session.conn = f.conn
	if err := f.service.restoreCurrentRaidSessionProjection(f.session, "test_object_retry"); err != nil {
		t.Fatalf("retry after object write failure: %v", err)
	}
	r195AssertRaidSessionRestoreWireOrder(t, r195ParseUpperFrames(t, f.conn))
	pending, initialized, failed = r195RaidSessionRestoreFlags(f.session)
	if pending || !initialized || failed {
		t.Fatalf("successful object retry pending=%t initialized=%t failed=%t", pending, initialized, failed)
	}
}

func TestRealPVFRaidSessionRestoreFinishLoadingDoesNotAdvanceProgressBeforeObject(t *testing.T) {
	f := newRaidSelectorRestoreR195Fixture(t)
	// This fixture starts after character selection. Load the real job catalog
	// before the timed command, as the character-selection path already does.
	// Keep the command timeout and all wire-order assertions unchanged.
	if _, err := f.service.initialCharacterSkills(context.Background(), 0); err != nil {
		t.Fatalf("initialize selected character skill catalog: %v", err)
	}
	if pending, _, _ := r195RaidSessionRestoreFlags(f.session); !pending {
		t.Fatal("replacement published without restore gate")
	}
	f.session.postStartMapPlayerStateSent = true
	if err := f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.GameTypeFinishLoading),
		make([]byte, currentFinishLoadingLegacyRequestBodySize),
	); err != nil {
		t.Fatalf("real finish-loading handler: %v", err)
	}
	packets := r195ParseUpperFrames(t, f.conn)
	status := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		return p.Header.Classification == dnfproto.DefaultChannelClassification &&
			p.Header.MsgID == uint16(dnfenum.CmdPacketFinishLoading)
	})
	object := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		return p.Header.Classification == 1 && p.Header.MsgID == 1359
	})
	firstProgress := r195FindUpperPacket(packets, func(p dnfproto.ChannelPacket) bool {
		return p.Header.MsgID == 583 || p.Header.MsgID == 584 || p.Header.MsgID == 585
	})
	if status < 0 || object < 0 || firstProgress < 0 {
		t.Fatalf("finish-loading restore frames status=%d object=%d progress=%d ids=%s",
			status, object, firstProgress, r195UpperPacketIDs(packets))
	}
	if !(status < object && object < firstProgress) {
		t.Fatalf("finish-loading advanced progress before object: status=%d object=%d progress=%d ids=%s",
			status, object, firstProgress, r195UpperPacketIDs(packets))
	}
	for i, packet := range packets[:object] {
		if packet.Header.MsgID == 583 || packet.Header.MsgID == 584 || packet.Header.MsgID == 585 {
			t.Fatalf("progress frame %d/%d was sent before ACK1359 at %d", i, packet.Header.MsgID, object)
		}
	}
	r195AssertRaidSessionRestoreWireOrder(t, packets)
	pending, initialized, failed := r195RaidSessionRestoreFlags(f.session)
	if pending || !initialized || failed {
		t.Fatalf("finish-loading restore pending=%t initialized=%t failed=%t", pending, initialized, failed)
	}
}
