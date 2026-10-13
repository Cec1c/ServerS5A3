package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestSystemTemplateEquipmentMailKeepsPVFSealOnClaim(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(bridgePVFSource{"monster/monster.lst": "", "stackable/stackable.lst": "", "equipment/equipment.lst": "700 `test_seal.equ`\n", "equipment/test_seal.equ": "[equipment type] `[weapon]`\n[attach type] `[sealing]`\n[durability] 50\n"})
	if err != nil {
		t.Fatal(err)
	}
	sealedRaw := make([]byte, currentItemListEntryWireSize)
	sealedRaw[0x0D] = 1
	for _, tt := range []sealMailClaimCase{
		{"系统新发装备", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{}, 1},
		{"旧GM模板邮件", "", map[string]string{"source": "dnf90-gm-tool"}, dnfrepo.MailAttachment{}, 1},
		{"GM指定品质新装备", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{Extra: map[string]string{"quality_seed": "42"}}, 1},
		{"玩家寄出的封装实例", "8", nil, dnfrepo.MailAttachment{RawEntry: sealedRaw}, 1},
		{"玩家寄出的解封实例", "8", nil, dnfrepo.MailAttachment{RawEntry: make([]byte, currentItemListEntryWireSize)}, 0},
		{"系统转交已有解封实例", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{RawEntry: make([]byte, currentItemListEntryWireSize)}, 0},
		{"系统转交旧格式解封行", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{Extra: map[string]string{"raw_entry_hex": hex.EncodeToString(make([]byte, currentItemListEntryWireSize))}}, 0},
		{"明确解封标记", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{Extra: map[string]string{"seal_flag": "0"}}, 0},
		{"首次使用已解封", "0", map[string]string{"system_mail": "true"}, dnfrepo.MailAttachment{Extra: map[string]string{"seal_removed_by_first_equip": "1"}}, 0},
		{"玩家旧邮件缺少实例", "8", nil, dnfrepo.MailAttachment{}, 0},
		{"来源不明的旧附件", "", nil, dnfrepo.MailAttachment{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, full := range []bool{false, true} {
				assertEquipmentMailSealClaim(t, catalog, 700, tt, full)
			}
		})
	}
}

type sealMailClaimCase struct {
	name       string
	sender     string
	metadata   map[string]string
	attachment dnfrepo.MailAttachment
	want       byte
}

func assertEquipmentMailSealClaim(t *testing.T, catalog *pvfDungeonDropCatalog, itemID int64, tt sealMailClaimCase, full bool) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "78", AccountID: "seal-mail", Name: "seal-mail", Job: "0", Level: 90}); err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{CharacterID: "78", Slots: make(map[string]dnfrepo.ItemStack)}
	if full {
		for slot := int16(9); slot <= 64; slot++ {
			inventory.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	attachment := tt.attachment
	attachment.ItemID, attachment.Count = itemID, 1
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "78", Mails: map[string]dnfrepo.MailRecord{"1": {MailID: "1", SenderCharacterID: tt.sender, Metadata: tt.metadata, Attachments: []dnfrepo.MailAttachment{attachment}}}}); err != nil {
		t.Fatal(err)
	}

	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, channel: channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}, selectedCharacterID: 78}
	service.bindGameSessionCharacter(session, 78)
	handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(1))
	if err != nil || !handled {
		t.Fatalf("邮件领取失败：handled=%t err=%v", handled, err)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "78")
	if err != nil || !found {
		t.Fatalf("领取库存不存在：found=%t err=%v", found, err)
	}
	slot := int16(9)
	if full {
		slot = 3
	}
	stack := inventory.Slots[currentDungeonPickupMainSlotKey(slot)]
	entry := currentItemListEntryFromStack(0, slot, stack)
	if stack.ItemID != itemID || entry.data[0x0D] != tt.want {
		t.Fatalf("邮件落格/封装错误：full=%t slot=%d seal=%d want=%d stack=%+v", full, slot, entry.data[0x0D], tt.want, stack)
	}
	if tt.want == 0 && stack.Extra["seal_flag"] == "1" {
		t.Fatal("邮件领取重新封装了已有解封实例")
	}
	if attachment.Extra["quality_seed"] == "42" && stack.Extra["quality_seed"] != "42" {
		t.Fatal("初始化封装覆盖了既有品质种子")
	}
	refreshFound := false
	for _, packet := range splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes()) {
		if packet.Header.MsgID == 14 && bytes.Contains(packet.Body, entry.data[:]) {
			refreshFound = true
		}
	}
	if !refreshFound {
		t.Fatal("邮件领取未下发保留封装状态的实际op14行")
	}
}
