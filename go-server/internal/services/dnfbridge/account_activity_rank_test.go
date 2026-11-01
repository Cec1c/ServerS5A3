package dnfbridge

import (
	"context"
	"encoding/json"
	"longheng.io/server/internal/modules/dnf/adminapi"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountActivityRankPrivateAPIAndGoldSummary(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	ch := repo.CharacterRecord{AccountID: "a", CharacterID: "1", Name: "角色"}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
		if err := repo.RecordGoldActivity(ctx, tx.Activity, ch, repo.ActivityGoldRepair, 123, time.Now()); err != nil {
			return err
		}
		for i := 0; i < 8; i++ {
			_, err := tx.Activity.Record(ctx, repo.ActivityEvent{ID: "rank-" + strconv.Itoa(i), AccountID: "a", CharacterID: "1", Metric: repo.ActivityFatigue, Amount: 1, DungeonID: 310, Source: "dungeon_room", OccurredAt: time.Now()})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("x", 32)
	api := &accountAPIServer{service: &Service{options: options{playerAdminSecret: secret}, repositoryProvider: func() (repo.Group, bool) { return g, true }}}
	for _, action := range []string{"activity_rank", "activity_rank_settle", "activity_rank_rewards"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/game/admin", strings.NewReader(`{"version":1,"action":"`+action+`"}`))
		api.handleGameAdmin(w, r)
		if w.Code != 404 {
			t.Fatal("匿名访问榜单管理接口", action, w.Code)
		}
	}
	for _, body := range []string{
		`{"version":1,"action":"activity_rank","rankQuery":{"metric":"fatigue_spent","period":"day","account_id":"a"}}`,
		`{"version":1,"action":"account_activity","activityQuery":{"account_id":"a"}}`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/game/admin", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		api.handleGameAdmin(w, r)
		var out adminapi.Response
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.OK {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(body, `"activity_rank"`) {
			if out.RankBoard == nil || out.RankBoard.Self == nil || out.RankBoard.Self.Score != 8 || out.RankBoard.Entries[0].Characters[0].Score != 8 {
				t.Fatal(out)
			}
		} else {
			if out.GoldSummary == nil || out.GoldSummary.Spent != 123 || out.CharacterGold["1"].Spent != 123 {
				t.Fatal(out)
			}
		}
	}
}
