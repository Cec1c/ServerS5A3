package dnfbridge

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func r195PrestartSelectorFixture(t *testing.T, channelType byte, started bool) (*Service, *gameSession, *gameSession, *runtimeRaidState) {
	t.Helper()
	_, s, leader, peer, lc, pc := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	mode, route := byte(1), byte(0)
	if channelType == dnfenum.HiddenRaidType {
		mode, route = 2, 1
	}
	for _, session := range []*gameSession{leader, peer} {
		session.loginPacketSeen, session.accountFromToken = true, true
		session.channel.Type = channelType
		// Deliberately distinguish a concrete channel ID from its type.
		session.channel.ID = 241
		if !s.currentRaidAuthenticated(session) {
			t.Fatal("fixture must establish real repository and current-session ownership")
		}
	}
	state := &runtimeRaidState{RaidKey: 241<<16 | 1001, LeaderID: 1001,
		RouteOrRaidType: route, CurrentLayout: true, Started: started,
		Members: []runtimeRaidMemberState{{CharID: 1001, GroupIndex: 1}, {CharID: 1002, GroupIndex: 1}}}
	if started {
		state.Battle = &runtimeRaidBattle{InstanceID: "prestart-selector-test", Mode: mode,
			ChannelID: leader.channel.ID, ChannelType: channelType,
			EnterAfter: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Hour),
			MemberAccounts: map[uint16]string{1001: "dnf:1", 1002: "dnf:2"}}
	}
	s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
	lc.write.Reset()
	pc.write.Reset()
	return s, leader, peer, state
}

func r195DispatchSelector(t *testing.T, s *Service, session *gameSession, legacy bool) {
	t.Helper()
	if legacy {
		if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeEnterSelectDungeon), nil); err != nil {
			t.Fatal("selector rejection must not turn into a connection error", err)
		}
		return
	}
	frame, err := dnfproto.BuildChannelPacket(15, nil, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, frame); err != nil {
		t.Fatal("selector dispatch", err)
	}
}

// Fixed v157 consumes success+reason, clears its pending request flag,
// and does not require an actor byte for neutral reason zero.
func r195WantNativeSelectorRejection(t *testing.T, stream []byte) {
	t.Helper()
	p, rest := splitLongHengGameServerUpperPacket(t, stream)
	if p.Header.MsgID != 15 || p.Header.Classification != 1 || !bytes.Equal(p.Body, []byte{0, 0}) || len(rest) != 0 {
		t.Fatal("rejection contains success/context/fatigue or differs from verified native contract")
	}
}

func TestR195PrestartSelectorRejectsAtDispatchBeforeSceneMutation(t *testing.T) {
	for _, channelType := range []byte{dnfenum.RaidChannelType, dnfenum.HiddenRaidType} {
		for _, legacy := range []bool{false} {
			for _, scenario := range []string{"created", "no_membership", "left_raid", "single_preparation", "missing_battle", "other_channel", "other_type", "foreign_account", "unbound"} {
				t.Run(fmt.Sprintf("type%d/legacy%t/%s", channelType, legacy, scenario), func(t *testing.T) {
					started := scenario == "missing_battle" || scenario == "other_channel" || scenario == "other_type" || scenario == "foreign_account" || scenario == "unbound"
					s, session, peer, state := r195PrestartSelectorFixture(t, channelType, started)
					switch scenario {
					case "no_membership":
						s.raids = nil
					case "left_raid":
						s.raidMu.Lock()
						_, removed, _ := s.removeRuntimeRaidMemberLocked(session.selectedCharacterID)
						s.raidMu.Unlock()
						if !removed {
							t.Fatal("departure fixture did not remove membership")
						}
					case "single_preparation":
						state.CurrentUIFlags[0], state.Members = 1, state.Members[:1]
					case "missing_battle":
						state.Battle = nil
					case "other_channel":
						session.channel.ID++
					case "other_type":
						if channelType == dnfenum.RaidChannelType {
							session.channel.Type = dnfenum.HiddenRaidType
						} else {
							session.channel.Type = dnfenum.RaidChannelType
						}
					case "foreign_account":
						state.Battle.MemberAccounts[session.selectedCharacterID] = "not-the-owner"
					case "unbound":
						delete(s.gameSessions, session.selectedCharacterID)
					}
					before := cloneRuntimeRaidState(*state)
					position := session.townPositionSnapshot
					for repeat := 0; repeat < 2; repeat++ {
						conn := session.conn.(*bufferConn)
						conn.write.Reset()
						r195DispatchSelector(t, s, session, legacy)
						if session.enterSelectDungeonSent || session.enterSelectDungeonAckSent || session.enterSelectDungeonContextSent || session.townSelectorOriginBound || session.selectPreviewActorRemoved || session.preDungeonContextPlayerStateSent {
							t.Fatal("prestart rejection mutated selector/actor ownership")
						}
						if !reflect.DeepEqual(position, session.townPositionSnapshot) || !reflect.DeepEqual(before, cloneRuntimeRaidState(*state)) || peer.conn.(*bufferConn).write.Len() != 0 {
							t.Fatal("rejection changed position/raid state or sent to a peer")
						}
						r195WantNativeSelectorRejection(t, conn.write.Bytes())
					}
				})
			}
		}
	}
}

func TestR195PrestartSelectorPreservesOrdinaryAndStartedWire(t *testing.T) {
	for _, channelType := range []byte{dnfenum.RaidChannelType, dnfenum.HiddenRaidType} {
		for _, legacy := range []bool{false, true} {
			for _, scenario := range []string{"ordinary_type_with_raid_number", "started", "single_started", "start_delay", "phase_break", "phase1", "timed_out"} {
				t.Run(fmt.Sprintf("type%d/legacy%t/%s", channelType, legacy, scenario), func(t *testing.T) {
					s, session, _, state := r195PrestartSelectorFixture(t, channelType, true)
					switch scenario {
					case "ordinary_type_with_raid_number":
						session.channel.ID, session.channel.Type, s.raids = int(channelType), 1, nil
					case "single_started":
						state.Battle.Single, state.Members = true, state.Members[:1]
					case "start_delay":
						state.Battle.EnterAfter = time.Now().Add(time.Minute)
					case "phase_break":
						state.Battle.PhaseCompleted = true
					case "phase1":
						state.Battle.Phase = 1
					case "timed_out":
						state.Battle.TimedOut = true
					}
					r195DispatchSelector(t, s, session, legacy)
					stream := session.conn.(*bufferConn).write.Bytes()
					if legacy {
						records, err := splitWrittenGameRecords(stream)
						if err != nil || len(records) != 2 {
							t.Fatal("legacy opening sequence", err, len(records))
						}
						if records[0].GameHeader.Type != 15 || !bytes.Equal(records[0].Body, buildEnterSelectDungeonBody(session.selectedCharacterID)) || records[1].GameHeader.Type != currentFatigueMsgID {
							t.Fatal("legacy opening contract changed")
						}
					} else {
						var ids []uint16
						for len(stream) > 0 {
							p, rest := splitLongHengGameServerUpperPacket(t, stream)
							stream = rest
							ids = append(ids, p.Header.MsgID)
							if p.Header.MsgID == 15 && (p.Header.Classification != 1 || !bytes.Equal(p.Body, []byte{1, 1, 0})) {
								t.Fatal("upper opening ACK changed")
							}
						}
						if !reflect.DeepEqual(ids, []uint16{15, currentFatigueMsgID, currentDungeonContextMsgID}) || !session.enterSelectDungeonContextSent {
							t.Fatal("upper opening context changed", ids)
						}
					}
					if !session.enterSelectDungeonSent || !session.enterSelectDungeonAckSent {
						t.Fatal("allowed opening did not commit its flags")
					}
				})
			}
		}
	}
}

func TestR195PrestartSelectorFollowerCannotBypassAdmission(t *testing.T) {
	for _, channelType := range []byte{dnfenum.RaidChannelType, dnfenum.HiddenRaidType} {
		for _, preserve := range []string{"on", "off"} {
			for _, stale := range []bool{false, true} {
				t.Run(fmt.Sprintf("type%d/preserve%s/stale%t", channelType, preserve, stale), func(t *testing.T) {
					t.Setenv(partySelectorPreserveActorEnv, preserve)
					s, leader, peer, _ := r195PrestartSelectorFixture(t, channelType, false)
					storeRuntimePartyState(leader, townCoPresencePartyState())
					storeRuntimePartyState(peer, townCoPresencePartyState())
					peer.enterSelectDungeonSent, peer.enterSelectDungeonContextSent = stale, stale
					prepared, err := s.prepareRuntimePartyFollowerEnterSelect(peer, 1001, 77, "prestart_test")
					if err != nil || prepared {
						t.Fatal("unstarted follower was prepared, or rejection became transport failure", prepared, err)
					}
					// Passive follower has no op15 request to ACK. This
					// policy is no output and no detach/rebuild, including stale flags.
					if peer.conn.(*bufferConn).write.Len() != 0 || leader.conn.(*bufferConn).write.Len() != 0 || peer.selectPreviewActorRemoved || peer.preDungeonContextPlayerStateSent || peer.townSelectorOriginBound {
						t.Fatal("prestart passive follower received a selector or actor transition")
					}
					if peer.enterSelectDungeonSent != stale || peer.enterSelectDungeonContextSent != stale || peer.enterSelectDungeonAckSent {
						t.Fatal("rejection mutated stale selector flags")
					}
				})
			}
		}
	}
}
