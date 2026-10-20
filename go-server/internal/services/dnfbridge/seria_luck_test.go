package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/packageitem"
	proto "longheng.io/server/internal/modules/dnf/protocol"
)

// 独立报文夹具：保留全部旧字节，仅追加2037和八阶段上限，不调用生产构建器。
func withSeriaLuckCatalogFixture(t *testing.T, legacy []byte) []byte {
	t.Helper()
	shape := parseCurrentSpendTimeEventInfoBody(t, legacy)
	activeOffset := 2
	for _, row := range shape.baseRows {
		activeOffset += len(row.raw)
	}
	base := mustHexBytes(t, "f50701000000000000000000000000")
	active := append([]byte{0xf5, 0x07, 8, 0, 0, 0}, make([]byte, 44)...)
	want := append([]byte(nil), legacy[:activeOffset]...)
	binary.LittleEndian.PutUint16(want[:2], shape.baseCount+1)
	want = append(want, base...)
	want = append(want, shape.activeCount+1)
	want = append(want, legacy[activeOffset+1:]...)
	return append(want, active...)
}

func TestSeriaLuckProductionInitialCatalogContainsNativeEntry(t *testing.T) {
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
	s := &Service{options: options{joustEventDisabled: true}, gameplayTimers: queue}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "seria-luck-catalog"}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: []uint32{100}, totalStageSeconds: 10800}
	if _, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, true); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), proto.GameServerUpperHeaderSize16)
	if packet.Header.Classification != 0 || packet.Header.MsgID != 108 {
		t.Fatalf("活动目录通知不符：%+v", packet.Header)
	}
	body, err := zlibDecompress(packet.Body)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if !shape.hasEvent(currentSeriaLuckEventID) || !shape.hasEvent(uint16(currentSpendTimeEventID)) {
		t.Fatal("实际登录目录缺少幸运值或既有在线入口")
	}
}

func TestSeriaLuckCatalogPreservesOtherEvents(t *testing.T) {
	for _, includeEpicRoad := range []bool{false, true} {
		old, err := buildCurrentActivityEventInfoBodyWithEpicRoad([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, includeEpicRoad)
		if err != nil {
			t.Fatal(err)
		}
		next, err := buildCurrentActivityEventInfoBodyWithSeriaLuck([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, includeEpicRoad, true)
		if err != nil {
			t.Fatal(err)
		}
		a, b := parseCurrentSpendTimeEventInfoBody(t, old), parseCurrentSpendTimeEventInfoBody(t, next)
		if !bytes.Equal(next, withSeriaLuckCatalogFixture(t, old)) {
			t.Fatal("新增活动不符合独立报文夹具")
		}
		if len(b.baseRows) != len(a.baseRows)+1 || len(b.activeRows) != len(a.activeRows)+1 {
			t.Fatalf("活动数量不符：%+v", b)
		}
		for i, row := range a.baseRows {
			if !bytes.Equal(row.raw, b.baseRows[i].raw) {
				t.Fatalf("原有活动%d基础行改变", row.eventID)
			}
		}
		for i, row := range a.activeRows {
			if !bytes.Equal(row.raw, b.activeRows[i].raw) {
				t.Fatalf("原有活动%d参数改变", row.eventID)
			}
		}
		base, active := b.baseRows[len(a.baseRows)], b.activeRows[len(a.activeRows)]
		if base.eventID != currentSeriaLuckEventID || base.hasExtended || active.eventID != currentSeriaLuckEventID || active.params[0] != uint32(packageitem.SeriaLuckValueMax) {
			t.Fatalf("幸运值目录不符：base=%+v active=%+v", base, active)
		}
		for _, value := range active.params[1:] {
			if value != 0 {
				t.Fatal("幸运值目录携带无关参数")
			}
		}
	}
}

func TestSeriaLuckCatalogCoexistsWithPCRoomAndLegacy(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "pcroom"}[native], func(t *testing.T) {
			ids := currentSpendTimeJoustCatalogGoldenRewards
			before, err := buildCurrentActivityEventInfoBodyWithOnlinePanel(ids, 10800, true, true, true, true, true, 2418, true, true, true, true, true, native)
			if err != nil {
				t.Fatal(err)
			}
			after, err := buildCurrentActivityEventInfoBodyWithRuntimeActivities(ids, 10800, true, true, true, true, true, 2418, true, true, true, true, true, nil, false, native, true)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, withSeriaLuckCatalogFixture(t, before)) {
				t.Fatal("combined catalog changed existing rows instead of appending Seria luck")
			}

			// Exercise the production caller as well as the catalog serialization.
			s, session, conn, catalog, _ := onlinePanelFixture(t)
			wantOwner, wantState := uint16(currentSpendTimeEventID), currentSpendTimeProgressMsgID
			if native {
				wantOwner, wantState = 228, 561
			} else {
				catalog.nativePCRoom = nil
			}
			if _, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, true); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), proto.GameServerUpperHeaderSize16)
			if packet.Header.MsgID != 108 {
				t.Fatalf("first packet=%d", packet.Header.MsgID)
			}
			body, err := zlibDecompress(packet.Body)
			if err != nil {
				t.Fatal(err)
			}
			shape := parseCurrentSpendTimeEventInfoBody(t, body)
			if !shape.hasEvent(wantOwner) || !shape.hasEvent(currentSeriaLuckEventID) {
				t.Fatal("production catalog lost online rewards or Seria luck")
			}
			state, tail := splitGameServerUpperPacket(t, rest)
			if state.Header.MsgID != wantState || len(tail) != 0 {
				t.Fatalf("online owner state=%d, want=%d, trailing=%d", state.Header.MsgID, wantState, len(tail))
			}
		})
	}
}

func TestInitialSeriaLuckRestoresAccountProgressAfterSceneAndOnlyOnce(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want byte
	}{{"", 0}, {"4", 20}, {"8", 40}} {
		t.Run(tc.raw, func(t *testing.T) {
			s, session := newDeferredTailResumeFixture(t)
			repos, _ := s.repositoryGroup()
			account, _, err := repos.Account.Load(context.Background(), "dnf:1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.raw != "" {
				account.Metadata[packageitem.SeriaLuckMetadataKey] = tc.raw
			}
			if err := repos.Account.Save(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			if err := s.sendDeferredSelectSceneTail(session, "测试幸运值登录恢复"); err != nil {
				t.Fatal(err)
			}
			conn := session.conn.(*bufferConn)
			packets := deferredTailPacketSignatures(t, conn.write.Bytes())
			last := packets[len(packets)-1]
			if last.classification != 0 || last.msgID != currentSeriaLuckGaugeMsgID || !bytes.Equal(last.body, []byte{tc.want, tc.want}) {
				t.Fatalf("末尾幸运值通知不符：%+v", last)
			}
			before := conn.write.Len()
			if err := s.sendDeferredSelectSceneTail(session, "测试重复场景回调"); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != before {
				t.Fatal("已完成场景回调重复下发幸运值")
			}
			after, _, err := repos.Account.Load(context.Background(), "dnf:1")
			if err != nil || after.Metadata[packageitem.SeriaLuckMetadataKey] != tc.raw {
				t.Fatalf("显示同步修改了账号记录：%+v err=%v", after, err)
			}
		})
	}
}

func TestInitialSeriaLuckRejectsMalformedProgressWithoutWritingAccount(t *testing.T) {
	for _, raw := range []string{"-1", "9", "bad", "999999999999999999999"} {
		t.Run(raw, func(t *testing.T) {
			s, session := newDeferredTailResumeFixture(t)
			repos, _ := s.repositoryGroup()
			account, _, _ := repos.Account.Load(context.Background(), "dnf:1")
			account.Metadata[packageitem.SeriaLuckMetadataKey] = raw
			if err := repos.Account.Save(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			if err := s.sendInitialSeriaLuckGauge(session, "测试异常记录"); err != nil {
				t.Fatal(err)
			}
			if session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("异常记录发送了幸运值")
			}
			after, _, _ := repos.Account.Load(context.Background(), "dnf:1")
			if after.Metadata[packageitem.SeriaLuckMetadataKey] != raw {
				t.Fatal("显示同步重写了异常记录")
			}
		})
	}
}

func TestSeriaLuckGaugeFollowsResultAndItemUpdates(t *testing.T) {
	for _, transition := range [][2]int64{{0, 1}, {7, 8}, {8, 1}, {7, 1}} {
		s, session := newDeferredTailResumeFixture(t)
		result := alignedcmd.Result{
			Operation: "测试开盒",
			UpperResponses: []alignedcmd.UpperResponse{
				{MsgID: 208, Classification: 1, Body: []byte{1}},
				{MsgID: 14, Classification: 0, Body: []byte{0x55}},
			},
			SeriaLuck: &alignedcmd.SeriaLuckProjection{Before: transition[0], After: transition[1]},
		}
		if err := s.sendAlignedUpperResponses(session, result); err != nil {
			t.Fatal(err)
		}
		packets := deferredTailPacketSignatures(t, session.conn.(*bufferConn).write.Bytes())
		if len(packets) != 3 || packets[0].classification != 1 || packets[0].msgID != 208 || packets[1].msgID != 14 || packets[2].classification != 0 || packets[2].msgID != currentSeriaLuckGaugeMsgID || !bytes.Equal(packets[2].body, []byte{byte(transition[0] * 5), byte(transition[1] * 5)}) {
			t.Fatalf("回包顺序或进度不符：%+v", packets)
		}
	}
}

func TestSeriaLuckDisplayFailureDoesNotRejectCommittedOpen(t *testing.T) {
	s, session := newDeferredTailResumeFixture(t)
	failure := &failNthDungeonWriteConn{failAt: 2, err: errors.New("测试幸运值通知写入失败")}
	session.conn = failure
	if err := s.sendAlignedUpperResponses(session, alignedcmd.Result{
		Operation:      "测试已提交开盒",
		UpperResponses: []alignedcmd.UpperResponse{{MsgID: 208, Classification: 1, Body: []byte{1}}},
		SeriaLuck:      &alignedcmd.SeriaLuckProjection{Before: 0, After: 1},
	}); err != nil {
		t.Fatalf("显示失败不应撤销成功回包：%v", err)
	}
	packets := deferredTailPacketSignatures(t, failure.bufferConn.write.Bytes())
	if len(packets) != 1 || packets[0].classification != 1 || packets[0].msgID != 208 {
		t.Fatalf("已提交的开盒成功回包丢失：%+v", packets)
	}
}
