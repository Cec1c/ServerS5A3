package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"reflect"
	"strconv"
	"testing"
)

func rejoinContextRequest(key uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, key)
	return b
}
func TestCurrentRaidRejoinPublicContextOwnerRejects(t *testing.T) {
	for _, kind := range []string{"wrong_account", "unauthenticated", "channel", "channel_alias", "server", "server_range", "ordinary", "luke", "route", "legacy_object", "battle_channel", "battle_type", "battle_account", "nonmember", "wrong_key", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			s, _, member, lc, mc, state := prepareApprovedCurrentRaid(t)
			live := s.raids[state.RaidKey]
			body := rejoinContextRequest(state.RaidKey)
			switch kind {
			case "wrong_account":
				member.accountID = "foreign"
			case "unauthenticated":
				member.accountFromToken = false
			case "channel":
				member.channel.ID++
			case "channel_alias":
				member.channel.ID += 256
			case "server":
				s.options.channelAdvertiseID = 7
			case "server_range":
				s.options.channelAdvertiseID = 256
			case "ordinary":
				member.channel.Type = 11
			case "luke":
				member.channel.Type = dnfenum.HiddenRaidType
			case "route":
				live.RouteOrRaidType = 1
			case "legacy_object":
				live.CurrentLayout = false
			case "battle_channel", "battle_type", "battle_account":
				live.Battle = &runtimeRaidBattle{ChannelID: member.channel.ID, ChannelType: member.channel.Type, MemberAccounts: map[uint16]string{member.selectedCharacterID: s.accountIDForSession(member)}}
				if kind == "battle_channel" {
					live.Battle.ChannelID++
				}
				if kind == "battle_type" {
					live.Battle.ChannelType = 11
				}
				if kind == "battle_account" {
					live.Battle.MemberAccounts[member.selectedCharacterID] = "foreign"
				}
			case "nonmember":
				live.Members = live.Members[:1]
			case "wrong_key":
				body = rejoinContextRequest(state.RaidKey + 1)
			case "malformed":
				body = []byte{1}
			}
			before := cloneRuntimeRaidState(*live)
			if err := s.handleGameCommand(member, 1, 668, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, mc.write.Bytes())
			if ack.Header.MsgID != 668 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, runtimeRaidFailureCode}) || len(rest) != 0 || lc.write.Len() != 0 {
				t.Fatalf("unauthorized rejoin projected success or peer state: ack=%x remaining=%d peer=%d", ack.Body, len(rest), lc.write.Len())
			}
			if !reflect.DeepEqual(before, cloneRuntimeRaidState(*live)) {
				t.Fatal("rejected rejoin changed raid")
			}
		})
	}
}
func TestCurrentRaidRejoinPublicSameContextRestoresMembers(t *testing.T) {
	for _, leaderOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "member", true: "leader"}[leaderOwner], func(t *testing.T) {
			s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
			session, conn := member, mc
			if leaderOwner {
				session, conn = leader, lc
			}
			for attempt := 0; attempt < 2; attempt++ {
				lc.write.Reset()
				mc.write.Reset()
				if err := s.handleGameCommand(session, 1, 668, rejoinContextRequest(state.RaidKey)); err != nil {
					t.Fatal(err)
				}
				ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.MsgID != 668 || !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("lawful existing member lost success", ack.Body)
				}
				after, ok := s.runtimeRaidForMember(session.selectedCharacterID)
				if !ok || !reflect.DeepEqual(state, after) {
					t.Fatal("rejoin changed owned state")
				}
			}
		})
	}
}

func rejoinAssetSnapshot(t *testing.T, s *Service, session *gameSession) []any {
	t.Helper()
	g, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repository unavailable")
	}
	id := strconv.Itoa(int(session.selectedCharacterID))
	ctx := context.Background()
	c, cf, ce := g.Character.Load(ctx, id)
	i, inf, ie := g.Inventory.Load(ctx, id)
	e, ef, ee := g.Equipment.Load(ctx, id)
	if ce != nil || ie != nil || ee != nil {
		t.Fatal(ce, ie, ee)
	}
	return []any{c, cf, i, inf, e, ef}
}
func TestRealPVFCurrentRaidRejoinOwnedBattleNoAssetMutation(t *testing.T) {
	f, session, _, _ := onlineAntonTerminalFixture(t)
	// This existing actual-PVF progression fixture is a current-client directory
	// object: its immutable channel and account owners come from real clear setup.
	f.state.CurrentLayout = true
	before := rejoinAssetSnapshot(t, f.service, session)
	key := f.state.RaidKey
	for _, kind := range []string{"battle_channel", "battle_type", "account", "channel", "same_context"} {
		t.Run(kind, func(t *testing.T) {
			oldChannel, oldType, oldAccount := f.state.Battle.ChannelID, f.state.Battle.ChannelType, f.state.Battle.MemberAccounts[19]
			sessionChannel := session.channel
			t.Cleanup(func() {
				f.state.Battle.ChannelID, f.state.Battle.ChannelType, f.state.Battle.MemberAccounts[19] = oldChannel, oldType, oldAccount
				session.channel = sessionChannel
			})
			switch kind {
			case "battle_channel":
				f.state.Battle.ChannelID++
			case "battle_type":
				f.state.Battle.ChannelType = 11
			case "account":
				f.state.Battle.MemberAccounts[19] = "foreign"
			case "channel":
				session.channel.ID++
			}
			f.conn.write.Reset()
			if err := f.service.handleGameCommand(session, 1, 668, rejoinContextRequest(key)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if kind == "same_context" {
				if !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("lawful actual owner rejected", ack.Body)
				}
				for len(rest) > 0 {
					p, next := splitGameServerUpperPacket(t, rest)
					rest = next
					if p.Header.MsgID == 587 {
						t.Fatal("rejoin replayed start/movie transition")
					}
				}
			} else if !bytes.Equal(ack.Body, []byte{0, runtimeRaidFailureCode}) || len(rest) != 0 {
				t.Fatal("foreign frozen owner accepted", kind, ack.Body)
			}
			f.state.Battle.ChannelID, f.state.Battle.ChannelType, f.state.Battle.MemberAccounts[19] = oldChannel, oldType, oldAccount
			session.channel = sessionChannel
			if after := rejoinAssetSnapshot(t, f.service, session); !reflect.DeepEqual(before, after) {
				t.Fatal("rejoin mutated physical assets")
			}
		})
	}
}

func TestCurrentRaidRejoinFreshBindingAndRetiredSocket(t *testing.T) {
	s, _, member, lc, mc, state := prepareApprovedCurrentRaid(t)
	freshConn := &bufferConn{}
	fresh := &gameSession{conn: freshConn, accountID: member.accountID, selectedCharacterID: member.selectedCharacterID, channel: member.channel, loginPacketSeen: true, accountFromToken: true}
	s.unbindGameSession(member)
	s.bindGameSessionCharacter(fresh, fresh.selectedCharacterID)
	lc.write.Reset()
	mc.write.Reset()
	if err := s.handleGameCommand(member, 1, 668, rejoinContextRequest(state.RaidKey)); err != nil {
		t.Fatal(err)
	}
	if lc.write.Len() != 0 {
		t.Fatal("retired socket projected peer roster")
	}
	ack, rest := splitGameServerUpperPacket(t, mc.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, runtimeRaidFailureCode}) || len(rest) != 0 {
		t.Fatal("retired socket accepted")
	}
	if err := s.handleGameCommand(fresh, 1, 668, rejoinContextRequest(state.RaidKey)); err != nil {
		t.Fatal(err)
	}
	ack, _ = splitGameServerUpperPacket(t, freshConn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatal("canonical rebind cannot recover")
	}
}
func TestCurrentRaidRejoinRejectedAckWriteFailureCannotFanout(t *testing.T) {
	s, _, member, lc, _, state := prepareApprovedCurrentRaid(t)
	member.channel.ID++
	member.conn = &expeditionRosterFailConn{}
	before := cloneRuntimeRaidState(*s.raids[state.RaidKey])
	if err := s.handleGameCommand(member, 1, 668, rejoinContextRequest(state.RaidKey)); err == nil {
		t.Fatal("write failure not returned")
	}
	if lc.write.Len() != 0 || !reflect.DeepEqual(before, cloneRuntimeRaidState(*s.raids[state.RaidKey])) {
		t.Fatal("failed rejection write touched raid or peer")
	}
}

func TestCurrentRaidRejoinLukeSameContext(t *testing.T) {
	s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
	live := s.raids[state.RaidKey]
	delete(s.raids, state.RaidKey)
	live.RaidKey = (state.RaidKey & 0xff00ffff) | (32 << 16)
	live.RouteOrRaidType = 1
	leader.channel.ID, leader.channel.Type = 32, dnfenum.HiddenRaidType
	member.channel = leader.channel
	s.raids[live.RaidKey] = live
	before := cloneRuntimeRaidState(*live)
	if err := s.handleGameCommand(member, 1, 668, rejoinContextRequest(live.RaidKey)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, mc.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1}) || lc.write.Len() == 0 {
		t.Fatal("lawful Luke directory rejoin failed")
	}
	if !reflect.DeepEqual(before, cloneRuntimeRaidState(*live)) {
		t.Fatal("Luke rejoin changed state")
	}
}
