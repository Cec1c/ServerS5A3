package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉死"史诗碎片 → 账号级史诗图鉴"的**另外两条入包路径**（2026-09-25 现场）：
//
//  1. 邮件附件领取（op95）—— GM 面板的"发放物品"本身就是走邮件交付的
//     （`gmtool.handleItemGrant` 明确拒绝直写背包），所以这一条同时覆盖 GM 与邮件；
//  2. 登录期清扫——把已经躺在背包里的存量碎片并入账号账本并从背包移除。
//
// 现场证据（`/opt/dnf90/logs/packet_log.txt`，conn_id=game-000003，角色 3）：
//
//	line 987 RECV type=95 ENUM_CMDPACKET_MAILBOX_EXTRACT_ITEM
//	line 996 SEND msg_id=14 list_type=0 slot=0x7a(122) item_id=10300783（=史诗碎片）
//	line 1011 game-read-close … connection reset by peer（客户端已崩）
//	全日志没有任何 0x047D/1149 事件 ⇒ 这条路径没走图鉴账本。
//
// 反向夹具（同一批日志的对照）：line 1035 附近 op95 领的是 item 3037（普通材料），
// 同一个 op14 形状照常工作、会话继续 —— 所以"附件领取"本身不是问题，
// 出问题的是**碎片被当成普通材料写进材料页**。

const (
	// epicPieceBookTestCharacterA / B 与 epic_piece_book_test.go 的夹具同账号
	// （该文件直接用字面量 "99"/"100"，这里给它们起个名字）。
	epicPieceBookTestCharacterA = "99"
	epicPieceBookTestCharacterB = "100"
)

func TestEpicPieceGrantEnvSwitchDefaultsOnAndRollsBackToLegacyBag(t *testing.T) {
	for _, test := range []struct {
		value string
		want  epicPieceGrantMode
	}{
		{"", epicPieceGrantOn},
		{"on", epicPieceGrantOn},
		{"ON", epicPieceGrantOn},
		{"nonsense", epicPieceGrantOn},
		{"off", epicPieceGrantOff},
		{"0", epicPieceGrantOff},
		{"false", epicPieceGrantOff},
		{"legacy", epicPieceGrantOff},
		{"disabled", epicPieceGrantOff},
	} {
		if got := readEpicPieceGrantMode(test.value); got != test.want {
			t.Fatalf("readEpicPieceGrantMode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	if epicPieceGrantEnv != "DNFBRIDGE_EPIC_PIECE_GRANT" {
		t.Fatalf("env name drifted: %s", epicPieceGrantEnv)
	}
	if !epicPieceGrantOn.enabled() || epicPieceGrantOff.enabled() {
		t.Fatal("enabled() must be true for on and false for off")
	}
	// 零值 options（探针/测试路径）= 默认档 on。
	var zero options
	if zero.epicPieceGrant != epicPieceGrantOn {
		t.Fatalf("zero-value option=%v want on", zero.epicPieceGrant)
	}
	// 两个开关的语义必须互相独立：BOOK=off 会关掉 GRANT（全局回退），
	// GRANT=off 不会关掉拾取路径。
	service := &Service{}
	service.options.epicPieceBook = epicPieceBookOn
	service.options.epicPieceGrant = epicPieceGrantOff
	if service.epicPieceGrantEnabled() {
		t.Fatal("GRANT=off must disable the claim/purge paths")
	}
	service.options.epicPieceBook = epicPieceBookOff
	service.options.epicPieceGrant = epicPieceGrantOn
	if service.epicPieceGrantEnabled() {
		t.Fatal("BOOK=off must disable every epic-piece path, including the grant paths")
	}
	service.options.epicPieceBook = epicPieceBookOn
	service.options.epicPieceGrant = epicPieceGrantOn
	if !service.epicPieceGrantEnabled() {
		t.Fatal("both switches on must enable the claim/purge paths")
	}
}

// TestAlignedEpicPieceCreditorIsScopedToTheMailboxExtractCommand 断言入账闭包只在
// op95（领取附件）上存在：其它任何 opcode 都不允许把附件改道，否则会静默改变无关
// 命令的行为。
func TestAlignedEpicPieceCreditorIsScopedToTheMailboxExtractCommand(t *testing.T) {
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	if creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID); creditor == nil {
		t.Fatal("op95 with an account and both switches on must expose the ledger creditor")
	}
	for _, opcode := range []dnfenum.CmdPacket{
		dnfenum.CmdPacketMailboxOpen,
		dnfenum.CmdPacketMailboxSend,
		dnfenum.CmdPacketMultiMailboxSend,
		dnfenum.CmdPacketMoveItemspace,
	} {
		if creditor := service.alignedEpicPieceCreditorForCommand(opcode, epicPieceBookTestAccountID); creditor != nil {
			t.Fatalf("opcode %d must not receive an epic-piece creditor", opcode)
		}
	}
	// 账号为空 → fail-closed（不提供闭包 ⇒ 碎片照旧入包，而不是记到别人的账上）。
	if creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, "   "); creditor != nil {
		t.Fatal("an empty account id must fail closed")
	}
	// 开关回退档 → 不提供闭包。
	service.options.epicPieceGrant = epicPieceGrantOff
	if creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID); creditor != nil {
		t.Fatal("GRANT=off must not expose the ledger creditor")
	}
	service.options.epicPieceGrant = epicPieceGrantOn
	service.options.epicPieceBook = epicPieceBookOff
	if creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID); creditor != nil {
		t.Fatal("BOOK=off must not expose the ledger creditor")
	}
	_ = repositories
}

// TestMailboxClaimRoutesEpicPieceAttachmentToAccountLedgerWithoutBagRow 是本批的
// 主回归：一封"GM 物品发放"形状的系统邮件（附件只有 itemID + count，没有 Extra）
// 被领取之后
//
//	① 账本 +n（账号级）；
//	② 背包一个槽位都没动（尤其没有材料页 121..176 的行）；
//	③ 回包刷新里没有这个碎片的槽位（因为没有槽位可刷）；
//	④ ACK 之后补发的 0x047D 增量包逐字节等于"reset=1 + 1 行 + 绝对值"。
func TestMailboxClaimRoutesEpicPieceAttachmentToAccountLedgerWithoutBagRow(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	seedCurrentEpicPiecePVFItemCatalog(t, service)
	mailID := appendCurrentEpicPieceSystemMail(t, ctx, repositories, epicPieceBookTestCharacterA, epicPieceBookTestPieceA, 7)

	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatalf("mailbox resolver: %v", err)
	}
	creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID)
	result, err := dnfmail.NewOwner(repositories, resolver).
		WithEpicPieceCreditor(creditor).
		Claim(ctx, epicPieceBookTestCharacterA, dnfmail.ExtractRequest{MailIDs: []uint32{mailID}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(result.EpicPieces) != 1 ||
		result.EpicPieces[0].PieceID != epicPieceBookTestPieceA ||
		result.EpicPieces[0].Count != 7 ||
		result.EpicPieces[0].Balance != 7 {
		t.Fatalf("epic piece credits=%+v want one row piece=%d count=7 balance=7",
			result.EpicPieces, epicPieceBookTestPieceA)
	}
	if len(result.ItemSlotRefreshes) != 0 {
		t.Fatalf("epic-piece claim must not refresh any slot: %+v", result.ItemSlotRefreshes)
	}
	if result.ItemCount != 1 {
		t.Fatalf("ItemCount=%d want 1 (the ledger row still counts as a claimed attachment)", result.ItemCount)
	}
	inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
	if len(inventory.Slots) != 0 {
		t.Fatalf("epic-piece claim wrote inventory rows: %+v", inventory.Slots)
	}
	account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 7 {
		t.Fatalf("account ledger piece=%d balance=%d want 7", epicPieceBookTestPieceA, got)
	}
	if account.Metadata["unrelated"] != "keep" {
		t.Fatal("the account ledger write must not drop unrelated metadata")
	}
	// 邮件必须被标记为已领取（附件已经换成账本进度，不能重复领）。
	mailbox, found, err := repositories.Mailbox.Load(ctx, epicPieceBookTestCharacterA)
	if err != nil || !found {
		t.Fatalf("load mailbox: found=%t err=%v", found, err)
	}
	claimed := mailbox.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if !claimed.Claimed || len(claimed.Attachments) != 0 || claimed.Gold != 0 {
		t.Fatalf("claimed mail=%+v want an empty read receipt", claimed)
	}
	// 重复领取必须被拒绝（0x04），账本也绝不能被结第二次。mail 模块的
	// claimed_letter.go 默认档让"领完即结清"：第一封信领完就已经移出收件箱
	// （claimed && deleted），所以这里同时钉住"结清后不会再发货"这条不变量。
	if _, err := dnfmail.NewOwner(repositories, resolver).
		WithEpicPieceCreditor(creditor).
		Claim(ctx, epicPieceBookTestCharacterA, dnfmail.ExtractRequest{MailIDs: []uint32{mailID}}); err == nil {
		t.Fatal("re-claiming the same letter must be rejected")
	}
	account, found, err = repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("reload account: found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 7 {
		t.Fatalf("re-claim changed the account ledger: piece=%d balance=%d want 7", epicPieceBookTestPieceA, got)
	}
	settledMailbox, found, err := repositories.Mailbox.Load(ctx, epicPieceBookTestCharacterA)
	if err != nil || !found {
		t.Fatalf("reload mailbox: found=%t err=%v", found, err)
	}
	if settled := settledMailbox.Mails[strconv.FormatUint(uint64(mailID), 10)]; !settled.Deleted {
		t.Fatalf("claimed letter was not finished (no empty receipt may remain): %+v", settled)
	}

	// ACK 之后的账本增量包：reset=1 + 行数 1 + (碎片 id, 绝对值)。
	// 这条钉子钉在 `always` 档（= 改动前的逐字节行为）上；默认档 login_only
	// **不发这条包**，由 epic_piece_push_test.go 逐字节钉住。
	service.options.epicPiecePush = epicPiecePushAlways
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "epic-piece-grant"}
	if err := service.sendCurrentEpicPieceCredits(session, result.EpicPieces, "test_mailbox_claim"); err != nil {
		t.Fatal(err)
	}
	want, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{epicPieceBookTestPieceA: 7}, false)
	if err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, want) ||
		len(rest) != 0 {
		t.Fatalf("ledger notice header=%+v body=%x want=%x rest=%x",
			packet.Header, packet.Body, want, rest)
	}
}

// TestMailboxClaimKeepsNonEpicPieceAttachmentsOnTheBagPath 是反向夹具：同一封信里
// 混着"不是碎片的材料"时，材料照旧进材料页、只有碎片改走账本 —— 证明改道是按
// 运行时 PVF 的 `[piece list]` 逐件判定的，不是整条路径被替换。
func TestMailboxClaimKeepsNonEpicPieceAttachmentsOnTheBagPath(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	seedCurrentEpicPiecePVFItemCatalog(t, service)
	mailID := appendCurrentEpicPieceSystemMailRows(t, ctx, repositories, epicPieceBookTestCharacterA, []dnfrepo.MailAttachment{
		{ItemID: int64(epicPieceBookTestPieceA), Count: 3},
		{ItemID: int64(epicPieceBookTestMaterial), Count: 5},
	})
	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatalf("mailbox resolver: %v", err)
	}
	// 非碎片附件必须解析出 EpicPieceID=0（否则改道会误伤普通材料）。
	resolution, err := resolver(epicPieceBookTestMaterial)
	if err != nil {
		t.Fatalf("resolve plain material: %v", err)
	}
	if resolution.EpicPieceID != 0 {
		t.Fatalf("plain material resolved as epic piece %d", resolution.EpicPieceID)
	}
	pieceResolution, err := resolver(epicPieceBookTestPieceA)
	if err != nil {
		t.Fatalf("resolve epic piece: %v", err)
	}
	if pieceResolution.EpicPieceID != epicPieceBookTestPieceA {
		t.Fatalf("epic piece resolved as %d want %d", pieceResolution.EpicPieceID, epicPieceBookTestPieceA)
	}

	creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID)
	result, err := dnfmail.NewOwner(repositories, resolver).
		WithEpicPieceCreditor(creditor).
		Claim(ctx, epicPieceBookTestCharacterA, dnfmail.ExtractRequest{MailIDs: []uint32{mailID}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(result.EpicPieces) != 1 || result.EpicPieces[0].PieceID != epicPieceBookTestPieceA || result.EpicPieces[0].Count != 3 {
		t.Fatalf("epic piece credits=%+v want the piece only", result.EpicPieces)
	}
	if len(result.ItemSlotRefreshes) != 1 {
		t.Fatalf("plain material must still refresh exactly one slot: %+v", result.ItemSlotRefreshes)
	}
	inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
	if len(inventory.Slots) != 1 {
		t.Fatalf("plain material inventory rows=%+v want exactly one", inventory.Slots)
	}
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(epicPieceBookTestMaterial) || stack.Count != 5 {
			t.Fatalf("plain material stack key=%s stack=%+v want item %d ×5",
				key, stack, epicPieceBookTestMaterial)
		}
		if !bytes.HasPrefix([]byte(key), []byte("0:")) {
			t.Fatalf("plain material landed outside the main bag: %s", key)
		}
	}
}

// TestMailboxClaimKeepsLegacyBagPathWhenTheGrantSwitchIsOff 钉死一键回退：
// `DNFBRIDGE_EPIC_PIECE_GRANT=off` 时碎片照旧作为普通 `[material]` 进材料页，
// 账本一个字节都不写 —— 与改动前逐字节同形。
func TestMailboxClaimKeepsLegacyBagPathWhenTheGrantSwitchIsOff(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	service.options.epicPieceGrant = epicPieceGrantOff
	seedCurrentEpicPiecePVFItemCatalog(t, service)
	mailID := appendCurrentEpicPieceSystemMail(t, ctx, repositories, epicPieceBookTestCharacterA, epicPieceBookTestPieceA, 4)

	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatalf("mailbox resolver: %v", err)
	}
	resolution, err := resolver(epicPieceBookTestPieceA)
	if err != nil {
		t.Fatalf("resolve epic piece: %v", err)
	}
	if resolution.EpicPieceID != 0 {
		t.Fatalf("GRANT=off must not mark the attachment: %d", resolution.EpicPieceID)
	}
	creditor := service.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID)
	if creditor != nil {
		t.Fatal("GRANT=off must not expose the creditor")
	}
	result, err := dnfmail.NewOwner(repositories, resolver).
		WithEpicPieceCreditor(creditor).
		Claim(ctx, epicPieceBookTestCharacterA, dnfmail.ExtractRequest{MailIDs: []uint32{mailID}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(result.EpicPieces) != 0 {
		t.Fatalf("GRANT=off credited the ledger: %+v", result.EpicPieces)
	}
	if len(result.ItemSlotRefreshes) != 1 {
		t.Fatalf("GRANT=off must refresh the material slot: %+v", result.ItemSlotRefreshes)
	}
	inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
	if len(inventory.Slots) != 1 {
		t.Fatalf("GRANT=off inventory rows=%+v want the legacy material row", inventory.Slots)
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID != int64(epicPieceBookTestPieceA) || stack.Count != 4 {
			t.Fatalf("legacy stack=%+v want the fragment ×4", stack)
		}
	}
	account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	if balances := dnfrepo.AccountEpicPieceBalances(account); len(balances) != 0 {
		t.Fatalf("GRANT=off wrote the ledger: %+v", balances)
	}
}

// TestEpicPieceInventoryPurgeMovesExistingBagFragmentsToAccountLedger 覆盖存量清理：
// 操作者背包 `0:122` 里那件 10300783 必须被并入账号账本并从背包移除，且**幂等**。
func TestEpicPieceInventoryPurgeMovesExistingBagFragmentsToAccountLedger(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	fragmentKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 122)
	materialKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 121)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: epicPieceBookTestCharacterA,
		Slots: map[string]dnfrepo.ItemStack{
			fragmentKey: {ItemID: int64(epicPieceBookTestPieceA), Count: 1},
			materialKey: {ItemID: int64(epicPieceBookTestMaterial), Count: 9},
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{connID: "epic-piece-purge", selectedCharacterID: 99}
	purged, err := service.purgeCurrentInventoryEpicPieces(ctx, session, "test_purge")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged rows=%d want 1", purged)
	}
	inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
	if _, leaked := inventory.Slots[fragmentKey]; leaked {
		t.Fatalf("fragment row survived the purge: %+v", inventory.Slots)
	}
	if stack, kept := inventory.Slots[materialKey]; !kept ||
		stack.ItemID != int64(epicPieceBookTestMaterial) || stack.Count != 9 {
		t.Fatalf("non-fragment row was disturbed: %+v", inventory.Slots)
	}
	account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 1 {
		t.Fatalf("account ledger balance=%d want 1", got)
	}
	// 幂等：第二次跑既不清扫也不写账本。
	purged, err = service.purgeCurrentInventoryEpicPieces(ctx, session, "test_purge_repeat")
	if err != nil {
		t.Fatalf("repeat purge: %v", err)
	}
	if purged != 0 {
		t.Fatalf("repeat purge rows=%d want 0", purged)
	}
	account, _, err = repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 1 {
		t.Fatalf("repeat purge double-credited the ledger: %d", got)
	}

	// 回退档：碎片留在背包、账本不变。
	service.options.epicPieceGrant = epicPieceGrantOff
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: epicPieceBookTestCharacterA,
		Slots: map[string]dnfrepo.ItemStack{
			fragmentKey: {ItemID: int64(epicPieceBookTestPieceB), Count: 2},
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	purged, err = service.purgeCurrentInventoryEpicPieces(ctx, session, "test_purge_disabled")
	if err != nil {
		t.Fatalf("disabled purge: %v", err)
	}
	if purged != 0 {
		t.Fatalf("disabled purge rows=%d want 0", purged)
	}
	inventory = loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
	if stack, kept := inventory.Slots[fragmentKey]; !kept || stack.ItemID != int64(epicPieceBookTestPieceB) {
		t.Fatalf("disabled purge touched the bag: %+v", inventory.Slots)
	}
}

// TestEpicPieceInventoryPurgeLeavesTheBagWhenTheLedgerCreditFails 断言 fail-closed
// 的顺序：清扫**先入账、后写背包**，所以账本写不进去时（这里用一个不存在的账号）
// 背包必须原封不动 —— 反过来的顺序会把碎片真的弄丢。
func TestEpicPieceInventoryPurgeLeavesTheBagWhenTheLedgerCreditFails(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	// 会话没绑账号时 accountIDForSession 会落到共享兜底账号 dnf:1；这个账号行在
	// 夹具里不存在 ⇒ 原子自增失败（生产上若该账号存在，清扫会正常入账到共享账号，
	// 与服务器对未绑定会话的其它处理同口径）。
	service.options.accountID = ""
	service.options.accountPrefix = ""
	fragmentKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 122)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: epicPieceBookTestCharacterB,
		Slots: map[string]dnfrepo.ItemStack{
			fragmentKey: {ItemID: int64(epicPieceBookTestPieceA), Count: 1},
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{connID: "epic-piece-purge-unbound", selectedCharacterID: 100, accountID: "   "}
	purged, err := service.purgeCurrentInventoryEpicPieces(ctx, session, "test_purge_credit_failure")
	if err == nil {
		t.Fatalf("purge with an unavailable ledger must fail closed, purged=%d", purged)
	}
	if purged != 0 {
		t.Fatalf("failed purge reported %d purged rows", purged)
	}
	inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterB)
	if stack, kept := inventory.Slots[fragmentKey]; !kept || stack.ItemID != int64(epicPieceBookTestPieceA) {
		t.Fatalf("failed purge touched the bag: %+v", inventory.Slots)
	}
}

// seedCurrentEpicPiecePVFItemCatalog 让 `currentPVFItemCatalog()` 使用夹具 PVF。
//
// 生产路径是从盘上的 `data/dnf/Script.pvf` 建这份目录（`initialEquipmentArchiveLocked`
// + `newPVFDungeonDropCatalog`）；测试里换成同一形状的内存 source，走的仍然是
// **同一个** `newPVFDungeonDropCatalog`，所以解析出的 `dungeonDropItemDefinition`
// 与生产逐字段同源。没有这一步，`alignedMailboxItemResolverForCommand` 会去 stat
// 一个测试机上不存在的 Script.pvf。
func seedCurrentEpicPiecePVFItemCatalog(t *testing.T, service *Service) {
	t.Helper()
	if service == nil || service.dungeonMonsterTable == nil || service.dungeonMonsterTable.source == nil {
		t.Fatal("epic piece fixture has no runtime PVF source")
	}
	catalog, err := newPVFDungeonDropCatalog(service.dungeonMonsterTable.source)
	if err != nil {
		t.Fatalf("build fixture runtime-PVF item catalog: %v", err)
	}
	service.pvfItemCatalog = catalog
	service.pvfItemCatalogLoadErr = nil
}

func appendCurrentEpicPieceSystemMail(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	itemID uint32,
	count int64,
) uint32 {
	t.Helper()
	return appendCurrentEpicPieceSystemMailRows(t, ctx, repositories, characterID, []dnfrepo.MailAttachment{
		{ItemID: int64(itemID), Count: count},
	})
}

// appendCurrentEpicPieceSystemMailRows 复刻 `gmtool.handleItemGrant` 的交付形状：
// 一封系统邮件、附件只有 (itemID, count)、没有 Extra（GM 面板没有附件 Extra）。
func appendCurrentEpicPieceSystemMailRows(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	attachments []dnfrepo.MailAttachment,
) uint32 {
	t.Helper()
	mailID, err := dnfrepo.AppendSystemMail(ctx, repositories.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: characterID,
		Title:                "GM物品发放",
		Body:                 "GM 已发放道具，请在邮件中领取。",
		Source:               "dnf90-gm-tool",
		Attachments:          attachments,
		CreatedAt:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("append system mail: %v", err)
	}
	parsed, err := strconv.ParseUint(mailID, 10, 32)
	if err != nil {
		t.Fatalf("system mail id %q: %v", mailID, err)
	}
	return uint32(parsed)
}

func loadCurrentEpicPieceTestInventory(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
) dnfrepo.InventoryRecord {
	t.Helper()
	record, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		return dnfrepo.InventoryRecord{CharacterID: characterID, Slots: map[string]dnfrepo.ItemStack{}}
	}
	if record.Slots == nil {
		record.Slots = map[string]dnfrepo.ItemStack{}
	}
	return record
}

// 编译期断言：账号账本闭包就是 alignedcmd 声明的那一种，memory 组也真的支持
// 本文件用到的两条事务路径。
var (
	_ alignedcmd.MailboxEpicPieceCreditor = func(context.Context, uint32, int64) (uint32, error) {
		return 0, nil
	}
	_ = func(repositories dnfrepo.Group) {
		_ = repositories.CharacterItems
		_ = repositories.MailboxAssets
		_ = dnfrepomemory.NewMemoryGroup
	}
)

// TestRealScriptPVFOperatorFragmentIsAnEpicPieceOnThePlainMaterialFamily 是
// **真档 PVF** 回归（`DNFBRIDGE_REAL_PVF_SMOKE` 指向 runtime 的 Script.pvf 时才跑），
// 钉死两条互相独立、且直接决定修法的事实：
//
//  1. 操作者那件 10300783（现场封包 op14 的 item_id）**确实**是史诗碎片：
//     它是 `etc/epicpieceinfo.etc [piece list]` 里 `100312425 10300783` 的右半，
//     而 100312425（启明星的指引）就在操作者背包 `0:11` —— 这也是"碎片被当成
//     普通材料堆进材料页"这条缺陷的直接触发物。
//  2. 碎片的 `[stackable type]` 是**普通** `[material]`，不是图鉴专用类型。
//     这条是**反证**：它排除了"碎片在材料页渲染必然空指针"的假设 —— 客户端
//     把一件合法 `[material]` 渲染成材料本身没问题（操作者同一页 `0:121` 里
//     还有 999 个 10303749 一直没崩）。所以"碎片进背包"是不是闪退的充分条件
//     仍未判定（见报告里的保留项），但**修法不依赖这个判定**：碎片按参考实现
//     本来就不该进背包。
func TestRealScriptPVFOperatorFragmentIsAnEpicPieceOnThePlainMaterialFamily(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	const operatorPiece = uint32(10300783)
	const operatorOutput = uint32(100312425)
	catalog, err := loadCurrentEpicPieceCatalog(archive)
	if err != nil {
		t.Fatalf("load real %s: %v", currentEpicPieceInfoPath, err)
	}
	if !catalog.isEpicPiece(operatorPiece) {
		t.Fatalf("real PVF: %d is not an epic piece; the operator report and the packet log disagree", operatorPiece)
	}
	if output, found := catalog.outputFor(operatorPiece); !found || output != operatorOutput {
		t.Fatalf("outputFor(%d)=%d found=%t want %d", operatorPiece, output, found, operatorOutput)
	}
	paths, _, err := loadDungeonDropList(archive, dungeonDropStackableList)
	if err != nil {
		t.Fatalf("load %s: %v", dungeonDropStackableList, err)
	}
	path := paths[int64(operatorPiece)]
	if !strings.HasPrefix(strings.ToLower(strings.TrimPrefix(path, "stackable/")), "epicpiece/") {
		t.Fatalf("operator fragment %d maps to %q, not stackable/epicPiece/", operatorPiece, path)
	}
	// `stackable.lst` 里的路径相对 `stackable/`（`epicPiece/10300783.stk`）。
	text, err := archive.ReadText("stackable/" + path)
	if err != nil {
		t.Fatalf("read stackable/%s: %v", path, err)
	}
	if !strings.Contains(text, "`[material]`") {
		t.Fatalf("%s does not declare the plain [material] family; the crash cannot be blamed on a dedicated PVF type", path)
	}
	// 反向：同区间的 10303749（装备增幅保护券）不是碎片，且操作者背包里那 999 个
	// 一直好好地待在材料页 0:121 ⇒ "1030xxxx 出现在材料页"本身不是崩溃的充分条件。
	if catalog.isEpicPiece(10303749) {
		t.Fatal("real PVF: 10303749 is the amplification-protection ticket, not an epic piece")
	}
}
