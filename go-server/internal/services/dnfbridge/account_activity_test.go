package dnfbridge

import (
	"context"
	"encoding/json"
	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountActivityFatigueActualDebitRevisitRecoveryAndZero(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Now()
	g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"})
	g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1", Stats: map[string]int64{"fatigue": 2, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(now)}})
	s := &Service{options: options{accountID: "a"}, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{selectedCharacterID: 1}
	r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}}
	for _, x := range []int64{0, 0, 1, 2} {
		if err := s.debitDungeonRoomFatigue(session, r, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: x}}); err != nil {
			t.Fatal(err)
		}
	}
	q := repo.ActivityQuery{AccountID: "a"}
	summary, _ := g.Activity.Summary(ctx, q)
	if summary.Metrics[repo.ActivityFatigue] != 2 {
		t.Fatal(summary)
	}
	ch, _, _ := g.Character.Load(ctx, "1")
	ch.Stats["fatigue"] = 50
	g.Character.Save(ctx, ch)
	if _, err := s.updateCurrentCharacterFatigue(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if err := s.debitDungeonRoomFatigue(session, r, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 3}}); err != nil {
		t.Fatal(err)
	}
	summary, _ = g.Activity.Summary(ctx, q)
	if summary.Metrics[repo.ActivityFatigue] != 3 {
		t.Fatal("药水恢复被计入消耗", summary)
	}
	events, _ := g.Activity.Events(ctx, q)
	if len(events) != 3 || events[0].DungeonID != 310 {
		t.Fatal(events)
	}
}

func TestAccountActivitySpecialAbyssRequiresRuntimeAndAcceptedFinal(t *testing.T) {
	for _, source := range []string{"ordinary", "rift", "epic_road"} {
		t.Run(source, func(t *testing.T) {
			s, session, r := newBackToVillageRuntime(t)
			if source == "rift" {
				r.riftLoot = &currentRiftLootDefinition{}
			}
			if source == "epic_road" {
				r.epicEntry = &currentEpicEntry{}
			}
			g, _ := s.repositoryGroup()
			ctx := context.Background()
			q := repo.ActivityQuery{AccountID: s.accountIDForSession(session)}
			scene := r.Session.Snapshot().Scene
			if err := s.recordCurrentActivityAbyss(session, r, scene, true); err != nil {
				t.Fatal(err)
			}
			before, _ := g.Activity.Summary(ctx, q)
			if before.Metrics[repo.ActivityAbyss] != 0 {
				t.Fatal("未完成提前记账")
			}
			if err := r.Session.CompleteCurrentRoom(); err != nil {
				t.Fatal(err)
			}
			scene = r.Session.Snapshot().Scene
			if err := s.recordCurrentActivityAbyss(session, r, scene, true); err != nil {
				t.Fatal(err)
			}
			before, _ = g.Activity.Summary(ctx, q)
			if before.Metrics[repo.ActivityAbyss] != 0 {
				t.Fatal("缺少权威终局仍记账")
			}
			r.bossDieCheckAccepted = true
			for i := 0; i < 2; i++ {
				if err := s.recordCurrentActivityAbyss(session, r, scene, true); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := g.Activity.Summary(ctx, q)
			want := int64(1)
			if source == "ordinary" {
				want = 0
			}
			if after.Metrics[repo.ActivityAbyss] != want {
				t.Fatal(source, after)
			}
			events, _ := g.Activity.Events(ctx, q)
			for _, event := range events {
				if event.Metric == repo.ActivityAbyss && event.Source != source {
					t.Fatal(event)
				}
			}
		})
	}
}

func TestAccountActivityHellLastWaveCountsWithoutBossAndReplay(t *testing.T) {
	s, session, _, r, scene := prepareHellPartyClearSettlementRuntime(t)
	r.HellPartyStarted = true
	g, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("缺少仓储")
	}
	q := repo.ActivityQuery{AccountID: s.accountIDForSession(session)}
	if err := s.recordCurrentActivityAbyss(session, r, scene, false); err != nil {
		t.Fatal(err)
	}
	summary, _ := g.Activity.Summary(context.Background(), q)
	if summary.Metrics[repo.ActivityAbyss] != 0 {
		t.Fatal("未击败深渊已计入")
	}
	killEntireHellRoomAndWaves(t, s, session, r)
	summary, err := g.Activity.Summary(context.Background(), q)
	if err != nil || summary.Metrics[repo.ActivityAbyss] != 1 {
		t.Fatalf("末波未记账：%+v %v", summary, err)
	}
	if r.bossDieCheckAccepted || r.settlementEntrySent {
		t.Fatal("统计改变了地下城结算")
	}
	if err := s.recordCurrentActivityAbyss(session, r, scene, false); err != nil {
		t.Fatal(err)
	}
	summary, _ = g.Activity.Summary(context.Background(), q)
	if summary.Metrics[repo.ActivityAbyss] != 1 {
		t.Fatal("重放重复计数")
	}
}

func TestAccountActivityPickupClassificationAndNativeAPI(t *testing.T) {
	g := memory.NewMemoryGroup()
	g.Character.Save(context.Background(), repo.CharacterRecord{AccountID: "a", CharacterID: "1"})
	s := &Service{options: options{accountID: "a", playerAdminSecret: strings.Repeat("x", 32)}, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{selectedCharacterID: 1}
	r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}}
	for _, rarity := range []int64{0, 1, 2, 3, 4, 5, 6} {
		drop := runtimeDungeonDrop{ObjectKey: 100, Item: dungeonDropItemDefinition{Kind: dungeonDropItemEquipment, ItemID: 100, Rarity: rarity}}
		e, err := s.currentActivityPickupPartyLocked(session, r, drop)
		if err != nil {
			t.Fatal(err)
		}
		if (e != nil) != (rarity >= 3 && rarity <= 5) {
			t.Fatalf("品质%d错误", rarity)
		}
		drop.DiscardOrigin = &runtimeDungeonDiscardOrigin{}
		e, err = s.currentActivityPickupPartyLocked(session, r, drop)
		if err != nil || e != nil {
			t.Fatal("弃置恢复参与统计")
		}
	}
	api := &accountAPIServer{service: s}
	for _, auth := range []string{"", s.options.playerAdminSecret} {
		req := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(`{"version":1,"action":"account_activity","activityQuery":{"account_id":"a"}}`))
		req.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		api.handleGameAdmin(w, req)
		if auth == "" {
			if w.Code != 404 {
				t.Fatal("未鉴权访问")
			}
			continue
		}
		var out adminapi.Response
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out.OK || out.ActivitySummary == nil || out.ActivitySummary.AccountID != "a" {
			t.Fatal(w.Body.String(), err)
		}
	}
}

func TestAccountActivityPartyHellCreditsEachPresentMemberOnce(t *testing.T) {
	s, leader, follower, _, r, scene := partyHellPartyWaveFixture(t)
	r.HellPartyStarted = true
	g, _ := s.repositoryGroup()
	account := s.accountIDForSession(leader)
	follower.accountID = account
	if err := g.Character.Save(context.Background(), repo.CharacterRecord{AccountID: account, CharacterID: "43", Name: "队员"}); err != nil {
		t.Fatal(err)
	}
	instance := r.partyInstance
	instance.mu.Lock()
	if instance.scenes == nil {
		instance.scenes = map[uint16]*worldmap.DungeonSession{}
	}
	instance.scenes[43] = r.Session
	instance.mu.Unlock()
	killEntireHellRoomAndWaves(t, s, leader, r)
	q := repo.ActivityQuery{AccountID: account}
	summary, err := g.Activity.Summary(context.Background(), q)
	if err != nil || summary.Metrics[repo.ActivityAbyss] != 2 {
		t.Fatalf("组队贡献错误：%+v %v", summary, err)
	}
	view := *r
	view.activityAbyssRecorded = false
	if err := s.recordCurrentActivityAbyss(follower, &view, scene, false); err != nil {
		t.Fatal(err)
	}
	summary, _ = g.Activity.Summary(context.Background(), q)
	if summary.Metrics[repo.ActivityAbyss] != 2 {
		t.Fatal("队友重放重复计入", summary)
	}
}

func TestAccountActivityPickupIdentityIncludesRoomAndFreshRun(t *testing.T) {
	g := memory.NewMemoryGroup()
	s := &Service{options: options{accountID: "a"}, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{selectedCharacterID: 1}
	r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}}
	drop := runtimeDungeonDrop{ObjectKey: 10, Room: runtimeDungeonRoomKey{MapID: 1}, Amount: 1, Item: dungeonDropItemDefinition{Kind: dungeonDropItemEquipment, ItemID: 100, Rarity: 4}}
	first, e := s.currentActivityPickupPartyLocked(session, r, drop)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.currentActivityPickupPartyLocked(session, r, drop)
	if e != nil || first.ID != replay.ID {
		t.Fatal("同一掉落身份不稳定")
	}
	drop.Room.X = 1
	other, e := s.currentActivityPickupPartyLocked(session, r, drop)
	if e != nil || first.ID == other.ID {
		t.Fatal("不同房间同编号冲突")
	}
	nextRun := &runtimeDungeonState{Dungeon: r.Dungeon}
	next, e := s.currentActivityPickupPartyLocked(session, nextRun, drop)
	if e != nil || other.ID == next.ID {
		t.Fatal("独立新局身份冲突")
	}
}

func TestAccountActivityParticipantRequiresMatchingMapAndCoordinate(t *testing.T) {
	_, _, _, _, scene := prepareHellPartyClearSettlementRuntime(t)
	if !currentActivitySameRoom(scene, scene) {
		t.Fatal("同房间被排除")
	}
	other := scene
	other.Coordinate.X++
	if currentActivitySameRoom(scene, other) {
		t.Fatal("不同坐标计入")
	}
	other = scene
	other.Map.Map.ID++
	if currentActivitySameRoom(scene, other) {
		t.Fatal("不同层房图计入")
	}
}
