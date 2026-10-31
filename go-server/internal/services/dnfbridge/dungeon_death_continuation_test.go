package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type deathContinuationWriteConn struct {
	bufferConn
	afterFirstDeath func()
	deathWritten    bool
}

func (c *deathContinuationWriteConn) Write(p []byte) (int, error) {
	n, err := c.bufferConn.Write(p)
	if !c.deathWritten && len(p) >= 3 && p[0] == 0 && binary.LittleEndian.Uint16(p[1:3]) == 38 {
		c.deathWritten = true
		if c.afterFirstDeath != nil {
			c.afterFirstDeath()
		}
	}
	return n, err
}

func deathContinuationNotifyCount(t *testing.T, wire []byte) int {
	t.Helper()
	count := 0
	for _, p := range splitAllCurrentGameServerUpperPackets(t, wire) {
		if p.Header.MsgID == 38 {
			count++
		}
	}
	return count
}

func TestDungeonDeathContinuationLateArrivalComparedWithFixedPrefix(t *testing.T) {
	for _, capped := range []bool{false, true} {
		for _, continuation := range []bool{false, true} {
			name := map[bool]string{false: "未满级", true: "满级"}[capped] + map[bool]string{false: "/原固定段", true: "/提交前接续"}[continuation]
			t.Run(name, func(t *testing.T) {
				s, p, repos, packets := prepareDungeonDeathBurstTest(t, capped)
				ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
				c := &deathContinuationWriteConn{afterFirstDeath: func() {
					// 第一只已通知之后第二只才读到，初次收集无法预先包含它。
					ahead.ready <- nativeResultReadChunk{data: packets[1].Data, readAt: time.Now()}
				}}
				p.conn = c
				probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
				wantDeaths := 1
				if continuation {
					wantDeaths = 2
				}
				probe.before = func() {
					if got := deathContinuationNotifyCount(t, c.write.Bytes()); got != wantDeaths {
						t.Fatalf("经验提交前死亡通知=%d，期望%d", got, wantDeaths)
					}
				}
				repos.CharacterProgression = probe
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
				if continuation {
					input, readyAt := packets[:1], time.Now()
					var tail []byte
					var held *nativeResultReadChunk
					n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &readyAt)
					if err != nil || n != 2 || len(ahead.ready) != 0 || held != nil || len(tail) != 0 {
						t.Fatalf("接续边界：n=%d err=%v held=%v tail=%d", n, err, held, len(tail))
					}
				} else {
					if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:1], false); err != nil || len(ahead.ready) != 1 {
						t.Fatalf("原入口未复现晚到包等待：%v", err)
					}
				}
				character, _, _ := repos.Character.Load(context.Background(), "99")
				key, gain := "exp", int64(200)
				if capped {
					key, gain = currentHonorExpertProgressExperienceStatKey, 10
				}
				if probe.calls != 1 || character.Stats[key] != gain*int64(wantDeaths) || p.dungeonDeathBurst != nil {
					t.Fatalf("提交次数或逐只累计异常：calls=%d stats=%v", probe.calls, character.Stats)
				}
			})
		}
	}
}

func TestDungeonDeathContinuationKeepsOtherCommandAndPartialEOF(t *testing.T) {
	for _, mode := range []string{"其他指令", "分片及EOF", "坏帧", "安静无后包"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
			ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 3)}
			other := nativeResultTestPacket(417, nil)
			c := &deathContinuationWriteConn{afterFirstDeath: func() {
				switch mode {
				case "其他指令":
					data := append(append(append([]byte(nil), packets[1].Data...), other.Data...), packets[2].Data...)
					ahead.ready <- nativeResultReadChunk{data: data}
				case "分片及EOF":
					ahead.ready <- nativeResultReadChunk{data: packets[1].Data[:4]}
					ahead.ready <- nativeResultReadChunk{data: packets[1].Data[4:]}
					ahead.ready <- nativeResultReadChunk{err: io.EOF}
				case "坏帧":
					ahead.ready <- nativeResultReadChunk{data: []byte{0xff, 0xff, 0, 0}}
				}
			}}
			p.conn = c
			input, readyAt := append([]dnfproto.LatestGameStreamPacket(nil), packets[:1]...), time.Now().Add(-time.Second)
			initialAt := readyAt
			var tail []byte
			var held *nativeResultReadChunk
			n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &readyAt)
			want := 2
			if mode == "坏帧" || mode == "安静无后包" {
				want = 1
			}
			if err != nil || n != want || deathContinuationNotifyCount(t, c.write.Bytes()) != want {
				t.Fatalf("边界处理n=%d want=%d err=%v", n, want, err)
			}
			if mode == "其他指令" && (len(input) != 4 || !bytes.Equal(input[2].Data, other.Data) || !bytes.Equal(input[3].Data, packets[2].Data) || !readyAt.After(initialAt)) {
				t.Fatal("跨过其他指令、丢失后包或沿用了早于到达的就绪时间")
			}
			if mode == "分片及EOF" && (held == nil || !errors.Is(held.err, io.EOF) || len(tail) != 0) {
				t.Fatal("EOF或半帧丢失")
			}
			if mode == "坏帧" && (held == nil || !bytes.Equal(held.data, []byte{0xff, 0xff, 0, 0})) {
				t.Fatal("坏帧未交回原循环")
			}
			character, _, _ := repos.Character.Load(context.Background(), "99")
			if character.Stats[currentHonorExpertProgressExperienceStatKey] != int64(want*10) {
				t.Fatal("停止接续时漏提交已有击杀")
			}
		})
	}
}

func TestDungeonDeathContinuationStopsAtLevelAndRoomBoundaries(t *testing.T) {
	for _, boundary := range []string{"升级", "清房"} {
		t.Run(boundary, func(t *testing.T) {
			var s *Service
			var p *gameSession
			var repos dnfrepo.Group
			var packets []dnfproto.LatestGameStreamPacket
			if boundary == "升级" {
				s, p, repos, packets = prepareDungeonDeathBurstLevelBoundary(t, "100")
			} else {
				s, p, repos, packets = prepareDungeonDeathBurstTest(t, true, 1)
			}
			ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
			p.conn = &deathContinuationWriteConn{afterFirstDeath: func() { ahead.ready <- nativeResultReadChunk{data: packets[0].Data} }}
			input, readyAt := packets[:1], time.Now()
			var tail []byte
			var held *nativeResultReadChunk
			n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &readyAt)
			if err != nil || n != 1 || len(ahead.ready) != 1 || p.dungeonDeathBurst != nil {
				t.Fatalf("%s后继续消费：n=%d err=%v", boundary, n, err)
			}
			if boundary == "升级" {
				c, _, _ := repos.Character.Load(context.Background(), "99")
				if c.Level != 21 || c.Stats["exp"] != 150 {
					t.Fatalf("升级未即时提交：%+v", c)
				}
			}
		})
	}
}

func TestDungeonDeathContinuationBoundsWorkAndPreservesUnreadBytes(t *testing.T) {
	baseline, baselineSession, baselineRepos, baselinePackets := prepareDungeonDeathBurstTest(t, true, 40)
	if err := baseline.handleCurrentDungeonDeathBurstPackets(baselineSession, baselinePackets[:32], false); err != nil {
		t.Fatal(err)
	}
	want, _, _ := baselineRepos.Character.Load(context.Background(), "99")
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true, 40)
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	p.conn = &deathContinuationWriteConn{afterFirstDeath: func() {
		var data []byte
		for _, packet := range packets[1:] {
			data = append(data, packet.Data...)
		}
		ahead.ready <- nativeResultReadChunk{data: data}
	}}
	input, at := append([]dnfproto.LatestGameStreamPacket(nil), packets[:1]...), time.Now()
	var tail []byte
	var held *nativeResultReadChunk
	n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &at)
	c, _, _ := repos.Character.Load(context.Background(), "99")
	if err != nil || n != 32 || len(input) != 40 || !bytes.Equal(input[32].Data, packets[32].Data) || !reflect.DeepEqual(c.Stats, want.Stats) {
		t.Fatalf("接续越过32条边界或丢失后续请求：n=%d input=%d exp=%d err=%v", n, len(input), c.Stats[currentHonorExpertProgressExperienceStatKey], err)
	}
}

func TestDungeonDeathContinuationBoundsPartialReadsAndRevokedLogin(t *testing.T) {
	for _, mode := range []string{"半帧预算", "替换登录"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
			ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 20)}
			p.conn = &deathContinuationWriteConn{afterFirstDeath: func() {
				if mode == "替换登录" {
					ahead.ready <- nativeResultReadChunk{data: packets[1].Data}
					p.loginRevoked.Store(true)
					return
				}
				for i := 0; i < 20; i++ {
					ahead.ready <- nativeResultReadChunk{data: packets[1].Data[i : i+1]}
				}
			}}
			input, at := append([]dnfproto.LatestGameStreamPacket(nil), packets[:1]...), time.Now()
			var tail []byte
			var held *nativeResultReadChunk
			n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &at)
			if mode == "替换登录" {
				if !errors.Is(err, errLoginReplaced) {
					t.Fatalf("登录撤销后仍接受后续击杀：%v", err)
				}
			} else if err != nil || n != 1 || len(ahead.ready) != 4 || !bytes.Equal(tail, packets[1].Data[:16]) || held != nil {
				t.Fatalf("半帧预算或未读字节丢失：n=%d queue=%d tail=%d err=%v", n, len(ahead.ready), len(tail), err)
			}
			character, _, _ := repos.Character.Load(context.Background(), "99")
			if character.Stats[currentHonorExpertProgressExperienceStatKey] != 10 || p.dungeonDeathBurst != nil {
				t.Fatal("停止接续后已有击杀未提交，或撤销登录仍给后包奖励")
			}
		})
	}
}

func TestDungeonDeathContinuationWriteFailureStillSettlesAcceptedPrefix(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	p.conn = &deathNotifyFailedConn{}
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	ahead.ready <- nativeResultReadChunk{data: packets[1].Data}
	input, at := packets[:1], time.Now()
	var tail []byte
	var held *nativeResultReadChunk
	n, err := s.handleCurrentDungeonDeathReadyPackets(p, &input, 0, 1, ahead, &tail, &held, &at)
	c, _, _ := repos.Character.Load(context.Background(), "99")
	if err == nil || n != 1 || len(ahead.ready) != 1 || c.Stats[currentHonorExpertProgressExperienceStatKey] != 10 || p.dungeonDeathBurst != nil {
		t.Fatalf("写失败后的已接受击杀或未处理包异常：n=%d exp=%d err=%v", n, c.Stats[currentHonorExpertProgressExperienceStatKey], err)
	}
}

// 第三次Read开始，证明第二个读块已经进入readAhead队列；不用sleep猜到达时机。
type deathContinuationInputConn struct {
	deathContinuationWriteConn
	first, second []byte
	readCount     int
	firstAck      chan struct{}
	secondQueued  chan struct{}
}

func (c *deathContinuationInputConn) Read(p []byte) (int, error) {
	c.readCount++
	switch c.readCount {
	case 1:
		return copy(p, c.first), nil
	case 2:
		<-c.firstAck
		return copy(p, c.second), nil
	case 3:
		close(c.secondQueued)
	}
	return 0, io.EOF
}

func TestDungeonDeathContinuationRealInputNotifiesLateDeathBeforeCommit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	c := &deathContinuationInputConn{first: packets[0].Data, second: packets[1].Data, firstAck: make(chan struct{}), secondQueued: make(chan struct{})}
	c.afterFirstDeath = func() { close(c.firstAck); <-c.secondQueued }
	p.conn = c
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		if n := deathContinuationNotifyCount(t, c.write.Bytes()); n != 2 {
			t.Fatalf("实际收包入口仍在第二只通知前提交：%d", n)
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	s.handleGameStreamInput(context.Background(), p)
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if probe.calls != 1 || character.Stats[currentHonorExpertProgressExperienceStatKey] != 20 || p.dungeonDeathBurst != nil {
		t.Fatal("实际收包及EOF未完整提交两只经验")
	}
}
