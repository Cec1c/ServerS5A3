package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报告："主线任务完成券显示已过期，等一系列'完成类型'券全部显示过期"。
// 这一整批券在运行时 PVF 里只声明相对 [usable period]、没有绝对 [expiration date]：
//
//	Lv1~90主线任务完成券      10164630  stackable/10164001/10164630.stk
//	Lv1~90主线/成就任务完成券  10301410  stackable/10301001/10301410.stk
//
// 两者都是 [stackable type] `[etc]` + [usable period] 1（见 RealScriptPVF... 用例）。
// 90CN 面板只支持邮件发放（internal/services/gmtool/api_items.go 的
// handleItemGrant 明确拒绝 inventory 模式），所以它们落地前一定经过邮箱附件：
// 附件没有实例到期时间 → mail 模块的到期投影只认绝对日期 → 附件与领出来的堆栈
// 到期字段都是 0 → 客户端把 0 渲染成"已超过使用期限"。
const (
	testCompletionCouponItemID  uint32 = 10301410
	testCompletionCouponPVFPath        = "stackable/10301001/10301410.stk"
	testCompletionCouponPVFText        = "[name] `Lv1~90主线/成就任务完成券`\n[stackable type] `[etc]`\n[stack limit] 1\n[usable period] 1\n"
)

func mustTestCompletionCouponCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": strconv.FormatUint(uint64(testCompletionCouponItemID), 10) +
			" `10301001/10301410.stk`\n",
		testCompletionCouponPVFPath: testCompletionCouponPVFText,
	})
	if err != nil {
		t.Fatalf("build completion-coupon catalog: %v", err)
	}
	return catalog
}

// TestMailboxItemResolutionProjectsRelativeUsablePeriod 钉住领取端：
// 只有 [usable period] 的券也必须拿到一个活着的实例到期时间，
// 否则 mail 模块的 "attachment.ExpireAt 为零才用投影" 分支永远是空操作。
func TestMailboxItemResolutionProjectsRelativeUsablePeriod(t *testing.T) {
	catalog := mustTestCompletionCouponCatalog(t)
	definition, err := catalog.ResolveItem(testCompletionCouponItemID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Kind != dungeonDropItemStackable || definition.UsablePeriodDays != 1 {
		t.Fatalf("catalog definition=%+v", definition)
	}
	if !definition.ExpirationDate.IsZero() {
		t.Fatalf("fixture must stay relative-only, got %s", definition.ExpirationDate)
	}

	before := time.Now().UTC()
	resolution, err := mailboxItemResolutionFromPVF(definition, true)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	if resolution.ExpireAt.IsZero() {
		t.Fatal("relative [usable period] coupon got no instance expiration projection")
	}
	lower := before.Add(24 * time.Hour).Add(-time.Minute)
	upper := after.Add(24 * time.Hour).Add(time.Minute)
	if resolution.ExpireAt.Before(lower) || resolution.ExpireAt.After(upper) {
		t.Fatalf("projected expiration=%s outside [%s,%s]", resolution.ExpireAt, lower, upper)
	}
	if got := resolution.DefaultExtra["expire_time"]; got != strconv.FormatInt(resolution.ExpireAt.Unix(), 10) {
		t.Fatalf("projected expire_time=%q want=%d", got, resolution.ExpireAt.Unix())
	}
	if resolution.DefaultExtra["item_kind"] != string(dungeonDropItemStackable) {
		t.Fatalf("projection lost item identity: %+v", resolution.DefaultExtra)
	}
}

// TestMailboxClaimProjectsRelativeUsablePeriodEndToEnd 走完整条 GM 面板发放路径：
// 邮件附件没有任何到期证据 → 领取 → 落地堆栈必须带活着的到期时间，而且
// 主背包行的 0x0B（当前客户端读作剩余使用期限，0 就渲染成"已超过使用期限"）
// 必须非零。
func TestMailboxClaimProjectsRelativeUsablePeriodEndToEnd(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-coupon",
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
				MailID:    "1",
				CreatedAt: time.Now().UTC(),
				// GM 面板邮件形状：附件既没有 ExpireAt，也没有 Extra。
				Attachments: []dnfrepo.MailAttachment{{ItemID: int64(testCompletionCouponItemID), Count: 1}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     mustTestCompletionCouponCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                &bufferConn{},
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		selectedCharacterID: 77,
	}
	service.bindGameSessionCharacter(session, 77)

	started := time.Now().UTC()
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
	claimedSlot := int16(-1)
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(testCompletionCouponItemID) {
			continue
		}
		_, slot, ok := parseSceneInventorySlotKey(key)
		if !ok {
			t.Fatalf("claimed stack landed under an unparsable key %q", key)
		}
		claimed, claimedSlot = stack, slot
		break
	}
	if claimed.ItemID != int64(testCompletionCouponItemID) {
		t.Fatalf("claimed coupon missing: %+v", inventory.Slots)
	}
	if claimed.ExpireAt.IsZero() {
		t.Fatal("claimed coupon landed with no instance expiration")
	}
	wantMin := started.Add(24 * time.Hour).Add(-time.Minute)
	wantMax := time.Now().UTC().Add(24 * time.Hour).Add(time.Minute)
	if claimed.ExpireAt.Before(wantMin) || claimed.ExpireAt.After(wantMax) {
		t.Fatalf("claimed expiration=%s outside [%s,%s]", claimed.ExpireAt, wantMin, wantMax)
	}

	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, claimedSlot, claimed)
	if got := binary.LittleEndian.Uint16(entry.data[0x0B:0x0D]); got == 0 {
		t.Fatalf("claimed coupon row 0x0B is still 0 (client renders that as 已超过使用期限): %x", entry.data[:0x20])
	}
	if got := binary.LittleEndian.Uint32(entry.data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got == 0 {
		t.Fatalf("claimed coupon row absolute expiration is still 0: %x", entry.data[:0x20])
	}
}

// TestReconcileCurrentMailAttachmentExpirationsProjectsRelativeUsablePeriod 钉住
// 登录期修补：已经躺在盘里的旧邮件（GM 面板发出来时附件到期为 0）也要被补上，
// 锚点是邮件自己的 CreatedAt —— 不是 now，否则每次登录都会静默续期。
func TestReconcileCurrentMailAttachmentExpirationsProjectsRelativeUsablePeriod(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	// 锚点必须是"相对当前时刻"的时间，不能写死绝对时刻。这条 fixture 的道具
	// 只声明 [usable period] 1 天（见文件顶部），修复路径按 CreatedAt+24h 投影，
	// 而 joust_settlement_mail_expiration.go 只在投影**仍晚于 now** 时才写入：
	//
	//	if expiration.IsZero() || !expiration.After(now) { continue }
	//
	// 这条口径是有意的（不许把已经过完的相对期限写成过去的时间点，也不许静默续期），
	// 由同文件 TestReconcileCurrentMailAttachmentExpirationsLeavesRetiredRelativePeriodAlone
	// 单独钉住。所以这里写死 2026-09-24T12:00:00Z 就等于埋了一个走时炸弹：
	// 过了 2026-09-25T12:00:00Z，投影点落进过去，changed 必然变成 0（实际就这么红的）。
	// 取 now-2h 且截断到整秒：投影只剩 ~22h 余量（一定在未来），同时"锚点是
	// CreatedAt 而不是 now"依旧可判别 —— 二者相差 2 小时，下面的 Equal 断言能区分出来。
	// 截断到秒是必须的：currentPVFItemDefinitionForGrantAt 算出的是整秒 Unix
	// （time.Unix(expireUnix, 0)），带纳秒的锚点会让 want 与投影差一个纳秒尾数。
	createdAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"7": {
				MailID:    "7",
				CreatedAt: createdAt,
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: int64(testCompletionCouponItemID),
					Count:  1,
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     mustTestCompletionCouponCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 1 {
		// 带上三个时刻，下次再有人踩到走时/时钟问题能一眼看出来：
		// 投影 = anchor+[usable period]，只有它仍晚于 now 才会被写入。
		t.Fatalf("repair changed=%d err=%v anchor=%s projected=%s now=%s",
			changed, err, createdAt, createdAt.Add(24*time.Hour), time.Now().UTC())
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	want := createdAt.Add(24 * time.Hour)
	if got := mailbox.Mails["7"].Attachments[0].ExpireAt; !got.Equal(want) {
		t.Fatalf("attachment expiry=%s want=%s", got, want)
	}
	// 幂等：同一封邮件再跑一次不能改动，也不能把期限往后推。
	if again, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19); err != nil || again != 0 {
		t.Fatalf("repeat repair changed=%d err=%v", again, err)
	}
	reloaded, _, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Mails["7"].Attachments[0].ExpireAt; !got.Equal(want) {
		t.Fatalf("repeat repair moved the anchor: %s want %s", got, want)
	}
}

// TestReconcileCurrentMailAttachmentExpirationsLeavesRetiredRelativePeriodAlone：
// 相对期限已经过完的旧邮件不许被写成一个过去的时间点（那等于凭空造证据），
// 也不许被推到未来（那等于静默续期）。
func TestReconcileCurrentMailAttachmentExpirationsLeavesRetiredRelativePeriodAlone(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	retired := time.Now().UTC().Add(-72 * time.Hour)
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"7": {
				MailID:      "7",
				CreatedAt:   retired,
				Attachments: []dnfrepo.MailAttachment{{ItemID: int64(testCompletionCouponItemID), Count: 1}},
			},
			"8": {
				MailID:      "8",
				Attachments: []dnfrepo.MailAttachment{{ItemID: int64(testCompletionCouponItemID), Count: 1}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     mustTestCompletionCouponCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 0 {
		t.Fatalf("retired/anchored-less mails changed=%d err=%v", changed, err)
	}
	mailbox, _, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, mailID := range []string{"7", "8"} {
		if got := mailbox.Mails[mailID].Attachments[0].ExpireAt; !got.IsZero() {
			t.Fatalf("mail %s attachment expiry was invented as %s", mailID, got)
		}
	}
}

// TestRealScriptPVFCompletionCouponsDeclareRelativeUsablePeriodOnly 用真实运行时
// PVF 证明根因不是猜的：这批"完成类型"券确实只有 [usable period]、没有
// [expiration date]，所以任何只认绝对日期的投影都会把它们漏成 0。
func TestRealScriptPVFCompletionCouponsDeclareRelativeUsablePeriodOnly(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the completion-coupon usable period")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		itemID uint32
		path   string
		name   string
	}{
		{10164630, "stackable/10164001/10164630.stk", "Lv1~90主线任务完成券"},
		{10301410, "stackable/10301001/10301410.stk", "Lv1~90主线/成就任务完成券"},
	}
	for _, test := range cases {
		definition, err := catalog.ResolveItem(test.itemID)
		if err != nil {
			t.Fatalf("resolve %d: %v", test.itemID, err)
		}
		if definition.Kind != dungeonDropItemStackable || definition.PVFPath != test.path {
			t.Fatalf("item %d definition=%+v", test.itemID, definition)
		}
		if definition.UsablePeriodDays != 1 {
			t.Fatalf("item %d [usable period]=%d want=1", test.itemID, definition.UsablePeriodDays)
		}
		if !definition.ExpirationDate.IsZero() {
			t.Fatalf("item %d unexpectedly has [expiration date] %s", test.itemID, definition.ExpirationDate)
		}
		resolution, err := mailboxItemResolutionFromPVF(definition, true)
		if err != nil {
			t.Fatalf("item %d resolution: %v", test.itemID, err)
		}
		if resolution.ExpireAt.IsZero() {
			t.Fatalf("item %d (%s) still projects no instance expiration", test.itemID, test.name)
		}
	}
}
