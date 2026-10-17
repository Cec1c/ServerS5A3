package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 操作者段102：租赁武器系统用金币购买幸运星（op1000 充值）后，背包金币显示不变，
// 重选角色（整容器重建）之后才减少。
//
// 根因（服务端侧）：成功路径只回厂商 13 字节应答（01 + mode + quantity + totalLuckyStar）
// 与一条 op985 租赁点面板刷新，没有任何报文携带已提交金币，客户端本地的金币行保持旧值。
//
// 修复：成功提交后追加一条已提交钱包行（class0/op14，list0/slot0，itemID 0，余额在
// stack_count），与登录引导/金库整理/普通 NPC 商店/掉落拾取/carry-gold 用的是同一个
// buildCurrentGoldStateBody 行；事务拒绝路径一条都不发（失败仍然不扣）。

// 开关归一化：空值默认开启（本条修复）；off 档回到改动前；
// 既有 DNFBRIDGE_RENTAL_REJECT_ACK=off 也抑制本投影。
func TestCurrentRentalChargeWalletProjectionSwitchNormalization(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "")
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"   ", true},
		{"on", true},
		{"1", true},
		{"true", true},
		{"yes", true},
		{"nonsense", true},
		{"off", false},
		{"OFF", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"legacy", false},
		{"disable", false},
		{"disabled", false},
	}
	for _, testCase := range cases {
		t.Setenv(currentRentalChargeWalletEnv, testCase.value)
		if got := currentRentalChargeWalletProjectionEnabled(); got != testCase.want {
			t.Fatalf("%s=%q enabled=%t want %t", currentRentalChargeWalletEnv, testCase.value, got, testCase.want)
		}
	}
	// 既有拒绝回包开关的 off 档仍然表示“整条租赁线逐字节回到今天”。
	t.Setenv(currentRentalChargeWalletEnv, "")
	t.Setenv(currentRentalRejectAckEnv, "off")
	if currentRentalChargeWalletProjectionEnabled() {
		t.Fatal("reject-ack off must also suppress the success-path wallet projection")
	}
}

// 已提交钱包值的形状钉子：一条 class0/op14，body 逐字节等于权威金币行构建器；
// 余额在行内 6..10（即 body 的 9..13），itemID 恒为 0。
func TestPublishCurrentRentalChargeWalletMatchesCommittedGoldRow(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "")
	t.Setenv(currentRentalChargeWalletEnv, "")
	for _, gold := range []int64{0, 738_400, 1_000_000, math.MaxInt32} {
		connection := &bufferConn{}
		service := Service{}
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		if err := service.publishCurrentRentalChargeWallet(session, gold); err != nil {
			t.Fatalf("gold=%d publish: %v", gold, err)
		}
		packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
		if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			t.Fatalf("gold=%d wallet packet class=%d msg=%d, want class0/op%d", gold, packet.Header.Classification, packet.Header.MsgID, uint16(dnfenum.CmdPacketWalkoutPartyMember))
		}
		want := buildCurrentGoldStateBody(gold)
		if !bytes.Equal(packet.Body, want) {
			t.Fatalf("gold=%d wallet body=%x want %x", gold, packet.Body, want)
		}
		if len(rest) != 0 {
			t.Fatalf("gold=%d emitted an extra packet len=%d", gold, len(rest))
		}
		row := packet.Body[3:]
		if binary.LittleEndian.Uint16(row[0:2]) != 0 {
			t.Fatalf("gold=%d wallet row slot=%d want 0", gold, binary.LittleEndian.Uint16(row[0:2]))
		}
		if binary.LittleEndian.Uint32(row[2:6]) != 0 {
			t.Fatalf("gold=%d wallet row itemID=%d want 0", gold, binary.LittleEndian.Uint32(row[2:6]))
		}
		if got := int64(binary.LittleEndian.Uint32(row[6:10])); got != gold {
			t.Fatalf("gold=%d wallet row amount=%d", gold, got)
		}
	}
}

// 负值与越界余额按权威构建器钳制，不写出非法金币。
func TestPublishCurrentRentalChargeWalletClampsOutOfRangeGold(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "")
	t.Setenv(currentRentalChargeWalletEnv, "")
	for _, testCase := range []struct {
		name string
		gold int64
		want int64
	}{
		{"negative", -1, 0},
		{"above current wallet ceiling", math.MaxInt32 + 1, math.MaxInt32},
	} {
		connection := &bufferConn{}
		service := Service{}
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		if err := service.publishCurrentRentalChargeWallet(session, testCase.gold); err != nil {
			t.Fatalf("%s publish: %v", testCase.name, err)
		}
		packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		row := packet.Body[3:]
		if got := int64(binary.LittleEndian.Uint32(row[6:10])); got != testCase.want {
			t.Fatalf("%s wallet amount=%d want %d", testCase.name, got, testCase.want)
		}
	}
}

// 一键回退：两个开关的 off 档都必须一个字节都不写。
func TestPublishCurrentRentalChargeWalletWritesNothingWhenDisabled(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		env   string
		value string
	}{
		{"charge wallet off", currentRentalChargeWalletEnv, "off"},
		{"reject ack off", currentRentalRejectAckEnv, "off"},
	} {
		t.Setenv(currentRentalRejectAckEnv, "")
		t.Setenv(currentRentalChargeWalletEnv, "")
		t.Setenv(testCase.env, testCase.value)
		connection := &bufferConn{}
		service := Service{}
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		if err := service.publishCurrentRentalChargeWallet(session, 900_000); err != nil {
			t.Fatalf("%s publish: %v", testCase.name, err)
		}
		if connection.write.Len() != 0 {
			t.Fatalf("%s wrote %d bytes: %x", testCase.name, connection.write.Len(), connection.write.Bytes())
		}
	}
}

// 端到端（真实 Script.pvf）：op1000 成功后的报文顺序必须是
// “op1000 应答（厂商 13 字节）→ op985 面板 → 已提交金币行 op14”，
// 且投影的余额逐字节等于事务写进角色仓储的余额。
func TestRealPVFRentalChargePublishesCommittedGoldWallet(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf rental charge check")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		t.Fatalf("parse real rental catalog: %v", err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const startGold int64 = 3_000_000
	const startPoints = 7
	const chargeCount = 3
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: strconv.Itoa(startPoints)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       1,
		Stats:       map[string]int64{"gold": startGold},
	}); err != nil {
		t.Fatal(err)
	}
	cost := int64(chargeCount) * catalog.GoldPerPoint
	if cost <= 0 || cost >= startGold {
		t.Fatalf("real PVF cost=%d does not fit the fixture gold=%d", cost, startGold)
	}
	if startPoints+chargeCount > int(catalog.Limit) {
		t.Fatalf("real PVF limit=%d does not fit the fixture points=%d count=%d", catalog.Limit, startPoints, chargeCount)
	}
	connection := &bufferConn{}
	service := Service{
		options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: archive,
		dungeonMonsterTable:     monsters,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	body := make([]byte, currentRentalRequestWireSize)
	binary.LittleEndian.PutUint32(body[13:17], 1)
	binary.LittleEndian.PutUint32(body[17:21], chargeCount)
	if err := service.handleCurrentChargeRentalPoint(session, body); err != nil {
		t.Fatalf("charge: %v", err)
	}

	data := connection.write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, data)
	if ack.Header.Classification != 1 || ack.Header.MsgID != uint16(dnfenum.CmdPacketChargeRentpoint) {
		t.Fatalf("charge ack class=%d msg=%d", ack.Header.Classification, ack.Header.MsgID)
	}
	wantAck := upperSuccessBody(buildCurrentRentalChargeSuccessBody(1, chargeCount, startPoints+chargeCount, false))
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("charge ack body=%x want %x", ack.Body, wantAck)
	}
	state, rest := splitGameServerUpperPacket(t, rest)
	if state.Header.Classification != 0 || state.Header.MsgID != currentRentalStateMsgID {
		t.Fatalf("rental point state class=%d msg=%d", state.Header.Classification, state.Header.MsgID)
	}
	wantState := buildCurrentRentalPointStateBody(startPoints+chargeCount, nil)
	if !bytes.Equal(state.Body, wantState) {
		t.Fatalf("rental point state body=%x want %x", state.Body, wantState)
	}
	gold := startGold - cost
	wallet, rest := splitGameServerUpperPacket(t, rest)
	if wallet.Header.Classification != 0 || wallet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("committed wallet class=%d msg=%d, want class0/op%d", wallet.Header.Classification, wallet.Header.MsgID, uint16(dnfenum.CmdPacketWalkoutPartyMember))
	}
	wantWallet := buildCurrentGoldStateBody(gold)
	if !bytes.Equal(wallet.Body, wantWallet) {
		t.Fatalf("committed wallet body=%x want %x", wallet.Body, wantWallet)
	}
	if len(rest) != 0 {
		t.Fatalf("rental charge emitted an extra packet len=%d body=%x", len(rest), rest)
	}
	account, _, err := repositories.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if got := account.Metadata[currentRentalPointMetadataKey]; got != strconv.Itoa(startPoints+chargeCount) {
		t.Fatalf("persisted lucky stars=%q want %d", got, startPoints+chargeCount)
	}
	if character.Stats["gold"] != gold {
		t.Fatalf("persisted gold=%d want %d", character.Stats["gold"], gold)
	}
}

// 端到端（真实 Script.pvf）：金币不足时保持“失败不扣”——只有一个拒绝应答，
// 没有金币行，仓储一分钱都不动。
func TestRealPVFRentalChargeInsufficientGoldKeepsWalletUntouched(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf rental charge check")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		t.Fatalf("parse real rental catalog: %v", err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const chargeCount = 2
	cost := int64(chargeCount) * catalog.GoldPerPoint
	if cost <= 0 {
		t.Fatalf("real PVF gold per point=%d", catalog.GoldPerPoint)
	}
	startGold := cost - 1
	const startPoints = 5
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: strconv.Itoa(startPoints)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       1,
		Stats:       map[string]int64{"gold": startGold},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := Service{
		options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: archive,
		dungeonMonsterTable:     monsters,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	body := make([]byte, currentRentalRequestWireSize)
	binary.LittleEndian.PutUint32(body[13:17], 1)
	binary.LittleEndian.PutUint32(body[17:21], chargeCount)
	if err := service.handleCurrentChargeRentalPoint(session, body); err != nil {
		t.Fatalf("charge: %v", err)
	}
	data := connection.write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, data)
	if ack.Header.Classification != 1 || ack.Header.MsgID != uint16(dnfenum.CmdPacketChargeRentpoint) {
		t.Fatalf("reject ack class=%d msg=%d", ack.Header.Classification, ack.Header.MsgID)
	}
	if !bytes.Equal(ack.Body, []byte{0x00, 0x01}) {
		t.Fatalf("reject ack body=%x want 0001", ack.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("insufficient gold emitted extra packets len=%d body=%x", len(rest), rest)
	}
	account, _, err := repositories.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if got := account.Metadata[currentRentalPointMetadataKey]; got != strconv.Itoa(startPoints) {
		t.Fatalf("insufficient gold changed lucky stars=%q", got)
	}
	if character.Stats["gold"] != startGold {
		t.Fatalf("insufficient gold charged anyway: gold=%d want %d", character.Stats["gold"], startGold)
	}
}

// 回退档的端到端形状：DNFBRIDGE_RENTAL_CHARGE_WALLET=off 时成功路径必须回到
// 改动前的两条报文（应答 + op985 面板），一条金币行都不发。
func TestRealPVFRentalChargeWalletProjectionCanBeRolledBack(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf rental charge check")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		t.Fatalf("parse real rental catalog: %v", err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const chargeCount = 1
	const startPoints = 1
	startGold := int64(chargeCount)*catalog.GoldPerPoint + 1
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: strconv.Itoa(startPoints)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       1,
		Stats:       map[string]int64{"gold": startGold},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(currentRentalRejectAckEnv, "")
	t.Setenv(currentRentalChargeWalletEnv, "off")
	connection := &bufferConn{}
	service := Service{
		options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: archive,
		dungeonMonsterTable:     monsters,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	body := make([]byte, currentRentalRequestWireSize)
	binary.LittleEndian.PutUint32(body[13:17], 1)
	binary.LittleEndian.PutUint32(body[17:21], chargeCount)
	if err := service.handleCurrentChargeRentalPoint(session, body); err != nil {
		t.Fatalf("charge: %v", err)
	}
	data := connection.write.Bytes()
	_, rest := splitGameServerUpperPacket(t, data)
	state, rest := splitGameServerUpperPacket(t, rest)
	if state.Header.MsgID != currentRentalStateMsgID {
		t.Fatalf("rollback state msg=%d want %d", state.Header.MsgID, currentRentalStateMsgID)
	}
	if len(rest) != 0 {
		t.Fatalf("rollback mode still wrote %d bytes: %x", len(rest), rest)
	}
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if got := character.Stats["gold"]; got != startGold-int64(chargeCount)*catalog.GoldPerPoint {
		t.Fatalf("rollback mode skipped the charge: gold=%d", got)
	}
}
