package fiendwar

import (
	"testing"
	"time"
)

// 罗什的四颗属性球：**两队共享一个池子，两侧各拿一颗不同的球**。
//
// 官方攻略（3DM/COLG「超时空漩涡宇宙恶魔罗什攻略」）里的原话，就是下面这个流程：
//
//	「罗什四属性球出现后开始随机选择：黑暗队伍视角第一个分身为虚无、
//	  纯白队伍视角第一个分身为火 … 当黑暗队伍消灭虚无分身：罗什选择了风分身、
//	  纯白队伍视角虚无球消失（显示冰球/风球）… 当纯白队伍消灭火分身：罗什自动选择冰分身、
//	  黑暗队伍视角火球消失（显示冰球）… 当纯白队伍消灭冰分身 → 两队视角所有球消失本体出现」
//
// 也就是说"两侧都报同一个 value 才推进"那个**锁步**模型是错的（那是本树 2026-10-02
// 之前的实现，操作员看到的"两颗球一直重复、机制顺序不对"就是它）。
func TestFiendWarRoshanBallPoolIsSharedAndSidesAreIndependent(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	// 钉住分球顺序，断言才能写死：宝石(5) → 火(6) → 冰(7) → 风(8)。
	b.PickClone = func(candidates []int) int { return 0 }

	b.resetBossPattern()
	if got := b.Value("boss pattern state"); got != 1111 {
		t.Fatalf("球池 = %d，want 1111", got)
	}
	if got := b.Value("top boss state"); got != 5 {
		t.Fatalf("上半侧第一颗球 = %d，want 5（宝石/虚无）", got)
	}
	if got := b.Value("bot boss state"); got != 6 {
		t.Fatalf("下半侧第一颗球 = %d，want 6（火，不能与上半侧同一颗）", got)
	}

	// 上半侧打掉自己那颗（虚无）：只清 1000 位，并且**只给上半侧**补一颗新的（冰）。
	if err := b.BossReport(4978, 1, 5, now); err != nil {
		t.Fatalf("上半侧机制上报: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 111 {
		t.Fatalf("打掉虚无之后球池 = %d，want 111", got)
	}
	if got := b.Value("top boss state"); got != 7 {
		t.Fatalf("上半侧补到的球 = %d，want 7（冰）", got)
	}
	if got := b.Value("bot boss state"); got != 6 {
		t.Fatalf("上半侧打自己的球却动了对面：下半侧 = %d，want 6", got)
	}
	// 客户端把已经打掉的那颗再报一遍（重发/迟到）必须被拒：那颗球不在池子里了。
	if err := b.BossReport(4978, 1, 5, now); err == nil {
		t.Fatal("已经打掉的球又被接受了一次")
	}
	if got := b.Value("boss pattern state"); got != 111 {
		t.Fatalf("重复上报动了球池：%d，want 111", got)
	}

	// 下半侧打掉自己那颗（火）：池子里只剩上半侧没拿的风。
	if err := b.BossReport(4953, 1, 6, now); err != nil {
		t.Fatalf("下半侧机制上报: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 11 {
		t.Fatalf("打掉火之后球池 = %d，want 11", got)
	}
	if got := b.Value("bot boss state"); got != 8 {
		t.Fatalf("下半侧补到的球 = %d，want 8（风）", got)
	}
	// 上半侧清掉冰：池子只剩下半侧手上的风，上半侧转4等待，不提前破防。
	if err := b.BossReport(4978, 1, 7, now); err != nil {
		t.Fatalf("上半侧第二颗球: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 1 {
		t.Fatalf("打掉冰之后球池 = %d，want 1", got)
	}
	if got := b.Value("top boss state"); got != 4 {
		t.Fatalf("上半侧应当在等下半侧收尾（state=4），got %d", got)
	}
	if got := b.Value("bot boss state"); got != 8 {
		t.Fatalf("下半侧还在打风：state=%d，want 8", got)
	}
	// 下半侧清掉风：四颗全清 → 两侧都是 9，本体破防。
	if err := b.BossReport(4953, 1, 8, now); err != nil {
		t.Fatalf("下半侧第二颗球: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 0 {
		t.Fatalf("四颗球清完球池 = %d，want 0", got)
	}
	if b.Value("top boss state") != 9 || b.Value("bot boss state") != 9 {
		t.Fatalf("四颗球清完两侧应当都是 9：top=%d bot=%d",
			b.Value("top boss state"), b.Value("bot boss state"))
	}
}

// 破防窗口没打完：官方口径是"两队血量均摊（向下取整）、整轮重开"。
//
//	「15秒内两队将各自的BOSS击杀即可顺利通关，如果未击杀成功，则会将BOSS剩余血量
//	  均摊给两个队伍，同上机制重新再打一轮即可。」
//	「破防结束后两队会平衡血量，结果向下取整。」
func TestFiendWarRoshanFailedWindowBalancesHPAndReopensThePattern(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	b.PickClone = func(candidates []int) int { return 0 }
	b.resetBossPattern()
	openDamageWindow(t, b, now)
	// 上半侧把本体打到 0、下半侧一点没打 —— 真机 2026-10-02 13:15 就是"一侧 100、
	// 一侧 0"，结果整轮重开、玩家看到"打完球 BOSS 不死、又开始出球"。
	b.set("top boss hp", 0)
	b.set("bot boss hp", 100)

	if err := b.BossReport(4978, 2, 0, now); err != nil {
		t.Fatalf("上半侧破防上报: %v", err)
	}
	// 只有一侧报尾时不许动：另一侧还没报它那一轮的结果。
	if got := b.Value("boss pattern state"); got != 0 {
		t.Fatalf("只报一侧就重开了机制：球池 = %d，want 0", got)
	}
	if err := b.BossReport(4953, 2, 100, now); err != nil {
		t.Fatalf("下半侧破防上报: %v", err)
	}
	if got := b.Value("top boss hp"); got != 50 {
		t.Fatalf("血量均摊后上半侧 = %d，want 50", got)
	}
	if got := b.Value("bot boss hp"); got != 50 {
		t.Fatalf("血量均摊后下半侧 = %d，want 50", got)
	}
	if got := b.Value("top boss view hp"); got != 50 {
		t.Fatalf("战况表上的上半侧血量 = %d，want 50", got)
	}
	if got := b.Value("boss pattern state"); got != 1111 {
		t.Fatalf("没打完就该重开一轮：球池 = %d，want 1111", got)
	}
	if b.Value("top boss state") < 5 || b.Value("top boss state") > 8 {
		t.Fatalf("重开之后上半侧没有拿到球：state=%d", b.Value("top boss state"))
	}
	if b.Value("top boss state") == b.Value("bot boss state") {
		t.Fatalf("重开之后两侧拿了同一颗球：top=%d bot=%d",
			b.Value("top boss state"), b.Value("bot boss state"))
	}
	if b.BossWindowDone[0] || b.BossWindowDone[1] {
		t.Fatal("重开之后破防报尾标志没有清")
	}
}

// 两队都在窗口里打死了本体：不许均摊、不许重开 —— 球池停在 0、两侧停在 9，
// 剩下的交给客户端证死之后的那条通关路（PVF：state 9 + clear → 10，两个 10 → CLEAR PHASE）。
func TestFiendWarRoshanWindowVictoryDoesNotReopenThePattern(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	b.PickClone = func(candidates []int) int { return 0 }
	b.resetBossPattern()
	openDamageWindow(t, b, now)
	if err := b.BossReport(4978, 2, 0, now); err != nil {
		t.Fatalf("上半侧破防上报: %v", err)
	}
	if err := b.BossReport(4953, 2, 0, now); err != nil {
		t.Fatalf("下半侧破防上报: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 0 {
		t.Fatalf("两队都打死了本体，机制却重开了：球池 = %d", got)
	}
	if b.Value("top boss state") != 9 || b.Value("bot boss state") != 9 {
		t.Fatalf("通关那一档两侧应当还在 9：top=%d bot=%d",
			b.Value("top boss state"), b.Value("bot boss state"))
	}
	if got := b.Value("top boss hp"); got != 0 {
		t.Fatalf("上半侧血量 = %d，want 0", got)
	}
}

// openDamageWindow 把四颗球报掉，让两侧都停在 9（本体破防）。
func openDamageWindow(t *testing.T, b *FiendWarBattle, now time.Time) {
	t.Helper()
	for _, room := range []struct {
		dungeon int
		side    string
	}{{4978, "top boss"}, {4953, "bot boss"}, {4978, "top boss"}, {4953, "bot boss"}} {
		state := b.Value(room.side + " state")
		if state < 5 || state > 8 {
			t.Fatalf("%s 没有球可打：state=%d", room.side, state)
		}
		if err := b.BossReport(room.dungeon, 1, state, now); err != nil {
			t.Fatalf("%s 机制上报 value=%d: %v", room.side, state, err)
		}
	}
	if b.Value("boss pattern state") != 0 {
		t.Fatalf("四颗球清完球池 = %d，want 0", b.Value("boss pattern state"))
	}
}

func newFixtureBossBattle(t *testing.T) (*FiendWarBattle, time.Time) {
	t.Helper()
	config := &FiendWarBattleConfig{
		Raid: &AntonConfig{},
		Areas: map[int]FiendWarArea{
			30: {Index: 30, Dungeon: 4978, Kind: "top boss", Symbol: 200},
			31: {Index: 31, Dungeon: 4953, Kind: "bot boss", Symbol: 201},
		},
		Symbols: map[string]uint32{
			"top boss state": 200, "bot boss state": 201,
			"top boss hp": 202, "bot boss hp": 203,
			"top boss view hp": 204, "bot boss view hp": 205,
			"boss pattern state": 206,
		},
		Rules: []FiendWarRule{{}},
	}
	b, err := NewFiendWarBattle(config)
	if err != nil {
		t.Fatalf("fixture 战场: %v", err)
	}
	b.Areas[30] = FiendWarAreaState{Group: 1}
	b.Areas[31] = FiendWarAreaState{Group: 2}
	return b, time.Unix(0, 0)
}
