package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	qst006AccountID = defaultAccountPrefix + "1"
	qst006Character = "19"
	qst006Parent    = uint16(3249)
	qst006MeetNPC   = uint16(3609)
	qst006SeekMeet  = uint16(3610)
	qst006QueenNPC  = int64(303)
	qst006Letter    = int64(10164786)
)

// TestSeekerQST006QueenLinkedObjectivesRealPVF verifies the real "梅娅女王"
// quest family through accept, the current op33 bridge handlers, persistence,
// reappearance snapshots and the parent op34 settlement path. Client callbacks
// are simulated; no quest state is fabricated after acceptance.
func TestSeekerQST006QueenLinkedObjectivesRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the QST-006 Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := archive.Snapshot()
	t.Logf("PVF path=%s sha256=%s size=%d files=%d", snapshot.Path, snapshot.Checksum, snapshot.Size, snapshot.FileCount)

	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	progressTables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	itemCatalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	qst006AssertRealIdentity(t, archive, catalog, itemCatalog)
	qst006CheckDangerousResearchMaterials(t, ctx, catalog, progressTables, itemCatalog)

	t.Run("missing-letter-rejected-without-archive-or-assets", func(t *testing.T) {
		repositories := qst006Repositories(t, ctx)
		owner := qst006Accept(t, ctx, repositories, catalog)
		conn, service, session := qst006Bridge(repositories, catalog, progressTables, itemCatalog)

		qst006CallTrigger(t, service, session, conn, qst006MeetNPC, 0x00)
		qst006CallChildFinish(t, service, session, conn, qst006MeetNPC)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "active", ProgressValue: 1},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "active", ProgressValue: 513},
		})

		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x10)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x20)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "active", ProgressValue: 1},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "active", ProgressValue: 0},
		})

		packets := qst006CallFinish(t, service, session, conn, qst006SeekMeet)
		qst006AssertFinishFailure(t, packets, qst006SeekMeet)
		record, _, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("missing-letter after direct child op34 parent=%+v child3610=%+v", record.States[3249], record.States[3610])
		if state := record.States[3610]; state.Status != "active" || state.ProgressValue != 0 ||
			state.Extra["settlement_receipt_v1"] != "" || state.Extra["reward_state"] != "" {
			t.Fatalf("missing letter archived child 3610: %+v", state)
		}
		if state := record.States[3249]; state.Status != "active" || state.ProgressValue != 1 {
			t.Fatalf("missing letter changed parent 3249: %+v", state)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("missing-letter inventory changed: count=%d", got)
		}

		packets = qst006CallFinish(t, service, session, conn, qst006Parent)
		qst006AssertFinishFailure(t, packets, qst006Parent)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "active", ProgressValue: 1},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "active", ProgressValue: 0},
		})
		record, _, err = repositories.Quest.Load(ctx, qst006Character)
		if err != nil {
			t.Fatal(err)
		}
		if state := record.States[int64(qst006Parent)]; state.Extra["settlement_receipt_v1"] != "" || state.Extra["reward_state"] != "" {
			t.Fatalf("missing terminal material archived parent: %+v", state)
		}
		if state := record.States[int64(qst006SeekMeet)]; state.Extra["settlement_receipt_v1"] != "" || state.Extra["reward_state"] != "" {
			t.Fatalf("missing terminal material archived child: %+v", state)
		}
		if _, finishErr := qst006Finish(ctx, owner, catalog, progressTables); !errors.Is(finishErr, dnfquest.ErrFinishRequiredItemsMissing) {
			t.Fatalf("parent terminal child missing material error=%v, want ErrFinishRequiredItemsMissing", finishErr)
		}
	})

	t.Run("direct-child-settlement-before-parent", func(t *testing.T) {
		repositories := qst006Repositories(t, ctx)
		owner := qst006Accept(t, ctx, repositories, catalog)
		qst006AddLetter(t, ctx, repositories)
		conn, service, session := qst006Bridge(repositories, catalog, progressTables, itemCatalog)

		qst006CallTrigger(t, service, session, conn, qst006MeetNPC, 0x00)
		qst006CallChildFinish(t, service, session, conn, qst006MeetNPC)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x10)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x20)

		packets := qst006CallFinish(t, service, session, conn, qst006SeekMeet)
		qst006AssertFinishSuccess(t, packets, qst006SeekMeet)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "active", ProgressValue: 0},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "completed", ProgressValue: 0},
		})
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("direct child settlement did not consume letter: count=%d", got)
		}
		record, _, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil {
			t.Fatal(err)
		}
		receipt := qst006ReadReceipt(t, record.States[3610])
		qst006AssertSingleLetterConsumption(t, receipt.ConsumedItems)

		conn.write.Reset()
		if err := service.handleCurrentFinishQuest(session, qst006ChildFinishBody(qst006SeekMeet)); err != nil {
			t.Fatal(err)
		}
		if got := conn.write.Len(); got != 0 {
			t.Fatalf("duplicate terminal child op34 wrote %d bytes", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("duplicate terminal child consumed letter again: count=%d", got)
		}

		reconnectConn := &bufferConn{}
		reconnect := &gameSession{conn: reconnectConn, selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19}}
		packets = qst006CallFinishOnSession(t, service, reconnect, qst006SeekMeet)
		qst006AssertFinishSuccess(t, packets, qst006SeekMeet)
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("reconnected child replay consumed letter again: count=%d", got)
		}

		beforeFinish, _, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil {
			t.Fatal(err)
		}
		rows := currentSelectAckQuestRows(beforeFinish, true)
		if !reflect.DeepEqual(rows, []currentSelectAckQuestRow{{questID: qst006Parent, triggerValue: 0}}) {
			t.Fatalf("active rows before finish=%+v", rows)
		}
		reconnectConn.write.Reset()
		if err := service.sendCurrentActiveQuestSnapshotForSession(reconnect, "qst006-relogin-snapshot"); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, reconnectConn.write.Bytes())
		if len(rest) != 0 || packet.Header.MsgID != currentActiveQuestSnapshotMsgID ||
			!reflect.DeepEqual(packet.Body, buildCurrentActiveQuestSnapshotBody(beforeFinish, true)) {
			t.Fatalf("relogin snapshot header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
		}

		packets = qst006CallFinish(t, service, session, conn, qst006Parent)
		qst006AssertFinishSuccess(t, packets, qst006Parent)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "completed", ProgressValue: 0},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "completed", ProgressValue: 0},
		})
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("parent settlement changed already-consumed child material: count=%d", got)
		}

		conn.write.Reset()
		if err := service.handleCurrentFinishQuest(session, qst006ChildFinishBody(qst006Parent)); err != nil {
			t.Fatal(err)
		}
		if got := conn.write.Len(); got != 0 {
			t.Fatalf("duplicate parent op34 wrote %d bytes", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("duplicate parent op34 consumed material again: count=%d", got)
		}
		if _, finishErr := qst006Finish(ctx, owner, catalog, progressTables); finishErr != nil {
			t.Fatalf("durable parent replay: %v", finishErr)
		}
	})

	t.Run("parent-callback-then-terminal-parent-settlement", func(t *testing.T) {
		repositories := qst006Repositories(t, ctx)
		qst006Accept(t, ctx, repositories, catalog)
		qst006AddLetter(t, ctx, repositories)
		conn, service, session := qst006Bridge(repositories, catalog, progressTables, itemCatalog)

		qst006CallTrigger(t, service, session, conn, qst006MeetNPC, 0x00)
		qst006CallChildFinish(t, service, session, conn, qst006MeetNPC)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x10)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x20)
		qst006CallTrigger(t, service, session, conn, qst006Parent, 0x00)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "active", ProgressValue: 0},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "active", ProgressValue: 0},
		})
		if got := qst006LetterCount(t, ctx, repositories); got != 1 {
			t.Fatalf("parent op33 claimed terminal child material: count=%d", got)
		}

		packets := qst006CallFinish(t, service, session, conn, qst006Parent)
		qst006AssertFinishSuccess(t, packets, qst006Parent)
		qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
			3249: {Status: "completed", ProgressValue: 0},
			3609: {Status: "completed", ProgressValue: 0},
			3610: {Status: "completed", ProgressValue: 0},
		})
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("parent terminal settlement left letter count=%d", got)
		}
		record, _, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil {
			t.Fatal(err)
		}
		receipt := qst006ReadReceipt(t, record.States[int64(qst006Parent)])
		if len(receipt.TerminalChildren) != 1 || receipt.TerminalChildren[0].QuestID != int64(qst006SeekMeet) {
			t.Fatalf("parent terminal child receipt=%+v", receipt.TerminalChildren)
		}
		qst006AssertSingleLetterConsumption(t, receipt.TerminalChildren[0].ConsumedItems)

		conn.write.Reset()
		if err := service.handleCurrentFinishQuest(session, qst006ChildFinishBody(qst006Parent)); err != nil {
			t.Fatal(err)
		}
		if got := conn.write.Len(); got != 0 {
			t.Fatalf("duplicate terminal parent op34 wrote %d bytes", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("duplicate terminal parent consumed material again: count=%d", got)
		}
	})
}

// Exercise the actual four-material quest through accept and bridge callbacks.
// The callback count follows the accepted trigger, so this settlement regression
// also runs on the public baseline preceding the separate QST-003 trigger fix.
func qst006CheckDangerousResearchMaterials(t *testing.T, ctx context.Context, catalog *dnfquest.Catalog, tables *progression.Tables, items *pvfDungeonDropCatalog) {
	t.Helper()
	definition, ok := catalog.Find(3763)
	if !ok || definition.MainQuestID != 3729 || !reflect.DeepEqual(definition.IntData, []int64{3033, 200, 3034, 200, 3035, 200, 3036, 200, 79}) {
		t.Fatalf("dangerous research material identity=%+v", definition)
	}
	for missing := -1; missing < 4; missing++ {
		t.Run(fmt.Sprintf("real-four-materials-missing-%d", missing), func(t *testing.T) {
			repositories := qst006Repositories(t, ctx)
			character, _, err := repositories.Character.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			character.Level = 81
			if err := repositories.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			skill, _, err := repositories.Skill.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			skill.Points.SyncedLevel = 81
			if err := repositories.Skill.Save(ctx, skill); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: qst006Character, States: map[int64]dnfrepo.QuestState{3710: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfquest.NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 81, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3729})); err != nil {
				t.Fatal(err)
			}
			inventory := dnfrepo.InventoryRecord{CharacterID: qst006Character, Slots: map[string]dnfrepo.ItemStack{}}
			for i := 0; i < 4; i++ {
				count := int64(200)
				if i == missing {
					count--
				}
				raw := make([]byte, currentItemListEntryWireSize)
				binary.LittleEndian.PutUint16(raw[0:2], uint16(40+i))
				binary.LittleEndian.PutUint32(raw[2:6], uint32(3033+i))
				binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
				inventory.Slots[fmt.Sprintf("0:%d", 40+i)] = dnfrepo.ItemStack{ItemID: int64(3033 + i), Count: count, RawEntry: raw}
			}
			if err := repositories.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			conn, service, session := qst006Bridge(repositories, catalog, tables, items)
			qst006CallTrigger(t, service, session, conn, 3762, 0)
			qst006CallChildFinish(t, service, session, conn, 3762)
			record, _, err := repositories.Quest.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			materialCallbacks := record.States[3763].ProgressValue & 0x1ff
			if materialCallbacks != 1 && materialCallbacks != 200 {
				t.Fatalf("unexpected accepted material trigger=%d", materialCallbacks)
			}
			for i := int64(0); i < materialCallbacks; i++ {
				qst006CallTrigger(t, service, session, conn, 3763, 0x10)
			}
			qst006CallTrigger(t, service, session, conn, 3763, 0x20)
			qst006CallTrigger(t, service, session, conn, 3729, 0)
			qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{3729: {Status: "active", ProgressValue: 0}, 3762: {Status: "completed"}, 3763: {Status: "active"}})
			before, _, err := repositories.Quest.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			packets := qst006CallFinish(t, service, session, conn, 3729)
			after, _, err := repositories.Inventory.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			if missing >= 0 {
				qst006AssertFinishFailure(t, packets, 3729)
				current, _, err := repositories.Quest.Load(ctx, qst006Character)
				if err != nil || !reflect.DeepEqual(current, before) || !reflect.DeepEqual(after, inventory) {
					t.Fatalf("missing material changed quest/inventory: err=%v quest=%+v inventory=%+v", err, current, after)
				}
				return
			}
			if len(packets) == 1 && bytes.Equal(packets[0].Body, []byte{0, 22}) {
				_, err := owner.ApplyFinishSettlement(ctx, catalog, dnfquest.FinishCommitInput{AccountID: qst006AccountID, CharacterID: qst006Character, QuestID: 3729, Multiplier: 1, CommittedAt: time.Now(), Progression: tables, AllocateItem: finishBridgeAllocator(nil)})
				t.Fatalf("four-material bridge finish rejected; owner diagnostic=%v", err)
			}
			qst006AssertFinishSuccess(t, packets, 3729)
			qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{3729: {Status: "completed"}, 3762: {Status: "completed"}, 3763: {Status: "completed"}})
			assertNoMaterials := func(inventory dnfrepo.InventoryRecord) {
				for _, stack := range inventory.Slots {
					if stack.ItemID >= 3033 && stack.ItemID <= 3036 && stack.Count != 0 {
						t.Fatalf("settlement left crystal material=%+v", stack)
					}
				}
			}
			assertNoMaterials(after)
			completed, _, err := repositories.Quest.Load(ctx, qst006Character)
			if err != nil {
				t.Fatal(err)
			}
			receipt := qst006ReadReceipt(t, completed.States[3729])
			if len(receipt.TerminalChildren) != 1 || receipt.TerminalChildren[0].QuestID != 3763 || len(receipt.TerminalChildren[0].ConsumedItems) != 4 {
				t.Fatalf("four-material receipt=%+v", receipt)
			}
			for i, consumed := range receipt.TerminalChildren[0].ConsumedItems {
				if consumed.ItemID != int64(3033+i) || consumed.Delta != 200 || consumed.PostCount != 0 {
					t.Fatalf("consumption[%d]=%+v", i, consumed)
				}
			}
			qst006CallFinish(t, service, session, conn, 3729)
			reconnected := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19}}
			qst006AssertFinishSuccess(t, qst006CallFinishOnSession(t, service, reconnected, 3729), 3729)
			replayed, _, err := repositories.Inventory.Load(ctx, qst006Character)
			if err != nil || !reflect.DeepEqual(replayed, after) {
				t.Fatalf("replay mutated settled inventory: err=%v got=%+v want=%+v", err, replayed, after)
			}
		})
	}
}

func qst006AssertRealIdentity(
	t *testing.T,
	archive *platformpvf.Archive,
	catalog *dnfquest.Catalog,
	itemCatalog *pvfDungeonDropCatalog,
) {
	t.Helper()
	parent, parentOK := catalog.Find(int64(qst006Parent))
	first, firstOK := catalog.Find(int64(qst006MeetNPC))
	second, secondOK := catalog.Find(int64(qst006SeekMeet))
	if !parentOK || !firstOK || !secondOK {
		t.Fatalf("quest family missing parent=%t first=%t second=%t", parentOK, firstOK, secondOK)
	}
	if parent.Path != "n_quest/quest_renewal/epic_quest/alphraira/alphraira_epic_26.qst" ||
		parent.Name != "梅娅女王" || parent.Type != "[quest clear]" ||
		!reflect.DeepEqual(parent.IntData, []int64{3609, 3610}) ||
		!reflect.DeepEqual(parent.PreRequiredGroups, [][]int64{{3248}}) {
		t.Fatalf("parent identity=%+v", parent)
	}
	if first.Path != "n_quest/quest_renewal/epic_quest/alphraira/alphraira_epic_26_01.qst" ||
		first.Name != "梅娅女王" || first.MainQuestID != 3249 || first.Type != "[meet npc]" ||
		!first.NoExperience || !reflect.DeepEqual(first.IntData, []int64{qst006QueenNPC}) {
		t.Fatalf("child 3609 identity=%+v", first)
	}
	if second.Path != "n_quest/quest_renewal/epic_quest/alphraira/alphraira_epic_26_02.qst" ||
		second.Name != "梅娅女王" || second.MainQuestID != 3249 || second.Type != "[seek n meet npc]" ||
		!second.NoExperience || !reflect.DeepEqual(second.IntData, []int64{qst006Letter, 1, qst006QueenNPC}) {
		t.Fatalf("child 3610 identity=%+v", second)
	}
	if !catalog.IsLinkedObjective(int64(qst006MeetNPC)) || catalog.IsLinkedObjective(int64(qst006SeekMeet)) {
		t.Fatalf("linked objective recognition: 3609=%t 3610=%t", catalog.IsLinkedObjective(int64(qst006MeetNPC)), catalog.IsLinkedObjective(int64(qst006SeekMeet)))
	}

	service := &Service{initialEquipmentArchive: archive}
	npcIndex, err := service.currentUpgradeNPCIndex(archive)
	if err != nil {
		t.Fatal(err)
	}
	npcReference, listed := npcIndex[uint16(qst006QueenNPC)]
	if !listed {
		t.Fatalf("npc %d is absent from npc/npc.lst", qst006QueenNPC)
	}
	npcPath, err := resolveCurrentUpgradeNPCReference(currentUpgradeNPCListPath, npcReference)
	if err != nil {
		t.Fatal(err)
	}
	npcText, err := archive.ReadText(npcPath)
	if err != nil {
		t.Fatal(err)
	}
	npcDocument, err := dnfpvf.Parse(npcPath, npcText)
	if err != nil {
		t.Fatal(err)
	}
	npcNames := npcDocument.Texts("name")
	if npcPath != "npc/Meia.npc" || len(npcNames) != 1 || npcNames[0] != "梅娅女王" {
		t.Fatalf("npc %d ref=%q path=%q names=%v", qst006QueenNPC, npcReference, npcPath, npcNames)
	}

	item, err := itemCatalog.ResolveItem(uint32(qst006Letter))
	if err != nil {
		t.Fatal(err)
	}
	itemText, err := archive.ReadText(item.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	itemDocument, err := dnfpvf.Parse(item.PVFPath, itemText)
	if err != nil {
		t.Fatal(err)
	}
	itemNames := itemDocument.Texts("name")
	if item.PVFPath != "stackable/10164001/10164786.stk" || len(itemNames) != 1 || itemNames[0] != "斯卡迪女王的书信" {
		t.Fatalf("item %d path=%q names=%v", qst006Letter, item.PVFPath, itemNames)
	}
	t.Logf("identity parent=3249/%q child1=3609/%s/%v child2=3610/%s/%v npc=303/%s item=10164786/%q", parent.Name, first.Type, first.IntData, second.Type, second.IntData, npcPath, itemNames[0])
}

func qst006Repositories(t *testing.T, ctx context.Context) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: qst006Character,
		AccountID:   qst006AccountID,
		Job:         "2",
		Level:       65,
		Stats:       map[string]int64{"grow_type": 0, "exp": 0, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: qst006Character,
		States:      map[int64]dnfrepo.QuestState{3248: {Status: "completed", ProgressValue: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: qst006Character,
		Points:      dnfrepo.SkillPointState{SyncedLevel: 65, TotalSP: 100, RemainingSP: 100, TotalTP: 20, RemainingTP: 20},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: qst006Character,
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func qst006Accept(t *testing.T, ctx context.Context, repositories dnfrepo.Group, catalog *dnfquest.Catalog) *dnfquest.Owner {
	t.Helper()
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ApplyAccept(ctx, catalog,
		dnfquest.CharacterEligibility{Level: 65, Job: 2, GrowType: 0},
		dnfquest.NewQuestIDCommand(alignedcmd.Request{
			AccountID: qst006AccountID, SelectedCharacterID: 19,
		}, "accept_quest", dnfquest.QuestIDRequest{QuestID: qst006Parent}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.QuestID != qst006Parent || accepted.InitTrigger != 2 || accepted.Idempotent {
		t.Fatalf("accept result=%+v", accepted)
	}
	qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
		3249: {Status: "active", ProgressValue: 2},
		3609: {Status: "active", ProgressValue: 1},
		3610: {Status: "active", ProgressValue: 513},
	})
	return owner
}

func qst006Bridge(
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	progressTables *progression.Tables,
	itemCatalog *pvfDungeonDropCatalog,
) (*bufferConn, *Service, *gameSession) {
	conn := &bufferConn{}
	service := &Service{
		options: options{
			accountID:            qst006AccountID,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			currentClientVersion: "official-24.9",
		},
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		questFinishResourceLoader: func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
			return progressTables, itemCatalog, nil
		},
	}
	session := &gameSession{
		conn: conn, connID: "qst006-real-pvf", selectedCharacterID: 19,
		channel: channelcatalog.Channel{ID: 19},
	}
	return conn, service, session
}

func qst006CallTrigger(t *testing.T, service *Service, session *gameSession, conn *bufferConn, questID uint16, triggerType byte) {
	t.Helper()
	conn.write.Reset()
	body := []byte{0x21, 0x00, byte(questID), byte(questID >> 8), triggerType, 0x00}
	if err := service.handleCurrentSetQuestTrigger(session, body); err != nil {
		t.Fatal(err)
	}
	if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); len(got) == 0 {
		t.Fatalf("quest=%d trigger=%#x produced no packet", questID, triggerType)
	}
}

func qst006CallChildFinish(t *testing.T, service *Service, session *gameSession, conn *bufferConn, questID uint16) {
	t.Helper()
	packets := qst006CallFinish(t, service, session, conn, questID)
	got := finishBridgePacketIDs(packets)
	if !reflect.DeepEqual(got, []string{"1/34", "0/574", "0/356"}) {
		t.Fatalf("child %d op34 packet order=%v", questID, got)
	}
}

func qst006CallFinish(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	questID uint16,
) []dnfproto.ChannelPacket {
	t.Helper()
	conn.write.Reset()
	if err := service.handleCurrentFinishQuest(session, qst006ChildFinishBody(questID)); err != nil {
		t.Fatal(err)
	}
	return splitAllFinishBridgePackets(t, conn.write.Bytes())
}

func qst006CallFinishOnSession(
	t *testing.T,
	service *Service,
	session *gameSession,
	questID uint16,
) []dnfproto.ChannelPacket {
	t.Helper()
	conn, ok := session.conn.(*bufferConn)
	if !ok || conn == nil {
		t.Fatalf("session connection is %T, want *bufferConn", session.conn)
	}
	return qst006CallFinish(t, service, session, conn, questID)
}

func qst006ChildFinishBody(questID uint16) []byte {
	return []byte{0x22, 0x00, byte(questID), byte(questID >> 8), 0xff, 0xff, 0x01, 0x00, 0xff, 0xff}
}

func qst006AssertFinishFailure(t *testing.T, packets []dnfproto.ChannelPacket, questID uint16) {
	t.Helper()
	if got := finishBridgePacketIDs(packets); !reflect.DeepEqual(got, []string{"1/34"}) {
		t.Fatalf("quest %d failure packet order=%v", questID, got)
	}
	if !bytes.Equal(packets[0].Body, []byte{0, 22}) {
		t.Fatalf("quest %d failure body=%x want=0016", questID, packets[0].Body)
	}
}

func qst006AssertFinishSuccess(t *testing.T, packets []dnfproto.ChannelPacket, questID uint16) {
	t.Helper()
	if len(packets) == 0 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
		bytes.Equal(packets[0].Body, []byte{0, 22}) {
		t.Fatalf("quest %d did not receive a finish success ACK: %+v", questID, finishBridgePacketIDs(packets))
	}
}

type qst006SettlementReceipt struct {
	ConsumedItems    []dnfquest.FinishConsumedItem            `json:"consumed_items"`
	TerminalChildren []dnfquest.FinishTerminalChildSettlement `json:"terminal_children"`
}

func qst006ReadReceipt(t *testing.T, state dnfrepo.QuestState) qst006SettlementReceipt {
	t.Helper()
	raw := state.Extra["settlement_receipt_v1"]
	if raw == "" {
		t.Fatalf("quest has no settlement receipt: %+v", state)
	}
	var receipt qst006SettlementReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		t.Fatalf("decode quest settlement receipt: %v state=%+v", err, state)
	}
	return receipt
}

func qst006AssertSingleLetterConsumption(t *testing.T, consumed []dnfquest.FinishConsumedItem) {
	t.Helper()
	if len(consumed) != 1 {
		t.Fatalf("letter consumed rows=%+v", consumed)
	}
	row := consumed[0]
	if row.ItemID != qst006Letter || row.Delta != 1 || row.PostCount != 0 {
		t.Fatalf("letter consumed row=%+v", row)
	}
}

func qst006AssertState(t *testing.T, ctx context.Context, repositories dnfrepo.Group, want map[int64]dnfrepo.QuestState) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	for questID, expected := range want {
		got := record.States[questID]
		if got.Status != expected.Status || got.ProgressValue != expected.ProgressValue {
			t.Fatalf("quest %d status/progress=%s/%d, want %s/%d", questID, got.Status, got.ProgressValue, expected.Status, expected.ProgressValue)
		}
	}
}

func qst006AddLetter(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots == nil {
		inventory.Slots = map[string]dnfrepo.ItemStack{}
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 40)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(qst006Letter))
	binary.LittleEndian.PutUint32(raw[6:10], 1)
	inventory.Slots["0:40"] = dnfrepo.ItemStack{
		ItemID: qst006Letter, Count: 1, RawEntry: raw,
		Extra: map[string]string{"count": "1"},
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
}

func qst006LogState(t *testing.T, ctx context.Context, repositories dnfrepo.Group, label string) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	t.Logf("%s parent=%+v child3609=%+v child3610=%+v", label, record.States[3249], record.States[3609], record.States[3610])
}

func qst006LetterCount(t *testing.T, ctx context.Context, repositories dnfrepo.Group) int64 {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == qst006Letter {
			count += stack.Count
		}
	}
	return count
}

func qst006Finish(
	ctx context.Context,
	owner *dnfquest.Owner,
	catalog *dnfquest.Catalog,
	tables *progression.Tables,
) (dnfquest.FinishCommitResult, error) {
	return owner.ApplyFinishSettlement(ctx, catalog, dnfquest.FinishCommitInput{
		AccountID: qst006AccountID, CharacterID: qst006Character,
		QuestID: int64(qst006Parent), Multiplier: 1,
		CommittedAt: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		Progression: tables, AllocateItem: finishBridgeAllocator(nil),
	})
}
