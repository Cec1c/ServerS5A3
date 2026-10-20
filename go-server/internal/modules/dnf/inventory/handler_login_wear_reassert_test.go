package inventory

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 登录后"武器被自动卸下"那条 op19 的域侧钉子。
//
// 现场（`_dist/weapon-unequip-diagnosis.md` §1.5，国内服 conn `game-001200`，角色 16，
// ≈15:09:46 CST）：同一会话两条**逐字节同形**的 28 字节 A 族 op19
//
//	01130029000000…00000a00 00000000 01000000 03 0c00 6dc10506 00000000 ffffffff 0000
//	src=(list0 主背包, slot10, IV=0) count=1 dst=(list3 装备, slot12, IV=101040493)
//
// 第一条判 noop（`noopGate=login_wear_reassert_noop`），第二条真落库卸下
// （`equipMove=unequip changed=true`）—— 差别只在桥侧的一次性窗口，不在域侧判据。
// 因此本文件钉两件事：
//
//  1. **只要窗口标记被透传**（`Request.LoginWearWindowOpen`），第 N 条同形包仍然是 noop、
//     库里一个字节都不动（桥侧现在对该族可重复命中，见
//     dnfbridge/equip_login_wear_family_window.go）；
//  2. 命中登录兜底闸门的 noop，在 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH 的
//     默认档下要照发真落库路径那份权威刷新（整容器动作 + mode0 外观）；开关 off 时
//     逐字节回到今天（PostActions 为空 ⇒ 桥侧只发一条 msg_id=19）。
//
// 反证（本文件的 "window closed" 用例）：同一条 body 只要窗口标记为假，就走了
// equip/owner.go:1046 的反转兜底，武器真的进背包 —— 这说明上面两条断言不是空转。
const (
	loginWearTestCharacterID  = 77
	loginWearTestWeaponItemID = int32(101040493)
)

// loginWearReassertBody 是现场 A 形状（客户端把身份写在装备端点、背包端点实例为 0）。
func loginWearReassertBody() []byte {
	return currentMoveItemspaceBody(listTypeMain, 10, 0, 1, listTypeEquipment, 12, loginWearTestWeaponItemID, 0, -1)
}

// loginWearIdentityEchoBody 是现场的 B 形状（客户端把身份写在背包端点，
// equipmentEndpointIdentityEcho 的厂商同源判据负责它）。它必须继续逐字节等于今天：
// 同一个 noop 结果、**不带**任何补发的权威刷新。
func loginWearIdentityEchoBody() []byte {
	return currentMoveItemspaceBody(listTypeMain, 10, loginWearTestWeaponItemID, 1, listTypeEquipment, 12, 0, 0, -1)
}

// newLoginWearReassertFixture 造一个"武器穿在槽 12、背包槽 10 为空"的角色。
func newLoginWearReassertFixture(t *testing.T) (context.Context, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc",
		Name:        "Actor77",
		Job:         "0",
		Level:       90,
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{},
	})
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: int64(loginWearTestWeaponItemID), RawEntry: []byte{0x70}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, repos
}

func loginWearReassertRequest(body []byte, repos dnfrepo.Group, windowOpen bool, noopRefresh bool) alignedcmd.Request {
	return alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "acc",
		SelectedCharacterID: loginWearTestCharacterID,
		Repositories:        repos,
		EquipmentPlacement:  func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
		// 桥侧 fill：窗口标记与 noop 刷新开关（见 dnfbridge/game_aligned.go 的调用点）。
		LoginWearWindowOpen:          windowOpen,
		LoginWearReassertNoopRefresh: noopRefresh,
	}
}

func handleLoginWearReassert(t *testing.T, ctx context.Context, request alignedcmd.Request) alignedcmd.Result {
	t.Helper()
	result, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !result.Handled || !result.ResponseAllowed || len(result.UpperResponses) != 1 {
		t.Fatalf("result = %+v", result)
	}
	return result
}

// enableLoginWearReassertNoop 打开 equip 域那条兜底开关本身
// （DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP，线上 /opt/dnf90/dnf90.env:67 = 1）。
// 桥侧窗口标记只是"输入"，没有这个开关闸门一律不生效。
func enableLoginWearReassertNoop(t *testing.T) {
	t.Helper()
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
}

// TestHandlerLoginWearReassertNoopGuardsEveryIdenticalPacket 是**现场回归本身**：
// 同一会话、同一条逐字节相同的 A 族包发两次，两次都必须只被判 noop、库里一个字节不动。
func TestHandlerLoginWearReassertNoopGuardsEveryIdenticalPacket(t *testing.T) {
	enableLoginWearReassertNoop(t)
	ctx, repos := newLoginWearReassertFixture(t)
	body := loginWearReassertBody()
	wantActions := []alignedcmd.PostAction{
		alignedcmd.PostActionRefreshSelectedWearContainers,
		alignedcmd.PostActionRefreshSelectedActorAppearance,
	}
	var firstAck []byte
	for round := 1; round <= 2; round++ {
		result := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, true, true))
		if !strings.Contains(result.Reason, "equipMove=noop") {
			t.Fatalf("round %d must stay a noop, reason = %q", round, result.Reason)
		}
		// 闸门名由 equip 域写出，常量在 equip.LoginWearReassertNoopGate。
		// 这条断言同时钉住 owner.go 里的字面量与本常量不许漂移。
		if !strings.Contains(result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("round %d reason = %q, want noopGate=%s", round, result.Reason, dnfequip.LoginWearReassertNoopGate)
		}
		if !strings.Contains(result.Reason, "changed=false") {
			t.Fatalf("round %d must not write the database, reason = %q", round, result.Reason)
		}
		if !reflect.DeepEqual(result.PostActions, wantActions) {
			t.Fatalf("round %d post actions = %v, want %v", round, result.PostActions, wantActions)
		}
		ack := result.UpperResponses[0].Body
		if len(ack) == 0 || ack[0] != 1 {
			t.Fatalf("round %d ack body = %x", round, ack)
		}
		if round == 1 {
			firstAck = append([]byte(nil), ack...)
		} else if !bytes.Equal(ack, firstAck) {
			t.Fatalf("the second identical packet got a different ACK: %x vs %x", ack, firstAck)
		}
		// 武器仍在槽 12、背包槽 10 仍为空：这就是"没有自动卸下"。
		equipment, ok, err := repos.Equipment.Load(ctx, "77")
		if err != nil || !ok {
			t.Fatalf("round %d equipment load ok=%t err=%v", round, ok, err)
		}
		if entry := equipment.Entries["12"]; entry.ItemID != int64(loginWearTestWeaponItemID) {
			t.Fatalf("round %d weapon left slot 12: %+v", round, entry)
		}
		if stack, occupied := loadTestInventory(t, ctx, repos, "77").Slots[slotKey(listTypeMain, 10)]; occupied {
			t.Fatalf("round %d bag slot 10 became occupied: %+v", round, stack)
		}
	}
}

// TestHandlerLoginWearReassertNoopIsWhatPreventsTheUnequip 是上一条的反证：
// 同一条 body、同一份库，只要窗口标记为假（= 今天的窗口外情形），武器就会被真卸下。
func TestHandlerLoginWearReassertNoopIsWhatPreventsTheUnequip(t *testing.T) {
	enableLoginWearReassertNoop(t)
	// 2026-09-28 第五轮：这条 A 形状（装备端点点名穿着的那件 + 源端实例 0）现在由
	// **方向判定本身**判 noop（equip/worn_slot_reassert_gate.go，与窗口无关）。
	// 本用例的主题是登录窗口闸门，所以把方向判定显式写回回退字面量，
	// 「窗口标记为假 ⇒ 真卸下」这句话才成立。
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT", "off")
	ctx, repos := newLoginWearReassertFixture(t)
	result := handleLoginWearReassert(t, ctx, loginWearReassertRequest(loginWearReassertBody(), repos, false, true))
	if !strings.Contains(result.Reason, "equipMove=unequip") || !strings.Contains(result.Reason, "changed=true") {
		t.Fatalf("window closed must reproduce the live unequip, reason = %q", result.Reason)
	}
	equipment, ok, err := repos.Equipment.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("equipment load ok=%t err=%v", ok, err)
	}
	if _, stillWorn := equipment.Entries["12"]; stillWorn {
		t.Fatalf("window-closed control did not unequip: %+v", equipment.Entries)
	}
	moved := false
	for _, stack := range loadTestInventory(t, ctx, repos, "77").Slots {
		if stack.ItemID == int64(loginWearTestWeaponItemID) {
			moved = true
		}
	}
	if !moved {
		t.Fatal("window-closed control lost the weapon instead of moving it to the bag")
	}
}

// TestHandlerWornSlotJudgementNoopCarriesTheAuthoritativeRefresh 是 2026-09-28 第五轮
// 加的那一半：方向判定本身的 noop（窗口各层放行之后由形状判据接管）**也要**照真落库
// 路径带权威刷新，否则客户端那份幻影副本（以为背包那一格还有一件 / 穿戴格还穿着）
// 得不到任何纠正，下一次登录再复述同一句。
//
// 判据只有一处真相：equip.IsWornClaimNoopGate（登录窗口兜底 + 方向判定两条）。
// 这一条走的是**窗口标记为假**（= 窗口层没吸收）的路径，所以它命中的必然是
// worn_slot_reassert；同一个开关（DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH）
// 的两侧与旧闸门一致：on 带两条后置动作，off 只有一条 ACK。
func TestHandlerWornSlotJudgementNoopCarriesTheAuthoritativeRefresh(t *testing.T) {
	enableLoginWearReassertNoop(t)
	ctx, repos := newLoginWearReassertFixture(t)
	body := loginWearReassertBody()

	on := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, false, true))
	if !strings.Contains(on.Reason, "equipMove=noop") ||
		!strings.Contains(on.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
		t.Fatalf("judgement noop missing: %q", on.Reason)
	}
	want := []alignedcmd.PostAction{
		alignedcmd.PostActionRefreshSelectedWearContainers,
		alignedcmd.PostActionRefreshSelectedActorAppearance,
	}
	if !reflect.DeepEqual(on.PostActions, want) {
		t.Fatalf("judgement noop post actions = %v, want %v (same family as the window gate)", on.PostActions, want)
	}
	if !strings.Contains(on.Reason, "login_wear_noop_refresh=authoritative_worn_state_without_item_write") {
		t.Fatalf("judgement noop reason must carry the refresh marker, got %q", on.Reason)
	}

	off := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, false, false))
	if len(off.PostActions) != 0 {
		t.Fatalf("the refresh switch off must leave only the ACK, got %v", off.PostActions)
	}
	if !bytes.Equal(on.UpperResponses[0].Body, off.UpperResponses[0].Body) {
		t.Fatalf("the switch must not change the ACK: on=%x off=%x", on.UpperResponses[0].Body, off.UpperResponses[0].Body)
	}
	// 两档都不许写库：方向判定永远只是"把权威状态说出来"。
	equipment, _, err := repos.Equipment.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if entry := equipment.Entries["12"]; entry.ItemID != int64(loginWearTestWeaponItemID) {
		t.Fatalf("the judgement noop must not move the weapon: %+v", entry)
	}
}

// TestHandlerLoginWearNoopRefreshSwitchOnlyAddsTheAuthoritativeRefresh 钉住开关的两侧：
//
//	on  = 命中闸门的 noop 照发真落库路径那两条后置动作；
//	off = 逐字节回到今天（PostActions 为空、ACK 完全不变、Reason 里没有补发标记）。
//
// 2026-09-28 第五轮追加：判据从"只认登录窗口兜底"改成 equip.IsWornClaimNoopGate 之后，
// **旧闸门这一档的字节必须一个字都不变** —— 所以下面额外与"把方向判定写回回退字面量"
// 的那一次跑对照（两者都命中 login_wear_reassert_noop，输出必须完全相同）。
func TestHandlerLoginWearNoopRefreshSwitchOnlyAddsTheAuthoritativeRefresh(t *testing.T) {
	enableLoginWearReassertNoop(t)
	ctx, repos := newLoginWearReassertFixture(t)
	body := loginWearReassertBody()

	on := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, true, true))
	off := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, true, false))

	wantOn := []alignedcmd.PostAction{
		alignedcmd.PostActionRefreshSelectedWearContainers,
		alignedcmd.PostActionRefreshSelectedActorAppearance,
	}
	if !reflect.DeepEqual(on.PostActions, wantOn) {
		t.Fatalf("on post actions = %v, want %v", on.PostActions, wantOn)
	}
	if len(off.PostActions) != 0 {
		t.Fatalf("off must not add any post action, got %v", off.PostActions)
	}
	if !bytes.Equal(on.UpperResponses[0].Body, off.UpperResponses[0].Body) {
		t.Fatalf("the switch must not change the ACK: on=%x off=%x", on.UpperResponses[0].Body, off.UpperResponses[0].Body)
	}
	if !strings.Contains(on.Reason, "login_wear_noop_refresh=") {
		t.Fatalf("on reason must carry the live marker, got %q", on.Reason)
	}
	if strings.Contains(off.Reason, "login_wear_noop_refresh=") {
		t.Fatalf("off reason must stay byte-identical to today, got %q", off.Reason)
	}
	// off 档的 Reason 只允许与 on 档差两处：没有补发标记，且 appearance_refresh
	// 如实报告"这次没补外观刷新"。
	wantOff := strings.Replace(on.Reason,
		" login_wear_noop_refresh=authoritative_worn_state_without_item_write post_actions=2", "", 1)
	wantOff = strings.Replace(wantOff, "appearance_refresh=true", "appearance_refresh=false", 1)
	if off.Reason != wantOff {
		t.Fatalf("off reason = %q\nwant       = %q", off.Reason, wantOff)
	}
	// 旧闸门的字节与"方向判定回退档"逐字段相同：新判据是**加**上去的，没有动它。
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT", "off")
	legacy := handleLoginWearReassert(t, ctx, loginWearReassertRequest(body, repos, true, true))
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT", "")
	if !strings.Contains(legacy.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("the window gate must keep owning the in-window noop: %q", legacy.Reason)
	}
	if legacy.Reason != on.Reason || !bytes.Equal(legacy.UpperResponses[0].Body, on.UpperResponses[0].Body) ||
		!reflect.DeepEqual(legacy.PostActions, on.PostActions) {
		t.Fatalf("the old gate changed after the judgement hook was widened:\nlegacy=%q %v %x\nnew   =%q %v %x",
			legacy.Reason, legacy.PostActions, legacy.UpperResponses[0].Body,
			on.Reason, on.PostActions, on.UpperResponses[0].Body)
	}
	// 两档都不许写库。
	equipment, _, err := repos.Equipment.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if entry := equipment.Entries["12"]; entry.ItemID != int64(loginWearTestWeaponItemID) {
		t.Fatalf("noop refresh must not move the weapon: %+v", entry)
	}
}

// TestHandlerLoginWearNoopRefreshLeavesTheVendorEchoGateAlone 钉住作用域：
// 开关只作用在登录复述兜底那一条闸门上。厂商同源的 B 形状（identity_echo）在
// 开关打开时也必须**仍然**只回一条 ACK —— 它的字节是 2026-09-25 修好之后钉住的。
func TestHandlerLoginWearNoopRefreshLeavesTheVendorEchoGateAlone(t *testing.T) {
	enableLoginWearReassertNoop(t)
	ctx, repos := newLoginWearReassertFixture(t)
	on := handleLoginWearReassert(t, ctx, loginWearReassertRequest(loginWearIdentityEchoBody(), repos, true, true))
	if !strings.Contains(on.Reason, "noopGate=identity_echo") {
		t.Fatalf("identity echo gate did not fire: %q", on.Reason)
	}
	off := handleLoginWearReassert(t, ctx, loginWearReassertRequest(loginWearIdentityEchoBody(), repos, true, false))
	if len(on.PostActions) != 0 || len(off.PostActions) != 0 {
		t.Fatalf("identity echo post actions on=%v off=%v, want none in both tiers", on.PostActions, off.PostActions)
	}
	if on.Reason != off.Reason {
		t.Fatalf("identity echo reason changed with the switch: on=%q off=%q", on.Reason, off.Reason)
	}
}
