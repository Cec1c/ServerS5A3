package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDungeonDeathContinuationPartyPeersReceiveBothDeathsAndReplayDoesNotRewardAgain(t *testing.T) {
	s, leader, peer, runtime, peerRuntime := partyDungeonDeathFixture(t, 5)
	progressionService, _, progressionRepos, _ := prepareDungeonDeathBurstTest(t, true)
	s.options.currentClientVersion = "official-24.9"
	s.dungeonMonsterTable, s.honorTable = progressionService.dungeonMonsterTable, progressionService.honorTable
	repos, _ := s.repositoryGroup()
	points, _, _ := progressionRepos.Skill.Load(context.Background(), "99")
	for _, tc := range []struct {
		id      string
		runtime *runtimeDungeonState
	}{{"99", runtime}, {"100", peerRuntime}} {
		c, _, _ := repos.Character.Load(context.Background(), tc.id)
		c.Level = currentAdventureCharacterLevelCap
		c.Stats["exp"] = 0
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		tc.runtime.Character = dnfrepo.CloneCharacter(c)
		points.CharacterID = tc.id
		if err := repos.Skill.Save(context.Background(), points); err != nil {
			t.Fatal(err)
		}
	}
	var packets []dnfproto.LatestGameStreamPacket
	for _, monster := range runtime.Room.Snapshot().Monsters[:2] {
		body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
		binary.LittleEndian.PutUint32(body, monster.ObjectKey)
		binary.LittleEndian.PutUint16(body[4:], currentSceneActorObjectKey(99))
		body[30] = 1
		packets = append(packets, nativeResultTestPacket(39, body))
	}
	peerConn := peer.conn.(*bufferConn)
	peerConn.write.Reset()
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	c := &deathContinuationWriteConn{afterFirstDeath: func() { ahead.ready <- nativeResultReadChunk{data: packets[1].Data} }}
	leader.conn = c
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		if deathContinuationNotifyCount(t, c.write.Bytes()) != 2 || deathContinuationNotifyCount(t, peerConn.write.Bytes()) != 2 {
			t.Fatal("经验提交挡住了接续死亡或队友权威死亡广播")
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	input, at := packets[:1], time.Now()
	var tail []byte
	var held *nativeResultReadChunk
	n, err := s.handleCurrentDungeonDeathReadyPackets(leader, &input, 0, 1, ahead, &tail, &held, &at)
	if err != nil || n != 2 || probe.calls != 1 {
		t.Fatalf("组队接续与提交：n=%d calls=%d err=%v", n, probe.calls, err)
	}
	before, _, _ := repos.Character.Load(context.Background(), "99")
	peerBefore, _, _ := repos.Character.Load(context.Background(), "100")
	if err := s.handleDungeonMonsterDeath(peer, packets[0].Data[13:]); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "99")
	peerAfter, _, _ := repos.Character.Load(context.Background(), "100")
	if probe.calls != 1 || !reflect.DeepEqual(before.Stats, after.Stats) || !reflect.DeepEqual(peerBefore.Stats, peerAfter.Stats) {
		t.Fatal("队友重复报告导致经验或其它角色属性重复变更")
	}
	if deathContinuationNotifyCount(t, c.write.Bytes()) != 2 || deathContinuationNotifyCount(t, peerConn.write.Bytes()) != 3 {
		t.Fatal("重复死亡没有只向请求队员重放原通知")
	}
}
