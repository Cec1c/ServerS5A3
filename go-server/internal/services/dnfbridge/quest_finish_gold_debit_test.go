package dnfbridge

import (
	"encoding/binary"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"testing"
)

func TestFinishQuestGoldDebitPublishesCommittedWallet(t *testing.T) {
	service := &Service{}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	result := dnfquest.FinishCommitResult{AtomicCommitted: true, QuestID: 649, CompletionKey: "gold-submit", Currency: []dnfquest.FinishCommittedCurrency{{Name: "gold", Delta: -300000, PostValue: 7}}}
	if err := service.sendCurrentFinishQuestRewardItemUpdate(session, result); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 14 || len(rest) != 0 {
		t.Fatalf("wallet packet=%+v rest=%d", packet.Header, len(rest))
	}
	// op14: list u8, row count u16, raw inventory row (slot/item/amount).
	if len(packet.Body) != 3+currentItemListEntryWireSize || packet.Body[0] != 0 || binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 || binary.LittleEndian.Uint32(packet.Body[9:13]) != 7 {
		t.Fatalf("wallet body=%x", packet.Body)
	}
	if !currentFinishQuestBusinessFailure(dnfquest.ErrFinishRequiredGoldMissing) {
		t.Fatal("insufficient quest gold must use business failure ACK")
	}
}
