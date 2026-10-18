package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailAttachmentSealDisplayPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]string
		bind    bool
		rawSeal int // -1表示没有实例行。
		want    byte
	}{
		{"明确解封覆盖绑定和旧行", map[string]string{"seal_flag": "0", "bind_flag": "1"}, true, 1, 0},
		{"明确解封覆盖旧别名", map[string]string{"seal_flag": "0", "seal": "1"}, true, 1, 0},
		{"旧解封别名保留零", map[string]string{"seal": "0", "bind": "1"}, true, 1, 0},
		{"首次穿戴解封证据", map[string]string{"seal_removed_by_first_equip": "1", "bind_flag": "1"}, true, 1, 0},
		{"首次穿戴交易锁证据", map[string]string{"trade_locked_by_first_equip": "true"}, true, 1, 0},
		{"已有解封实例不重封", nil, true, 0, 0},
		{"已有封装实例保留", nil, false, 1, 1},
		{"明确再封装覆盖旧行", map[string]string{"seal_flag": "1", "seal_removed_by_first_equip": "1"}, true, 0, 1},
		{"圣物旧解封字段", map[string]string{"source": "equipment_emancipate", "emancipate_seal_flag": "0"}, true, 1, 0},
		{"系统新装备明确封装", map[string]string{"seal_flag": "1"}, false, -1, 1},
		{"无原始行也保留明确解封", map[string]string{"seal_flag": "0"}, true, -1, 0},
		{"旧附件绑定回落保持", nil, true, -1, 1},
		{"旧附件绑定别名保持", map[string]string{"bind_flag": "1"}, false, -1, 1},
		{"非法首字段不遮住解封", map[string]string{"seal_flag": "256", "seal": "0"}, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := dnfrepo.MailAttachment{ItemID: 100310806, Count: 1, Bind: tc.bind, Extra: cloneStringMap(tc.extra)}
			if tc.rawSeal >= 0 {
				a.RawEntry = make([]byte, mailboxItemRawSize)
				binary.LittleEndian.PutUint16(a.RawEntry[:2], 9)
				binary.LittleEndian.PutUint32(a.RawEntry[2:6], uint32(a.ItemID))
				binary.LittleEndian.PutUint32(a.RawEntry[6:10], 280027066)
				a.RawEntry[10], a.RawEntry[13] = 12, byte(tc.rawSeal)
				binary.LittleEndian.PutUint16(a.RawEntry[11:13], 28)
			}
			originalRaw, originalExtra := append([]byte(nil), a.RawEntry...), cloneStringMap(a.Extra)
			raw := mailboxAttachmentRaw(a)
			if raw[13] != tc.want {
				t.Errorf("附件封装=%d，期望%d", raw[13], tc.want)
			}
			if len(a.RawEntry) != 0 && (!bytes.Equal(raw[10:13], a.RawEntry[10:13]) || !bytes.Equal(raw[14:], a.RawEntry[14:])) {
				t.Fatal("封装显示改变了其它实例字段")
			}
			m := invMail("6", func(m *dnfrepo.MailRecord) { m.Attachments = []dnfrepo.MailAttachment{a} })
			body, err := buildMailboxListNotification([]dnfrepo.MailRecord{m}, 0, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			assertMailSealDisplayBody(t, body, tc.want)
			if !bytes.Equal(a.RawEntry, originalRaw) || !reflect.DeepEqual(a.Extra, originalExtra) || a.Bind != tc.bind || a.Count != 1 {
				t.Fatal("邮件显示修改了附件资产")
			}
		})
	}
}

func assertMailSealDisplayBody(t *testing.T, body []byte, want byte) {
	t.Helper()
	invWalkMailboxList(t, body, []bool{false})
	c := invCursor{data: body, off: 2}
	c.need(t, 8, "邮件和发送者编号")
	c.dstring(t, "发送者名字")
	c.need(t, 8, "金币和物品编号")
	if got := c.u8(t, "原生物品创建封装参数"); got != want {
		t.Errorf("原生创建封装参数%d，期望%d", got, want)
	}
	c.need(t, 6, "数量和耐久")
	if got := c.u8(t, "摘要封装"); got != want {
		t.Errorf("邮件摘要封装%d，期望%d", got, want)
	}
	c.need(t, 8, "其余摘要字段")
	raw := c.need(t, mailboxItemRawSize, "附件实例行")
	if got := raw[13]; got != want {
		t.Errorf("邮件附件行封装%d，期望%d", got, want)
	}
}

func TestMailAttachmentSealDisplayNativeCreationDoesNotControlPresence(t *testing.T) {
	attachments := []dnfrepo.MailAttachment{
		{ItemID: 100100533, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "524643994", "seal_flag": "0"}},
		{ItemID: 100310913, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "687760913", "seal_flag": "1"}},
		{ItemID: 42, Count: 10, Extra: map[string]string{"item_kind": "stackable"}},
	}
	m := invMail("7", func(m *dnfrepo.MailRecord) { m.Attachments = attachments })
	body, err := buildMailboxListNotification([]dnfrepo.MailRecord{m}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	walk := invWalkMailboxList(t, body, []bool{false, false, false})
	if walk.summaryCount != 3 || walk.totalMails != 1 || walk.detailCount != 1 {
		t.Fatal("修改创建状态改变了附件数量或详情", walk)
	}
	c := invCursor{data: body, off: 2}
	for i, a := range attachments {
		if !walk.summaries[i].hasItem {
			t.Fatal("解封状态0被误当作没有附件")
		}
		c.need(t, 8, "邮件和发送者编号")
		c.dstring(t, "发送者")
		c.u32(t, "金币")
		if got := c.u32(t, "物品编号"); got != uint32(a.ItemID) {
			t.Fatalf("附件编号%d，期望%d", got, a.ItemID)
		}
		wantSeal := byte(0)
		if i == 1 {
			wantSeal = 1
		}
		// 当前EXE的2043ea7将这个字节转成布尔值交给2a32b30，
		// 2a32ac0将它写进模板附加数据+0x5c；附件存在由物品编号决定。
		if got := c.u8(t, "原生创建封装参数"); got != wantSeal {
			t.Errorf("附件%d创建封装参数%d，期望%d", a.ItemID, got, wantSeal)
		}
		c.need(t, 6, "品级数量和耐久")
		if got := c.u8(t, "摘要封装"); got != wantSeal {
			t.Errorf("附件%d摘要封装%d", a.ItemID, got)
		}
		c.need(t, 8, "其余摘要字段")
		raw := c.need(t, mailboxItemRawSize, "原始行")
		if raw[13] != wantSeal || binary.LittleEndian.Uint32(raw[2:6]) != uint32(a.ItemID) {
			t.Fatal("附件实例行与创建参数不一致")
		}
		c.u32(t, "剩余时间")
		if id := c.u32(t, "领取对象"); id != 7 {
			t.Fatalf("领取对象被改变：%d", id)
		}
		c.u8(t, "邮件类型")
	}
}

func TestMailAttachmentSealDisplaySendOpenClaimConsistency(t *testing.T) {
	for _, seal := range []byte{0, 1} {
		t.Run(fmt.Sprint(seal), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "local-account", Name: "sender", Stats: map[string]int64{"gold": 2000}})
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", AccountID: "local-account", Name: "receiver", Slot: 1})
			raw := make([]byte, mailboxItemRawSize)
			binary.LittleEndian.PutUint16(raw[:2], 9)
			binary.LittleEndian.PutUint32(raw[2:6], 100310806)
			binary.LittleEndian.PutUint32(raw[6:10], 280027066)
			raw[10], raw[13] = 12, seal
			binary.LittleEndian.PutUint16(raw[11:13], 28)
			stack := dnfrepo.ItemStack{ItemID: 100310806, Count: 1, Bind: seal == 0, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "seal_flag": fmt.Sprint(seal), "quality_seed": "280027066", "durability": "28", "max_durability": "57"}}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:9": stack}}); err != nil {
				t.Fatal(err)
			}
			resolver := func(uint32) (alignedcmd.MailboxItemResolution, error) {
				return alignedcmd.MailboxItemResolution{Kind: "equipment", Price: 100, SlotStart: 9, SlotEnd: 64, StackLimit: 1, DefaultExtra: map[string]string{"seal_flag": "1", "quality_seed": "1", "durability": "57"}}, nil
			}
			send, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMultiMailboxSend), SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: resolver, Body: currentSendBody("receiver", 0, []SendAttachment{{ListType: 0, SlotIndex: 9, ItemID: 100310806, Count: 1}}, "邮件封装显示回归", 0, false, true)})
			if err != nil || len(send.UpperResponses) == 0 || send.UpperResponses[0].Body[0] != 1 {
				t.Fatalf("发送失败：%+v %v", send, err)
			}
			opened, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 88, Repositories: repos})
			if err != nil || len(opened.UpperResponses) != 2 {
				t.Fatalf("打开失败：%+v %v", opened, err)
			}
			assertMailSealDisplayBody(t, opened.UpperResponses[1].Body, seal)
			claim, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 88, Repositories: repos, MailboxItemResolver: resolver, Body: currentMailIDList(1)})
			if err != nil || len(claim.UpperResponses) == 0 || claim.UpperResponses[0].Body[0] != 1 {
				t.Fatalf("领取失败：%+v %v", claim, err)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "88")
			found := 0
			for _, got := range inv.Slots {
				if got.ItemID != stack.ItemID {
					continue
				}
				found++
				if got.Count != 1 || got.Bind != stack.Bind || got.Extra["seal_flag"] != stack.Extra["seal_flag"] || got.Extra["quality_seed"] != "280027066" || !bytes.Equal(got.RawEntry, raw) {
					t.Fatalf("显示修复改变了领取实例：%+v", got)
				}
			}
			if found != 1 {
				t.Fatalf("领取装备数量%d", found)
			}
		})
	}
}
