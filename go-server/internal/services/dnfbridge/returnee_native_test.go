package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	event "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestReturneeNative352LayoutAndBothTransportRoutes(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	conn := &bufferConn{}
	now := time.Now().UTC()
	state := event.ReturneeState{Started: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), UserType: 5, Claimed: 1, AttendanceDays: []string{now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Add(-6 * time.Hour).Format("2006-01-02")}, AttendanceClaimed: 1}
	state.Progress[0] = 1
	raw, _ := json.Marshal(state)
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{event.ReturneeMetadataKey: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "7", Level: 90, Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }}
	q := &gameSession{ctx: ctx, conn: conn, accountID: "a", selectedCharacterID: 7, characterGeneration: 1}
	enablePortedActivityTest(t, s, q)
	c := &event.ReturneeCatalog{}
	for i := range c.Missions {
		c.Missions[i] = event.ReturneeMission{ID: uint32(i + 1), Condition: "clear quest", Required: 1}
	}
	for i := range c.Attendance {
		c.Attendance[i] = event.ReturneeMission{ID: uint32(33 + i), Condition: "attendance count", Required: 1}
	}
	s.returneeCatalog = c
	s.luckyCharacterCatalog = &currentLuckyCatalog{}
	if handled, err := s.handleCurrentPortedActivityRequest(q, 812, 1, nil); handled || err != nil {
		t.Fatal("开启回归不应顺带接入未核对的旧赠礼协议", handled, err)
	}
	for _, upper := range []bool{false, true} {
		conn.write.Reset()
		body := []byte{0, 45, 0, 0, 0}
		request := gameplayRequest{Opcode: 1429, Classification: 1, Body: body}
		var handled bool
		var err error
		if upper {
			handled, err = currentGameplayModules.dispatchUpperRequest(s, q, request)
		} else {
			handled, err = currentGameplayModules.dispatchLegacyRequest(s, q, request)
		}
		if !handled || err != nil {
			t.Fatal("引导未从真实模块分派", upper, handled, err)
		}
		a, _, _ := g.Account.Load(ctx, "a")
		saved, err := event.LoadReturneeState(a)
		if err != nil || saved.Guide != uint64(1)<<45 {
			t.Fatal(saved, err)
		}
		wire := conn.write.Bytes()
		seen := false
		for len(wire) > 0 {
			p, rest := splitGameServerUpperPacket(t, wire)
			wire = rest
			if p.Header.MsgID == 1369 {
				seen = true
				if p.Header.Classification != 0 || len(p.Body) != 352 || binary.LittleEndian.Uint32(p.Body[176:]) != 33 || p.Body[188] != 1 || p.Body[189] != 1 {
					t.Fatal("签到布局错误", p.Body)
				}
			}
		}
		if !seen {
			t.Fatal("没有现用1369状态包")
		}
	}
	for _, test := range []struct {
		op   uint16
		body []byte
		ok   bool
	}{
		{1429, []byte{1, 255, 1, 0, 0}, true}, {1430, []byte{1, 0, 0, 0}, true}, {1430, []byte{33, 0, 0, 0}, true},
		{1429, []byte{0, 0, 0, 0}, false}, {1429, []byte{0, 46, 0, 0, 0}, false}, {1429, []byte{1, 0, 2, 0, 0}, false}, {1429, []byte{2, 0, 0, 0, 0}, false}, {1430, []byte{34, 0, 0, 0}, false},
	} {
		conn.write.Reset()
		handled, err := s.handleCurrentPortedActivityRequest(q, test.op, 1, test.body)
		if !handled || err != nil {
			t.Fatal(test, handled, err)
		}
		p, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if (p.Body[0] == 1) != test.ok {
			t.Fatal(test, p.Body)
		}
	}
	a, _, _ := g.Account.Load(ctx, "a")
	saved, _ := event.LoadReturneeState(a)
	if saved.GuideExtra[63] != 128 {
		t.Fatal("扩展位未保存")
	}
	body := buildCurrentReturneeBodyAt(c, saved, now)
	if body[351] != 128 {
		t.Fatal("扩展引导未写入352字节尾区")
	}
	if inactive := buildCurrentReturneeBodyAt(c, saved, saved.ExpiresAt); binary.LittleEndian.Uint16(inactive[:]) != 6 || !bytes.Equal(inactive[16:288], make([]byte, 272)) {
		t.Fatal("过期残留参与任务")
	}
	if _, err := s.activityControls.Save(ctx, s.activityControls.Snapshot().Revision, []event.ControlChange{{ID: 1370, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	handled, err := currentGameplayModules.dispatchLegacyRequest(s, q, gameplayRequest{Opcode: 1429, Body: []byte{0, 0, 0, 0}})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	p, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(p.Body, []byte{0, 3}) {
		t.Fatal("活动关闭后误入历史清主线", p.Body)
	}
}
