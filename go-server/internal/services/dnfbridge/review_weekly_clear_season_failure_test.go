package dnfbridge

import (
	"context"
	"errors"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type reviewWeeklyActivationFailure struct{ repo.CeraShopAssetUnitOfWork }

func (u reviewWeeklyActivationFailure) WithinCeraShopAssets(ctx context.Context, a, c, scope string, f func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	if scope == onlineevent.WeeklySeasonScope {
		return errors.New("review real season settings transaction unavailable")
	}
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, scope, f)
}
func TestReviewWeeklyEnabledSeasonFailureDoesNotConsumeClearReceipt(t *testing.T) {
	s, r, a, _, _ := prepareCompletedSettlementRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	loadCurrentAdmissionArchive(t, s)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(nil, "99")
	id := ch.AccountID
	a.accountID = id
	a.loginPacketSeen = true
	a.accountFromToken = true
	s.mu.Lock()
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	s.gameSessions[99] = a
	a.characterGeneration = 1
	s.mu.Unlock()
	if e := g.Account.Save(nil, repo.AccountRecord{AccountID: id, Metadata: map[string]string{"keep": "yes"}}); e != nil {
		t.Fatal(e)
	}
	if e := s.initializeActivityControls(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !s.currentWeeklyAttendanceEnabled() {
		t.Fatal("precondition actual weekly catalog enabled")
	}
	ch.Level = 1
	ch.Stats["exp"] = 0
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	if e := g.Skill.Save(nil, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 1}}); e != nil {
		t.Fatal(e)
	}
	if e := g.Inventory.Save(nil, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	r.Character = ch
	r.Dungeon.Metadata.RecommendedLevels = []int64{1, 90}
	r.ordinaryFinalRoomClearAccepted = true
	r.clearMapCompletionKey = "review-weekly-season-unavailable-clear"
	resources := currentDungeonSettlementTestResources(t)
	original := g
	g.CeraShopAssets = reviewWeeklyActivationFailure{g.CeraShopAssets}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	result, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
	after, _, _ := g.Character.Load(nil, "99")
	if e == nil || after.Stats["exp"] != 0 {
		t.Fatalf("enabled season error silently dropped legitimate weekly count while committing exp/receipt: err=%v replay=%v exp=%d", e, result.Replayed, after.Stats["exp"])
	}
	g = original
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	r.Character, _, _ = g.Character.Load(nil, "99")
	result, e = s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
	if e != nil || result.Replayed {
		t.Fatalf("retry %+v %v", result, e)
	}
	season, e := s.currentWeeklySeason(a)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := s.currentWeeklyCatalog(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	query := func() onlineevent.WeeklySnapshot {
		state, e := onlineevent.SettleWeeklyAttendance(nil, g, catalog, onlineevent.WeeklyCommand{AccountID: id, CharacterID: "99", Now: s.gameplayNow(), Season: season, Deliver: func(repo.MailboxRepository, onlineevent.AttendancePVFItemReward, string) (string, error) {
			t.Fatal("query grants mail")
			return "", nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		return state
	}
	if state := query(); state.Clears != 1 {
		t.Fatalf("retry clear %+v", state)
	}
	r.Character, _, _ = g.Character.Load(nil, "99")
	result, e = s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
	if e != nil || !result.Replayed || query().Clears != 1 {
		t.Fatalf("replay%+v %v", result, e)
	}

}

func TestReviewWeeklyUnavailableNonRecommendedDisabledAndExpiredDoNotBlockCore(t *testing.T) {
	for _, mode := range []string{"nonrecommended", "disabled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			s, r, a, _, _ := prepareCompletedSettlementRuntime(t)
			s.options.currentClientVersion = "official-24.9"
			loadCurrentAdmissionArchive(t, s)
			g, _ := s.repositoryGroup()
			ch, _, _ := g.Character.Load(nil, "99")
			id := ch.AccountID
			a.accountID = id
			a.loginPacketSeen = true
			a.accountFromToken = true
			s.mu.Lock()
			if s.gameSessions == nil {
				s.gameSessions = make(map[uint16]*gameSession)
			}
			s.gameSessions[99] = a
			a.characterGeneration = 1
			s.mu.Unlock()
			if e := g.Account.Save(nil, repo.AccountRecord{AccountID: id, Metadata: map[string]string{"keep": "yes"}}); e != nil {
				t.Fatal(e)
			}
			if e := s.initializeActivityControls(context.Background()); e != nil {
				t.Fatal(e)
			}
			if !s.currentWeeklyAttendanceEnabled() {
				t.Fatal("precondition actual weekly catalog enabled")
			}
			ch.Level = 1
			ch.Stats["exp"] = 0
			if e := g.Character.Save(nil, ch); e != nil {
				t.Fatal(e)
			}
			if e := g.Skill.Save(nil, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 1}}); e != nil {
				t.Fatal(e)
			}
			if e := g.Inventory.Save(nil, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); e != nil {
				t.Fatal(e)
			}
			r.Character = ch
			r.Dungeon.Metadata.RecommendedLevels = []int64{1, 90}
			r.ordinaryFinalRoomClearAccepted = true
			r.clearMapCompletionKey = "review-weekly-season-unavailable-clear"
			resources := currentDungeonSettlementTestResources(t)

			var season onlineevent.WeeklySeason
			switch mode {
			case "nonrecommended":
				r.Dungeon.Metadata.RecommendedLevels = nil
				g.CeraShopAssets = reviewWeeklyActivationFailure{g.CeraShopAssets}
			case "disabled":
				var e error
				s.activityControls, e = onlineevent.NewControls(context.Background(), g.Settings, []onlineevent.ControlDefinition{{ID: 2208, Name: "weekly", DefaultEnabled: false}})
				if e != nil {
					t.Fatal(e)
				}
				g.CeraShopAssets = reviewWeeklyActivationFailure{g.CeraShopAssets}
			case "expired":
				var e error
				season, e = s.currentWeeklySeason(a)
				if e != nil {
					t.Fatal(e)
				}
				s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return season.Ends }}
			}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			result, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
			if e != nil || result.Replayed {
				t.Fatalf("mode%s core %+v %v", mode, result, e)
			}
			after, _, _ := g.Character.Load(nil, "99")
			if after.Stats["exp"] != 1 {
				t.Fatal("core exp lost")
			}
			account, _, _ := g.Account.Load(nil, id)
			if mode == "expired" && account.Metadata[season.Key()] != "" {
				t.Fatal("expired counted")
			}
		})
	}
}
