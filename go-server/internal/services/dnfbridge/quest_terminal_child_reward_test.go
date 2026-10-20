package dnfbridge

import (
	"encoding/binary"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"testing"
)

func TestTerminalChildFragmentSettlementACKAndRefresh(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 65)
	binary.LittleEndian.PutUint32(raw[2:6], 10164800)
	binary.LittleEndian.PutUint32(raw[6:10], 5)
	result := dnfquest.FinishCommitResult{
		AtomicCommitted: true, QuestID: 3458, CompletionKey: "parent/3458", Source: "quest_pvf:test",
		ConsumedItems: []dnfquest.FinishConsumedItem{{SlotKey: "0:65", SlotIndex: 65, ItemID: 10164800, Delta: 3, PostCount: 1}},
		Items:         []dnfquest.FinishCommittedItem{{SlotKey: "0:65", SlotIndex: 65, ItemID: 10164800, Delta: 4, PostCount: 5, CountOrSeed: 4, RawEntry: raw}},
	}
	ack, err := buildCurrentFinishQuestSuccessBody(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(ack) < 27 || binary.LittleEndian.Uint16(ack[:2]) != 3458 || ack[7] != 1 || binary.LittleEndian.Uint32(ack[11:15]) != 3 || ack[16] != 1 || binary.LittleEndian.Uint32(ack[23:27]) != 4 {
		t.Fatalf("fragment ACK %x", ack)
	}
	connection := &bufferConn{}
	svc := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	if err := svc.sendCurrentFinishQuestRewardItemUpdate(&gameSession{conn: connection, selectedCharacterID: 19}, result); err != nil {
		t.Fatal(err)
	}
	packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
	if len(packets) != 1 {
		t.Fatalf("refresh count=%d", len(packets))
	}
	body := packets[0].Body
	if len(body) != 3+currentItemListEntryWireSize || binary.LittleEndian.Uint16(body[1:3]) != 1 || binary.LittleEndian.Uint32(body[9:13]) != 5 {
		t.Fatalf("final authoritative refresh %x", body)
	}
}
