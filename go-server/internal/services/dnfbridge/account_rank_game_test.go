package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func rankRequest(id uint32, metric, period, mode byte, offset uint32) []byte {
	b := make([]byte, 20)
	copy(b, "RKQ1")
	binary.LittleEndian.PutUint32(b[4:8], id)
	binary.LittleEndian.PutUint32(b[8:12], 9)
	b[12], b[13], b[14] = metric, period, mode
	binary.LittleEndian.PutUint32(b[16:20], offset)
	return b
}
func rankName(p []byte) string {
	v := []uint16{}
	for len(p) >= 2 {
		u := binary.LittleEndian.Uint16(p)
		if u == 0 {
			break
		}
		v = append(v, u)
		p = p[2:]
	}
	return string(utf16.Decode(v))
}

func TestAccountRankGameProjection(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	for _, period := range []byte{0, 1, 2} {
		r, ok := decodeAccountRankRequest(rankRequest(1, 0, period, 0, 99995))
		if !ok {
			t.Fatal("合法请求未解析")
		}
		b, e := s.buildAccountRankGame(context.Background(), id, r, now)
		if e != nil {
			t.Fatal(e)
		}
		if len(b) != 224 || string(b[:4]) != "RKA1" || b[7] != 2 || binary.LittleEndian.Uint32(b[8:12]) != 2 || binary.LittleEndian.Uint32(b[12:16]) != 0 || binary.LittleEndian.Uint32(b[16:20]) != 1 || binary.LittleEndian.Uint64(b[20:28]) != 1 {
			t.Fatalf("账号分页或自身排名错误 %x", b)
		}
		if rankName(b[80:144]) != "当前角色" || rankName(b[160:224]) != "其它账号" {
			t.Fatal("代表角色不符")
		}
		if binary.LittleEndian.Uint32(b[64:68]) != 1 || binary.LittleEndian.Uint32(b[144:148]) != 1 {
			t.Fatal("同分并列失效")
		}
	}
	r, _ := decodeAccountRankRequest(rankRequest(2, 1, 2, 1, 0))
	b, e := s.buildAccountRankGame(context.Background(), id, r, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != 144 || b[7] != 1 || binary.LittleEndian.Uint64(b[72:80]) != 1 || rankName(b[80:144]) != "即将删除角色" {
		t.Fatal("角色贡献不符")
	}
	r.period = 0
	b, e = s.buildAccountRankGame(context.Background(), id, r, now)
	if e != nil || len(b) != 64 || binary.LittleEndian.Uint32(b[8:12]) != 0 {
		t.Fatal("空榜错误", e)
	}
}

func TestAccountRankGameRequestAndNameBounds(t *testing.T) {
	good := rankRequest(1, 0, 0, 0, 0)
	for n := 0; n < len(good); n++ {
		if _, ok := decodeAccountRankRequest(good[:n]); ok {
			t.Fatal("截断请求被接受", n)
		}
	}
	for _, edit := range []func([]byte){func(b []byte) { b[0] = 'X' }, func(b []byte) { b[4] = 0 }, func(b []byte) { b[8] = 0 }, func(b []byte) { b[12] = 5 }, func(b []byte) { b[13] = 3 }, func(b []byte) { b[14] = 2 }, func(b []byte) { b[15] = 1 }, func(b []byte) { b[16] = 1 }, func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 100005) }} {
		b := bytes.Clone(good)
		edit(b)
		if _, ok := decodeAccountRankRequest(b); ok {
			t.Fatal("越界请求被接受", b)
		}
	}
	if _, ok := decodeAccountRankRequest(append(good, []byte("指定其他账号")...)); ok {
		t.Fatal("含额外身份的请求被接受")
	}
	dst := make([]byte, 64)
	putAccountRankName(dst, "<角色>\n\x00\u202e")
	if rankName(dst) != "角色" {
		t.Fatal("名称控制字符未过滤")
	}
	dst = make([]byte, 64)
	putAccountRankName(dst, string(bytes.Repeat([]byte{'a'}, 30))+"😀")
	if rankName(dst) != string(bytes.Repeat([]byte{'a'}, 30)) {
		t.Fatal("UTF16代理对被截断")
	}
}

type rankObserved struct {
	repo.ActivityRepository
	repo.ActivityRankingRepository
	reads            atomic.Int32
	entered, release chan struct{}
}

func (a *rankObserved) RankContributions(ctx context.Context, m string, w repo.ActivityRankWindow) ([]repo.ActivityRankContribution, error) {
	a.reads.Add(1)
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.ActivityRankingRepository.RankContributions(ctx, m, w)
}
func rankFixture(t *testing.T) (*Service, *gameSession, *rankObserved, *playerStatsWireConn) {
	s, x, old, c := playerStatsGameFixture(t)
	g, _ := s.repositoryProvider()
	a := &rankObserved{ActivityRepository: old.ActivityRepository, ActivityRankingRepository: old.ActivityRepository.(repo.ActivityRankingRepository)}
	g.Activity = a
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	return s, x, a, c
}
func rankCall(t *testing.T, s *Service, x *gameSession, id uint32) {
	t.Helper()
	playerStatsEdit(t, s, x, func() {
		if e := s.handleGameCommand(x, 1, accountRankGameOpcode, rankRequest(id, 0, 2, 0, 0)); e != nil {
			t.Fatal(e)
		}
	})
}
func rankPacket(t *testing.T, c *playerStatsWireConn) []byte {
	t.Helper()
	select {
	case b := <-c.packets:
		if len(b) < 36 || len(b) > 500 || b[0] != 1 || binary.LittleEndian.Uint16(b[1:3]) != accountRankGameOpcode || binary.LittleEndian.Uint32(b[3:7]) != uint32(len(b)) || b[15] != 0 || string(b[16:20]) != "RKR1" {
			t.Fatalf("排行榜帧不符 %x", b)
		}
		return b
	case <-time.After(4 * time.Second):
		t.Fatal("未收到排行榜应答")
	}
	return nil
}
func rankIdle(t *testing.T, x *gameSession) {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for x.accountRank.busy.Load() {
		if time.Now().After(until) {
			t.Fatal("排行榜查询未退出")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAccountRankGameTransport(t *testing.T) {
	s, x, a, c := rankFixture(t)
	a.entered = make(chan struct{}, 1)
	a.release = make(chan struct{})
	rankCall(t, s, x, 1)
	select {
	case <-a.entered:
	case <-time.After(time.Second):
		t.Fatal("未进入仓储")
	}
	for i := uint32(2); i < 20; i++ {
		rankCall(t, s, x, i)
	}
	if a.reads.Load() != 1 {
		t.Fatal("重复请求增加查询")
	}
	close(a.release)
	b := rankPacket(t, c)
	rankIdle(t, x)
	if b[28] != 0 || string(b[36:40]) != "RKA1" || binary.LittleEndian.Uint32(b[20:24]) != 1 {
		t.Fatal("回复未绑定原请求")
	}
	playerStatsEdit(t, s, x, func() { x.accountRank.last = time.Time{} })
	rankCall(t, s, x, 50)
	cached := rankPacket(t, c)
	if a.reads.Load() != 1 || !bytes.Equal(b[36:], cached[36:]) {
		t.Fatal("15秒缓存未复用")
	}
	if x.playerStats.request != 0 || !x.battlePassLast.IsZero() {
		t.Fatal("污染个人统计或战令状态")
	}
}

func TestAccountRankGameIdentityAndStale(t *testing.T) {
	for _, mode := range []string{"未认证", "换角", "换账号", "替换登录", "容量已满"} {
		t.Run(mode, func(t *testing.T) {
			s, x, a, c := rankFixture(t)
			if mode == "未认证" {
				playerStatsEdit(t, s, x, func() { x.accountFromToken = false })
				rankCall(t, s, x, 1)
				if a.reads.Load() != 0 || len(c.packets) != 0 {
					t.Fatal("未认证会话可读")
				}
				return
			}
			if mode == "容量已满" {
				for i := 0; i < 4; i++ {
					if !s.playerStatsQueries.acquire() {
						t.Fatal("无法设置容量")
					}
				}
				defer func() {
					for i := 0; i < 4; i++ {
						s.playerStatsQueries.release()
					}
				}()
				rankCall(t, s, x, 1)
				b := rankPacket(t, c)
				if len(b) != 36 || b[28] != 2 || a.reads.Load() != 0 {
					t.Fatal("未限制并发")
				}
				return
			}
			a.entered = make(chan struct{}, 1)
			a.release = make(chan struct{})
			rankCall(t, s, x, 1)
			select {
			case <-a.entered:
			case <-time.After(time.Second):
				t.Fatal("未进入仓储")
			}
			playerStatsEdit(t, s, x, func() {
				switch mode {
				case "换角":
					s.mu.Lock()
					x.characterGeneration++
					s.mu.Unlock()
				case "换账号":
					x.accountID = "不同登录账号"
				case "替换登录":
					x.loginRevoked.Store(true)
				}
			})
			close(a.release)
			rankIdle(t, x)
			// 被替换的登录已禁止提交任何会话事件；其后台查询结束即是栅栏。
			if mode != "替换登录" {
				playerStatsEdit(t, s, x, func() {})
			}
			if len(c.packets) != 0 {
				t.Fatal("迟到数据跨会话返回")
			}
		})
	}
}

// 固定跨语言样例由服务端编码产生，供实际合入EXE的解码器隔离验证。
func TestAccountRankGameClientFixture(t *testing.T) {
	if dir := os.Getenv("ACCOUNT_RANK_FIXTURE_DIR"); dir != "" {
		s, id, now := playerStatsFixture(t)
		r, _ := decodeAccountRankRequest(rankRequest(0x12345678, 0, 2, 0, 0))
		b, e := s.buildAccountRankGame(context.Background(), id, r, now)
		if e != nil {
			t.Fatal(e)
		}
		p := encodeGamePlayerStatsReply(r.gamePlayerStatsRequest, 0, b)
		copy(p, "RKR1")
		if e = os.WriteFile(filepath.Join(dir, "rank_game_reply.bin"), p, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
