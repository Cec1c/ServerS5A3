package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagSpecialChoiceFixture(t *testing.T, mode tagtournament.Mode) (*Service, *gameSession, repo.Group, *time.Time) {
	t.Helper()
	s, p, g := tagPlayFixture(t)
	tagPlayChallenge(t, s, p, mode)
	b := p.tagTournamentBattle
	now := time.Now().Add(time.Second)
	b.now = func() time.Time { return now }
	owner, err := tagtournament.NewOwnerWithClock(s.tagTournamentCatalog, g, b.now)
	if err != nil {
		t.Fatal(err)
	}
	b.owner = owner
	tagPlayMoveNext(t, s, p)
	if err := s.handleCurrentTagTournamentDeath(p, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := b.run.ApplyVitals(now, 1, 700, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := b.run.UseConsumable(now, 0); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 4; round++ {
		if round > 0 {
			tagPlayMoveNext(t, s, p)
		}
		if round == 3 {
			if err := b.run.ApplyVitals(now, 1, 333, 44); err != nil {
				t.Fatal(err)
			}
		}
		tagPlayKillBoss(t, s, p)
	}
	if mode == tagtournament.Normal && b.run.Snapshot().Phase != tagtournament.SpecialChoice {
		t.Fatal("未到普通四关后的选择阶段")
	}
	return s, p, g, &now
}

func TestTagSpecialNativeConfirmationKeepsDeadRoleAndCommitsOnlyAfterMapWrite(t *testing.T) {
	s, p, g, now := tagSpecialChoiceFixture(t, tagtournament.Normal)
	b := p.tagTournamentBattle
	before := b.run.Snapshot()
	root, _, _ := g.Character.Load(context.Background(), "29")
	backup, _, _ := g.Character.Load(context.Background(), "60")
	roomBefore := b.prepared.runtime.Session.Snapshot()
	want := b.prepared.layout.Rounds[4].Coordinate
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0], body[1] = byte(want.X), byte(want.Y)
	failure := errors.New("隔离王房地图通知写失败")
	p.conn = &failNthDungeonWriteConn{failAt: 1, err: failure}
	if err := s.handleDungeonMoveMap(p, body); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, b.run.Snapshot()) || !reflect.DeepEqual(roomBefore, b.prepared.runtime.Session.Snapshot()) {
		t.Fatal("通知写失败仍恢复或推进房间")
	}
	p.conn = &bufferConn{}
	tagPlayMoveNext(t, s, p)
	after := b.run.Snapshot()
	if after.Phase != tagtournament.Fighting || after.Round != 4 || after.Active != 1 || after.NextTag != before.NextTag || after.Vitals[0] != before.Vitals[0] || after.Vitals[1].HP != after.Vitals[1].MaxHP || after.Vitals[1].MP != after.Vitals[1].MaxMP {
		t.Fatal("王房恢复影响死者、切人冷却或当前队员", before, after)
	}
	if _, err := b.run.UseConsumable(*now, 0); err == nil {
		t.Fatal("王房恢复清空了专用道具冷却")
	}
	seen := 0
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		rest = tail
		if packet.Header.MsgID == 29 {
			seen++
		}
		if packet.Header.MsgID >= 1389 && packet.Header.MsgID <= 1394 {
			t.Fatal("用重建角色或技能包模拟王房恢复", packet.Header.MsgID)
		}
		if packet.Header.MsgID == 1400 || packet.Header.MsgID == 1401 {
			t.Fatal("把属性或公会Buff包误当王房恢复")
		}
	}
	if seen != 1 {
		t.Fatal("实际王房地图通知不唯一", seen)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonMoveMap(p, body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, b.run.Snapshot()) {
		t.Fatal("重复王房请求再次改变生命或进度")
	}
	for _, old := range []repo.CharacterRecord{root, backup} {
		actual, _, _ := g.Character.Load(context.Background(), old.CharacterID)
		if !reflect.DeepEqual(old, actual) {
			t.Fatal("恢复写入了持久角色资产")
		}
	}
}

func TestTagHardFifthRoomDoesNotApplyNormalKingRecovery(t *testing.T) {
	s, p, _, _ := tagSpecialChoiceFixture(t, tagtournament.Hard)
	b := p.tagTournamentBattle
	before := b.run.Snapshot()
	tagPlayMoveNext(t, s, p)
	after := b.run.Snapshot()
	if after.Round != 4 || after.Vitals != before.Vitals || after.Active != before.Active || after.NextTag != before.NextTag {
		t.Fatal("困难第五回合错误执行普通王房恢复", before, after)
	}
}

func TestTagSpecialRejectsPreparedMapPlanChangedBeforeSendingOrRestoring(t *testing.T) {
	for _, change := range []string{"错误Boss", "删除特殊标记"} {
		t.Run(change, func(t *testing.T) {
			s, p, _, _ := tagSpecialChoiceFixture(t, tagtournament.Normal)
			b := p.tagTournamentBattle
			if change == "错误Boss" {
				b.prepared.layout.Rounds[4].Encounter.MonsterID++
			} else {
				b.prepared.layout.Rounds[4].Encounter.Optional = false
			}
			before := b.run.Snapshot()
			p.conn.(*bufferConn).write.Reset()
			coordinate := b.prepared.layout.Rounds[4].Coordinate
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0], body[1] = byte(coordinate.X), byte(coordinate.Y)
			if err := s.handleDungeonMoveMap(p, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 45 || packet.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(before, b.run.Snapshot()) {
				t.Fatal("错误地图仍发包或执行恢复")
			}
		})
	}
}
