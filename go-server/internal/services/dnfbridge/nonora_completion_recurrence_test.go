package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Reconstructs the persisted task state observed after the player reconnected.
// The scene itself is not simulated by this test.
func TestNonoraPersistedTerminalState(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "same-session"
		if reconnect {
			name = "reconnected"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			catalog, tables, items := qst028MedicineFixtureCatalog(t), qst028MedicineProgression(t), qst028MedicineFixtureItemCatalog(t)
			repos := qst028MedicineRepositories(t, ctx)
			qst028MedicineSeedMaterials(t, ctx, repos, items)
			conn, service, session := qst028MedicineBridge(t, repos, catalog, tables, items)
			if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineSourceID)); err != nil {
				t.Fatal(err)
			}
			boxCallTrigger(t, service, session, conn, qst028MedicineSourceID, 0)
			qst006AssertFinishSuccess(t, boxCallFinish(t, service, session, conn, qst028MedicineSourceID), qst028MedicineSourceID)
			if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineParentID)); err != nil {
				t.Fatal(err)
			}
			boxCallTrigger(t, service, session, conn, qst028MedicineDeliveryID, 0)
			boxCallTrigger(t, service, session, conn, qst028MedicineMeetID, 16)
			boxCallFinish(t, service, session, conn, qst028MedicineMeetID)
			// The scene callback itself must publish the ready parent. Do not
			// manufacture a parent op33 or an extra refresh to make it ready.
			assertNonoraActiveQuestSnapshotProgress(t, conn.write.Bytes(), map[uint16]uint32{
				qst028MedicineParentID: 0, qst028MedicineDeliveryID: 0,
			})
			qst006AssertState(t, ctx, repos, map[int64]dnfrepo.QuestState{
				int64(qst028MedicineParentID):   {Status: "active", ProgressValue: 1},
				int64(qst028MedicineMeetID):     {Status: "completed", ProgressValue: 0},
				int64(qst028MedicineDeliveryID): {Status: "active", ProgressValue: 0},
			})
			if reconnect {
				conn, service, session = qst028MedicineBridge(t, repos, catalog, tables, items)
			}
			characterBefore, characterFound, err := repos.Character.Load(ctx, qst028MedicineCharacterID)
			if err != nil || !characterFound {
				t.Fatalf("load character before snapshot found=%t err=%v", characterFound, err)
			}
			conn.write.Reset()
			if err := service.sendCurrentActiveQuestSnapshotForSession(session, "nonora_reconnect_evidence"); err != nil {
				t.Fatal(err)
			}
			assertNonoraActiveQuestSnapshotProgress(t, conn.write.Bytes(), map[uint16]uint32{
				qst028MedicineParentID:   0,
				qst028MedicineDeliveryID: 0,
			})
			qst006AssertState(t, ctx, repos, map[int64]dnfrepo.QuestState{
				int64(qst028MedicineParentID):   {Status: "active", ProgressValue: 1},
				int64(qst028MedicineMeetID):     {Status: "completed", ProgressValue: 0},
				int64(qst028MedicineDeliveryID): {Status: "active", ProgressValue: 0},
			})
			if got := qst028MedicineCount(t, ctx, repos); got != 1 {
				t.Fatalf("snapshot consumed medicine: count=%d want=1", got)
			}
			characterAfter, characterFound, err := repos.Character.Load(ctx, qst028MedicineCharacterID)
			if err != nil || !characterFound {
				t.Fatalf("load character after snapshot found=%t err=%v", characterFound, err)
			}
			if !reflect.DeepEqual(characterBefore, characterAfter) {
				t.Fatal("snapshot projection changed character experience or progression")
			}
			// Exact ten-byte output of the matched V164/V166 native sender.
			body := []byte{0x22, 0, 0x13, 0x0d, 0xff, 0xff, 1, 0, 0xff, 0xff}
			conn.write.Reset()
			if err := service.handleCurrentFinishQuest(session, body); err != nil {
				t.Fatal(err)
			}
			qst006AssertFinishSuccess(t, splitAllFinishBridgePackets(t, conn.write.Bytes()), qst028MedicineParentID)
			qst028MedicineAssertSettled(t, ctx, repos)
			if qst028MedicineCount(t, ctx, repos) != 0 {
				t.Fatal("medicine not consumed exactly once")
			}
			settled, _, err := repos.Character.Load(ctx, qst028MedicineCharacterID)
			if err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := service.handleCurrentFinishQuest(session, body); err != nil {
				t.Fatal(err)
			}
			replayed, _, err := repos.Character.Load(ctx, qst028MedicineCharacterID)
			if err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 || qst028MedicineCount(t, ctx, repos) != 0 || !reflect.DeepEqual(settled, replayed) {
				t.Fatal("duplicate completion changed the response, medicine or reward")
			}
		})
	}
}

func assertNonoraActiveQuestSnapshotProgress(t *testing.T, data []byte, want map[uint16]uint32) {
	t.Helper()
	packets := splitAllFinishBridgePackets(t, data)
	var body []byte
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentActiveQuestSnapshotMsgID {
			body = packet.Body
			break
		}
	}
	if len(body) < 4 {
		t.Fatalf("active quest snapshot missing: packets=%v body=%x", finishBridgePacketIDs(packets), body)
	}
	count := binary.LittleEndian.Uint32(body[:4])
	if got, max := uint64(4)+uint64(count)*6, uint64(len(body)); got != max {
		t.Fatalf("active quest snapshot count=%d body_len=%d", count, len(body))
	}
	got := make(map[uint16]uint32, count)
	for offset := 4; offset < len(body); offset += 6 {
		got[binary.LittleEndian.Uint16(body[offset:])] = binary.LittleEndian.Uint32(body[offset+2:])
	}
	for questID, progress := range want {
		if value, exists := got[questID]; !exists || value != progress {
			t.Fatalf("active quest %d progress=%d want=%d; rows=%v", questID, got[questID], progress, got)
		}
	}
}
