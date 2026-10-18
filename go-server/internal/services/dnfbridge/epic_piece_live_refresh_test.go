package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestEpicPieceLiveDefaultInitializesEmptyBookAndCoversDecreaseToZero(t *testing.T) {
	s, repos := prepareEpicPiecePushModeService(t, readEpicPiecePushMode(""))
	if err := repos.Account.Save(context.Background(), r.AccountRecord{AccountID: epicPieceBookTestAccountID, Metadata: map[string]string{"unrelated": "keep"}}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "图鉴实时同步"}
	if err := s.sendCurrentEpicPieceBalanceSnapshot(context.Background(), session, "空账号初始化"); err != nil {
		t.Fatal(err)
	}
	for _, balance := range []uint32{7, 3, 0} {
		if err := s.sendCurrentEpicPieceBalanceUpdate(session, epicPieceBookTestPieceA, balance, "实时数量覆盖"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		epicPiecePushTestFrame(1149, 0, "0000000000"),
		epicPiecePushTestFrame(1149, 0, "0101000000602a9d0007000000"),
		epicPiecePushTestFrame(1149, 0, "0101000000602a9d0003000000"),
		epicPiecePushTestFrame(1149, 0, "0101000000602a9d0000000000"),
	}
	if got := epicPiecePushTestFrames(t, conn.write.Bytes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("空账号初始化及7→3→0数量覆盖错误：got=%v want=%v", got, want)
	}
	account, _, _ := repos.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if !reflect.DeepEqual(account.Metadata, map[string]string{"unrelated": "keep"}) {
		t.Fatal("发包错误修改账本")
	}
}

func TestEpicPieceLiveDungeonPickupPublishesImmediatelyAndReplayDoesNotCreditTwice(t *testing.T) {
	s, runtime, repos := prepareCurrentEpicPieceDropTest(t)
	s.options.epicPiecePush = readEpicPiecePushMode("")
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "深渊拾取实时同步", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	key := seedCurrentDungeonPickupDrop(t, s, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99))
	for attempt := 0; attempt < 2; attempt++ {
		conn.write.Reset()
		if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
			t.Fatal(err)
		}
		pickup, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if pickup.Header.MsgID != currentDungeonPickupResultOpcode {
			t.Fatal("拾取应答缺失")
		}
		if len(rest) == 0 {
			t.Fatal("拾取成功后没有立即刷新史诗碎片")
		}
		update, tail := splitGameServerUpperPacket(t, rest)
		if update.Header.MsgID != 1149 || len(update.Body) != 13 || update.Body[0] != 1 || binary.LittleEndian.Uint32(update.Body[5:]) != epicPieceBookTestPieceA || binary.LittleEndian.Uint32(update.Body[9:]) != 1 || len(tail) != 0 {
			t.Fatalf("拾取刷新不是当前绝对余额：%x", update.Body)
		}
		account, _, _ := repos.Account.Load(context.Background(), epicPieceBookTestAccountID)
		if r.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA] != 1 {
			t.Fatal("重复拾取重复入账")
		}
	}
}

func TestEpicPieceLiveMailboxClaimSendsCommittedBalanceWithoutReselecting(t *testing.T) {
	ctx := context.Background()
	s, _, repos := prepareCurrentEpicPieceDropTest(t)
	s.options.epicPiecePush = readEpicPiecePushMode("")
	seedCurrentEpicPiecePVFItemCatalog(t, s)
	mail := appendCurrentEpicPieceSystemMail(t, ctx, repos, epicPieceBookTestCharacterA, epicPieceBookTestPieceA, 7)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "邮件领取后实时更新", selectedCharacterID: 99}
	s.bindGameSessionCharacter(session, 99)
	for attempt := 0; attempt < 2; attempt++ {
		conn.write.Reset()
		handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(mail))
		if err != nil || !handled {
			t.Fatal(handled, err)
		}
		data := conn.write.Bytes()
		found := 0
		sawAck := false
		for len(data) > 0 {
			packet, rest := splitGameServerUpperPacket(t, data)
			data = rest
			if packet.Header.MsgID == 95 {
				sawAck = true
			}
			if packet.Header.MsgID == 1149 {
				if !sawAck || !bytes.Equal(packet.Body, []byte{1, 1, 0, 0, 0, 0x60, 0x2a, 0x9d, 0, 7, 0, 0, 0}) {
					t.Fatalf("领取应答后没有立即同步实际余额：%x", packet.Body)
				}
				found++
			}
		}
		if attempt == 0 && found != 1 || attempt == 1 && found != 0 {
			t.Fatalf("领取次数%d发送了%d条图鉴更新", attempt, found)
		}
		account, _, _ := repos.Account.Load(ctx, epicPieceBookTestAccountID)
		if r.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA] != 7 {
			t.Fatal("领取或重复领取改变账本不正确")
		}
		inv, _, _ := repos.Inventory.Load(ctx, epicPieceBookTestCharacterA)
		if len(inv.Slots) != 0 {
			t.Fatal("碎片错误进入背包")
		}
	}
}

func TestRealPVFEpicPieceLiveCatalogSupportsFullInitializationAndZeroUpdates(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("设置真实PVF核对全部碎片目录及零余额更新")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	book, err := loadCurrentEpicPieceCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	balances := map[uint32]uint32{}
	for _, id := range book.pieces {
		balances[id] = 1
	}
	body, err := buildCurrentEpicPieceBalanceSyncBody(balances, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.pieces) == 0 || body[0] != 0 || int(binary.LittleEndian.Uint32(body[1:])) != len(book.pieces) || len(body) != 5+8*len(book.pieces) {
		t.Fatal("真实碎片全量初始化与目录不一致")
	}
	for at := 5; at < len(body); at += 8 {
		if !book.isEpicPiece(binary.LittleEndian.Uint32(body[at:])) {
			t.Fatal("全量同步含未知碎片，客户端无法覆盖")
		}
	}
	zero, err := buildCurrentEpicPieceBalanceSyncBody(map[uint32]uint32{book.pieces[0]: 0}, false)
	if err != nil || len(zero) != 13 || zero[0] != 1 || binary.LittleEndian.Uint32(zero[1:]) != 1 || binary.LittleEndian.Uint32(zero[5:]) != book.pieces[0] || binary.LittleEndian.Uint32(zero[9:]) != 0 {
		t.Fatalf("真实碎片零余额行丢失：%x %v", zero, err)
	}
	t.Logf("当前PVF共%d种合法碎片，初始化、单项归零均按现用消费者契约生成", len(book.pieces))
}
