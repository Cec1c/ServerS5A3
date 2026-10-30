package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉死"史诗碎片 → 账号级史诗图鉴"的四件事：
//  1. 下行包的**字节形状**（含厂商 9.16 那条 entries=0 的 5 字节夹具）；
//  2. 账本是**账号级**的（同账号两个角色共享进度）；
//  3. 拾取史诗碎片**不写背包任何槽位**，只入账 + 发增量包（含幂等重放）；
//  4. `DNFBRIDGE_EPIC_PIECE_BOOK=off` 一键回退到"碎片当普通材料进背包"。

const (
	epicPieceBookTestAccountID = "account-1"
	epicPieceBookTestPieceA    = uint32(10300000)
	epicPieceBookTestPieceB    = uint32(10300001)
	// epicPieceBookTestMaterial 在同一份 PVF 的同一段掉落路径上，但**不在**
	// `etc/epicpieceinfo.etc [piece list]` 里 ⇒ 必须照旧进背包（反向断言）。
	epicPieceBookTestMaterial = uint32(10300002)
)

func TestEpicPieceBookEnvSwitchDefaultsOnAndRollsBackToLegacyBag(t *testing.T) {
	for _, test := range []struct {
		value string
		want  epicPieceBookMode
	}{
		{"", epicPieceBookOn},
		{"on", epicPieceBookOn},
		{"ON", epicPieceBookOn},
		{"nonsense", epicPieceBookOn},
		{"always", epicPieceBookAlways},
		{"vendor", epicPieceBookAlways},
		{"off", epicPieceBookOff},
		{"0", epicPieceBookOff},
		{"false", epicPieceBookOff},
		{"legacy", epicPieceBookOff},
	} {
		if got := readEpicPieceBookMode(test.value); got != test.want {
			t.Fatalf("readEpicPieceBookMode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	if !epicPieceBookOn.enabled() || !epicPieceBookAlways.enabled() || epicPieceBookOff.enabled() {
		t.Fatal("enabled() must be true for on/always and false for off")
	}
	if epicPieceBookOn.snapshotEvenWhenEmpty() || !epicPieceBookAlways.snapshotEvenWhenEmpty() {
		t.Fatal("only `always` may push an empty snapshot")
	}
	if epicPieceBookEnv != "DNFBRIDGE_EPIC_PIECE_BOOK" {
		t.Fatalf("env name drifted: %s", epicPieceBookEnv)
	}
	// 零值 options（探针/测试路径）必须等于默认档 on，而不是"关"。
	var zero options
	if zero.epicPieceBook != epicPieceBookOn {
		t.Fatalf("zero-value option=%v want on", zero.epicPieceBook)
	}
}

func TestCurrentEpicPieceCatalogReadsPieceListPairs(t *testing.T) {
	source := bridgePVFSource{
		currentEpicPieceInfoPath: "[equipment piece drop info]\n" +
			"[piece list]\n450004 10300000 450013 10300001 450013 10300002 0 10300003\n" +
			"[/piece list]\n[/equipment piece drop info]\n",
	}
	catalog, err := loadCurrentEpicPieceCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	// 重复的成品 id（450013 出现两次）与非法行（成品 0）都被丢掉，只留首见。
	if catalog.count() != 2 || !catalog.isEpicPiece(epicPieceBookTestPieceA) || !catalog.isEpicPiece(epicPieceBookTestPieceB) {
		t.Fatalf("catalog pieces=%v", catalog.pieces)
	}
	if catalog.isEpicPiece(10300002) || catalog.isEpicPiece(10300003) || catalog.isEpicPiece(0) {
		t.Fatalf("catalog accepted a duplicate/invalid row: %v", catalog.pieces)
	}
	if output, found := catalog.outputFor(epicPieceBookTestPieceA); !found || output != 450004 {
		t.Fatalf("outputFor(pieceA)=%d found=%t", output, found)
	}
	if _, err := loadCurrentEpicPieceCatalog(bridgePVFSource{
		currentEpicPieceInfoPath: "[equipment piece drop info]\n[/equipment piece drop info]\n",
	}); err == nil {
		t.Fatal("a PVF without [piece list] must fail loudly")
	}
}

// TestBuildCurrentEpicPieceBalanceSyncBodyMatchesVendorShape 用厂商 9.16 参考服务端
// 实测的那 5 个 NUL 字节当夹具（`D:\DNF90-test\server\runtime\logs\packet_log.txt`：
// `EVENT kind=game-epic-book-snapshot-sent … entries=0 wire_opcode=1149`，
// body 逐字节 `00 00 00 00 00`），再钉增量与多行的升序排列。
func TestBuildCurrentEpicPieceBalanceSyncBodyMatchesVendorShape(t *testing.T) {
	if currentEpicPieceBalanceSyncOpcode != 0x047d {
		t.Fatalf("opcode=%#04x want current-90CN/vendor S0/047D", currentEpicPieceBalanceSyncOpcode)
	}
	empty, err := buildCurrentEpicPieceBalanceSyncBody(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(empty, []byte{0, 0, 0, 0, 0}) || len(empty) != currentEpicPieceBalanceMinBodySize {
		t.Fatalf("empty snapshot body=%x want vendor 0000000000", empty)
	}

	snapshot, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{
		10300808: 2,
		10300807: 7,
		10300999: 0, // 零值行必须被丢掉（客户端按行覆盖，0 行没有意义）
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 21 || snapshot[0] != 0 ||
		binary.LittleEndian.Uint32(snapshot[1:5]) != 2 ||
		binary.LittleEndian.Uint32(snapshot[5:9]) != 10300807 ||
		binary.LittleEndian.Uint32(snapshot[9:13]) != 7 ||
		binary.LittleEndian.Uint32(snapshot[13:17]) != 10300808 ||
		binary.LittleEndian.Uint32(snapshot[17:21]) != 2 {
		t.Fatalf("snapshot body=%x", snapshot)
	}
	incremental, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{10300808: 3}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(incremental) != 13 || incremental[0] != 1 ||
		binary.LittleEndian.Uint32(incremental[1:5]) != 1 ||
		binary.LittleEndian.Uint32(incremental[5:9]) != 10300808 ||
		binary.LittleEndian.Uint32(incremental[9:13]) != 3 {
		t.Fatalf("incremental body=%x", incremental)
	}
}

// TestCurrentEpicPieceBalanceIsAccountWideAndIgnoresMalformedKeys 是账本层的
// "账号级"用例：同一个账号下两个角色各自贡献，计数累加在账号上；畸形键一律忽略。
func TestCurrentEpicPieceBalanceIsAccountWideAndIgnoresMalformedKeys(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	accountID := "dnf:10013"
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		Metadata:  map[string]string{"unrelated": "keep"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	counter, ok := repositories.Account.(dnfrepo.AccountEpicPieceBalanceCounter)
	if !ok {
		t.Fatal("memory account repository has no epic-piece counter")
	}
	// 角色 A（sample-account-a）拾到 2 个碎片 A；角色 B（fmcz）再拾 1 个碎片 A + 3 个碎片 B。
	// 三次调用之间**没有**任何角色参数：账本只按账号 ID 定位。
	if balance, err := counter.AddAccountEpicPieceBalance(ctx, accountID, 10300807, 2, time.Now().UTC()); err != nil || balance != 2 {
		t.Fatalf("first credit balance=%d err=%v", balance, err)
	}
	if balance, err := counter.AddAccountEpicPieceBalance(ctx, accountID, 10300807, 1, time.Now().UTC()); err != nil || balance != 3 {
		t.Fatalf("second credit balance=%d err=%v", balance, err)
	}
	if balance, err := counter.AddAccountEpicPieceBalance(ctx, accountID, 10300000, 3, time.Now().UTC()); err != nil || balance != 3 {
		t.Fatalf("third credit balance=%d err=%v", balance, err)
	}
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	balances := dnfrepo.AccountEpicPieceBalances(account)
	if balances[10300807] != 3 || balances[10300000] != 3 || len(balances) != 2 {
		t.Fatalf("balances=%v", balances)
	}
	if account.Metadata["unrelated"] != "keep" {
		t.Fatalf("unrelated metadata was clobbered: %v", account.Metadata)
	}
	key := dnfrepo.AccountEpicPieceMetadataKey(10300807)
	if account.Metadata[key] != "3" {
		t.Fatalf("stored value=%q want 3", account.Metadata[key])
	}
	if dnfrepo.AccountEpicPieceMetadataKey(10300807) != "epic_piece:10300807" {
		t.Fatalf("metadata key shape drifted: %s", key)
	}
	for name, record := range map[string]dnfrepo.AccountRecord{
		"empty suffix":    {Metadata: map[string]string{"epic_piece:": "5"}},
		"zero id":         {Metadata: map[string]string{"epic_piece:0": "5"}},
		"non numeric id":  {Metadata: map[string]string{"epic_piece:abc": "5"}},
		"zero balance":    {Metadata: map[string]string{"epic_piece:10300807": "0"}},
		"other prefix":    {Metadata: map[string]string{"epic_piece_items": "5"}},
		"negative digits": {Metadata: map[string]string{"epic_piece:10300807": "-4"}},
	} {
		if got := dnfrepo.AccountEpicPieceBalances(record); len(got) != 0 {
			t.Fatalf("%s: malformed epic-piece key accepted: %v", name, got)
		}
	}
}

// TestEpicPieceBookSnapshotIsAccountScopedAndSwitchable 断言选角引导那条全量包的
// 发包/不发包条件与形状，以及 off 档一个包都不发。
func TestEpicPieceBookSnapshotIsAccountScopedAndSwitchable(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const accountID = "dnf:10013"
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountID: accountID, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "epic-piece-snapshot"}

	// 空账本 + 默认档（on）：不发包 —— 选角序列顺序敏感，空账本没有信息量。
	if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_empty"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("empty account emitted packet=%x", connection.write.Bytes())
	}

	// always 档：逐字节复刻厂商那条 entries=0 的空表包。
	service.options.epicPieceBook = epicPieceBookAlways
	if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_empty_vendor"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, []byte{0, 0, 0, 0, 0}) ||
		len(rest) != 0 {
		t.Fatalf("vendor-shape empty snapshot header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
	connection.write.Reset()

	// 有进度 + 默认档：发全量（reset=0），行按碎片 id 升序。
	service.options.epicPieceBook = epicPieceBookOn
	if _, err := dnfrepo.AddAccountEpicPieceBalance(ctx, repositories.Account, accountID, 10300807, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := dnfrepo.AddAccountEpicPieceBalance(ctx, repositories.Account, accountID, 10300000, 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_with_balance"); err != nil {
		t.Fatal(err)
	}
	packet, rest = splitGameServerUpperPacket(t, connection.write.Bytes())
	want, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{10300807: 4, 10300000: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, want) ||
		len(rest) != 0 {
		t.Fatalf("snapshot header=%+v body=%x want=%x rest=%x", packet.Header, packet.Body, want, rest)
	}
	connection.write.Reset()

	// off 档（一键回退）：连有进度也不发包。
	service.options.epicPieceBook = epicPieceBookOff
	if err := service.sendCurrentEpicPieceBalanceSnapshot(ctx, session, "test_disabled"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("disabled mode emitted packet=%x", connection.write.Bytes())
	}
}

// TestCurrentDungeonEpicPiecePickupCreditsAccountLedgerWithoutBagStack 是本修法的
// 主回归：一件史诗碎片掉在地上被捡起之后
//
//	① op39 回包的目的槽位是哨兵 64（不是材料页槽位）；
//	② 紧跟一条 reset=1 的增量账本包（msg_id 0x047D），绝对值正确；
//	③ 角色背包**一个槽位都没有变**（不会有碎片行）；
//	④ 账号元数据里出现 `epic_piece:<碎片 id>`；
//	⑤ 重复拾取只重发同样两条包，绝不二次入账。
func TestCurrentDungeonEpicPiecePickupCreditsAccountLedgerWithoutBagStack(t *testing.T) {
	service, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "epic-piece-pickup-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	connection := session.conn.(*bufferConn)
	dropObjectKey := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99),
	)

	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatal(err)
	}
	pickupPacket, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	balancePacket, trailing := splitGameServerUpperPacket(t, rest)
	if pickupPacket.Header.MsgID != currentDungeonPickupResultOpcode ||
		pickupPacket.Header.Classification != 0 ||
		len(pickupPacket.Body) != currentDungeonPickupResultSize ||
		binary.LittleEndian.Uint32(pickupPacket.Body[0:4]) != dropObjectKey ||
		binary.LittleEndian.Uint16(pickupPacket.Body[16:18]) != currentEpicPiecePickupEffectSlot {
		t.Fatalf("epic-piece pickup result header=%+v body=%x", pickupPacket.Header, pickupPacket.Body)
	}
	wantBalance, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{epicPieceBookTestPieceA: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if balancePacket.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		balancePacket.Header.Classification != 0 ||
		!bytes.Equal(balancePacket.Body, wantBalance) ||
		len(trailing) != 0 {
		// trailing 必须为空：史诗碎片不产生任何背包刷新包。
		t.Fatalf("epic-piece balance header=%+v body=%x want=%x trailing=%x",
			balancePacket.Header, balancePacket.Body, wantBalance, trailing)
	}

	// ③ 背包零变化。
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("epic fragment entered the bag: %+v", inventory.Slots)
	}
	// ④ 账号账本。
	account, found, err := repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 1 {
		t.Fatalf("account epic balance=%d metadata=%v", got, account.Metadata)
	}
	drop := runtime.DropOwner.byObjectKey[dropObjectKey]
	if drop.Status != runtimeDungeonDropConsumed ||
		drop.DestinationSlot != currentEpicPiecePickupEffectSlot ||
		drop.EpicPieceBalance != 1 ||
		len(drop.PickupItemUpdateBody) != 0 {
		t.Fatalf("consumed epic-piece drop=%+v", drop)
	}

	// ⑤ 幂等重放。
	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatal(err)
	}
	replayPickup, replayRest := splitGameServerUpperPacket(t, connection.write.Bytes())
	replayBalance, replayTrailing := splitGameServerUpperPacket(t, replayRest)
	if !bytes.Equal(replayPickup.Body, pickupPacket.Body) ||
		!bytes.Equal(replayBalance.Body, balancePacket.Body) ||
		len(replayTrailing) != 0 {
		t.Fatalf("replay body=%x balance=%x trailing=%x", replayPickup.Body, replayBalance.Body, replayTrailing)
	}
	account, _, err = repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != 1 {
		t.Fatalf("replay credited the ledger twice: %d", got)
	}
}

// TestCurrentDungeonEpicPiecePickupSharesProgressAcrossCharactersOfOneAccount 是
// "账号进度同步"的端到端用例：同账号第二个角色捡到另一件碎片之后，
//
//	① 账号账本同时有两行（两个角色各自贡献）；
//	② 第二个角色再登录时那条**全量**账本包把两份进度一起还给它（reset=0）。
func TestCurrentDungeonEpicPiecePickupSharesProgressAcrossCharactersOfOneAccount(t *testing.T) {
	service, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
	firstSession := &gameSession{
		conn:                &bufferConn{},
		connID:              "epic-piece-account-a",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	firstDrop := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99),
	)
	if err := service.handleCurrentDungeonPickup(firstSession, currentDungeonPickupTestBody(firstDrop)); err != nil {
		t.Fatal(err)
	}

	secondSession := &gameSession{
		conn:                &bufferConn{},
		connID:              "epic-piece-account-b",
		selectedCharacterID: 100,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	secondDrop := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestPieceB, currentSceneActorObjectKey(100),
	)
	if err := service.handleCurrentDungeonPickup(secondSession, currentDungeonPickupTestBody(secondDrop)); err != nil {
		t.Fatal(err)
	}
	secondConnection := secondSession.conn.(*bufferConn)
	_, secondRest := splitGameServerUpperPacket(t, secondConnection.write.Bytes())
	secondBalance, secondTrailing := splitGameServerUpperPacket(t, secondRest)
	wantSecond, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{epicPieceBookTestPieceB: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if secondBalance.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		!bytes.Equal(secondBalance.Body, wantSecond) ||
		len(secondTrailing) != 0 {
		t.Fatalf("second character balance header=%+v body=%x want=%x trailing=%x",
			secondBalance.Header, secondBalance.Body, wantSecond, secondTrailing)
	}

	account, found, err := repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	balances := dnfrepo.AccountEpicPieceBalances(account)
	if balances[epicPieceBookTestPieceA] != 1 || balances[epicPieceBookTestPieceB] != 1 || len(balances) != 2 {
		t.Fatalf("account-wide balances=%v", balances)
	}

	// ② 第二个角色重新登录：全量包必须把两行一起下发（顺序与形状都对）。
	secondConnection.write.Reset()
	if err := service.sendCurrentEpicPieceBalanceSnapshot(
		context.Background(), secondSession, "test_account_wide_restore",
	); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, secondConnection.write.Bytes())
	want, err := buildCurrentEpicPieceBalanceSyncBody(balances, true)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		!bytes.Equal(packet.Body, want) ||
		len(rest) != 0 {
		t.Fatalf("account restore body=%x want=%x rest=%x", packet.Body, want, rest)
	}
}

// TestCurrentDungeonNonEpicPieceStillEntersTheBag 是**反向**断言：同一份 PVF、同一段
// 掉落路径上，目录里没有的 10300002 必须照旧进背包 —— 证明这次改动只动了
// `[piece list]` 里的碎片，没有把材料页整体改道。
func TestCurrentDungeonNonEpicPieceStillEntersTheBag(t *testing.T) {
	service, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "non-epic-piece-pickup-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	connection := session.conn.(*bufferConn)
	dropObjectKey := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestMaterial, currentSceneActorObjectKey(99),
	)
	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatal(err)
	}
	pickupPacket, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	itemUpdate, trailing := splitGameServerUpperPacket(t, rest)
	if pickupPacket.Header.MsgID != currentDungeonPickupResultOpcode ||
		binary.LittleEndian.Uint16(pickupPacket.Body[16:18]) == currentEpicPiecePickupEffectSlot ||
		itemUpdate.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(trailing) != 0 {
		t.Fatalf("non-epic pickup header=%+v body=%x update=%+v trailing=%x",
			pickupPacket.Header, pickupPacket.Body, itemUpdate.Header, trailing)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found || len(inventory.Slots) != 1 {
		t.Fatalf("non-epic material must still reach the bag: found=%t slots=%+v err=%v", found, inventory.Slots, err)
	}
	account, found, err := repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	if len(dnfrepo.AccountEpicPieceBalances(account)) != 0 {
		t.Fatalf("non-epic material was credited to the epic book: %v", account.Metadata)
	}
}

// TestCurrentDungeonEpicPiecePickupHonoursOptOut 断言 off 档下同一件碎片照旧进背包
// （一键回退），且不发任何账本包。
func TestCurrentDungeonEpicPiecePickupHonoursOptOut(t *testing.T) {
	service, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
	service.options.epicPieceBook = epicPieceBookOff
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "epic-piece-optout-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	connection := session.conn.(*bufferConn)
	dropObjectKey := seedCurrentDungeonPickupDrop(
		t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99),
	)
	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatal(err)
	}
	pickupPacket, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	itemUpdate, trailing := splitGameServerUpperPacket(t, rest)
	if pickupPacket.Header.MsgID != currentDungeonPickupResultOpcode ||
		binary.LittleEndian.Uint16(pickupPacket.Body[16:18]) == currentEpicPiecePickupEffectSlot ||
		itemUpdate.Header.MsgID == currentEpicPieceBalanceSyncOpcode ||
		len(trailing) != 0 {
		t.Fatalf("opt-out pickup header=%+v body=%x update=%+v trailing=%x",
			pickupPacket.Header, pickupPacket.Body, itemUpdate.Header, trailing)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found || len(inventory.Slots) != 1 {
		t.Fatalf("opt-out must restore the legacy bag behavior: found=%t slots=%+v err=%v", found, inventory.Slots, err)
	}
	account, found, err := repositories.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	if len(dnfrepo.AccountEpicPieceBalances(account)) != 0 {
		t.Fatalf("opt-out still credited the epic book: %v", account.Metadata)
	}
}

// TestEpicPieceCatalogUsesTheRuntimeDungeonPVFSource 保证 Service 侧取目录走的是
// 运行时 PVF source（而不是另一份硬编码表），并且缓存按 monster catalog 指针生效。
func TestEpicPieceCatalogUsesTheRuntimeDungeonPVFSource(t *testing.T) {
	service, _, _ := prepareCurrentEpicPieceDropTest(t)
	first, err := service.epicPieceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if first.count() != 2 || !first.isEpicPiece(epicPieceBookTestPieceA) || first.isEpicPiece(epicPieceBookTestMaterial) {
		t.Fatalf("service catalog=%v", first.pieces)
	}
	second, err := service.epicPieceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("catalog must be cached per runtime PVF source")
	}
}

// TestCurrentSelectInventoryBootstrapCarriesTheAccountEpicPieceLedger 把"账号进度同步"
// 钉在**真实插入点**上：厂商 9.16 的事件 source 是
// `…after_selected_containers`（actor 绑定之后、六个容器之后、完整 mode1 之前），
// 本服务端对应的位置就是物品锁快照之后。这里逐字节断言那一条包**紧跟**在物品锁
// 快照之后，且同一角色的引导只发一次。
func TestCurrentSelectInventoryBootstrapCarriesTheAccountEpicPieceLedger(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const accountID = "dnf:1"
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   accountID,
		Stats:       map[string]int64{"gold": 1000000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		Metadata: map[string]string{
			dnfrepo.AccountEpicPieceMetadataKey(epicPieceBookTestPieceA): "6",
			dnfrepo.AccountEpicPieceMetadataKey(epicPieceBookTestPieceB): "2",
			"unrelated": "keep",
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountPrefix: "dnf:"},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "select-inventory-epic-piece",
		channel:             channelcatalog.Channel{ID: 38},
		selectedCharacterID: 77,
	}
	if err := service.sendCurrentSelectInventoryBootstrap(session, "epic_piece_snapshot"); err != nil {
		t.Fatal(err)
	}
	rest, _, _ := requireCurrentSelectInventoryBootstrapPackets(t, connection.write.Bytes())
	packet, trailing := splitCurrentGameServerUpperPacketAuto(t, rest)
	want, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{
		epicPieceBookTestPieceA: 6,
		epicPieceBookTestPieceB: 2,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Header.MsgID != currentEpicPieceBalanceSyncOpcode ||
		packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, want) {
		t.Fatalf("select bootstrap epic-piece ledger header=%+v body=%x want=%x trailing=%x",
			packet.Header, packet.Body, want, trailing)
	}
	trailing, _, _ = requireCurrentFateLoginTailPackets(t, trailing, dnfproto.GameServerUpperHeaderSize, 0, 0)
	if len(trailing) != 0 {
		t.Fatalf("select bootstrap emitted unknown packets after Fate: %x", trailing)
	}
	// 同一角色的引导是 one-shot：第二次不能再发。
	connection.write.Reset()
	if err := service.sendCurrentSelectInventoryBootstrap(session, "epic_piece_snapshot_repeat"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("repeated select bootstrap re-emitted packets: %x", connection.write.Bytes())
	}
}

// prepareCurrentEpicPieceDropTest 自建一份"带史诗碎片目录"的副本掉落夹具。
// 目录、stackable.lst 与两件碎片定义都必须在 `loadBridgeDungeonStaticData` **之前**
// 装好，否则 `itemRefs` 里查不到碎片 id、`seedCurrentDungeonPickupDrop` 会解析失败。
func prepareCurrentEpicPieceDropTest(t *testing.T) (*Service, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["map/dungeon/test/start.map"] = "[map name]\n`start`\n" +
		"[dungeon]\n700\n" +
		"[type]\n`[start]`\n" +
		"[monster]\n3001 10 0 100 200 0 0 1 `[fixed]` `[normal]`\n"
	source["monster/test.gob"] += "[item]\n" + strconv.FormatUint(uint64(epicPieceBookTestPieceA), 10) + " 10\n[/item]\n"
	source[dungeonDropStackableList] =
		strconv.FormatUint(uint64(epicPieceBookTestPieceA), 10) + " `epicPiece/10300000.stk`\n" +
			strconv.FormatUint(uint64(epicPieceBookTestPieceB), 10) + " `epicPiece/10300001.stk`\n" +
			strconv.FormatUint(uint64(epicPieceBookTestMaterial), 10) + " `material/not_a_piece.stk`\n"
	source[dungeonDropEquipmentList] = "9001 `weapon/test_drop.equ`\n"
	for _, piece := range []struct {
		id   uint32
		name string
	}{
		{epicPieceBookTestPieceA, "普拉塔尼的黄金石碎片"},
		{epicPieceBookTestPieceB, "火焰赫瑞克的眼泪碎片"},
	} {
		source["stackable/epicPiece/"+strconv.FormatUint(uint64(piece.id), 10)+".stk"] =
			"[name]\n`" + piece.name + "`\n" +
				"[explain]\n`史诗装备碎片， 用于制作相应装备的材料。`\n[rarity]\n4\n" +
				"[stackable type]\n`[material]`\n[stack limit]\n9999\n[attach type]\n`[trade]`\n"
	}
	source["stackable/material/not_a_piece.stk"] = "[name]\n`普通材料`\n" +
		"[explain]\n`不是史诗碎片。`\n[rarity]\n1\n" +
		"[stackable type]\n`[material]`\n[stack limit]\n9999\n[attach type]\n`[trade]`\n"
	source["equipment/weapon/test_drop.equ"] = "[name]\n`Test Equipment`\n[durability]\n50\n[attach type]\n`[trade]`\n"
	// 目录真源：真档同名文件，这里只放两行二元组。
	source[currentEpicPieceInfoPath] = "[equipment piece drop info]\n[piece list]\n" +
		strconv.FormatUint(uint64(450004), 10) + " " + strconv.FormatUint(uint64(epicPieceBookTestPieceA), 10) + " " +
		strconv.FormatUint(uint64(450013), 10) + " " + strconv.FormatUint(uint64(epicPieceBookTestPieceB), 10) + "\n" +
		"[/piece list]\n[/equipment piece drop info]\n"

	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, characterID := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   epicPieceBookTestAccountID,
			Level:       90,
			Stats:       map[string]int64{"fatigue": 100},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: characterID,
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 账号级账本要求账号行存在（0.1.8 的 AddMetadataCounter 与它一致：账号行缺失即失败）。
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: epicPieceBookTestAccountID,
		Metadata:  map[string]string{"unrelated": "keep"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		// epicPiecePush 显式取 `always`（改动前的逐字节行为）：本夹具下的用例都是
		// "拾取/领取之后补一条增量 1149"的字节级钉子，必须钉在 always 档上。
		// 默认档（login_only）与 vendor_empty / off 的包序在 epic_piece_push_test.go。
		options: options{
			accountID:          epicPieceBookTestAccountID,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			epicPiecePush:      epicPiecePushAlways,
		},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	runtime, _, err := service.prepareDungeonRuntime(
		context.Background(),
		&gameSession{selectedCharacterID: 99},
		dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	return service, runtime, repositories
}
