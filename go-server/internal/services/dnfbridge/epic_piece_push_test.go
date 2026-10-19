package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是 DNFBRIDGE_EPIC_PIECE_PUSH（史诗图鉴账本下行线档位）的回归。
//
// 它要同时钉住两件互相独立的事：
//
//	A. **数据口径不受档位影响**：领取/拾取照旧入账号账本、照旧一个背包格都不占；
//	B. **线上字节按档位精确成立**：每一档发什么包、发几字节，逐字节可判。
//
// 现场背景与"1149 不是已证凶手"的静态反证见 epic_piece_push.go 文件头。
const (
	// epicPiecePushTestVendorEmptyBodyHex 是厂商 9.16 参考日志里 1149 的**唯一**形状：
	// `plain_body_len=5`、body 逐字节 `00 00 00 00 00`（7/7 条 entries=0）。
	epicPiecePushTestVendorEmptyBodyHex = "0000000000"
	// epicPiecePushTestSnapshotBodyHex 是非空全量快照（reset=0 + 1 行 + 绝对值 7），
	// 碎片 id 10300000 = 0x009D2A60。
	epicPiecePushTestSnapshotBodyHex = "0001000000602a9d0007000000"
	// epicPiecePushTestUpdateBodyHex 是非空增量（reset=1 + 1 行 + 绝对值 7）。
	epicPiecePushTestUpdateBodyHex = "0101000000602a9d0007000000"
)

// epicPiecePushTestLedgerBalance 是夹具账号里预置的账本余额（碎片 A = 7）。
const epicPiecePushTestLedgerBalance uint32 = 7

// epicPiecePushTestLedgerMetadata 与上面那个余额同源，供夹具写账号行。
func epicPiecePushTestLedgerMetadata() map[string]string {
	return map[string]string{
		dnfrepo.AccountEpicPieceMetadataKey(epicPieceBookTestPieceA): strconv.Itoa(int(epicPiecePushTestLedgerBalance)),
		"unrelated": "keep",
	}
}

// TestReadEpicPiecePushModePinsEverySwitchValue 钉死档位解析、env 名与零值默认。
//
// 关键取向：**未知值一律落默认（login_only）**，不落 `always` —— 打错的档位
// 不会静默把那条零厂商先例的增量包发出去。
func TestReadEpicPiecePushModePinsEverySwitchValue(t *testing.T) {
	if epicPiecePushEnv != "DNFBRIDGE_EPIC_PIECE_PUSH" {
		t.Fatalf("env name drifted: %s", epicPiecePushEnv)
	}
	for _, test := range []struct {
		value string
		want  epicPiecePushMode
	}{
		{"", epicPiecePushAlways},
		{"login_only", epicPiecePushLoginOnly},
		{"LOGIN_ONLY", epicPiecePushLoginOnly},
		{"  login_only  ", epicPiecePushLoginOnly},
		{"nonsense", epicPiecePushLoginOnly},
		{"always", epicPiecePushAlways},
		{"legacy", epicPiecePushAlways},
		{"vendor_empty", epicPiecePushVendorEmpty},
		{"vendor", epicPiecePushVendorEmpty},
		{"empty", epicPiecePushVendorEmpty},
		{"off", epicPiecePushOff},
		{"0", epicPiecePushOff},
		{"false", epicPiecePushOff},
		{"disabled", epicPiecePushOff},
	} {
		if got := readEpicPiecePushMode(test.value); got != test.want {
			t.Fatalf("readEpicPiecePushMode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	// 零值必须是"最保守"的那一档，否则测试/探针里的 Service 字面量会与线上默认分叉。
	var zero options
	if zero.epicPiecePush != epicPiecePushLoginOnly {
		t.Fatalf("zero-value option=%v want login_only", zero.epicPiecePush)
	}
	var nilService *Service
	if nilService.epicPiecePush() != epicPiecePushLoginOnly {
		t.Fatalf("nil service mode=%v want login_only", nilService.epicPiecePush())
	}
	// 档位矩阵：只有 always 发增量；只有 off 连引导位快照都不发；
	// 只有 vendor_empty 强制空表、且空账本也照发（厂商 7/7 条形状）。
	for _, test := range []struct {
		mode            epicPiecePushMode
		snapshot        bool
		emptySnapshot   bool
		snapshotOnEmpty bool
		update          bool
	}{
		{epicPiecePushLoginOnly, true, false, false, false},
		{epicPiecePushAlways, true, false, true, true},
		{epicPiecePushVendorEmpty, true, true, true, false},
		{epicPiecePushOff, false, false, false, false},
	} {
		if got := test.mode.snapshotEnabled(); got != test.snapshot {
			t.Fatalf("%v snapshotEnabled=%t want %t", test.mode, got, test.snapshot)
		}
		if got := test.mode.forceEmptySnapshot(); got != test.emptySnapshot {
			t.Fatalf("%v forceEmptySnapshot=%t want %t", test.mode, got, test.emptySnapshot)
		}
		if got := test.mode.snapshotEvenWhenEmpty(); got != test.snapshotOnEmpty {
			t.Fatalf("%v snapshotEvenWhenEmpty=%t want %t", test.mode, got, test.snapshotOnEmpty)
		}
		if got := test.mode.updateEnabled(); got != test.update {
			t.Fatalf("%v updateEnabled=%t want %t", test.mode, got, test.update)
		}
		if got := test.mode.String(); got == "" {
			t.Fatalf("%v String() is empty", test.mode)
		}
	}
}

// TestEpicPiecePushModeEmitsExactlyItsDocumentedWire 逐档把三条出口
// （拾取尾巴 / 领取补发 / 引导位全量快照）的**字节序列**钉死。
//
// 三条出口在 always 档下都是 13 字节的 `reset + count + (pieceId, absolute)`；
// 默认档只留下引导位那一条；vendor_empty 只发厂商那 5 字节空表；off 一个都不发。
func TestEpicPiecePushModeEmitsExactlyItsDocumentedWire(t *testing.T) {
	ctx := context.Background()
	credit := []alignedcmd.MailboxEpicPieceCredit{{
		PieceID: epicPieceBookTestPieceA,
		Count:   int64(epicPiecePushTestLedgerBalance),
		Balance: epicPiecePushTestLedgerBalance,
	}}
	for _, test := range []struct {
		mode       epicPiecePushMode
		wantFrames []string
	}{
		{
			mode: epicPiecePushAlways,
			wantFrames: []string{
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestUpdateBodyHex),
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestUpdateBodyHex),
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestSnapshotBodyHex),
			},
		},
		{
			mode: epicPiecePushLoginOnly,
			wantFrames: []string{
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestSnapshotBodyHex),
			},
		},
		{
			mode: epicPiecePushVendorEmpty,
			wantFrames: []string{
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestVendorEmptyBodyHex),
			},
		},
		{
			mode:       epicPiecePushOff,
			wantFrames: nil,
		},
	} {
		service, repositories := prepareEpicPiecePushModeService(t, test.mode)
		connection := &bufferConn{}
		session := &gameSession{conn: connection, connID: "epic-piece-push-" + test.mode.String()}

		// ① 地面拾取尾巴。
		if err := service.epicPiecePickupFollowups(
			session, epicPieceBookTestPieceA, epicPiecePushTestLedgerBalance, "test_pickup",
		); err != nil {
			t.Fatal(err)
		}
		// ② 邮件/GM 领取之后的补发（ACK 之后那条）。
		if err := service.sendCurrentEpicPieceCredits(session, credit, "test_claim"); err != nil {
			t.Fatal(err)
		}
		// ③ 登录/选角引导位的全量快照。
		if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_bootstrap"); err != nil {
			t.Fatal(err)
		}

		if got := epicPiecePushTestFrames(t, connection.write.Bytes()); !epicPiecePushTestFramesEqual(got, test.wantFrames) {
			t.Fatalf("mode=%v wire=%v want=%v", test.mode, got, test.wantFrames)
		}
		// A. 档位只动线上，账本必须一个字节都没被改。
		account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
		if err != nil || !found {
			t.Fatalf("mode=%v load account found=%t err=%v", test.mode, found, err)
		}
		balances := dnfrepo.AccountEpicPieceBalances(account)
		if balances[epicPieceBookTestPieceA] != epicPiecePushTestLedgerBalance {
			t.Fatalf("mode=%v ledger balance=%d want %d",
				test.mode, balances[epicPieceBookTestPieceA], epicPiecePushTestLedgerBalance)
		}
		if account.Metadata["unrelated"] != "keep" {
			t.Fatalf("mode=%v unrelated metadata was dropped: %v", test.mode, account.Metadata)
		}
	}
}

// TestEpicPiecePushVendorEmptySendsTheVendorTableEvenWithAnEmptyLedger 钉住
// vendor_empty 的另一半语义：账本为空时**也要**发（厂商 7/7 条 entries=0 都照发），
// 而默认档对空账本一个包都不追加（选角序列是顺序敏感区）。
func TestEpicPiecePushVendorEmptySendsTheVendorTableEvenWithAnEmptyLedger(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		mode       epicPiecePushMode
		wantFrames []string
	}{
		{
			mode: epicPiecePushVendorEmpty,
			wantFrames: []string{
				epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestVendorEmptyBodyHex),
			},
		},
		{mode: epicPiecePushLoginOnly, wantFrames: nil},
		{mode: epicPiecePushAlways, wantFrames: []string{epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestVendorEmptyBodyHex)}},
		{mode: epicPiecePushOff, wantFrames: nil},
	} {
		service, repositories := prepareEpicPiecePushModeService(t, test.mode)
		// 清空账本（只剩无关元数据），模拟"这个账号一条碎片进度都没有"。
		if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
			AccountID: epicPieceBookTestAccountID,
			Metadata:  map[string]string{"unrelated": "keep"},
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		connection := &bufferConn{}
		session := &gameSession{conn: connection, connID: "epic-piece-push-empty-" + test.mode.String()}
		if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_empty_ledger"); err != nil {
			t.Fatal(err)
		}
		if got := epicPiecePushTestFrames(t, connection.write.Bytes()); !epicPiecePushTestFramesEqual(got, test.wantFrames) {
			t.Fatalf("mode=%v empty-ledger wire=%v want=%v", test.mode, got, test.wantFrames)
		}
	}
}

// TestMailboxEpicPieceClaimKeepsLedgerAndBagButStopsTheLedgerPacket 是本次缺陷的
// 主回归（默认档）：GM 形状的系统邮件里放一件史诗碎片，领取之后
//
//	① 账号账本 +7（数据正确，与档位无关）；
//	② 角色背包一个槽位都没有（碎片不进材料页，上一条已验收的行为保住）；
//	③ **线上一个字节都没有**（默认档不发那条零厂商先例的增量 1149）；
//	④ 重登时那条全量快照照旧把 7 带回去（进度不丢，只是不再"当场"推）。
func TestMailboxEpicPieceClaimKeepsLedgerAndBagButStopsTheLedgerPacket(t *testing.T) {
	ctx := context.Background()
	service, _, repositories := prepareCurrentEpicPieceDropTest(t)
	seedCurrentEpicPiecePVFItemCatalog(t, service)
	// 显式压回默认档：共享夹具为了保住 always 档的字节钉子而被设成 always。
	service.options.epicPiecePush = epicPiecePushLoginOnly
	mailID := appendCurrentEpicPieceSystemMail(
		t, ctx, repositories, epicPieceBookTestCharacterA, epicPieceBookTestPieceA, 7,
	)
	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatalf("mailbox resolver: %v", err)
	}
	creditor := service.alignedEpicPieceCreditorForCommand(
		dnfenum.CmdPacketMailboxExtractItem, epicPieceBookTestAccountID,
	)
	result, err := dnfmail.NewOwner(repositories, resolver).
		WithEpicPieceCreditor(creditor).
		Claim(ctx, epicPieceBookTestCharacterA, dnfmail.ExtractRequest{MailIDs: []uint32{mailID}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(result.EpicPieces) != 1 ||
		result.EpicPieces[0].PieceID != epicPieceBookTestPieceA ||
		result.EpicPieces[0].Balance != 7 {
		t.Fatalf("epic piece credits=%+v want one row balance=7", result.EpicPieces)
	}
	// ② 背包零变化。
	if inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA); len(inventory.Slots) != 0 {
		t.Fatalf("epic fragment entered the bag: %+v", inventory.Slots)
	}
	// ① 账号账本。
	account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 7 {
		t.Fatalf("account ledger balance=%d want 7", got)
	}

	// ③ 默认档：ACK 之后的补发必须一个字节都不写。
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "epic-piece-push-claim"}
	if err := service.sendCurrentEpicPieceCredits(session, result.EpicPieces, "test_mailbox_claim"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("login_only must not push any packet, got %x", connection.write.Bytes())
	}

	// ④ 重登引导位快照照旧把 7 带回去（逐字节）。
	if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_relogin"); err != nil {
		t.Fatal(err)
	}
	wantFrames := []string{
		epicPiecePushTestFrame(currentEpicPieceBalanceSyncOpcode, 0, epicPiecePushTestSnapshotBodyHex),
	}
	if got := epicPiecePushTestFrames(t, connection.write.Bytes()); !epicPiecePushTestFramesEqual(got, wantFrames) {
		t.Fatalf("relogin snapshot wire=%v want=%v", got, wantFrames)
	}
}

// TestDungeonEpicPiecePickupInLoginOnlyModeKeepsLedgerWithoutAnyLedgerPacket
// 是同一件事在地面拾取那一侧的回归：默认档下拾取仍然
//
//	① 只发 op39 回包（碎片不写背包，所以没有任何 op14 背包刷新）；
//	② 账本 +1、背包为空；
//	③ 一个 1149 都不发。
func TestDungeonEpicPiecePickupInLoginOnlyModeKeepsLedgerWithoutAnyLedgerPacket(t *testing.T) {
	service, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
	service.options.epicPiecePush = epicPiecePushLoginOnly
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "epic-piece-push-pickup",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	connection := session.conn.(*bufferConn)
	dropObjectKey := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99),
	)
	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatal(err)
	}
	pickup, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if pickup.Header.MsgID != currentDungeonPickupResultOpcode ||
		binary.LittleEndian.Uint32(pickup.Body[0:4]) != dropObjectKey ||
		binary.LittleEndian.Uint16(pickup.Body[16:18]) != currentEpicPiecePickupEffectSlot {
		t.Fatalf("pickup result header=%+v body=%x", pickup.Header, pickup.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("login_only must not emit any ledger packet after a pickup: %x", trailing)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("epic fragment entered the bag: %+v", inventory.Slots)
	}
	account, found, err := repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 1 {
		t.Fatalf("account epic balance=%d want 1", got)
	}
}

// prepareEpicPiecePushModeService 造一个只有账号账本、没有副本夹具的轻量 Service：
// 账本里预置碎片 A = 7，用来逐档检查三条下行出口的字节。
func prepareEpicPiecePushModeService(t *testing.T, mode epicPiecePushMode) (*Service, dnfrepo.Group) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: epicPieceBookTestAccountID,
		Metadata:  epicPiecePushTestLedgerMetadata(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options: options{
			accountID:          epicPieceBookTestAccountID,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			epicPiecePush:      mode,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return service, repositories
}

// epicPiecePushTestFrames 把一段上行字节流拆成 `msg_id:classification:bodyHex` 列表。
// 任何拆包失败都直接判死（半包绝不能算过）。
func epicPiecePushTestFrames(t *testing.T, data []byte) []string {
	t.Helper()
	frames := make([]string, 0, 4)
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		frames = append(frames, epicPiecePushTestFrame(
			packet.Header.MsgID, packet.Header.Classification, hex.EncodeToString(packet.Body),
		))
		data = rest
	}
	return frames
}

func epicPiecePushTestFrame(msgID uint16, classification byte, bodyHex string) string {
	return strconv.Itoa(int(msgID)) + ":" +
		strconv.Itoa(int(classification)) + ":" + bodyHex
}

func epicPiecePushTestFramesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
