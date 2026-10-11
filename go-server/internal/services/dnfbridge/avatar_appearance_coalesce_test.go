package dnfbridge

import (
	"context"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// 连点时装时，第一次穿脱照旧同步下发角色外观，紧随其后的几次合并成一次"尾随刷新"：
// 线上实测每点一件都会紧跟一个 class0/op2 的角色体，连穿几件就卡得厉害；
// 但尾随刷新必须存在，否则又会回到"卡顿后时装消失"。
//
// 2026-09-28 第一轮：这条合并**只属于 mode1 回退档**
// （DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode1），默认档（mode0）当时每次穿脱都同步发。
//
// 2026-09-28 第二轮（操作者："现在穿戴时装模型会跟着变 但是我对穿上脱下的速度不太满意"）：
// mode0 是**真正刷新模型**的那条包，不能再用 mode1/mode0 切换换速度，所以合并被搬到
// mode0 档并改成 leading + trailing —— 见紧随其后的
// TestAvatarAppearanceMode0BurstSendsLeadingThenSingleTrailing 与
// TestAvatarAppearanceMode0CoalesceOffSendsEveryWearImmediately。
// 本用例继续钉住 mode1 回退档那条**行为不变**的路径。
func TestAvatarAppearanceCoalescesWearBurstAndFlushesTrailingRefresh(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)
	// 连点合并只在 mode1 回退档存在；本用例覆盖的正是那一档。
	service.options.equipAppearanceRefreshMode1Rollback = true
	if service.equipAppearanceRefreshUsesMode0() {
		t.Fatal("the coalescing fixture must run the mode1 rollback")
	}
	if !currentTownSelfActorAppearanceReady(session) {
		t.Fatal("fixture session is not appearance-ready")
	}

	// 第一次：同步下发，立刻能看到角色体。
	if err := service.sendAlignedPostAction(session, avatarAppearanceTestResult(), alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
		t.Fatalf("first appearance refresh: %v", err)
	}
	if conn.write.Len() == 0 {
		t.Fatal("the first avatar wear must still send the actor body synchronously")
	}
	firstLen := conn.write.Len()
	conn.write.Reset()

	// 紧随其后的第二次：并入尾随刷新，不再立刻重建角色。
	if err := service.sendAlignedPostAction(session, avatarAppearanceTestResult(), alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
		t.Fatalf("coalesced appearance refresh: %v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("a burst wear rebuilt the actor immediately: %d bytes", conn.write.Len())
	}

	// 尾随刷新必须补上，否则最后一件时装的外观会丢。
	waitForAvatarAppearancePackets(t, conn, 1)
	if conn.write.Len() != firstLen {
		t.Logf("trailing actor body length=%d (first=%d)", conn.write.Len(), firstLen)
	}
	// 关会话必须能取消待发刷新，不能给已断开的连接发包。
	service.stopAvatarAppearanceCoalesce(session, "test_teardown")
}

// TestAvatarAppearanceMode0BurstSendsLeadingThenSingleTrailing 是第二轮性能修复的钉子：
// 默认档（mode0）下一次穿脱仍然是**一个包**（点一下立刻看到），但**N 连点最多 2 个包**
// ——窗口内的第一次（leading）同步发，窗口内其余几次合并成窗口末尾的一次尾随刷新。
//
// 现场依据（客户端 trace 2026-09-28，pid=3776）：每一次 mode0 都会让客户端走
// `mode0-owner-compare branch=current-scene-create` → `local-actor-create`
// → `game-options-apply`（整题重建本地角色），所以 N 连点时装 = N 次重建，
// 这就是操作者"穿上脱下的速度不太满意"的那一半。leading + trailing 把
// 一次 4 连穿从 4 个 mode0 压到 2 个。
func TestAvatarAppearanceMode0BurstSendsLeadingThenSingleTrailing(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)
	if !service.equipAppearanceRefreshUsesMode0() {
		t.Fatal("the default appearance refresh must be mode0")
	}
	if !service.avatarAppearanceBurstCoalesceEnabled() {
		t.Fatal("mode0 burst coalescing must be enabled by default")
	}
	// 窗口压到 60 ms，让尾随刷新在测试里立刻到达。
	service.options.avatarAppearanceCoalesceWindowMillis = 60
	if !currentTownSelfActorAppearanceReady(session) {
		t.Fatal("fixture session is not appearance-ready")
	}

	const clicks = 4
	for click := 0; click < clicks; click++ {
		if err := service.sendAlignedPostAction(session, avatarAppearanceTestResult(), alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
			t.Fatalf("appearance refresh #%d: %v", click+1, err)
		}
	}
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != 1 {
		t.Fatalf("a %d-click avatar burst sent %d mode0 packets immediately, want only the leading one", clicks, got)
	}

	// 尾随刷新必须补上，否则最后一件时装的外观会丢。
	waitForAvatarAppearancePackets(t, conn, 2)
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != 2 {
		t.Fatalf("a %d-click avatar burst sent %d mode0 packets in total, want leading+trailing = 2", clicks, got)
	}
	service.stopAvatarAppearanceCoalesce(session, "test_teardown")
}

// TestAvatarAppearanceMode0CoalesceOffSendsEveryWearImmediately 是第二轮的一键回退钉子：
// DNFBRIDGE_EQUIP_APPEARANCE_COALESCE=off（= options.avatarAppearanceCoalesceOff）
// 必须逐字节回到 2026-09-28 第一轮：mode0 档每一次穿脱都同步发一个 mode0，
// 不再有窗口、也不再有尾随刷新。
func TestAvatarAppearanceMode0CoalesceOffSendsEveryWearImmediately(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)
	if !service.equipAppearanceRefreshUsesMode0() {
		t.Fatal("the default appearance refresh must be mode0")
	}
	service.options.avatarAppearanceCoalesceOff = true
	if service.avatarAppearanceBurstCoalesceEnabled() {
		t.Fatal("the rollback switch did not disable mode0 burst coalescing")
	}
	service.options.avatarAppearanceCoalesceWindowMillis = 60
	if !currentTownSelfActorAppearanceReady(session) {
		t.Fatal("fixture session is not appearance-ready")
	}

	const clicks = 4
	for click := 0; click < clicks; click++ {
		if err := service.sendAlignedPostAction(session, avatarAppearanceTestResult(), alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
			t.Fatalf("appearance refresh #%d: %v", click+1, err)
		}
	}
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != clicks {
		t.Fatalf("coalesce=off sent %d mode0 packets for %d clicks, want one per click", got, clicks)
	}
	// 关掉合并后不应该再排任何尾随刷新。
	time.Sleep(3 * 60 * time.Millisecond)
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != clicks {
		t.Fatalf("coalesce=off scheduled a trailing refresh: %d packets, want %d", got, clicks)
	}
}

// avatarAppearanceTestResult 是 sendAlignedPostAction 需要的最小 Result：
// 只用来点名 operation（日志/证据字段），不带任何额外投影。
func avatarAppearanceTestResult() alignedcmd.Result {
	return alignedcmd.Result{Operation: "move_itemspace"}
}

// countGameServerUpperPackets 数一份流里有多少个 upper 包（每个包都用
// splitGameServerUpperPacket 的同一套头解析），用于钉住"连点发了几个 mode0"。
func countGameServerUpperPackets(t *testing.T, data []byte) int {
	t.Helper()
	count := 0
	rest := data
	for len(rest) != 0 {
		_, remaining := splitGameServerUpperPacket(t, rest)
		count++
		if len(remaining) >= len(rest) {
			t.Fatalf("packet splitter did not consume bytes: rest=%d count=%d", len(rest), count)
		}
		rest = remaining
	}
	return count
}

// waitForAvatarAppearancePackets 等尾随刷新到达（最多 3 s），到达后即返回。
func waitForAvatarAppearancePackets(t *testing.T, conn *bufferConn, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countGameServerUpperPackets(t, conn.write.Bytes()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d appearance packets arrived, want at least %d", countGameServerUpperPackets(t, conn.write.Bytes()), want)
}

func newAvatarAppearanceCoalesceTestService(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "31",
		AccountID:   "acc-coalesce",
		Name:        "weaver",
		Job:         "0",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "31",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	// 与线上同一条启动路径：尾随刷新依赖 gameplay 定时器队列，队列没 Start 就不会触发。
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	service.startGameplayTimeQueue(runCtx)
	session := &gameSession{
		conn:                            conn,
		connID:                          "avatar-coalesce",
		channel:                         channelcatalog.Channel{ID: 42, Type: 1, Name: "ch.42", Port: 10042},
		accountID:                       "acc-coalesce",
		selectedCharacterID:             31,
		townActorOwnerChannel:           42,
		connectionTownActorOwnerChannel: 42,
	}
	service.bindGameSessionCharacter(session, 31)
	session.townMu.Lock()
	session.townSceneReadyCharacterID = 31
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   31,
		PositionValid: true,
		PositionX:     450,
		PositionY:     234,
	}
	session.townMu.Unlock()
	return service, session, conn
}
