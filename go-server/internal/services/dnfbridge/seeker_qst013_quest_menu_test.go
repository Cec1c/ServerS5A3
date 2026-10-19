package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
)

func TestSeekerQST013RealPVFAcceptAndList(t *testing.T) {
	source, _, _ := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3466, 3474, 3475} {
		q, ok := catalog.Find(id)
		if !ok {
			t.Fatal(id)
		}
		t.Logf("quest=%d name=%s parent=%d type=%s data=%v", id, q.Name, q.MainQuestID, q.Type, q.IntData)
		if id != 3466 && q.MainQuestID != 3466 {
			t.Fatal("unexpected parent")
		}
	}
	repos := memory.NewMemoryGroup()
	for _, err := range []error{
		repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "17", AccountID: "dnf:1", Level: 57, Job: "2", Stats: map[string]int64{"fatigue": 156}}),
		repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "17", States: map[int64]repo.QuestState{3465: {Status: "completed"}}}),
		repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "17", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{options: options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "qst013", selectedCharacterID: 17, initialTownRouteCharacterID: 17, initialTownRouteStage: currentInitialTownRoutePlayerStateSent}
	request := []byte{31, 0, 0, 0}
	binary.LittleEndian.PutUint16(request[2:], 3466)
	if err := svc.handleCurrentAcceptQuest(session, request); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 31 || len(ack.Body) != 8 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:]) != 3466 {
		t.Fatalf("ack=%+v body=%x", ack.Header, ack.Body)
	}
	active, trailing := splitGameServerUpperPacket(t, rest)
	if active.Header.MsgID != currentActiveQuestSnapshotMsgID || len(trailing) != 0 {
		t.Fatalf("snapshot=%+v tail=%x", active.Header, trailing)
	}
	if len(active.Body) < 4 {
		t.Fatal("short snapshot")
	}
	count := int(binary.LittleEndian.Uint32(active.Body))
	if len(active.Body) != 4+6*count {
		t.Fatalf("snapshot count=%d bytes=%d", count, len(active.Body))
	}
	ids := map[uint16]bool{}
	for pos := 4; pos < len(active.Body); pos += 6 {
		id := binary.LittleEndian.Uint16(active.Body[pos:])
		if ids[id] {
			t.Fatalf("duplicate active id=%d", id)
		}
		ids[id] = true
	}
	if !ids[3466] {
		t.Fatal("parent absent from active snapshot")
	}
	stored, ok, err := repos.Quest.Load(ctx, "17")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if stored.States[3466].Status != "active" {
		t.Fatalf("parent=%+v", stored.States[3466])
	}
	before := repo.CloneQuest(stored)
	conn.write.Reset()
	if err := svc.handleCurrentAcceptQuest(session, request); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("repeated accept appended another ACK or snapshot")
	}
	after, _, err := repos.Quest.Load(ctx, "17")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("repeated accept changed quest")
	}
	eligibility := dnfquest.CharacterEligibility{Level: 57, Job: 2}
	list := catalog.QuestList(eligibility, after)
	seen := map[int32]bool{}
	for _, id := range list.IDs {
		if seen[id] {
			t.Fatalf("duplicate list id=%d", id)
		}
		seen[id] = true
	}
	if !seen[3466] {
		t.Fatal("active parent missing from task list")
	}
	t.Logf("initial trigger=%d active IDs=%v family in list parent=%t child1=%t child2=%t", binary.LittleEndian.Uint32(ack.Body[3:]), ids, seen[3466], seen[3474], seen[3475])
}
