package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type deathNotifyProgressionProbe struct {
	base   dnfrepo.CharacterProgressionUnitOfWork
	before func()
	reject error
	calls  int
}

func (p *deathNotifyProgressionProbe) WithinCharacterProgression(ctx context.Context, key string, apply func(dnfrepo.CharacterRepository, dnfrepo.SkillRepository) error) error {
	p.calls++
	if p.before != nil {
		p.before()
	}
	if p.reject != nil {
		return p.reject
	}
	return p.base.WithinCharacterProgression(ctx, key, apply)
}

// 在经验事务尚未开始提交时观察线上报文，确保慢提交不挡住已确认的怪物死亡。
// 成功和失败均检查：未提交的经验不能提前通知，重复死亡不能再次发奖。
func TestDungeonDeathNotifyLatencyPrecedesExperienceCommit(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		name := "提交成功"
		if failCommit {
			name = "提交失败"
		}
		t.Run(name, func(t *testing.T) {
			service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
			connection := &bufferConn{}
			probe := &deathNotifyProgressionProbe{base: repositories.CharacterProgression}
			probe.before = func() {
				if connection.write.Len() == 0 {
					t.Fatal("经验事务开始时尚未发送死亡通知，白影仍会等待数据库")
				}
				death, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
				if death.Header.Classification != 0 || death.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) || len(rest) != 0 {
					t.Fatalf("经验未提交前应仅发送一次死亡通知：header=%+v rest=%x", death.Header, rest)
				}
				character, found, err := repositories.Character.Load(context.Background(), "99")
				if err != nil || !found || character.Stats["exp"] != 0 {
					t.Fatalf("观察点的经验状态错误：found=%t character=%+v err=%v", found, character, err)
				}
			}
			if failCommit {
				probe.reject = errors.New("隔离测试：模拟经验事务失败")
			}
			repositories.CharacterProgression = probe
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
			session := &gameSession{conn: connection, connID: "death-notify-latency", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			monster := runtime.Room.Snapshot().Monsters[0]
			request := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(request, monster.ObjectKey)
			binary.LittleEndian.PutUint16(request[4:], currentSceneActorObjectKey(99))
			if err := service.handleDungeonMonsterDeath(session, request); err != nil {
				t.Fatal(err)
			}
			_, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			if !failCommit {
				state, tail := splitGameServerUpperPacket(t, rest)
				if state.Header.MsgID != currentDungeonCharacterStateMsgID || binary.LittleEndian.Uint32(state.Body[1:5]) != 200 {
					t.Fatalf("提交成功后的经验通知错误：header=%+v body=%x", state.Header, state.Body)
				}
				rest = tail
			}
			for len(rest) > 0 {
				packet, tail := splitGameServerUpperPacket(t, rest)
				if packet.Header.MsgID == currentDungeonCharacterStateMsgID || packet.Header.MsgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
					t.Fatalf("出现重复死亡通知或未提交经验通知：%+v", packet.Header)
				}
				rest = tail
			}
			character, _, err := repositories.Character.Load(context.Background(), "99")
			want := int64(200)
			if failCommit {
				want = 0
			}
			if err != nil || character.Stats["exp"] != want || probe.calls != 1 {
				t.Fatalf("经验或事务次数错误：exp=%d want=%d calls=%d err=%v", character.Stats["exp"], want, probe.calls, err)
			}
			connection.write.Reset()
			if err := service.handleDungeonMonsterDeath(session, request); err != nil || connection.write.Len() != 0 || probe.calls != 1 {
				t.Fatalf("重复死亡重发或重新提交经验：bytes=%d calls=%d err=%v", connection.write.Len(), probe.calls, err)
			}
		})
	}
}

type deathNotifyFailedConn struct{ bufferConn }

func (c *deathNotifyFailedConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDungeonDeathNotifyLatencyWriteFailureStillCommitsExperience(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
	connection := &deathNotifyFailedConn{}
	session := &gameSession{conn: connection, connID: "death-notify-write-failure", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	monster := runtime.Room.Snapshot().Monsters[0]
	request := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(request, monster.ObjectKey)
	binary.LittleEndian.PutUint16(request[4:], currentSceneActorObjectKey(99))
	if err := service.handleDungeonMonsterDeath(session, request); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("未返回原连接写入错误：%v", err)
	}
	character, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found || character.Stats["exp"] != 200 {
		t.Fatalf("死亡通知写入失败导致经验漏记：found=%t character=%+v err=%v", found, character, err)
	}
}
