package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
)

type raidSelectorRestoreR195Fixture struct {
	service *Service
	state   *runtimeRaidState
	old     *gameSession
	session *gameSession
	conn    *bufferConn
}

func newRaidSelectorRestoreR195Fixture(t *testing.T) *raidSelectorRestoreR195Fixture {
	t.Helper()
	f, old, _ := normalLukeFixture(t)
	f.service.raidMu.Lock()
	oldKey := f.state.RaidKey
	newKey := uint32(f.service.channelAdvertiseID())<<24 | uint32(old.channel.ID)<<16 | oldKey&0xffff
	delete(f.service.raids, oldKey)
	f.state.RaidKey = newKey
	f.service.raids[newKey] = f.state
	f.service.raidMu.Unlock()
	for _, id := range []string{"19", "20"} {
		character, found, err := f.repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatalf("load Luke profile character %s: found=%t err=%v", id, found, err)
		}
		character.Level, character.Job, character.Name = 90, "0", "r195-"+id
		if err := f.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "r195-selector-restore",
		channel:             old.channel,
		accountID:           old.accountID,
		selectedCharacterID: old.selectedCharacterID,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	f.service.bindGameSessionCharacter(session, old.selectedCharacterID)
	conn.write.Reset()
	return &raidSelectorRestoreR195Fixture{
		service: f.service,
		state:   f.state,
		old:     old,
		session: session,
		conn:    conn,
	}
}

func r195DispatchRealUpper15(t *testing.T, service *Service, session *gameSession) error {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(15, nil, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	return service.handleGameUpper(session, frame)
}

func r195ParseUpperFrames(t *testing.T, conn *bufferConn) []dnfproto.ChannelPacket {
	t.Helper()
	raw := append([]byte(nil), conn.write.Bytes()...)
	var packets []dnfproto.ChannelPacket
	for len(raw) != 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		packets = append(packets, packet)
	}
	return packets
}

func r195RaidRefreshMode(packet dnfproto.ChannelPacket) (uint32, bool) {
	if packet.Header.Classification != 0 || packet.Header.MsgID != raid.RaidMemberRefreshMsgID || len(packet.Body) < 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(packet.Body[4:8]), true
}

func r195FindUpperPacket(packets []dnfproto.ChannelPacket, predicate func(dnfproto.ChannelPacket) bool) int {
	for i, packet := range packets {
		if predicate(packet) {
			return i
		}
	}
	return -1
}

func r195SelectorRestoreProjectionInitialized(session *gameSession) bool {
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	return session.raidProjection.initialized
}

func r195SelectorRestoreFlags(session *gameSession) (initialized, startTransition, metadataReady bool) {
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	return session.raidProjection.initialized,
		session.raidProjection.startTransitionSent,
		session.raidProjection.metadataReady
}

func r195AssertNoSelectorRestoreProjection(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	for _, packet := range packets {
		if packet.Header.MsgID == 752 || packet.Header.MsgID == 585 {
			t.Fatalf("unexpected self raid projection message %d", packet.Header.MsgID)
		}
		if mode, ok := r195RaidRefreshMode(packet); ok && (mode == 2 || mode == raid.RaidMemberRefreshMode3) {
			t.Fatalf("unexpected self raid roster/metadata mode %d", mode)
		}
		if packet.Header.MsgID == 668 {
			t.Fatal("selector restore invented a rejoin ACK")
		}
	}
}

func TestRealPVFR195Upper15RestoresMissingLukeSelfProjectionWithoutRejoin(t *testing.T) {
	f := newRaidSelectorRestoreR195Fixture(t)
	if r195SelectorRestoreProjectionInitialized(f.session) {
		t.Fatal("new session unexpectedly starts with an initialized raid projection")
	}
	before := cloneRuntimeRaidState(*f.state)
	if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
		t.Fatalf("real upper15: %v", err)
	}
	packets := r195ParseUpperFrames(t, f.conn)

	profiles := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 752
	})
	roster := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(packet)
		return ok && mode == raid.RaidMemberRefreshMode3
	})
	metadata := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(packet)
		return ok && mode == 2
	})
	maps := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 585
	})
	selector := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 1 && packet.Header.MsgID == uint16(dnfenum.CmdPacketEnterSelectDungeon)
	})
	if profiles < 0 || roster < 0 || metadata < 0 || maps < 0 || selector < 0 {
		t.Fatalf("missing restore frame: profiles=%d roster=%d metadata=%d maps=%d selector=%d ids=%v",
			profiles, roster, metadata, maps, selector, r195UpperPacketIDs(packets))
	}
	if !(profiles < roster && roster < metadata && metadata < maps && maps < selector) {
		t.Fatalf("restore order differs from verified rejoin tail: profiles=%d roster=%d metadata=%d maps=%d selector=%d",
			profiles, roster, metadata, maps, selector)
	}

	profilesCount := 0
	rosterCount, metadataCount, mapsCount := 0, 0, 0
	for _, packet := range packets {
		if packet.Header.MsgID == 668 || packet.Header.MsgID == 587 {
			t.Fatalf("reconnect restore emitted rejoin ACK or start transition: %v", r195UpperPacketIDs(packets))
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 752 {
			profilesCount++
		}
		if mode, ok := r195RaidRefreshMode(packet); ok {
			if mode == raid.RaidMemberRefreshMode3 {
				rosterCount++
			}
			if mode == 2 {
				metadataCount++
			}
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 585 {
			mapsCount++
		}
	}
	if profilesCount != 1 || rosterCount != 1 || metadataCount != 1 || mapsCount != 1 {
		t.Fatalf("restore frame counts profiles=%d roster=%d metadata=%d maps=%d",
			profilesCount, rosterCount, metadataCount, mapsCount)
	}
	mapBody := packets[maps].Body
	if len(mapBody) < 6 || mapBody[0] != 0 {
		t.Fatalf("full map body header=%x", mapBody)
	}
	rows := int(mapBody[1])
	if len(mapBody) != 2+rows*5+4 || rows != len(before.Battle.Dungeons) {
		t.Fatalf("full map rows=%d body_len=%d want rows=%d", rows, len(mapBody), len(before.Battle.Dungeons))
	}
	if !r195SelectorRestoreProjectionInitialized(f.session) {
		t.Fatal("successful restore did not initialize the session projection")
	}
	if !f.session.enterSelectDungeonSent || !f.session.enterSelectDungeonAckSent {
		t.Fatal("upper15 did not commit its success flags after restore")
	}
	if got := cloneRuntimeRaidState(*f.state); len(got.Members) != len(before.Members) || got.Started != before.Started || got.Battle == nil || before.Battle == nil {
		t.Fatal("selector restore changed core raid ownership")
	}

	f.conn.write.Reset()
	if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
		t.Fatalf("repeated real upper15: %v", err)
	}
	repeated := r195ParseUpperFrames(t, f.conn)
	r195AssertNoSelectorRestoreProjection(t, repeated)
}

func r195AssertSelectorRestoreFrames(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	profiles := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 752
	})
	roster := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(packet)
		return ok && mode == raid.RaidMemberRefreshMode3
	})
	metadata := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		mode, ok := r195RaidRefreshMode(packet)
		return ok && mode == 2
	})
	maps := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 585
	})
	selector := r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 1 && packet.Header.MsgID == uint16(dnfenum.CmdPacketEnterSelectDungeon)
	})
	if profiles < 0 || roster < 0 || metadata < 0 || maps < 0 || selector < 0 ||
		!(profiles < roster && roster < metadata && metadata < maps && maps < selector) {
		t.Fatalf("selector restore frames/order profiles=%d roster=%d metadata=%d maps=%d selector=%d ids=%s",
			profiles, roster, metadata, maps, selector, r195UpperPacketIDs(packets))
	}
}

func TestRealPVFR195Upper15RestoresMetadataAfterBackgroundProgressInitialized(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *raidSelectorRestoreR195Fixture)
	}{
		{
			name: "progress_only",
			setup: func(t *testing.T, f *raidSelectorRestoreR195Fixture) {
				state, found := f.service.runtimeRaidForMember(f.session.selectedCharacterID)
				if !found {
					t.Fatal("Luke raid membership missing")
				}
				// Production clock/progress callers initialize with
				// initial=false/resubscribe=false before upper15.
				if err := f.service.sendCurrentRaidProgressProjection(f.session, state, false, false); err != nil {
					t.Fatalf("background-style progress initialization: %v", err)
				}
			},
		},
		{
			name: "phase_one_battle_state",
			setup: func(t *testing.T, f *raidSelectorRestoreR195Fixture) {
				f.state.Battle.Phase = 1
				f.state.Battle.ProgressVersion = 1
				state, found := f.service.runtimeRaidForMember(f.session.selectedCharacterID)
				if !found {
					t.Fatal("Luke raid membership missing")
				}
				// This production phase-one queue sends a start transition and
				// initializes progress without sending profile/metadata.
				if err := f.service.sendCurrentRaidBattleState(f.session, state); err != nil {
					t.Fatalf("background phase-one battle state: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			tc.setup(t, f)
			initialized, startTransition, metadataReady := r195SelectorRestoreFlags(f.session)
			if !initialized || metadataReady || (tc.name == "progress_only" && startTransition) {
				t.Fatalf("background setup initialized=%t start=%t metadata_ready=%t",
					initialized, startTransition, metadataReady)
			}
			if tc.name == "phase_one_battle_state" && !startTransition {
				t.Fatal("phase-one background fixture did not establish the metadata-less start transition")
			}

			f.conn.write.Reset()
			if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
				t.Fatalf("upper15 after background setup: %v", err)
			}
			packets := r195ParseUpperFrames(t, f.conn)
			r195AssertSelectorRestoreFrames(t, packets)
			if r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
				return packet.Header.MsgID == 587
			}) >= 0 {
				t.Fatal("selector metadata restore replayed a start/cinematic transition")
			}
			_, _, metadataReady = r195SelectorRestoreFlags(f.session)
			if !metadataReady {
				t.Fatal("background initialized progress suppressed the selector-specific restore")
			}
		})
	}
}

type raidSelectorRestoreR195FailConn struct{ bufferConn }

func (c *raidSelectorRestoreR195FailConn) Write([]byte) (int, error) {
	return 0, errors.New("injected selector restore first-write failure")
}

func TestRealPVFR195SelectorRestoreFirstFrameFailureRemainsRetryable(t *testing.T) {
	f := newRaidSelectorRestoreR195Fixture(t)
	f.session.conn = &raidSelectorRestoreR195FailConn{}
	if err := r195DispatchRealUpper15(t, f.service, f.session); err == nil {
		t.Fatal("first projection write failure was reported as success")
	}
	if r195SelectorRestoreProjectionInitialized(f.session) ||
		f.session.enterSelectDungeonSent || f.session.enterSelectDungeonAckSent ||
		f.session.enterSelectDungeonContextSent {
		t.Fatal("failed first projection write committed selector or projection success")
	}

	f.conn.write.Reset()
	f.session.conn = f.conn
	if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
		t.Fatalf("retry after first-write failure: %v", err)
	}
	packets := r195ParseUpperFrames(t, f.conn)
	if r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 752
	}) < 0 || r195FindUpperPacket(packets, func(packet dnfproto.ChannelPacket) bool {
		return packet.Header.Classification == 0 && packet.Header.MsgID == 585
	}) < 0 {
		t.Fatal("retry did not replay the missing self raid projection")
	}
	if !r195SelectorRestoreProjectionInitialized(f.session) {
		t.Fatal("successful retry did not initialize the projection")
	}
}

func TestRealPVFR195SelectorRestoreRejectsUnverifiedContexts(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *raidSelectorRestoreR195Fixture) *gameSession
	}{
		{
			name: "foreign_account",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.state.Battle.MemberAccounts[f.session.selectedCharacterID] = "foreign"
				return f.session
			},
		},
		{
			name: "wrong_channel",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.session.channel.ID++
				return f.session
			},
		},
		{
			name: "old_session",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.old.conn.(*bufferConn).write.Reset()
				return f.old
			},
		},
		{
			name: "not_started",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.state.Started = false
				return f.session
			},
		},
		{
			name: "non_luke",
			run: func(t *testing.T, f *raidSelectorRestoreR195Fixture) *gameSession {
				f.state.RouteOrRaidType = 0
				f.state.Battle.Mode = 1
				return f.session
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			session := tc.run(t, f)
			_ = r195DispatchRealUpper15(t, f.service, session)
			conn := session.conn.(*bufferConn)
			packets := r195ParseUpperFrames(t, conn)
			r195AssertNoSelectorRestoreProjection(t, packets)
			if r195SelectorRestoreProjectionInitialized(session) {
				t.Fatalf("%s initialized an unverified self projection", tc.name)
			}
		})
	}
}

func TestRealPVFR195SelectorRestoreIdentityTracksInstancePhaseAndGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *raidSelectorRestoreR195Fixture)
	}{
		{
			name: "new_instance",
			mutate: func(t *testing.T, f *raidSelectorRestoreR195Fixture) {
				f.state.Battle.InstanceID = "r195-selector-new-instance"
			},
		},
		{
			name: "new_phase",
			mutate: func(t *testing.T, f *raidSelectorRestoreR195Fixture) {
				f.state.Battle.Phase = 1
			},
		},
		{
			name: "new_connection_generation",
			mutate: func(t *testing.T, f *raidSelectorRestoreR195Fixture) {
				if advanceGameSessionCharacterGeneration(f.session) == 0 {
					t.Fatal("advance connection generation")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
				t.Fatalf("initial upper15: %v", err)
			}
			_, _, metadataReady := r195SelectorRestoreFlags(f.session)
			if !metadataReady {
				t.Fatal("initial selector metadata marker missing")
			}

			tc.mutate(t, f)
			f.conn.write.Reset()
			if err := r195DispatchRealUpper15(t, f.service, f.session); err != nil {
				t.Fatalf("upper15 after identity change: %v", err)
			}
			r195AssertSelectorRestoreFrames(t, r195ParseUpperFrames(t, f.conn))
		})
	}
}

func r195UpperPacketIDs(packets []dnfproto.ChannelPacket) string {
	ids := make([]string, 0, len(packets))
	for _, packet := range packets {
		if mode, ok := r195RaidRefreshMode(packet); ok {
			ids = append(ids, fmt.Sprintf("%d/%d", packet.Header.MsgID, mode))
			continue
		}
		ids = append(ids, fmt.Sprintf("%d/%d", packet.Header.MsgID, packet.Header.Classification))
	}
	return fmt.Sprint(ids)
}
