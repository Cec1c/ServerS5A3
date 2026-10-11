package inventory

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件锁死运维报障「490006368 / 490007878 / 490008465 强化的奥秘 完全无法使用
// 不消耗无效果」的服务端根因与修复；判据与 PVF 原文见 use_upgrad_buff.go 顶部。
//
// 线上原文（dnf90.out 3406）：
//
//	msg="dnfbridge use-stackable rejected; sent failure ack" ... item_code=490006368
//	error_code=13 reason="... inventory owner 未落库：use stackable requires
//	PVF-proven [waste] provenance and stable item identity: list=0 slot=103 item=490006368"

const (
	itemUpgradBuffUsualType   = "[item upgrad buff usual upgrade]"
	itemUpgradBuffAmplifyType = "[item upgrad buff amplify upgrade]"
)

func TestPVFProvenUseConsumableKeepsWasteAndAddsUpgradBuffByDefault(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "")
	cases := []struct {
		name          string
		stackableType string
		want          bool
	}{
		{"waste no suffix", "[waste]", true},
		{"waste with stack limit", "[waste] 4", true},
		{"waste with backticks", "`[waste]` 4", true},
		{"reported usual upgrade", itemUpgradBuffUsualType, true},
		{"reported usual upgrade with limit", itemUpgradBuffUsualType + " 1000", true},
		{"amplify upgrade", itemUpgradBuffAmplifyType, true},
		{"unrelated expert town potion", "[expert town potion]", false},
		// 第二轮改动：这两族族名以 waste 打头却吃不到 HasPrefix("[waste]") 兜底，
		// 现在各自按自己的开关放行（语义依据见 use_limited_waste.go）。
		{"unlimited waste", "[unlimited waste]", true},
		{"unlimited waste with limit", "[unlimited waste] 1", true},
		{"waste instant item", "[waste instant item]", true},
		{"waste instant item with limit", "[waste instant item] 1", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stackableTypeIsPVFProvenUseConsumable(tc.stackableType); got != tc.want {
				t.Fatalf("stackableTypeIsPVFProvenUseConsumable(%q) = %t, want %t", tc.stackableType, got, tc.want)
			}
		})
	}
}

func TestPVFProvenUseConsumableLegacySwitchRestoresOldContract(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "legacy")
	// 回退后 [waste] 行为逐字节不变。
	for _, waste := range []string{"[waste]", "[waste] 4", "`[waste]` 4"} {
		if !stackableTypeIsPVFProvenUseConsumable(waste) {
			t.Fatalf("legacy mode rejected %q", waste)
		}
	}
	// 回退后强化/增幅 buff 药水重新落回 fail-closed。
	for _, upgrad := range []string{itemUpgradBuffUsualType, itemUpgradBuffUsualType + " 1000", itemUpgradBuffAmplifyType} {
		if stackableTypeIsPVFProvenUseConsumable(upgrad) {
			t.Fatalf("legacy mode accepted %q", upgrad)
		}
	}
}

func TestPVFProvenWasteStackGatesOnItemKindAndPVFPath(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "")
	base := func() dnfrepo.ItemStack {
		return dnfrepo.ItemStack{
			ItemID: 490006368,
			Count:  1,
			Extra: map[string]string{
				"item_kind":      "stackable",
				"pvf_path":       "stackable/490006001/chn_490006368.stk",
				"stackable_type": itemUpgradBuffUsualType + " 1000",
			},
		}
	}
	if _, _, ok := pvfProvenWasteStack(base()); !ok {
		t.Fatal("upgrad buff stack must be PVF-proven by default")
	}
	noKind := base()
	noKind.Extra["item_kind"] = ""
	if _, _, ok := pvfProvenWasteStack(noKind); ok {
		t.Fatal("stack without item_kind must not pass")
	}
	noPath := base()
	noPath.Extra["pvf_path"] = "   "
	if _, _, ok := pvfProvenWasteStack(noPath); ok {
		t.Fatal("stack without pvf_path must not pass")
	}
	noExtra := base()
	noExtra.Extra = nil
	if _, _, ok := pvfProvenWasteStack(noExtra); ok {
		t.Fatal("stack without Extra must not pass")
	}
}

// TestHandlerUseStackableItemUpgradBuffDecrementsAndReturnsExactAck 是修复的端到端判据：
// 同一个 op44 请求体，修复前 owner 落库失败（bridge 只能回 error_code=13），
// 修复后扣 1 件并回与 [waste] 完全同形的成功 ACK。
func TestHandlerUseStackableItemUpgradBuffDecrementsAndReturnsExactAck(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "78",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 103): {
				ItemID: 490006368,
				Count:  2,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/490006001/chn_490006368.stk",
					"stackable_type": itemUpgradBuffUsualType + " 1000",
				},
			},
		},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x67, 0x00,
			listTypeMain,
			0x22, 0x2B, 0x00, 0x00,
			0x60, 0xE7, 0x34, 0x1D,
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 78,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || got.Operation != "use_stackable" || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	response := got.UpperResponses[0]
	wantBody := []byte{1, 103, 0, listTypeMain, 0x22, 0x2B, 0x00, 0x00, 0x60, 0xE7, 0x34, 0x1D}
	if response.MsgID != uint16(dnfenum.CmdPacketUseStackable) ||
		response.Classification != dnfproto.DefaultChannelClassification ||
		string(response.Body) != string(wantBody) {
		t.Fatalf("response = %+v body=%x want=%x", response, response.Body, wantBody)
	}
	if len(got.ItemSlotRefreshes) != 1 ||
		got.ItemSlotRefreshes[0] != (alignedcmd.ItemSlotRefresh{ListType: listTypeMain, SlotIndex: 103}) {
		t.Fatalf("refreshes = %+v", got.ItemSlotRefreshes)
	}

	loaded := loadTestInventory(t, ctx, repos, "78")
	stack := loaded.Slots[slotKey(listTypeMain, 103)]
	if stack.ItemID != 490006368 || stack.Count != 1 {
		t.Fatalf("stack mutated: %+v", stack)
	}
}

// TestHandlerUseStackableItemUpgradBuffLegacySwitchStaysBlocked 是对照组：
// 回退开关打开时，同一请求重新变成"owner 未落库"，库存一格不动——
// 即证伪"修复只是把拒绝改成成功"这一读法。
func TestHandlerUseStackableItemUpgradBuffLegacySwitchStaysBlocked(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "legacy")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "78",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 103): {
				ItemID: 490006368,
				Count:  2,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/490006001/chn_490006368.stk",
					"stackable_type": itemUpgradBuffUsualType + " 1000",
				},
			},
		},
	})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x67, 0x00,
			listTypeMain,
			0x22, 0x2B, 0x00, 0x00,
			0x60, 0xE7, 0x34, 0x1D,
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 78,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if got.ResponseAllowed || len(got.UpperResponses) != 0 {
		t.Fatalf("legacy result must stay blocked: %+v", got)
	}
	loaded := loadTestInventory(t, ctx, repos, "78")
	if stack := loaded.Slots[slotKey(listTypeMain, 103)]; stack.ItemID != 490006368 || stack.Count != 2 {
		t.Fatalf("legacy mode mutated the stack: %+v", stack)
	}
}

// TestRealPVFItemUpgradBuffStackableTypes 用真档 Script.pvf 复核本文件写死的类型串：
// 只读，且只在 DNFBRIDGE_REAL_PVF_SMOKE 指向真档时运行。
func TestRealPVFItemUpgradBuffStackableTypes(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open pvf: %v", err)
	}
	cases := []struct {
		path string
		want string
	}{
		{"stackable/490006001/chn_490006368.stk", itemUpgradBuffUsualType},
		{"stackable/490007001/chn_490007878.stk", itemUpgradBuffUsualType},
		{"stackable/490008001/chn_490008465.stk", itemUpgradBuffUsualType},
		{"stackable/490001001/chn_490001534.stk", itemUpgradBuffAmplifyType},
	}
	for _, tc := range cases {
		text, err := archive.ReadText(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		doc, err := dnfpvf.Parse(tc.path, text)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.path, err)
		}
		got, ok := doc.Text("stackable type")
		if !ok || !stackableTypeIsItemUpgradBuff(got) || stackableTypeIsItemUpgradBuff(tc.want) == false {
			t.Fatalf("%s stackable type = %q", tc.path, got)
		}
	}
}
