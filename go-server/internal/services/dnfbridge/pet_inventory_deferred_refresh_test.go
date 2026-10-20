package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// list 7（宠物背包）的 op14 单行在客户端会走"宠物对象替换"路径：已存在的行只更新
// 序号/数量，新建一行才会创建对象。线上实测角色 3 领取宠物更名卡（list 7 slot 191）
// 时客户端在库存界面模块 0x23082D3 直接闪退，而同批次的 list 0 爆竹行完全正常。
// 所以 list 7 的增量刷新必须改成"延迟整容器下发"：立刻不能发那行 op14，
// 稍后必须补一次 list 7 容器包（登录引导用的同一条包，已在 7:189/7:190 上验证）。
func TestPetInventoryIncrementalRefreshDefersList7RowThenSendsContainer(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)

	if err := service.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"mailbox_extract_ack",
		[]alignedcmd.ItemSlotRefresh{{ListType: currentPetInventoryListType, SlotIndex: 191}},
	); err != nil {
		t.Fatalf("pet inventory refresh: %v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("list 7 op14 row must not be sent immediately; wrote %d bytes", conn.write.Len())
	}

	deadline := time.Now().Add(5 * time.Second)
	for conn.write.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if conn.write.Len() == 0 {
		t.Fatal("deferred list-7 container refresh never arrived")
	}
}

// list 0 的增量刷新行为必须完全不变：领取爆竹这类主背包物品仍旧立刻下发 op14 行。
//
// 2026-09-28 曾按"领取邮件附件闪退是那条 op14 单行造成的"这一假设把本用例改成
// "领取路径必须延迟整容器下发"，并实机验证；A/B 结果证明该假设**不成立**——
// 改走 msg_id=13 整容器之后客户端在同一时刻照崩（四次复现，含叠进已有槽位与
// 双角色各一次）。因此改动已回退，本用例恢复原口径。
func TestMainInventoryIncrementalRefreshStillSendsImmediately(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)

	if err := service.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"mailbox_extract_ack",
		[]alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: 69}},
	); err != nil {
		t.Fatalf("main inventory refresh: %v", err)
	}
	if conn.write.Len() == 0 {
		t.Fatal("list 0 op14 row must still be sent synchronously")
	}
}
