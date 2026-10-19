package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPVPTotalMissionsBeforeTeamRegistration(t *testing.T) {
	s := pvpRoomTestService(t)
	s.pvpTotalCatalog = pvpDailyTestCatalog()
	s.pvpTotalCatalog.Normal = []dnfpvp.Mission{
		{ID: 800, Category: "battle", Condition: "hereafter join", Target: 100},
		{ID: 801, Category: "battle", Condition: "hereafter join", Target: 333, PreRequired: 800},
		{ID: 824, Category: "victory", Condition: "hereafter winning count", Target: 100},
	}
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "pvp-29", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Character.Load(ctx, "29")
	p := &gameSession{pvpClientGameType: 7, conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29,
		channel: channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}}
	var first []byte
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.sendCurrentPVPDailyState(p); err != nil {
			t.Fatal(err)
		}
		packets := totalTestPackets(t, p)
		var mission []byte
		for _, packet := range packets {
			if packet.Header.MsgID == 323 {
				if mission != nil {
					t.Fatal("second op323 would replace the first task family")
				}
				mission = packet.Body
			}
			if packet.Header.MsgID == 48 || packet.Header.MsgID == 801 || packet.Header.MsgID == 802 {
				t.Fatal("mission publication invented a three-character roster")
			}
		}
		if len(mission) != 61 || mission[0] != 6 {
			t.Fatalf("unregistered team got no complete native mission snapshot: %x", mission)
		}
		for i, id := range []uint16{800, 801, 824} {
			row := mission[1+(3+i)*10:]
			if binary.LittleEndian.Uint16(row) != id || binary.LittleEndian.Uint32(row[2:]) != 0 {
				t.Fatal("target row missing or personal match facts leaked", id)
			}
		}
		if attempt == 0 {
			first = append([]byte(nil), mission...)
		} else if !reflect.DeepEqual(first, mission) {
			t.Fatal("reconnecting rerolled missions")
		}
	}
	team, err := dnfpvp.ReadTotalTeam(ctx, repos, p.accountID)
	if err != nil || team.Team.Name != "" || team.Slots != [3]byte{255, 255, 255} {
		t.Fatal("mission initialization registered a team", err)
	}
	after, _, _ := repos.Character.Load(ctx, "29")
	if !reflect.DeepEqual(before.Stats, after.Stats) {
		t.Fatal("total initialization altered ordinary character counters")
	}
}
