package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func mailIDListBody(ids ...uint32) []byte {
	out := make([]byte, 0, 4+4*len(ids))
	var number [4]byte
	binary.LittleEndian.PutUint32(number[:], uint32(len(ids)))
	out = append(out, number[:]...)
	for _, id := range ids {
		binary.LittleEndian.PutUint32(number[:], id)
		out = append(out, number[:]...)
	}
	return out
}

// 端到端（bridge 层）钉子：GM 面板发出的邮件附件自己没有到期时间，玩家领取时
// **必须**由 dnfbridge 的运行时 PVF 解析器把到期投影补进落地的堆栈。
// 这条覆盖"解析器接线 → mail 模块应用 → 持久化"整条路，而不只是某一段。
func TestMailboxClaimAppliesRuntimePVFExpirationEndToEnd(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-mail",
		Name:        "claimer",
		Job:         "0",
		Level:       90,
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID: "1",
				// 关键：附件不带实例到期时间 —— GM 面板邮件就是这个形状。
				Attachments: []dnfrepo.MailAttachment{{ItemID: 490005585, Count: 2}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     mustTestJoustCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                &bufferConn{},
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		selectedCharacterID: 77,
	}
	service.bindGameSessionCharacter(session, 77)

	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMailboxExtractItem),
		mailIDListBody(1),
	)
	if err != nil {
		t.Fatalf("mailbox extract: %v", err)
	}
	if !handled {
		t.Fatal("mailbox extract was not handled by the aligned mail module")
	}

	inventory, found, err := repositories.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	var claimed dnfrepo.ItemStack
	ok := false
	for _, stack := range inventory.Slots {
		if stack.ItemID == 490005585 {
			claimed, ok = stack, true
			break
		}
	}
	if !ok {
		t.Fatalf("claimed item missing: %+v", inventory.Slots)
	}
	want := time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC)
	if !claimed.ExpireAt.Equal(want) {
		t.Fatalf("claimed expiry = %s, want the runtime PVF deadline %s", claimed.ExpireAt, want)
	}
	if got := claimed.Extra["expire_time"]; got != strconv.FormatInt(want.Unix(), 10) {
		t.Fatalf("claimed expire_time = %q, want %d (the same PVF deadline as ExpireAt)", got, want.Unix())
	}
	if claimed.Extra["item_kind"] != "stackable" || claimed.Extra["pvf_path"] == "" {
		t.Fatalf("PVF projection incomplete on the claimed stack: %+v", claimed.Extra)
	}
}
