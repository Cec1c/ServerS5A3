package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func multiAttachmentIDs(t *testing.T, result alignedcmd.Result) []uint32 {
	t.Helper()
	var ids []uint32
	for _, p := range result.UpperResponses {
		if p.MsgID != 97 || p.Classification != 0 {
			continue
		}
		flags := make([]bool, int(p.Body[0]))
		walk := invWalkMailboxList(t, p.Body, flags)
		for _, span := range walk.summaries {
			ids = append(ids, binary.LittleEndian.Uint32(p.Body[span.start:]))
		}
	}
	return ids
}

func TestMailboxSystemMailSourcesShareMultiAttachmentContract(t *testing.T) {
	// 此处验证各来源标签经过共同投递/打开/领取链路；生产者自身的触发与结算由原有专项覆盖。
	sources := []string{"magic_box_reward_inventory_full", "cera_package_inventory_full", "dungeon_card_reward_inventory_full", "raid_reward_overflow", "tag_tournament", "grandi_equipment_transport", "weekly_attendance", "monthly_attendance", "ordinary_attendance", "attendance_marble", "semester_cumulative", "epic_road_benefits", "joust_settlement", "pcroom_timepoint", "level_up_reward", "auction_return", "auction_purchase", "金币寄售", "dnf90-gm-tool"}
	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 0}})
			_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: source, Gold: 7, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2, Bind: true, Extra: map[string]string{"custom_instance": "keep"}}, {ItemID: 1002, Count: 3}}, CreatedAt: time.Now()})
			if e != nil {
				t.Fatal(e)
			}
			h := NewHandler()
			req := alignedcmd.Request{Opcode: 96, SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99)}
			opened, e := h.Handle(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			ids := multiAttachmentIDs(t, opened)
			if len(ids) != 2 || ids[0] == ids[1] {
				t.Fatal("显示少附件", ids)
			}
			req.Opcode = 95
			req.Body = currentMailIDList(ids...)
			claimed, e := h.Handle(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			ack := claimed.UpperResponses[0].Body
			if len(ack) != 29 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[1:]) != 2 {
				t.Fatalf("全部接受未确认两件：%x", ack)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "77")
			counts := map[int64]int64{}
			for _, s := range inv.Slots {
				counts[s.ItemID] += s.Count
				if s.ItemID == 1001 && (!s.Bind || s.Extra["custom_instance"] != "keep") {
					t.Fatal("实例属性丢失", s)
				}
			}
			if counts[1001] != 2 || counts[1002] != 3 {
				t.Fatal(counts)
			}
			ch, _, _ := repos.Character.Load(ctx, "77")
			if ch.Stats["gold"] != 7 {
				t.Fatal(ch.Stats)
			}
			_, e = h.Handle(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			after, _, _ := repos.Inventory.Load(ctx, "77")
			if !reflect.DeepEqual(inv, after) {
				t.Fatal("重放重复发货")
			}
		})
	}
}

func TestMailboxClaimIDsStayStableWhenNewMailArrives(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	deliver := func(items []dnfrepo.MailAttachment) string {
		id, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "magic_box_reward_inventory_full", Attachments: items, CreatedAt: time.Now()})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	first := deliver([]dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}, {ItemID: 1002, Count: 1}})
	owner := NewOwner(repos, mailboxClaimTestResolver(65, 120, 99))
	if _, e := owner.Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	before, _, _ := repos.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(before.Mails[first])
	second := deliver([]dnfrepo.MailAttachment{{ItemID: 1003, Count: 1}})
	// 第二封父信编号恰好与旧信第二附件编号相同；旧领取对象必须继续指向1002。
	if fmt.Sprint(ids[1]) != second {
		t.Fatal("夹具没有覆盖编号冲突", ids, second)
	}
	result, e := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{ids[1]}})
	if e != nil {
		t.Fatal(e)
	}
	if result.ItemCount != 1 {
		t.Fatal(result)
	}
	if _, e = owner.Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	after, _, _ := repos.Mailbox.Load(ctx, "77")
	left, _, _ := mailboxStoredClaimIDs(after.Mails[first])
	newIDs, _, _ := mailboxStoredClaimIDs(after.Mails[second])
	if len(left) != 1 || left[0] != ids[0] || newIDs[0] == ids[1] || after.Mails[second].Claimed {
		t.Fatal("编号重排或领错邮件", after)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	counts := map[int64]int64{}
	for _, s := range inv.Slots {
		counts[s.ItemID] += s.Count
	}
	if counts[1002] != 1 || counts[1001] != 0 || counts[1003] != 0 {
		t.Fatal(counts)
	}
}

func TestMailboxPartialFullClaimsKeepRemainingAssetsAndShowPopup(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 0}})
	slots := map[string]dnfrepo.ItemStack{}
	for slot := int16(3); slot <= 8; slot++ {
		slots[mailboxSlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 9999, Count: 1}
	}
	slots["0:65"] = dnfrepo.ItemStack{ItemID: 9999, Count: 1}
	if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); e != nil {
		t.Fatal(e)
	}
	_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "magic_box_reward_inventory_full", Gold: 50, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2}, {ItemID: 1002, Count: 1}}, CreatedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	h := NewHandler()
	req := alignedcmd.Request{Opcode: 96, SelectedCharacterID: 77, Repositories: repos}
	opened, e := h.Handle(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	ids := multiAttachmentIDs(t, opened)
	req.Opcode = 95
	req.Body = currentMailIDList(ids...)
	req.MailboxItemResolver = func(id uint32) (alignedcmd.MailboxItemResolution, error) {
		if id == 1001 {
			return mailboxClaimTestResolver(65, 65, 99)(id)
		}
		return mailboxClaimTestResolver(9, 64, 1)(id)
	}
	got, e := h.Handle(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	successes, popups := 0, 0
	for _, p := range got.UpperResponses {
		if p.MsgID == 95 {
			if p.Body[0] == 1 {
				successes++
				if binary.LittleEndian.Uint32(p.Body[9:]) != ids[1] {
					t.Fatal("确认了未入包对象")
				}
			} else if reflect.DeepEqual(p.Body, []byte{0, 4}) {
				popups++
			}
		}
	}
	if successes != 1 || popups != 1 {
		t.Fatal("部分成功缺少满包提示", got.UpperResponses)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	m := box.Mails["1"]
	if m.Claimed || m.Deleted || len(m.Attachments) != 1 || m.Attachments[0].ItemID != 1001 || m.Gold != 50 {
		t.Fatal(m)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	delete(inv.Slots, "0:65")
	if e = repos.Inventory.Save(ctx, inv); e != nil {
		t.Fatal(e)
	}
	req.Body = currentMailIDList(ids[0])
	got, e = h.Handle(ctx, req)
	if e != nil || got.UpperResponses[0].Body[0] != 1 {
		t.Fatal("腾格后不能继续领", got, e)
	}
	ch, _, _ := repos.Character.Load(ctx, "77")
	if ch.Stats["gold"] != 50 {
		t.Fatal(ch)
	}
}

func TestMailboxLargeAttachmentSnapshotUsesNativeContinuation(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	attachments := make([]dnfrepo.MailAttachment, 300)
	for i := range attachments {
		attachments[i] = dnfrepo.MailAttachment{ItemID: 1001, Count: 1}
	}
	_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "magic_box_reward_inventory_full", Attachments: attachments, CreatedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	h := NewHandler()
	req := alignedcmd.Request{Opcode: 96, SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 999)}
	opened, e := h.Handle(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	ids := multiAttachmentIDs(t, opened)
	if len(ids) != 300 {
		t.Fatal("续包少附件", len(ids))
	}
	seen := map[uint32]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatal("续包编号重复", id)
		}
		seen[id] = true
	}
	packets := 0
	for _, p := range opened.UpperResponses {
		if p.MsgID == 97 {
			want := byte(0)
			if packets > 0 {
				want = 1
			}
			if p.Body[1] != want {
				t.Fatal("续包清空前面的附件")
			}
			packets++
		}
	}
	if packets < 2 {
		t.Fatal("未分包")
	}
	req.Opcode = 95
	req.Body = currentMailIDList(ids...)
	got, e := h.Handle(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if got.UpperResponses[0].Body[0] != 1 || binary.LittleEndian.Uint32(got.UpperResponses[0].Body[1:]) != 300 {
		t.Fatal("大批量领取被旧邮件数量限制拒绝", got.Operation)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	var count int64
	for _, s := range inv.Slots {
		if s.ItemID == 1001 {
			count += s.Count
		}
	}
	if count != 300 {
		t.Fatal(count)
	}
}

func TestMailboxClaimIdentitiesRejectDuplicateRequestAndOtherOwner(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	for _, id := range []string{"77", "78"} {
		mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: id})
	}
	_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "tag_tournament", Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}, {ItemID: 1002, Count: 1}}, CreatedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	owner := NewOwner(repos, mailboxClaimTestResolver(65, 120, 99))
	if _, e = owner.Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	before, _, _ := repos.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(before.Mails["1"])
	for _, tc := range []struct {
		character string
		ids       []uint32
	}{{"77", []uint32{ids[1], ids[1]}}, {"78", []uint32{ids[1]}}, {"77", []uint32{999999}}} {
		if _, e = owner.Claim(ctx, tc.character, ExtractRequest{MailIDs: tc.ids}); e == nil {
			t.Fatal("非法选择成功", tc)
		}
	}
	after, _, _ := repos.Mailbox.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("拒绝改变存档")
	}
	if _, e = decodeExtractRequest(currentMailIDList(make([]uint32, maxMailboxExtractBatch+1)...)); e == nil {
		t.Fatal("越界批量请求未拦截")
	}
}

func exportMailboxClaimFixture(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := os.Getenv("DNF_MAIL_NATIVE_EXPORT")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "claims")
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, name+".bin"), body, 0644); e != nil {
		t.Fatal(e)
	}
}

func TestMailboxMultipleEquipmentKeepsEveryInstanceField(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	attachments := make([]dnfrepo.MailAttachment, 2)
	for i := range attachments {
		raw := make([]byte, 119)
		binary.LittleEndian.PutUint32(raw[2:6], uint32(9001+i))
		binary.LittleEndian.PutUint32(raw[6:10], 85180097+uint32(i))
		raw[10] = 31
		binary.LittleEndian.PutUint16(raw[11:13], 28+uint16(i))
		raw[13] = 1
		raw[19] = 4
		raw[20] = 7
		for j := 22; j < len(raw); j++ {
			raw[j] = byte(j + i)
		}
		attachments[i] = dnfrepo.MailAttachment{ItemID: int64(9001 + i), Count: 1, Bind: true, ExpireAt: time.Now().Add(24 * time.Hour).Truncate(time.Second), RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "enchant_card": "12345", "socket_data": "11223344", "custom_instance": fmt.Sprint(i)}}
	}
	_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "grandi_equipment_transport", Attachments: attachments, CreatedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	resolver := func(id uint32) (alignedcmd.MailboxItemResolution, error) {
		r, e := mailboxClaimTestResolver(9, 64, 1)(id)
		r.Kind = "equipment"
		return r, e
	}
	owner := NewOwner(repos, resolver)
	if _, e = owner.Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(box.Mails["1"])
	for i := len(ids) - 1; i >= 0; i-- {
		if _, e = owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{ids[i]}}); e != nil {
			t.Fatal(e)
		}
	}
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	if len(inv.Slots) != 2 {
		t.Fatal(inv)
	}
	for _, stack := range inv.Slots {
		a := attachments[stack.ItemID-9001]
		if !stack.Bind || !stack.ExpireAt.Equal(a.ExpireAt) || !bytes.Equal(stack.RawEntry[2:], a.RawEntry[2:]) {
			t.Fatal("品质、耐久、封装、增幅或实例行改变", stack)
		}
		for k, v := range a.Extra {
			if stack.Extra[k] != v {
				t.Fatal("附魔/徽章等元数据改变", k, stack.Extra)
			}
		}
	}
}

type rejectingMailIdentityAssets struct {
	inner dnfrepo.MailboxAssetUnitOfWork
}

func TestMailboxRetiredAttachmentIDCannotClaimNewLetter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	deliver := func(items []dnfrepo.MailAttachment) string {
		id, err := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "magic_box_reward_inventory_full", Attachments: items, CreatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := deliver([]dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}, {ItemID: 1002, Count: 1}, {ItemID: 1003, Count: 1}})
	owner := NewOwner(repos, mailboxClaimTestResolver(65, 120, 99))
	if _, err := owner.Open(ctx, "77"); err != nil {
		t.Fatal(err)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(box.Mails[first])
	retired := ids[1]
	if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{retired}}); err != nil {
		t.Fatal(err)
	}
	newMail := deliver([]dnfrepo.MailAttachment{{ItemID: 1004, Count: 1}})
	if _, err := owner.Open(ctx, "77"); err != nil {
		t.Fatal(err)
	}
	box, _, _ = repos.Mailbox.Load(ctx, "77")
	newIDs, _, _ := mailboxStoredClaimIDs(box.Mails[newMail])
	if newIDs[0] == retired {
		t.Fatal("已领取编号被新邮件复用，延迟点击会领错物品", retired)
	}
	before, _, _ := repos.Inventory.Load(ctx, "77")
	if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{retired}}); err == nil {
		t.Fatal("旧领取请求仍然成功")
	}
	after, _, _ := repos.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("延迟请求改变新邮件资产")
	}
}

func (u rejectingMailIdentityAssets) WithinMailboxAssets(ctx context.Context, a, b string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return u.inner.WithinMailboxAssets(ctx, a, b, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		if e := apply(c, i, m); e != nil {
			return e
		}
		return errors.New("邮件事务提交故障夹具")
	})
}

func TestMailboxIdentityAndPartialClaimCommitFailureRollBack(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 3}})
	_, e := dnfrepo.AppendSystemMail(ctx, repos.Mailbox, dnfrepo.SystemMailDelivery{RecipientCharacterID: "77", Source: "dnf90-gm-tool", Gold: 7, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2}, {ItemID: 1002, Count: 3}}, CreatedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	before, _, _ := repos.Mailbox.Load(ctx, "77")
	broken := repos
	broken.MailboxAssets = rejectingMailIdentityAssets{inner: repos.MailboxAssets}
	if _, e = NewOwner(broken).Open(ctx, "77"); e == nil {
		t.Fatal("未持久化编号却显示成功")
	}
	after, _, _ := repos.Mailbox.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("分配编号失败未回滚")
	}
	if _, e = NewOwner(repos).Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	before, _, _ = repos.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(before.Mails["1"])
	if _, e = NewOwner(broken, mailboxClaimTestResolver(65, 120, 99)).Claim(ctx, "77", ExtractRequest{MailIDs: ids}); e == nil {
		t.Fatal("提交失败仍成功")
	}
	after, _, _ = repos.Mailbox.Load(ctx, "77")
	ch, _, _ := repos.Character.Load(ctx, "77")
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) || ch.Stats["gold"] != 3 || len(inv.Slots) != 0 {
		t.Fatal("领取失败资产没有整体回滚")
	}
}
