package dnfbridge

import (
	"fmt"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBattlePassNativeMallVisitSeparatesLoginQuery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  []byte
		class byte
		owner string
		want  int64
	}{
		{"login query", nil, 1, "a", 0},
		{"native shop open", []byte("MSV1"), 1, "a", 1},
		{"wrong class", []byte("MSV1"), 0, "a", 0},
		{"short", []byte("MSV"), 1, "a", 0},
		{"unknown", []byte("MSV2"), 1, "a", 0},
		{"trailing", []byte("MSV1\x00"), 1, "a", 0},
		{"owner changed", []byte("MSV1"), 1, "other", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, _, conn := playerStatsGameFixture(t)
			g, _ := s.repositoryGroup()
			ch, _, err := g.Character.Load(nil, "2")
			if err != nil {
				t.Fatal(err)
			}
			ch.AccountID = tc.owner
			if err = g.Character.Save(nil, ch); err != nil {
				t.Fatal(err)
			}
			playerStatsEdit(t, s, p, func() {
				raw, e := proto.BuildChannelPacket(63, tc.body, 0, tc.class)
				if e != nil {
					t.Fatal(e)
				}
				// BuildChannelPacket uses classification zero as its default;
				// write the requested wire class explicitly for the negative case.
				raw[0] = tc.class
				if e = s.handleGameUpper(p, raw); e != nil {
					t.Fatal(e)
				}
			})
			ch, _, err = g.Character.Load(nil, "2")
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("%s%s:11:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(s.gameplayNow()))
			if ch.Stats[key] != tc.want {
				t.Fatalf("visit counter=%d want %d", ch.Stats[key], tc.want)
			}
			if len(conn.packets) != 1 {
				t.Fatalf("Cera ACK count=%d", len(conn.packets))
			}
		})
	}
}
