package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
)

func singleLukeSelectorGuardFixture(t *testing.T) (*raidStartGuardFixture, *gameSession) {
	t.Helper()
	f := raidSafetyFixture(t, nil, 1)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.channel.ID, session.channel.Type = 28, 32
	session.loginPacketSeen, session.accountFromToken = true, true
	s.bindGameSessionCharacter(session, 19)
	f.state.Started, f.state.CurrentLayout = true, true
	f.state.RouteOrRaidType, f.state.CurrentUIFlags = 1, [3]byte{1, 0, 0}
	f.state.Battle = &runtimeRaidBattle{
		Single: true, Mode: 2, ChannelID: 28, ChannelType: 32,
		SingleSequence: []uint32{5323, 5324, 5325, 5326, 5327, 5328},
		Dungeons:       []raid.DungeonState{{ID: 5323, State: 0}, {ID: 5324, State: 1}, {ID: 5325, State: 1}, {ID: 5326, State: 1}, {ID: 5327, State: 1}, {ID: 5328, State: 1}},
		MemberAccounts: map[uint16]string{19: "account-1"},
		EnterAfter:     time.Now().Add(-time.Minute), Deadline: time.Now().Add(time.Minute),
	}
	return f, session
}

func TestSingleLukeSelectorAuthenticatedOwnerAndRefusal(t *testing.T) {
	for _, name := range []string{"valid", "unstarted", "no_battle", "legacy_layout", "wrong_route", "team_flag", "wrong_account", "nonmember", "wrong_channel", "wrong_channel_type", "stale_generation", "team_battle", "anton_battle", "older_client", "future", "other_weekly", "unknown", "expired", "admission_delay", "timeout", "finished", "malformed_sequence"} {
		t.Run(name, func(t *testing.T) {
			f, session := singleLukeSelectorGuardFixture(t)
			s := f.service
			requested := uint32(5054)
			switch name {
			case "unstarted":
				f.state.Started = false
			case "no_battle":
				f.state.Battle = nil
			case "legacy_layout":
				f.state.CurrentLayout = false
			case "wrong_route":
				f.state.RouteOrRaidType = 0
			case "team_flag":
				f.state.CurrentUIFlags[0] = 0
			case "wrong_account":
				session.accountID = "different"
			case "nonmember":
				delete(f.state.Battle.MemberAccounts, 19)
			case "wrong_channel":
				session.channel.ID++
			case "wrong_channel_type":
				session.channel.Type = 23
			case "stale_generation":
				replacement := f.session()
				replacement.loginPacketSeen, replacement.accountFromToken = true, true
				s.bindGameSessionCharacter(replacement, 19)
			case "team_battle":
				f.state.Battle.Single = false
			case "anton_battle":
				f.state.Battle.Mode = 1
			case "older_client":
				s.options.currentClientVersion = "legacy"
			case "future":
				requested = 5055
			case "other_weekly":
				requested = 5056
			case "unknown":
				requested = 9999
			case "expired":
				f.state.Battle.Deadline = time.Now().Add(-time.Second)
			case "admission_delay":
				f.state.Battle.EnterAfter = time.Now().Add(time.Minute)
			case "timeout":
				f.state.Battle.TimedOut = true
				for i := range f.state.Battle.Dungeons {
					f.state.Battle.Dungeons[i].State = 2
				}
			case "finished":
				f.state.Battle.SingleIndex, f.state.Battle.PhaseCompleted = 6, true
				for i := range f.state.Battle.Dungeons {
					f.state.Battle.Dungeons[i].State = 1
				}
			case "malformed_sequence":
				f.state.Battle.SingleSequence[1] = 5330
			}
			before := cloneRuntimeRaidBattle(f.state.Battle)
			unchanged := func() bool {
				return reflect.DeepEqual(before, cloneRuntimeRaidBattle(f.state.Battle))
			}
			resolved := s.resolveCurrentSingleLukeSelector(session, requested)
			want := requested
			if name == "valid" || name == "expired" || name == "admission_delay" {
				want = 5323 // Clock authority belongs to the following admission guard.
			}
			if resolved != want || !unchanged() {
				t.Fatalf("resolution=%d want=%d or owner mutated", resolved, want)
			}
			if name == "valid" {
				if !s.currentRaidDungeonSelectionAllowed(session, resolved) {
					t.Fatal("valid native entry rejected")
				}
				return
			}
			if name == "older_client" {
				return // Legacy dungeon routing is outside the current raid guard.
			}
			body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
			binary.LittleEndian.PutUint32(body, requested)
			if err := s.handleDungeonSelectUpper(session, body); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != nil || !unchanged() {
				t.Fatal("refused request changed native owner")
			}
			packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if packet.Header.MsgID != 16 || len(packet.Body) != 3 || packet.Body[0] != 0 || len(rest) != 0 {
				t.Fatal("refused request missing single failure ACK", packet.Body)
			}
		})
	}
}
