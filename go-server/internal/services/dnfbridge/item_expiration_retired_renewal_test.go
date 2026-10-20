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

// 现场报告（本轮的第二步）：修好"只声明相对 [usable period]"那批券之后，
// 操作者要的是**全部过期道具续期到 2028-01-01**。仍然显示"已超过使用期限"的是
// 另一类：PVF 里声明了**绝对** [expiration date]、而那个日期已经过去的退役道具。
//
// 真实运行时 PVF（195651 个道具）里有 16001 个这样的道具，比如：
//
//	闪亮/灿烂/光辉的主线任务完成券 490005938 / 490005971 / 490005972
//	  stackable/490005001/chn_490005938.stk
//	  [stackable type] `[etc]` + [expiration date] `2017-11-02 06:00:00`
//
// 这批道具的到期时间会被**每条**路径照抄到客户端：
// 邮件附件行（mail/body.go 的 raw+0x38）、背包/仓库行（0x38 + 0x0B 剩余使用期限）、
// 领取后落地的堆栈、掉落/礼包产出。所以策略必须是"一个常量 + 一个判据"，
// 而不是每个路径各自打补丁。见 currentPVFRetiredExpirationRenewalDate。
const (
	testRetiredCouponItemID  uint32 = 490005938
	testRetiredCouponPVFPath        = "stackable/490005001/chn_490005938.stk"
	testRetiredCouponPVFText        = "[name] `闪亮的主线任务完成券`\n[stackable type] `[etc]`\n[stack limit] 1000\n[expiration date] `2017-11-02 06:00:00`\n"
)

// currentPVFExpirationDateLocation 是 CST(UTC+8)：2017-11-02 06:00:00 → 2017-11-01T22:00:00Z。
var testRetiredCouponExpiration = time.Date(2017, time.November, 1, 22, 0, 0, 0, time.UTC)

func mustTestRetiredCouponCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": strconv.FormatUint(uint64(testRetiredCouponItemID), 10) +
			" `490005001/chn_490005938.stk`\n",
		testRetiredCouponPVFPath: testRetiredCouponPVFText,
	})
	if err != nil {
		t.Fatalf("build retired-coupon catalog: %v", err)
	}
	return catalog
}

// TestCurrentPVFRetiredExpirationRenewalDateIsTheOperatorPolicy 钉住口径：
// **本地（PVF）时区的 2028-01-01 00:00:00**，也就是 CST(UTC+8) 声明、
// UTC 表示 2027-12-31T16:00:00Z。换时区就等于换掉操作者看到的日期，所以这里显式锁死。
func TestCurrentPVFRetiredExpirationRenewalDateIsTheOperatorPolicy(t *testing.T) {
	wantUTC := time.Date(2027, time.December, 31, 16, 0, 0, 0, time.UTC)
	if !currentPVFRetiredExpirationRenewalDate.Equal(wantUTC) {
		t.Fatalf("renewal horizon=%s want=%s", currentPVFRetiredExpirationRenewalDate, wantUTC)
	}
	local := currentPVFRetiredExpirationRenewalDate.In(currentPVFExpirationDateLocation)
	if local.Year() != 2028 || local.Month() != time.January || local.Day() != 1 ||
		local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
		t.Fatalf("horizon in PVF local zone=%s want 2028-01-01 00:00:00", local)
	}
	if unix := currentPVFExpirationUnix(currentPVFRetiredExpirationRenewalDate); unix != 1830268800 {
		t.Fatalf("horizon unix=%d want=1830268800", unix)
	}
}

// TestCurrentPVFItemDefinitionForGrantRenewsRetiredAbsoluteDateAndLeavesOthers
// 钉住**发放口径**：退役绝对日期续期；未到期的绝对日期、只声明相对期限的道具、
// 以及"本就无期限"的道具都保持原样。
func TestCurrentPVFItemDefinitionForGrantRenewsRetiredAbsoluteDateAndLeavesOthers(t *testing.T) {
	now := time.Date(2026, time.September, 25, 3, 0, 0, 0, time.UTC)
	horizon := currentPVFRetiredExpirationRenewalDate

	retired, err := currentPVFItemDefinitionForGrantAt(dungeonDropItemDefinition{
		ItemID:         testRetiredCouponItemID,
		Kind:           dungeonDropItemStackable,
		PVFPath:        testRetiredCouponPVFPath,
		StackableType:  "[etc]",
		StackLimit:     1000,
		ExpirationDate: testRetiredCouponExpiration,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !retired.ExpirationDate.Equal(horizon) {
		t.Fatalf("retired grant expiration=%s want=%s", retired.ExpirationDate, horizon)
	}

	live := dungeonDropItemDefinition{
		ItemID:         currentJoustEventItemFirst,
		Kind:           dungeonDropItemStackable,
		ExpirationDate: time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC),
	}
	gotLive, err := currentPVFItemDefinitionForGrantAt(live, now)
	if err != nil {
		t.Fatal(err)
	}
	if !gotLive.ExpirationDate.Equal(live.ExpirationDate) {
		t.Fatalf("live absolute date was moved: %s want %s", gotLive.ExpirationDate, live.ExpirationDate)
	}

	permanent := dungeonDropItemDefinition{ItemID: 490005593, Kind: dungeonDropItemStackable}
	gotPermanent, err := currentPVFItemDefinitionForGrantAt(permanent, now)
	if err != nil {
		t.Fatal(err)
	}
	if !gotPermanent.ExpirationDate.IsZero() {
		t.Fatalf("permanent definition gained an expiration: %s", gotPermanent.ExpirationDate)
	}

	// 相对 [usable period]：行为不变（仍然是"发放时刻 + N 天"，不是地平线）。
	relative, err := currentPVFItemDefinitionForGrantAt(dungeonDropItemDefinition{
		ItemID:           10301410,
		Kind:             dungeonDropItemStackable,
		UsablePeriodDays: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	wantRelative := time.Unix(now.Unix()+86400, 0).UTC()
	if !relative.ExpirationDate.Equal(wantRelative) {
		t.Fatalf("relative usable period changed: %s want %s", relative.ExpirationDate, wantRelative)
	}
}

// TestApplyCurrentPVFItemExpirationRenewsRetiredAbsoluteDateOnStaleStack 钉住背包/仓库行：
// 堆栈里当年照抄下来的 2017 退役时间戳必须被换成地平线 —— Extra、0x38、以及
// 0x0B（当前客户端读作剩余使用期限，0 就渲染成"已超过使用期限"）三处一起换。
func TestApplyCurrentPVFItemExpirationRenewsRetiredAbsoluteDateOnStaleStack(t *testing.T) {
	now := time.Date(2026, time.September, 25, 3, 0, 0, 0, time.UTC)
	retired := testRetiredCouponExpiration
	horizon := currentPVFRetiredExpirationRenewalDate
	stack := dnfrepo.ItemStack{
		ItemID:   int64(testRetiredCouponItemID),
		Count:    7,
		ExpireAt: retired,
		Extra: map[string]string{
			"expire_time": strconv.FormatInt(retired.Unix(), 10),
			"expire_unix": strconv.FormatInt(retired.Unix(), 10),
		},
	}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 75, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)

	got, changed := applyCurrentPVFItemExpirationAt(stack, dungeonDropItemDefinition{
		ItemID:         testRetiredCouponItemID,
		Kind:           dungeonDropItemStackable,
		PVFPath:        testRetiredCouponPVFPath,
		StackableType:  "[etc]",
		StackLimit:     1000,
		ExpirationDate: retired,
	}, now)
	if !changed {
		t.Fatal("retired stack was not renewed")
	}
	wantUnix := currentPVFExpirationUnix(horizon)
	if !got.ExpireAt.Equal(horizon) ||
		got.Extra["expire_time"] != strconv.FormatInt(horizon.Unix(), 10) ||
		got.Extra["expire_unix"] != strconv.FormatInt(horizon.Unix(), 10) ||
		binary.LittleEndian.Uint32(got.RawEntry[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4]) != wantUnix ||
		binary.LittleEndian.Uint16(got.RawEntry[0x0B:0x0D]) != 0xFFFF {
		t.Fatalf("renewed stack=%+v row=%x", got, got.RawEntry)
	}
	// 幂等：同一堆栈再投影一次不得再改（不得把期限越推越远）。
	again, changedAgain := applyCurrentPVFItemExpirationAt(got, dungeonDropItemDefinition{
		ItemID:         testRetiredCouponItemID,
		Kind:           dungeonDropItemStackable,
		PVFPath:        testRetiredCouponPVFPath,
		StackableType:  "[etc]",
		StackLimit:     1000,
		ExpirationDate: retired,
	}, now)
	if changedAgain && !again.ExpireAt.Equal(horizon) {
		t.Fatalf("repeat projection moved the horizon: %+v", again)
	}
	if !again.ExpireAt.Equal(horizon) {
		t.Fatalf("repeat projection expiration=%s want=%s", again.ExpireAt, horizon)
	}
}

// TestApplyCurrentPVFItemExpirationKeepsLiveRentalAndPermanentStacks：
// 续期只针对"已经过去的绝对日期"。活的绝对日期、租用实例、无期限道具、
// 以及只声明相对期限的道具都不能被动。
func TestApplyCurrentPVFItemExpirationKeepsLiveRentalAndPermanentStacks(t *testing.T) {
	now := time.Date(2026, time.September, 25, 3, 0, 0, 0, time.UTC)

	live := time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC)
	liveStack := dnfrepo.ItemStack{ItemID: int64(currentJoustEventItemFirst), Count: 3, ExpireAt: live}
	gotLive, _ := applyCurrentPVFItemExpirationAt(liveStack, dungeonDropItemDefinition{
		ItemID:         currentJoustEventItemFirst,
		Kind:           dungeonDropItemStackable,
		ExpirationDate: live,
	}, now)
	if !gotLive.ExpireAt.Equal(live) {
		t.Fatalf("live absolute expiration changed: %s want %s", gotLive.ExpireAt, live)
	}

	// 租用：实例到期时间是 now+24h，晚于退役的 PVF 日期（2017），必须是权威。
	rentalExpire := now.Add(24 * time.Hour)
	rental := dnfrepo.ItemStack{
		ItemID:   416000000,
		Count:    1,
		ExpireAt: rentalExpire,
		Extra: map[string]string{
			"source":      currentRentalItemSource,
			"expire_time": strconv.FormatInt(rentalExpire.Unix(), 10),
		},
	}
	gotRental, _ := applyCurrentPVFItemExpirationAt(rental, dungeonDropItemDefinition{
		ItemID:         416000000,
		Kind:           dungeonDropItemEquipment,
		ExpirationDate: testRetiredCouponExpiration,
	}, now)
	if !gotRental.ExpireAt.Equal(rentalExpire) {
		t.Fatalf("rental deadline was renewed to %s want %s", gotRental.ExpireAt, rentalExpire)
	}

	// 无期限道具：一个字节都不写。
	permanent := dnfrepo.ItemStack{ItemID: 490005593, Count: 5}
	gotPermanent, changed := applyCurrentPVFItemExpirationAt(permanent, dungeonDropItemDefinition{
		ItemID: 490005593,
		Kind:   dungeonDropItemStackable,
	}, now)
	if changed || !gotPermanent.ExpireAt.IsZero() || gotPermanent.Extra["expire_time"] != "" {
		t.Fatalf("permanent stack was written: changed=%t stack=%+v", changed, gotPermanent)
	}

	// 只声明相对 [usable period] 的道具：目录定义没有绝对日期，堆栈自己的
	// 实例到期时间（哪怕已经过去）保持不变 —— 相对期限的行为不变。
	relativeExpired := now.Add(-72 * time.Hour)
	relative := dnfrepo.ItemStack{
		ItemID:   10301410,
		Count:    1,
		ExpireAt: relativeExpired,
		Extra: map[string]string{
			"expire_time": strconv.FormatInt(relativeExpired.Unix(), 10),
			"item_kind":   string(dungeonDropItemStackable),
		},
	}
	gotRelative, _ := applyCurrentPVFItemExpirationAt(relative, dungeonDropItemDefinition{
		ItemID:           10301410,
		Kind:             dungeonDropItemStackable,
		UsablePeriodDays: 1,
	}, now)
	if !gotRelative.ExpireAt.Equal(relativeExpired) {
		t.Fatalf("relative usable period stack was renewed to %s want %s", gotRelative.ExpireAt, relativeExpired)
	}
}

// TestApplyCurrentPVFUsePeriodsRenewsRetiredRowOnWire 钉住发给客户端的行：
// 盘上的行还带着 2017 退役时间戳时，这一屏也必须改成地平线（0x38 与 0x0B 同步）。
func TestApplyCurrentPVFUsePeriodsRenewsRetiredRowOnWire(t *testing.T) {
	catalog := mustTestRetiredCouponCatalog(t)
	service := &Service{pvfItemCatalog: catalog}
	now := time.Date(2026, time.September, 25, 3, 0, 0, 0, time.UTC)
	horizonUnix := currentPVFExpirationUnix(currentPVFRetiredExpirationRenewalDate)

	definition, err := catalog.ResolveItem(testRetiredCouponItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.ExpirationDate.Equal(testRetiredCouponExpiration) {
		t.Fatalf("fixture definition=%+v", definition)
	}

	rows := []currentItemListEntry{{}}
	rows[0].patchCore(75, testRetiredCouponItemID, 7)
	binary.LittleEndian.PutUint32(
		rows[0].data[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4],
		uint32(testRetiredCouponExpiration.Unix()),
	)
	patched, err := service.applyCurrentPVFUsePeriodsToEntriesAt(rows, now)
	if err != nil || patched != 1 {
		t.Fatalf("rows patched=%d err=%v", patched, err)
	}
	if got := binary.LittleEndian.Uint32(rows[0].data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != horizonUnix {
		t.Fatalf("wire expiration=%d want=%d", got, horizonUnix)
	}
	if got := binary.LittleEndian.Uint16(rows[0].data[0x0B:0x0D]); got != 0xFFFF {
		t.Fatalf("wire use period=%d want=%d", got, 0xFFFF)
	}
}

// TestReconcileCurrentMailAttachmentExpirationsRenewsRetiredAbsoluteDate 钉住邮箱：
// 附件行 0x38 写的是 attachment.ExpireAt，所以退役绝对日期道具（不论附件本来
// 是"没有到期时间"还是"存着 2017 那个退役日期"）都必须被修补成地平线，且幂等。
func TestReconcileCurrentMailAttachmentExpirationsRenewsRetiredAbsoluteDate(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"7": {
				MailID: "7",
				// GM 面板邮件形状：附件既没有 ExpireAt，也没有 Extra。
				Attachments: []dnfrepo.MailAttachment{{ItemID: int64(testRetiredCouponItemID), Count: 1}},
			},
			"8": {
				MailID: "8",
				// 旧邮件形状：附件里存着当年照抄的退役日期。
				Attachments: []dnfrepo.MailAttachment{{
					ItemID:   int64(testRetiredCouponItemID),
					Count:    2,
					ExpireAt: testRetiredCouponExpiration,
				}},
			},
			"9": {
				MailID: "9",
				// 无期限道具（PVF 没有 [expiration date]）：不许被写上到期时间。
				Attachments: []dnfrepo.MailAttachment{{ItemID: 490005593, Count: 1}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 目录同时收录退役券与"无期限"道具（PVF 没有 [expiration date]）。
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":                   "",
		"equipment/equipment.lst":               "",
		"stackable/stackable.lst":               "490005938 `490005001/chn_490005938.stk`\n490005593 `490005001/chn_490005593.stk`\n",
		testRetiredCouponPVFPath:                testRetiredCouponPVFText,
		"stackable/490005001/chn_490005593.stk": "[name] `骑士马战爆竹`\n[stackable type] `[random reward item]`\n[stack limit] 1000\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 2 {
		t.Fatalf("repair changed=%d err=%v want=2", changed, err)
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	want := currentPVFRetiredExpirationRenewalDate
	for _, mailID := range []string{"7", "8"} {
		if got := mailbox.Mails[mailID].Attachments[0].ExpireAt; !got.Equal(want) {
			t.Fatalf("mail %s attachment expiry=%s want=%s", mailID, got, want)
		}
	}
	if got := mailbox.Mails["9"].Attachments[0].ExpireAt; !got.IsZero() {
		t.Fatalf("permanent attachment gained an expiration: %s", got)
	}
	// 幂等：再跑一次不得改动，也不得把期限往后推。
	again, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || again != 0 {
		t.Fatalf("repeat repair changed=%d err=%v", again, err)
	}
	reloaded, _, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, mailID := range []string{"7", "8"} {
		if got := reloaded.Mails[mailID].Attachments[0].ExpireAt; !got.Equal(want) {
			t.Fatalf("repeat repair moved mail %s to %s want %s", mailID, got, want)
		}
	}
}

// TestMailboxClaimProjectsRetiredAbsoluteDateEndToEnd 走完整条领取路径：
// 退役绝对日期道具从邮件领进背包后，堆栈与背包行（0x38 + 0x0B）都必须是地平线，
// 而不是 2017 那个死日期。
func TestMailboxClaimProjectsRetiredAbsoluteDateEndToEnd(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-retired",
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
				MailID:      "1",
				CreatedAt:   time.Now().UTC(),
				Attachments: []dnfrepo.MailAttachment{{ItemID: int64(testRetiredCouponItemID), Count: 5}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     mustTestRetiredCouponCatalog(t),
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
	claimedSlot := int16(-1)
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(testRetiredCouponItemID) {
			continue
		}
		_, slot, ok := parseSceneInventorySlotKey(key)
		if !ok {
			t.Fatalf("claimed stack landed under an unparsable key %q", key)
		}
		claimed, claimedSlot = stack, slot
		break
	}
	if claimed.ItemID != int64(testRetiredCouponItemID) {
		t.Fatalf("claimed coupon missing: %+v", inventory.Slots)
	}
	want := currentPVFRetiredExpirationRenewalDate
	if !claimed.ExpireAt.Equal(want) {
		t.Fatalf("claimed expiration=%s want=%s", claimed.ExpireAt, want)
	}
	if got := claimed.Extra["expire_time"]; got != strconv.FormatInt(want.Unix(), 10) {
		t.Fatalf("claimed expire_time=%q want=%d", got, want.Unix())
	}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, claimedSlot, claimed)
	if got := binary.LittleEndian.Uint32(entry.data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != currentPVFExpirationUnix(want) {
		t.Fatalf("claimed row 0x38=%d want=%d", got, currentPVFExpirationUnix(want))
	}
	if got := binary.LittleEndian.Uint16(entry.data[0x0B:0x0D]); got == 0 {
		t.Fatalf("claimed row 0x0B is still 0 (client renders that as 已超过使用期限): %x", entry.data[:0x20])
	}
}

// TestRealScriptPVFRetiredAbsoluteDatesAreRenewedOnEveryPath 用真实运行时 PVF 证明
// 根因与修法都不是猜的：那批券的 [expiration date] 确实停在 2017，而且
// 发放口径 / 邮件附件口径 / 堆栈口径给出的都是同一个 2028-01-01。
func TestRealScriptPVFRetiredAbsoluteDatesAreRenewedOnEveryPath(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the retired absolute dates")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	want := currentPVFRetiredExpirationRenewalDate
	retired := []struct {
		itemID uint32
		path   string
	}{
		{490005938, "stackable/490005001/chn_490005938.stk"},
		{490005971, "stackable/490005001/chn_490005971.stk"},
		{490005972, "stackable/490005001/chn_490005972.stk"},
	}
	for _, test := range retired {
		definition, err := catalog.ResolveItem(test.itemID)
		if err != nil {
			t.Fatalf("resolve %d: %v", test.itemID, err)
		}
		if definition.PVFPath != test.path {
			t.Fatalf("item %d path=%s want=%s", test.itemID, definition.PVFPath, test.path)
		}
		if !definition.ExpirationDate.Equal(testRetiredCouponExpiration) {
			t.Fatalf("item %d retired date=%s want=%s (fixture assumption changed)",
				test.itemID, definition.ExpirationDate, testRetiredCouponExpiration)
		}
		granted, err := currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil {
			t.Fatalf("item %d grant: %v", test.itemID, err)
		}
		if !granted.ExpirationDate.Equal(want) {
			t.Fatalf("item %d grant expiration=%s want=%s", test.itemID, granted.ExpirationDate, want)
		}
		resolution, err := mailboxItemResolutionFromPVF(definition, true)
		if err != nil {
			t.Fatalf("item %d mailbox resolution: %v", test.itemID, err)
		}
		if !resolution.ExpireAt.Equal(want) {
			t.Fatalf("item %d mailbox attachment expiration=%s want=%s", test.itemID, resolution.ExpireAt, want)
		}
		stack, changed := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{
			ItemID:   int64(test.itemID),
			Count:    1,
			ExpireAt: definition.ExpirationDate,
			Extra: map[string]string{
				"expire_time": strconv.FormatInt(definition.ExpirationDate.Unix(), 10),
				"expire_unix": strconv.FormatInt(definition.ExpirationDate.Unix(), 10),
			},
		}, definition, now)
		if !changed || !stack.ExpireAt.Equal(want) {
			t.Fatalf("item %d stack projection=%s changed=%t want=%s", test.itemID, stack.ExpireAt, changed, want)
		}
	}

	// 还没到期的绝对日期不得被提前续期（马战那一批已经是 2028-08-16 06:00）。
	live, err := catalog.ResolveItem(currentJoustEventItemFirst)
	if err != nil {
		t.Fatal(err)
	}
	liveWant := time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC)
	if !live.ExpirationDate.Equal(liveWant) {
		t.Fatalf("live joust item date=%s want=%s", live.ExpirationDate, liveWant)
	}
	grantedLive, err := currentPVFItemDefinitionForGrantAt(live, now)
	if err != nil {
		t.Fatal(err)
	}
	if !grantedLive.ExpirationDate.Equal(liveWant) {
		t.Fatalf("live absolute date was moved to %s want %s", grantedLive.ExpirationDate, liveWant)
	}

	// 只声明相对 [usable period] 的完成券：行为不变（发放时刻 + 1 天）。
	coupon, err := catalog.ResolveItem(testCompletionCouponItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !coupon.ExpirationDate.IsZero() || coupon.UsablePeriodDays != 1 {
		t.Fatalf("relative coupon definition=%+v", coupon)
	}
	grantedCoupon, err := currentPVFItemDefinitionForGrantAt(coupon, now)
	if err != nil {
		t.Fatal(err)
	}
	wantCoupon := time.Unix(now.Unix()+86400, 0).UTC()
	if !grantedCoupon.ExpirationDate.Equal(wantCoupon) {
		t.Fatalf("relative coupon grant=%s want=%s", grantedCoupon.ExpirationDate, wantCoupon)
	}
}
