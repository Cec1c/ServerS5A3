package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

func TestRaidDungeonSelectionRequiresAdmittedChannel(t *testing.T) {
	for _, name := range []string{"admitted", "other_channel", "other_raid_type", "missing_channel", "missing_channel_type", "foreign_account", "expired"} {
		t.Run(name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 1)
			f.service.options.currentClientVersion = "official-24.9"
			session := f.session()
			session.loginPacketSeen, session.accountFromToken = true, true
			f.service.bindGameSessionCharacter(session, 19)
			f.state.Started = true
			f.state.Battle = &runtimeRaidBattle{
				InstanceID: "channel-ownership-test", Mode: 1, Single: true,
				ChannelID: session.channel.ID, ChannelType: session.channel.Type,
				EnterAfter: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Hour),
				MemberAccounts: map[uint16]string{19: "account-1"},
				Dungeons:       []raid.DungeonState{{ID: 210, State: 0}},
			}
			switch name {
			case "other_channel":
				session.channel.ID++
			case "other_raid_type":
				session.channel.Type = dnfenum.HiddenRaidType
			case "missing_channel":
				f.state.Battle.ChannelID = 0
			case "missing_channel_type":
				f.state.Battle.ChannelType = 0
			case "foreign_account":
				f.state.Battle.MemberAccounts[19] = "other-account"
			case "expired":
				f.state.Battle.Deadline = time.Now().Add(-time.Second)
			}
			if got := f.service.currentRaidDungeonSelectionAllowed(session, 210); got != (name == "admitted") {
				t.Fatalf("selection allowed=%v; case=%s admitted=%d/%d actual=%d/%d", got, name, f.state.Battle.ChannelID, f.state.Battle.ChannelType, session.channel.ID, session.channel.Type)
			}
			if f.service.currentRaidDungeonSelectionAllowed(session, 211) {
				t.Fatal("unlisted dungeon allowed")
			}
		})
	}
}
