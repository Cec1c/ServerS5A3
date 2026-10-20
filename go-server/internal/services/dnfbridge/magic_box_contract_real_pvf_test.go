package dnfbridge

import (
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实运行时 PVF 回归：契约奖励必须在**发放期**就被识别成"账号契约时长"，而不是
// 普通可堆叠道具。
//
// 现场（2026-09-25 香港 packet_log）：
//   - 操作者开 490006574「王者契约礼包 (1天)」，服务端一次抽出四条契约
//     `rewards=[{30 1} {43 1} {2660703 1} {10000391 1}]`（霸王/达人/成长/晶之）；
//   - 赛丽亚的幸运（2682272）十连同理，背包满时这些契约被写进
//     dnf_mails「背包已满：礼盒奖励」（库里现存 76 封）而不是叠加时长。
//
// 契约身份唯一来源 = etc/premiumlist_new.etc 的 [type]/[item]/[term]；本用例把
// "奖励模板 id → (premium type, 秒)" 这条映射钉在真实 PVF 上。
func TestRealPVFMagicBoxContractRewardMapping(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the runtime-PVF contract reward mapping")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	premiumCatalog, err := buildCurrentPremiumCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	// 现场那只礼包的奖励池：[booster info] 下四条 [cera] 行，单开每组各抽 1。
	boosterResolution, err := resolveCurrentMagicBox(catalog, archive, 490006574)
	if err != nil {
		t.Fatalf("resolve box 490006574: %v", err)
	}
	drawn := make(map[int64]bool)
	for _, group := range boosterResolution.Groups {
		for _, entry := range group.Entries {
			drawn[entry.ItemID] = true
		}
	}
	for _, itemID := range []int64{30, 43, 2660703, 10000391} {
		if !drawn[itemID] {
			t.Fatalf("box 490006574 no longer offers contract item=%d (drawn=%v)", itemID, drawn)
		}
	}

	// 赛丽亚的幸运本身（packageitem 魔盒路径，现场 op1128 box=2682272 opens=10；
	// packageitem.currentSeriaLuckItemID 就是这一只）：它的奖励池里只要有契约，
	// 就必须走"发放期分流成账号时长"。
	const seriaLuckItemID = int64(2682272)
	seriaResolution, err := resolveCurrentMagicBox(catalog, archive, seriaLuckItemID)
	if err != nil {
		t.Fatalf("resolve Seria box %d: %v", seriaLuckItemID, err)
	}
	if seriaResolution.Kind != "random" {
		t.Fatalf("Seria box kind = %q, want random", seriaResolution.Kind)
	}
	// 该 PVF 的赛丽亚池里就是操作者报障的那几种契约（报障原话：晶之/达人之/成长之）。
	// 4 种 1 天 + 4 种 3 天，共 7 条。
	seriaWant := map[int64]struct {
		premiumType     int64
		durationSeconds int64
	}{
		30:       {premiumType: premium.TypeOverEquip, durationSeconds: 86400},
		2660703:  {premiumType: premium.TypeBonusExp, durationSeconds: 86400},
		10000391: {premiumType: premium.TypeCrystal, durationSeconds: 86400},
		44:       {premiumType: premium.TypeOverSkill, durationSeconds: 259200},
		31:       {premiumType: premium.TypeOverEquip, durationSeconds: 259200},
		2660409:  {premiumType: premium.TypeBonusExp, durationSeconds: 259200},
		10000388: {premiumType: premium.TypeCrystal, durationSeconds: 259200},
	}
	seriaPool := make(map[int64]bool)
	for _, group := range seriaResolution.Groups {
		for _, entry := range group.Entries {
			seriaPool[entry.ItemID] = true
		}
	}
	for itemID, expected := range seriaWant {
		if !seriaPool[itemID] {
			t.Fatalf("Seria box pool no longer offers contract item=%d (pool=%v)", itemID, seriaPool)
		}
		if premiumType, seconds := currentMagicBoxRewardContract(premiumCatalog, itemID); premiumType != expected.premiumType || seconds != expected.durationSeconds {
			t.Fatalf("Seria contract item=%d premium=(%d,%d), want (%d,%d)",
				itemID, premiumType, seconds, expected.premiumType, expected.durationSeconds)
		}
	}
	for _, group := range seriaResolution.Groups {
		for _, entry := range group.Entries {
			if _, isContract := seriaWant[entry.ItemID]; !isContract {
				continue
			}
			t.Logf("seria box reward contract item=%d premium_type=%d seconds=%d",
				entry.ItemID, seriaWant[entry.ItemID].premiumType, seriaWant[entry.ItemID].durationSeconds)
		}
	}

	want := map[int64]struct {
		premiumType     int64
		durationSeconds int64
	}{
		30:       {premiumType: premium.TypeOverEquip, durationSeconds: 86400}, // 霸王之契约 (1天)
		43:       {premiumType: premium.TypeOverSkill, durationSeconds: 86400}, // 达人之契约
		2660703:  {premiumType: premium.TypeBonusExp, durationSeconds: 86400},  // 成长之契约
		10000391: {premiumType: premium.TypeCrystal, durationSeconds: 86400},   // 晶之契约
	}
	for itemID, expected := range want {
		rewardItem := resolveMagicBoxRewardItemForTest(t, catalog, archive, itemID, premiumCatalog)
		if rewardItem.PremiumType != expected.premiumType || rewardItem.PremiumDurationSeconds != expected.durationSeconds {
			t.Fatalf("reward item=%d premium=(%d,%d), want (%d,%d)",
				itemID, rewardItem.PremiumType, rewardItem.PremiumDurationSeconds,
				expected.premiumType, expected.durationSeconds)
		}
		if rewardItem.Kind != string(dungeonDropItemStackable) {
			t.Fatalf("reward item=%d kind=%q, want stackable", itemID, rewardItem.Kind)
		}
		info := premiumCatalog.contractsByItem[itemID]
		t.Logf("contract item=%d path=%s premium_type=%d seconds=%d",
			itemID, rewardItem.PVFPath, info.PremiumType, info.DurationSeconds)
	}

	// 非契约奖励不得被误判成契约（否则会被错误地"不占格"）。
	for _, itemID := range []int64{2600014, 10007367, 2683898} {
		rewardItem := resolveMagicBoxRewardItemForTest(t, catalog, archive, itemID, premiumCatalog)
		if rewardItem.PremiumType != 0 || rewardItem.PremiumDurationSeconds != 0 {
			t.Fatalf("non-contract reward item=%d was stamped as contract (type=%d seconds=%d)",
				itemID, rewardItem.PremiumType, rewardItem.PremiumDurationSeconds)
		}
	}
}

// resolveMagicBoxRewardItemForTest mirrors the service resolver closure: resolve
// the grant template through the runtime PVF and stamp the contract mapping that
// grant-time code branches on.
func resolveMagicBoxRewardItemForTest(
	t *testing.T,
	catalog *pvfDungeonDropCatalog,
	archive *platformpvf.Archive,
	itemID int64,
	premiumCatalog *currentPremiumCatalog,
) alignedcmd.MagicBoxRewardItem {
	t.Helper()
	item, err := resolveCurrentMagicBoxRewardItem(catalog, archive, itemID)
	if err != nil {
		t.Fatalf("resolve reward item=%d: %v", itemID, err)
	}
	item.PremiumType, item.PremiumDurationSeconds = currentMagicBoxRewardContract(premiumCatalog, itemID)
	return item
}
