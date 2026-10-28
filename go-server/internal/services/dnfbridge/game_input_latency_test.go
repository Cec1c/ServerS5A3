package dnfbridge

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGameInputLatencyDiagnosticsKeepDeathRewardsAndPackets(t *testing.T) {
	for _, capped := range []bool{false, true} {
		var reference [][]byte
		var referenceStats map[string]int64
		for _, trace := range []bool{false, true} {
			s, p, repos, packets := prepareDungeonDeathBurstTest(t, capped)
			path := filepath.Join(t.TempDir(), "等待诊断.txt")
			logger, err := openPacketLogger(path)
			if err != nil {
				t.Fatal(err)
			}
			s.packetLog = logger
			readyAt := time.Time{}
			if trace {
				readyAt = time.Now().Add(-500 * time.Millisecond)
			}
			if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false, readyAt); err != nil {
				t.Fatal(err)
			}
			var bodies [][]byte
			for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
				key := []byte{frame.Header.Classification, byte(frame.Header.MsgID), byte(frame.Header.MsgID >> 8)}
				bodies = append(bodies, append(key, frame.Body...))
			}
			character, _, err := repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if !trace {
				reference, referenceStats = bodies, character.Stats
			} else if !reflect.DeepEqual(reference, bodies) || !reflect.DeepEqual(referenceStats, character.Stats) {
				t.Fatal("等待诊断改变了死亡、经验报文或持久化奖励")
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			text, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if trace {
				expected = 3
			}
			if strings.Count(string(text), "kind=死亡请求同批等待耗时 ") != expected {
				t.Fatal("慢请求诊断与逐只死亡不匹配")
			}
		}
	}
}

func TestGameInputLatencyReadAheadRetainsBytesAndReadTime(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ahead := newNativeResultReadAhead(server)
	defer ahead.stop()
	payload := []byte("独立夹具字节，不含真实凭据")
	started := time.Now()
	written := make(chan error, 1)
	go func() { _, err := client.Write(payload); written <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunk := ahead.next(ctx)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if chunk.err != nil || !bytes.Equal(chunk.data, payload) || chunk.readAt.Before(started) || chunk.readAt.After(time.Now()) {
		t.Fatal("提前读取的内容、错误或读取时间不符")
	}
}

func TestGameInputLatencyReadWaitNeverLogsPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "读块等待.txt")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	s, p := &Service{packetLog: logger}, &gameSession{connID: "独立等待夹具"}
	payload := []byte("禁止进入日志的独立夹具数据")
	s.logCurrentGameReadWait(p, nativeResultReadChunk{data: payload})
	s.logCurrentGameReadWait(p, nativeResultReadChunk{data: payload, readAt: time.Now()})
	s.logCurrentGameReadWait(p, nativeResultReadChunk{readAt: time.Now().Add(-time.Second)})
	s.logCurrentGameReadWait(p, nativeResultReadChunk{data: payload, readAt: time.Now().Add(-time.Second)})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "kind=游戏读块消费等待耗时 ") != 1 || bytes.Contains(raw, payload) || strings.Contains(string(raw), "hex=") {
		t.Fatal("读块等待必须只记录慢样本的时间与长度")
	}
}
