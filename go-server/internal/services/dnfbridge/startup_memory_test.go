package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFStartupCacheReleaseKeepsParsedRewardsAndItemCatalog(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: archive}
	rewards, err := s.currentLevelUpRewardCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantItem, err := items.ResolveItem(3330)
	if err != nil {
		t.Fatal(err)
	}
	wantReward, found := rewards.Reward(85)
	if !found {
		t.Fatal("真实PVF没有预期的85级配置")
	}
	if before := archive.Snapshot(); before.CachedTexts == 0 || before.CachedChunks == 0 {
		t.Fatal("预载未实际形成解码缓存")
	}
	s.releaseStartupPVFReadCaches()
	if after := archive.Snapshot(); after.CachedTexts != 0 || after.CachedChunks != 0 {
		t.Fatalf("启动解码缓存未释放: %+v", after)
	}
	gotRewards, err := s.currentLevelUpRewardCatalog(context.Background())
	if err != nil || gotRewards != rewards {
		t.Fatalf("已解析奖励目录被替换: %v", err)
	}
	if got, found := gotRewards.Reward(85); !found || got != wantReward {
		t.Fatalf("清理改变了PVF奖励: %+v", got)
	}
	gotItems, err := s.currentPVFItemCatalog()
	if err != nil || gotItems != items {
		t.Fatalf("已解析物品目录被替换: %v", err)
	}
	gotItem, err := gotItems.ResolveItem(3330)
	if err != nil || !reflect.DeepEqual(gotItem, wantItem) {
		t.Fatalf("清理改变了物品定义: %v", err)
	}
}
