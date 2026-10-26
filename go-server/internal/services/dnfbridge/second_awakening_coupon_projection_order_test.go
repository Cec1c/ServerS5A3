package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// An empty op574 clears the client's completed-quest lookup. The coupon handler
// must therefore publish the committed active view first, then the completed
// table, and expose the acceptable list only after both.
func TestSecondAwakeningCouponEmptyActiveProjectionOrderRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("current real PVF required")
	}
	s, p, g, _ := levelUpTicketBridgeFixture(t, 10162929, 90)
	ctx := context.Background()
	p.loginPacketSeen = true

	character, found, err := g.Character.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["grow_type"] = 0x11
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	bag, found, err := g.Inventory.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	stack := bag.Slots["0:4"]
	stack.Count = 3
	bag.Slots["0:4"] = stack
	if err := g.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}

	conn := p.conn.(*bufferConn)
	conn.write.Reset()
	req := testQuestClearCouponRequestBody(4, 121)
	handled, err := s.handleAlignedGameCommand(p, 1, 515, req)
	if err != nil || !handled {
		t.Fatalf("op515 dispatch handled=%t err=%v", handled, err)
	}
	packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
	if len(packets) == 0 || packets[0].Header.MsgID != 515 || packets[0].Header.Classification != 1 ||
		!bytes.Equal(packets[0].Body, []byte{1, 4, 0, 0, 121, 0, 0, 0}) {
		t.Fatalf("success ACK mismatch: %s", finishBridgePacketIDs(packets))
	}

	activeIndex, completedIndex, acceptableIndex, completedBody := -1, -1, -1, []byte(nil)
	questProjectionOps := make([]uint16, 0, 4)
	for index, packet := range packets {
		switch packet.Header.MsgID {
		case currentActiveQuestSnapshotMsgID:
			if activeIndex < 0 {
				activeIndex = index
			}
			questProjectionOps = append(questProjectionOps, packet.Header.MsgID)
		case currentClearQuestListMsgID:
			if completedIndex < 0 {
				completedIndex = index
				completedBody = packet.Body
			}
			questProjectionOps = append(questProjectionOps, packet.Header.MsgID)
		case currentAcceptableQuestListMsgID:
			acceptableIndex = index
			questProjectionOps = append(questProjectionOps, packet.Header.MsgID)
		}
	}
	if activeIndex < 0 || completedIndex < 0 || acceptableIndex < 0 ||
		!(activeIndex < completedIndex && completedIndex < acceptableIndex) {
		t.Fatalf("quest projection order active=%d completed=%d acceptable=%d packets=%v",
			activeIndex, completedIndex, acceptableIndex, finishBridgePacketIDs(packets))
	}
	if acceptableIndex != len(packets)-1 || questProjectionOps[len(questProjectionOps)-1] != currentAcceptableQuestListMsgID {
		t.Fatalf("acceptable list is not the final projection: packets=%v", finishBridgePacketIDs(packets))
	}
	if active := packets[activeIndex].Body; !bytes.Equal(active, []byte{0, 0, 0, 0}) {
		t.Fatalf("active quest count must be zero after the final awakening completion: %x", active)
	}

	if len(completedBody) < 3 || !bytes.Equal(completedBody[:3], []byte{0, 0, 1}) {
		t.Fatalf("completed table fixed header=%x", completedBody)
	}
	plain, err := zlibDecompress(completedBody[3:])
	if err != nil {
		t.Fatalf("completed table zlib: %v", err)
	}
	if len(plain) != 4+currentClearQuestListPayloadSize ||
		binary.LittleEndian.Uint32(plain[:4]) != currentClearQuestListPayloadSize {
		t.Fatalf("completed table boundary len=%d prefix=%d", len(plain), binary.LittleEndian.Uint32(plain[:4]))
	}

	persisted, found, err := g.Quest.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load committed quests found=%t err=%v", found, err)
	}
	completedIDs := make([]int64, 0, len(persisted.States))
	for questID, state := range persisted.States {
		if normalizeDungeonQuestStatus(state.Status) == "completed" {
			completedIDs = append(completedIDs, questID)
		}
	}
	if len(completedIDs) == 0 {
		t.Fatal("coupon transaction persisted no completed awakening quests")
	}
	for _, questID := range completedIDs {
		if questID <= 0 || questID >= currentClearQuestListPayloadSize || plain[4+int(questID)] != 1 {
			t.Fatalf("completed quest %d missing from op356 table", questID)
		}
	}

	awakened, found, err := g.Character.Load(ctx, "29")
	if err != nil || !found || awakened.Stats["grow_type"] != 0x21 {
		t.Fatalf("awakening not committed found=%t grow_type=%d err=%v", found, awakened.Stats["grow_type"], err)
	}
	afterUse, found, err := g.Inventory.Load(ctx, "29")
	if err != nil || !found || afterUse.Slots["0:4"].Count != 2 {
		t.Fatalf("coupon count after one use found=%t count=%d err=%v", found, afterUse.Slots["0:4"].Count, err)
	}

	beforeRetry := levelUpTicketBridgeSnapshot(t, g)
	conn.write.Reset()
	handled, err = s.handleAlignedGameCommand(p, 1, 515, req)
	if err != nil || !handled {
		t.Fatalf("op515 retry handled=%t err=%v", handled, err)
	}
	retryPackets := splitAllFinishBridgePackets(t, conn.write.Bytes())
	if len(retryPackets) != 1 || retryPackets[0].Header.MsgID != 515 || retryPackets[0].Header.Classification != 1 ||
		!bytes.Equal(retryPackets[0].Body, []byte{0, 19, 4, 0, 0, 121}) {
		t.Fatalf("retry must emit the exact failure ACK only: %v bodies=%x", finishBridgePacketIDs(retryPackets), conn.write.Bytes())
	}
	afterRetry := levelUpTicketBridgeSnapshot(t, g)
	if !reflect.DeepEqual(beforeRetry, afterRetry) {
		afterCharacter := afterRetry[0].(repo.CharacterRecord)
		afterInventory := afterRetry[1].(repo.InventoryRecord)
		t.Fatalf("retry mutated committed state: grow_type=%d coupon_count=%d",
			afterCharacter.Stats["grow_type"], afterInventory.Slots["0:4"].Count)
	}
}
