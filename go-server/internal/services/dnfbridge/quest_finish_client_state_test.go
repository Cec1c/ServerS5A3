package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Model the current EXE's state changes, rather than just comparing packet
// positions: op574 count=0 clears QuestManager+0xc4 (0x1cb7e40), and op356
// inserts the completed IDs back into that prerequisite lookup (0x1cc07f0).
// This does not model or assert that the native acceptance dialog opens.
func TestCurrentFinishQuestPreservesClientCompletedPrerequisites(t *testing.T) {
	for _, otherActive := range []bool{false, true} {
		name := "last_active_quest"
		if otherActive {
			name = "another_active_quest"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repositories := finishBridgeSeed(t)
			record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3145: {Status: "completed"},
			}}
			if otherActive {
				record.States[2636] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
			}
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			character, _, err := repositories.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			skill, _, err := repositories.Skill.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			connection := &bufferConn{}
			service := &Service{
				options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
				questCatalog:       finishBridgeCatalog(t),
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{conn: connection, selectedCharacterID: 19}
			if err := service.sendCurrentFinishQuestPostCommitSnapshots(session, dnfquest.FinishCommitResult{
				AtomicCommitted: true, CharacterID: "19", QuestID: 3145,
				PreviousLevel: character.Level, NewLevel: character.Level,
				PostCommitCharacter: character, PostCommitSkill: skill, PostCommitQuest: record,
			}); err != nil {
				t.Fatal(err)
			}
			packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
			completed := map[int]bool{}
			acceptableSeen := false
			for _, packet := range packets {
				switch packet.Header.MsgID {
				case currentActiveQuestSnapshotMsgID:
					count := binary.LittleEndian.Uint32(packet.Body[:4])
					if count == 0 {
						clear(completed)
					}
					want := uint32(0)
					if otherActive {
						want = 1
					}
					if count != want {
						t.Fatalf("active rows=%d want=%d", count, want)
					}
				case currentClearQuestListMsgID:
					assertFinishBridgeClearQuestProjection(t, packet, 3145)
					plain, err := zlibDecompress(packet.Body[3:])
					if err != nil {
						t.Fatal(err)
					}
					for id, flag := range plain[4:] {
						if flag != 0 {
							completed[id] = true
						}
					}
				case currentAcceptableQuestListMsgID:
					acceptableSeen = true
					if !completed[3145] {
						t.Fatal("native completed prerequisite 3145 lost before successor definitions arrived")
					}
				}
			}
			if !acceptableSeen || !completed[3145] {
				t.Fatalf("client final state: acceptable=%t prerequisite=%t", acceptableSeen, completed[3145])
			}
			assertFinishBridgeSuccessorSnapshots(t, packets, 3145, 3146)
			stored, _, err := repositories.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := stored.States[3146]; exists {
				t.Fatal("publishing successor definitions must not accept the next quest")
			}
		})
	}
}
