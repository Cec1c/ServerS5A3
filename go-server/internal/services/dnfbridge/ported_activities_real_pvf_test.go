package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPortedRealPVFAllFourCatalogs(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("真实PVF专项未指定资源")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	s := &Service{options: options{currentClientVersion: "official-24.9"}, initialEquipmentArchive: archive, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	configurePortedActivityTestControls(t, s)
	if err := s.initializePortedActivityPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.preloadPortedActivities(ctx); err != nil {
		t.Fatal(err)
	}
	returnee, lucky, err := s.currentReturneeLuckyCatalogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{47, 48, 49, 50, 51, 52, 53, 20, 21, 22}
	if len(returnee.PendingConditions) != 0 || returnee.Missions[0].ConditionValue != -1 {
		t.Fatalf("现用回归任务条件差异未明确登记：%+v", returnee)
	}
	var rewards []onlineevent.AttendancePVFItemReward
	if returnee.Newbie == nil {
		t.Fatal("现用PVF缺少新手组")
	}
	newIDs := []uint32{40, 41, 42, 43, 44, 45, 46, 17, 18, 19}
	for i, m := range returnee.Newbie.Missions {
		if m.ID != newIDs[i] {
			t.Fatal("新手任务编号不匹配", i, m)
		}
		rewards = append(rewards, m.Reward)
	}
	for i, m := range returnee.Newbie.Attendance {
		if m.ID != uint32(26+i) || m.Required != 1 || m.Condition != "attendance count" {
			t.Fatal("新手签到不匹配", i, m)
		}
		rewards = append(rewards, m.Reward)
	}
	for i, m := range returnee.Attendance {
		if m.ID != uint32(33+i) || m.Required != 1 || m.Condition != "attendance count" {
			t.Fatalf("回归签到%d配置不符：%+v", i, m)
		}
		rewards = append(rewards, m.Reward)
	}
	for i, m := range returnee.Missions {
		if m.ID != want[i] {
			t.Fatalf("回归任务%d=%d", i, m.ID)
		}
		rewards = append(rewards, m.Reward)
	}
	if lucky.Monster != 10 || lucky.Clear != 15 || lucky.Quest != 10 || lucky.Party != 10 {
		t.Fatalf("幸运角色经验%+v", lucky)
	}
	level, err := s.currentLevelAimingCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if level.Job != 8 || level.Limit != 5 || len(level.Stages) != 16 || !level.Ends.IsZero() {
		t.Fatalf("现用预约限制%+v", level)
	}
	rewards = append(rewards, level.Main)
	for _, row := range level.Stages {
		rewards = append(rewards, row.Item)
	}
	legacy, err := s.currentLegacyLevelCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range append(legacy.Main, legacy.Stage...) {
		rewards = append(rewards, row.Item)
	}
	labor, err := s.currentLaborDayCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if labor == nil || !labor.Policy.Ends.IsZero() {
		t.Fatalf("劳动节并非长期配置%+v", labor)
	}
	for i, reward := range labor.Login {
		if reward.ItemID != int64(10005051+i) {
			t.Fatalf("登录奖励%d=%d", i, reward.ItemID)
		}
	}
	rewards = append(rewards, labor.Login[:]...)
	rewards = append(rewards, labor.Online[:]...)
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, reward := range rewards {
		d, err := items.ResolveItem(uint32(reward.ItemID))
		if err != nil {
			t.Fatalf("物品%d: %v", reward.ItemID, err)
		}
		d, err = currentPVFItemDefinitionForGrantAt(d, time.Now())
		if err != nil || (!d.ExpirationDate.IsZero() && !d.ExpirationDate.After(time.Now())) {
			t.Fatalf("物品%d期限无效: %v", reward.ItemID, err)
		}
		if d.Kind != dungeonDropItemStackable && d.Kind != dungeonDropItemEquipment {
			t.Fatalf("奖励%d类型%s未支持", reward.ItemID, d.Kind)
		}
	}
	if _, err := onlineevent.LoadReturneeGiftRules(archive, 2); err != nil {
		t.Fatal(err)
	}
	t.Logf("实际PVF核对：回归任务%d项、签到%d项，预约阶段%d项，旧预约主奖%d项，劳动节登录3档/在线3档，奖励引用%d项；只核对资源与服务端，不算游戏界面验收。", len(returnee.Missions), len(returnee.Attendance), len(level.Stages), len(legacy.Main), len(rewards))
}
