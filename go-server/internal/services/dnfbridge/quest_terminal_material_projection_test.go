package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentActiveQuestSnapshotTerminalProjectionCatalogFailureKeepsBody(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			int64(qst028MedicineParentID):   {Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}},
			int64(qst028MedicineMeetID):     {Status: "completed"},
			int64(qst028MedicineDeliveryID): {Status: "active"},
		},
	}
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		questCatalogLoadErr: errors.New("injected catalog failure"),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}

	if err := service.sendCurrentActiveQuestSnapshotForSession(session, "catalog_failure"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != currentActiveQuestSnapshotMsgID {
		t.Fatalf("snapshot packet=%+v rest=%x", packet.Header, rest)
	}
	if !reflect.DeepEqual(packet.Body, buildCurrentActiveQuestSnapshotBody(record, true)) {
		t.Fatalf("catalog failure changed snapshot: got=%x want=%x",
			packet.Body, buildCurrentActiveQuestSnapshotBody(record, true))
	}
}

func TestCurrentActiveQuestSnapshotTerminalProjectionUsesStatesBeforeProgress(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			int64(qst028MedicineParentID):   {Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}},
			int64(qst028MedicineMeetID):     {Status: "completed"},
			int64(qst028MedicineDeliveryID): {Status: "active"},
		},
		Progress: map[int64]dnfrepo.QuestState{
			int64(qst028MedicineParentID):   {Status: "completed"},
			int64(qst028MedicineDeliveryID): {Status: "active", ProgressValue: 1},
		},
	}
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		questCatalog:       qst028MedicineFixtureCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}

	if err := service.sendCurrentActiveQuestSnapshotForSession(session, "states_priority"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != currentActiveQuestSnapshotMsgID {
		t.Fatalf("snapshot packet=%+v rest=%x", packet.Header, rest)
	}
	rows := currentSelectAckQuestRows(record, true)
	projected := record
	projected.States = dnfrepo.CloneQuest(record).States
	parent := projected.States[int64(qst028MedicineParentID)]
	parent.ProgressValue = 0
	projected.States[int64(qst028MedicineParentID)] = parent
	want := buildCurrentActiveQuestSnapshotBody(projected, true)
	if !reflect.DeepEqual(packet.Body, want) {
		t.Fatalf("states-priority snapshot got=%x want=%x rows=%v", packet.Body, want, rows)
	}
}

func TestCurrentActiveQuestSnapshotTerminalProjectionAppliesToCommittedSend(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{questCatalog: qst028MedicineFixtureCatalog(t)}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	record := dnfrepo.QuestRecord{
		CharacterID: qst028MedicineCharacterID,
		States: map[int64]dnfrepo.QuestState{
			int64(qst028MedicineParentID):   {Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}},
			int64(qst028MedicineMeetID):     {Status: "completed"},
			int64(qst028MedicineDeliveryID): {Status: "active"},
		},
	}

	if err := service.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, record, "committed_projection"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != currentActiveQuestSnapshotMsgID {
		t.Fatalf("snapshot packet=%+v rest=%x", packet.Header, rest)
	}
	projected := record
	projected.States = dnfrepo.CloneQuest(record).States
	parent := projected.States[int64(qst028MedicineParentID)]
	parent.ProgressValue = 0
	projected.States[int64(qst028MedicineParentID)] = parent
	if want := buildCurrentActiveQuestSnapshotBody(projected, true); !reflect.DeepEqual(packet.Body, want) {
		t.Fatalf("committed snapshot got=%x want=%x", packet.Body, want)
	}
	if got := record.States[int64(qst028MedicineParentID)].ProgressValue; got != 1 {
		t.Fatalf("committed projection mutated input progress=%d", got)
	}
}

func TestCurrentActiveQuestSnapshotTerminalProjectionOnlyLoadsForQuestClearParents(t *testing.T) {
	for _, test := range []struct {
		name  string
		state dnfrepo.QuestState
		want  bool
	}{
		{"quest clear", dnfrepo.QuestState{Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}}, true},
		{"ordinary objective", dnfrepo.QuestState{Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[hunt enemy]"}}, false},
		{"unknown type", dnfrepo.QuestState{Status: "active", ProgressValue: 1}, false},
		{"completed parent", dnfrepo.QuestState{Status: "completed", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}}, false},
		{"already ready", dnfrepo.QuestState{Status: "active", Extra: map[string]string{"quest_type": "[quest clear]"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{100: test.state}}
			if got := currentActiveQuestSnapshotNeedsTerminalProjection(record); got != test.want {
				t.Fatalf("catalog candidate=%t want=%t", got, test.want)
			}
		})
	}
	record := dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 0},
		},
		Progress: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[quest clear]"}},
		},
	}
	service := &Service{questCatalogLoadErr: errors.New("catalog must not load")}
	session := &gameSession{selectedCharacterID: 19}
	if got := service.projectCurrentActiveQuestSnapshot(context.Background(), session, record, "test"); !reflect.DeepEqual(got, record) {
		t.Fatalf("projection=%+v want=%+v", got, record)
	}
}
