package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 操作者第 14 条：灿烂徽章礼盒「能获得道具但弹窗物品栏空间不足（最后一个会报错）」。
//
// 现场（HK 线上 192.0.2.11，`/opt/dnf90/logs/packet_log.txt`，02:5x，角色 6）：
//
//	game-current-booster-rejected conn_id=game-000018 source_slot=97
//	  selected_item=10095130 selected_count=1 request_kind=1
//	  reason=dnf booster runtime PVF data is invalid:
//	         source item=490700910: duplicate booster selection category=1031
//
// op160 的失败体恒为 `00 04`（gameplay_module_booster.go 的四处失败出口），而客户端把
// body[1] = 4 渲染成「物品栏空间不足」（厂商 Network/Builders/CeraShop/
// CeraShopPurchaseAckBuilder.cs:8-13；Game/Inventory/InventoryModels.cs:385-393 的
// BoosterUseResult.ErrorInventoryFull = 0x04）。也就是说：**弹窗文案与真实原因无关**，
// 真正的拒绝发生在运行时 PVF 解析阶段。
//
// 真档形状（本机 `runtime/data/dnf/Script.pvf`，与线上同一份礼盒）：
// `stackable/cash/chn_490700910.stk` 有 61 个 `[booster select category]` 块、只有 60 个
// 不同 id —— `7 4`（id 1031）出现两次，第二段是 `[stackable]` 空占位块。旧代码对重复
// id 直接返回 `duplicate booster selection category=1031`，于是整只礼盒被判非法 PVF。
func TestCurrentBoosterSelectionCatalogMergesRepeatedCategoryPlaceholder(t *testing.T) {
	const document = "[stackable type]\n`[booster selection]` 0\n" +
		"[booster selection num]\n1\n" +
		"[booster select category]\n0 0\n[stackable]\n2540007 1 10095130 1\n[/stackable]\n[/booster select category]\n" +
		"[booster select category]\n0 0\n[stackable]\n[/stackable]\n[/booster select category]\n"
	parsed, err := dnfpvf.Parse("stackable/cash/chn_490700910.stk", document)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	t.Setenv(boosterDuplicateSelectionCategoryEnv, "")
	candidates, categories, err := parseCurrentBoosterSelectionCatalog(parsed)
	if err != nil {
		t.Fatalf("empty repeated placeholder block must merge, got error: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%d want 2", len(candidates))
	}
	if got := categories[0]; len(got) != 2 {
		t.Fatalf("category 0 rows=%d want 2 (merged)", len(got))
	}

	// 一键回退档：逐字节回到今天的行为 —— 整份文档判非法，错误串与线上一致。
	t.Setenv(boosterDuplicateSelectionCategoryEnv, "reject")
	_, _, err = parseCurrentBoosterSelectionCatalog(parsed)
	if err == nil {
		t.Fatal("reject档必须复现旧行为（duplicate booster selection category）")
	}
	if !strings.Contains(err.Error(), "duplicate booster selection category=0") {
		t.Fatalf("reject档错误串=%q want duplicate booster selection category=0", err.Error())
	}
	// 别名也回退（运维习惯写法）。
	for _, alias := range []string{"0", "false", "off", "legacy"} {
		t.Setenv(boosterDuplicateSelectionCategoryEnv, alias)
		if boosterDuplicateSelectionCategoryMergeEnabled() {
			t.Fatalf("alias %q must roll back to reject", alias)
		}
	}
	t.Setenv(boosterDuplicateSelectionCategoryEnv, "merge")
	if !boosterDuplicateSelectionCategoryMergeEnabled() {
		t.Fatal("merge must enable the fixed read")
	}
}

// 真档回归：与线上拒绝理由逐字对应的那只礼盒必须能解析出全部 60 个 category，
// 并且 `7 4`（id 1031）这个被重复声明的 category 必须在表里。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<repo>\runtime\data\dnf\Script.pvf go test -count=1 \
//
//	-run RealPVF -v ./internal/services/dnfbridge
func TestRealPVFEmblemBoosterBoxRepeatedCategoryParses(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	const path = "stackable/cash/chn_490700910.stk"
	text, err := archive.ReadText(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	parsed, err := dnfpvf.Parse(path, text)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	t.Setenv(boosterDuplicateSelectionCategoryEnv, "")
	_, categories, err := parseCurrentBoosterSelectionCatalog(parsed)
	if err != nil {
		t.Fatalf("real box %s must parse with merge档: %v", path, err)
	}
	if len(categories) != 60 {
		t.Fatalf("category count=%d want 60 (61 blocks, id 1031 repeated)", len(categories))
	}
	if _, ok := categories[1031]; !ok {
		t.Fatal("repeated category id 1031 (header `7 4`) must exist after the merge")
	}
	t.Setenv(boosterDuplicateSelectionCategoryEnv, "reject")
	if _, _, err := parseCurrentBoosterSelectionCatalog(parsed); err == nil ||
		!strings.Contains(err.Error(), "duplicate booster selection category=1031") {
		t.Fatalf("reject档 error=%v want the live string duplicate booster selection category=1031", err)
	}
}
