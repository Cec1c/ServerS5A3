package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 当前v97的356同时建立普通完成表和QuestManager+0xc4集合；574/count=0
// 仅清空后者。街道虚拟行走与城镇出口读取后者，不能只检查普通完成表。
type emptyActiveNativeQuestView struct {
	completed map[int64]bool
	gate      map[int64]bool
}

func (v *emptyActiveNativeQuestView) consume(t *testing.T, stream []byte) {
	t.Helper()
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		switch packet.Header.MsgID {
		case currentClearQuestListMsgID:
			if len(packet.Body) < 4 || packet.Body[0] != 0 || packet.Body[1] != 0 || packet.Body[2] != 1 {
				t.Fatal("完成表必须采用已取证的fixed16保护头")
			}
			plain, err := zlibDecompress(packet.Body[3:])
			if err != nil || len(plain) != 4+currentClearQuestListPayloadSize || binary.LittleEndian.Uint32(plain) != currentClearQuestListPayloadSize {
				t.Fatalf("原生完成表边界错误: len=%d err=%v", len(plain), err)
			}
			for id, flag := range plain[4:] {
				if flag != 0 {
					v.completed[int64(id)] = true
					v.gate[int64(id)] = true
				}
			}
		case currentActiveQuestSnapshotMsgID:
			if binary.LittleEndian.Uint32(packet.Body) == 0 {
				v.gate = make(map[int64]bool)
			}
		}
	}
}

func TestEmptyActiveQuestNativeGateSurvivesFinishAndSuccessorAccept(t *testing.T) {
	service, session, repos, result := questGateRefreshFixture(t)
	service.options.currentClientVersion = "official-24.9"
	// 此测试验证任务投影本身；不借助地图切换、位置通知或虚构任务记录。
	session.townPositionSnapshot.PositionValid = false
	view := emptyActiveNativeQuestView{completed: make(map[int64]bool), gate: make(map[int64]bool)}
	for _, id := range []int64{3155, 3156} {
		result.QuestID = id
		result.PostCommitQuest.States[id] = dnfrepo.QuestState{Status: "completed"}
		if id == 3155 {
			delete(result.PostCommitQuest.States, 3156)
		}
		if err := repos.Quest.Save(context.Background(), result.PostCommitQuest); err != nil {
			t.Fatal(err)
		}
		beforeChar, _, _ := repos.Character.Load(context.Background(), "29")
		beforeQuest, _, _ := repos.Quest.Load(context.Background(), "29")
		connection := session.conn.(*bufferConn)
		connection.write.Reset()
		if err := service.sendCurrentFinishQuestPostCommitSnapshots(session, result); err != nil {
			t.Fatal(err)
		}
		view.consume(t, connection.write.Bytes())
		if !view.completed[id] || !view.gate[id] {
			t.Fatalf("完成%d后普通表=%t，入口读取的集合=%t；不能依赖换图恢复", id, view.completed[id], view.gate[id])
		}
		afterChar, _, _ := repos.Character.Load(context.Background(), "29")
		afterQuest, _, _ := repos.Quest.Load(context.Background(), "29")
		if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeQuest, afterQuest) {
			t.Fatal("完成状态通知不得改写真实角色或任务")
		}
		if id == 3155 {
			accepted := dnfrepo.CloneQuest(result.PostCommitQuest)
			accepted.States[3156] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
			connection.write.Reset()
			if err := service.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, accepted, "test_successor_accept"); err != nil {
				t.Fatal(err)
			}
			view.consume(t, connection.write.Bytes())
			if !view.gate[3155] || view.gate[3156] {
				t.Fatal("接后续任务必须保留真实前置完成，不能放行尚未完成的后续任务")
			}
		}
	}
}

func TestEmptyActiveQuestRestoreScopeAndCanonicalState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		states  map[int64]dnfrepo.QuestState
		legacy  map[int64]dnfrepo.QuestState
		wantIDs []uint16
		cleared int64
	}{
		{"有真实完成任务", "official-24.9", map[int64]dnfrepo.QuestState{3155: {Status: "completed"}}, nil, []uint16{574, 356}, 3155},
		{"兼容旧完成记录", "official-24.9", nil, map[int64]dnfrepo.QuestState{3155: {Status: "complete"}}, []uint16{574, 356}, 3155},
		{"仍有进行中任务", "official-24.9", map[int64]dnfrepo.QuestState{3155: {Status: "completed"}, 3156: {Status: "active"}}, nil, []uint16{574}, 0},
		{"没有完成记录", "official-24.9", nil, nil, []uint16{574}, 0},
		{"正式状态覆盖旧完成", "official-24.9", map[int64]dnfrepo.QuestState{3155: {Status: "given_up"}}, map[int64]dnfrepo.QuestState{3155: {Status: "completed"}}, []uint16{574}, 0},
		{"超出原生表范围", "official-24.9", map[int64]dnfrepo.QuestState{30000: {Status: "completed"}}, nil, []uint16{574}, 0},
		{"其他客户端口径保持", "probe", map[int64]dnfrepo.QuestState{3155: {Status: "completed"}}, nil, []uint16{574}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, committed := range []bool{false, true} {
				record := dnfrepo.QuestRecord{CharacterID: "29", States: tc.states, Progress: tc.legacy}
				repos := dnfrepomemory.NewMemoryGroup()
				if err := repos.Quest.Save(context.Background(), record); err != nil {
					t.Fatal(err)
				}
				connection := &bufferConn{}
				service := &Service{options: options{currentClientVersion: tc.version}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				session := &gameSession{conn: connection, selectedCharacterID: 29}
				var err error
				if committed {
					err = service.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, record, "test_committed")
				} else {
					err = service.sendCurrentActiveQuestSnapshotForSession(session, "test_persisted")
				}
				if err != nil {
					t.Fatal(err)
				}
				var ids []uint16
				for stream := connection.write.Bytes(); len(stream) > 0; {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
					ids = append(ids, packet.Header.MsgID)
					if packet.Header.MsgID == 356 {
						assertFinishBridgeClearQuestProjection(t, packet, uint16(tc.cleared))
					}
					stream = rest
				}
				if !reflect.DeepEqual(ids, tc.wantIDs) {
					t.Fatalf("committed=%t 包序列=%v want=%v", committed, ids, tc.wantIDs)
				}
			}
		})
	}
}

func TestEmptyActiveQuestRestorePropagatesTransportFailure(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		wantErr := errors.New("空活动任务状态通知写入失败")
		connection := &failNthDungeonWriteConn{failAt: failAt, err: wantErr}
		service := &Service{options: options{currentClientVersion: "official-24.9"}}
		session := &gameSession{conn: connection, selectedCharacterID: 29}
		record := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{3155: {Status: "completed"}}}
		if err := service.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, record, "test_write_failure"); !errors.Is(err, wantErr) {
			t.Fatalf("第%d次写入失败没有返回: %v", failAt, err)
		}
		if connection.writes != failAt {
			t.Fatalf("失败后继续发包: writes=%d", connection.writes)
		}
	}
}
