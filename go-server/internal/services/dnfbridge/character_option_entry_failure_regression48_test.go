package dnfbridge

import (
	"errors"
	"testing"
)

// skill-entry-failure-review48：让【承载 390 的那一次 Write】真实失败，再观察角色 gate 与重试。
//
// 两遍式：
//
//	pass1 用只计数 conn 走真实 op7→op8→op4，按【每次 Write 的字节区间】切帧，精确定位携带 390 的写序号 k；
//	pass2 用**同构新实例**在 k 处注入真实写错误（failNthDungeonWriteConn），断言 gate 仍 false、且后续重试能回放。
//
// 不手填 ready/owner/notice flag；不换用别的成功 conn 假装成功。
var errReview48Write = errors.New("review48 injected 390 write failure")

type review48CountingConn struct {
	bufferConn
	writes []int
}

func (c *review48CountingConn) Write(p []byte) (int, error) {
	n, err := c.bufferConn.Write(p)
	c.writes = append(c.writes, n)
	return n, err
}

// review48Seed 用真实 op4 选角 + 447 保存准备同一条连接上的 30(B)/29(A) 两个角色。
func review48Seed(t *testing.T, s *Service, se *gameSession) {
	t.Helper()
	seed := scope44Fresh(se, "review48-seed-B")
	scope44Select(t, s, seed, 30)
	scope44Save(t, s, seed, scope44Body(0x22))
	scope44Select(t, s, se, 29)
	scope44Save(t, s, se, scope44Body(0x11))
}

// review48Reenter 走真实 op7→op8→op4 再入场（与作者 44 批同一条链）。
func review48Reenter(t *testing.T, s *Service, se *gameSession) {
	t.Helper()
	scope44Upper(t, s, se, 7, nil)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 30)
}

func review48Count390(t *testing.T, raw []byte) int {
	t.Helper()
	count := 0
	for _, p := range splitAllCurrentGameServerUpperPackets(t, raw) {
		if p.Header.Classification == 0 && p.Header.MsgID == 390 {
			count++
		}
	}
	return count
}

func TestReview48FailedCharacterOptionWriteKeepsGateOpen(t *testing.T) {
	s, se, _ := scope44Fixture(t)
	review48Seed(t, s, se)
	base := se.conn.(*bufferConn)
	base.write.Reset()

	// ---- pass1：正常流程，定位携带 390 的写序号 k ----
	cc := &review48CountingConn{}
	se.conn = cc
	review48Reenter(t, s, se)
	k, offset := 0, 0
	raw := cc.bufferConn.write.Bytes()
	for i, n := range cc.writes {
		chunk := raw[offset : offset+n]
		offset += n
		if review48Count390(t, chunk) > 0 {
			k = i + 1 // failNthDungeonWriteConn 是 1 基
		}
	}
	if k == 0 {
		t.Fatal("normal flow emitted no 390 write to target")
	}
	t.Logf("review48 located 390 at write #%d (of %d writes)", k, len(cc.writes))

	// ---- pass2：同构新实例，在 k 处注入真实写错误 ----
	s2, se2, _ := scope44Fixture(t)
	review48Seed(t, s2, se2)
	se2.conn.(*bufferConn).write.Reset()
	fc := &failNthDungeonWriteConn{failAt: k, err: errReview48Write}
	se2.conn = fc
	review48Reenter(t, s2, se2)
	if got := review48Count390(t, fc.bufferConn.write.Bytes()); got != 0 {
		t.Fatalf("injected failure still produced %d x 390", got)
	}
	if se2.characterOptionNoticeSent {
		t.Fatal("character-option gate latched true after a failed 390 write")
	}
	t.Logf("review48 after failed write: characterOptionNoticeSent=false (gate not consumed)")

	// ---- 重试：同一连接、同一真实流程，应当还能回放一次 390 ----
	fc.failAt = 0 // 只失败一次；后续写不再注入错误
	review48Reenter(t, s2, se2)
	if got := review48Count390(t, fc.bufferConn.write.Bytes()); got != 1 {
		t.Fatalf("retry replay count=%d want 1 (lost replay must stay a real FAIL)", got)
	}
	if !se2.characterOptionNoticeSent {
		t.Fatal("successful retry did not latch the character gate")
	}
	t.Logf("review48 retry emitted exactly one 390 and latched the gate afterwards")
}
