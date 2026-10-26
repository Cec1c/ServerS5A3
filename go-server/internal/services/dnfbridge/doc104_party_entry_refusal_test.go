package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/eventloop"
)

// TestDoc104RoutedTokenDiscardsOnlyItsOwnRequest 钉住令牌的资格判定：一次路由请求一个令牌
// （角色号 + generation + seq 三者都匹配），只结清"它自己那一次"。队员换角、或又按了一次进图
// 留下的新挂起态，都必须留着给它自己的流程处理。
func TestDoc104RoutedTokenDiscardsOnlyItsOwnRequest(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	session := fixture.follower

	// 正控：自己的令牌结清自己的挂起态，且只能消费一次。
	token := fixture.service.bindPartyDungeonEntryRoutedPending(session)
	if token == nil {
		t.Fatal("bind returned no token")
	}
	if !fixture.service.discardPartyDungeonEntryRoutedRequest(token) {
		t.Fatal("discard refused its own token")
	}
	if pending := doc104PendingCharacterForTest(session); pending != 0 {
		t.Fatalf("pending=%d want 0 (own request must be discarded)", pending)
	}
	if fixture.service.discardPartyDungeonEntryRoutedRequest(token) {
		t.Fatal("the same token was consumed twice")
	}

	// 换角（真实换角入口）之后旧令牌不再匹配。
	stale := fixture.service.bindPartyDungeonEntryRoutedPending(session)
	if stale == nil {
		t.Fatal("bind returned no token for the second request")
	}
	fixture.service.bindGameSessionCharacter(session, 101)
	if fixture.service.discardPartyDungeonEntryRoutedRequest(stale) {
		t.Fatal("an old token consumed a request after the character switched away")
	}
	if pending := doc104PendingCharacterForTest(session); pending != 100 {
		t.Fatalf("pending=%d want 100 (the switched-away request must survive)", pending)
	}

	// 换回来之后是**另一次**请求：旧令牌仍然不能动它。
	fixture.service.bindGameSessionCharacter(session, 100)
	fresh := fixture.service.bindPartyDungeonEntryRoutedPending(session)
	if fresh == nil {
		t.Fatal("bind returned no token after switching back")
	}
	if fixture.service.discardPartyDungeonEntryRoutedRequest(stale) {
		t.Fatal("an old token consumed a newer request")
	}
	if pending := doc104PendingCharacterForTest(session); pending != 100 {
		t.Fatalf("pending=%d want 100 (the newer request must survive the old token)", pending)
	}
	if !fixture.service.discardPartyDungeonEntryRoutedRequest(fresh) {
		t.Fatal("the newest token could not discard its own request")
	}
}

// TestDoc104OlderRoutedRefusalDoesNotConsumeANewerRequest 钉住"旧拒绝不吞新请求"：同角色
// 连续按两次进图（两次绑定）时，旧那条流程拿到的令牌在拒绝时必须一包不发，把新请求的挂起态
// 留给**新请求自己那条流程**应答（一个 op16 一包结论）。
func TestDoc104OlderRoutedRefusalDoesNotConsumeANewerRequest(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	state := runtimePartyStateSnapshot(fixture.leader)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(doc104SelectBody())
	if err != nil {
		t.Fatal(err)
	}
	answer := func(token *partyDungeonEntryRoutedRequest) bool {
		return fixture.service.answerRoutedPartyEntryOwnerOnRefusal(
			context.Background(), fixture.leader, state, token, request,
			"doc104_repeated_request_probe", "entry_level_too_low",
			currentSelectDungeonEntryLevelTooLowReason, dnfdungeon.ErrEntryLevelTooLow)
	}
	older := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	newer := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if older == nil || newer == nil {
		t.Fatal("bind returned no token")
	}

	// 旧那条流程现在才给出拒绝：既不能回包，也不能吞掉新请求的挂起态。
	if answer(older) {
		t.Fatal("the older request was still answered after a newer request was bound")
	}
	if steps := doc104Op16Steps(doc104StepsFor(t, fixture, 100)); len(steps) != 0 {
		t.Fatalf("the older flow answered a newer request: %+v", steps)
	}
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 100 {
		t.Fatalf("pending=%d want 100 (the newer request must survive)", pending)
	}

	// 新请求由它自己的流程应答：恰好一包，随后挂起态清空。
	if !answer(newer) {
		t.Fatal("the newer request was not answered by its own flow")
	}
	doc104AssertRefusalFrames(t, 100, doc104Op16Steps(doc104StepsFor(t, fixture, 100)),
		currentSelectDungeonEntryLevelTooLowReason)
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 0 {
		t.Fatalf("pending=%d want 0 after its own request was answered", pending)
	}
}

// TestDoc104QueuedRefusalDoesNotReachASwitchedCharacter 钉住"投递期间换角"这条竞态：
// 解挂那条包是投递到**队员自己的事件循环**上执行的，循环排队期间队员可能已经换角。
// 资格判定与 take 必须落在 callback 里，否则拒绝包会发给换角之后的新角色、并把不属于
// 这条流程的挂起态吃掉。
//
// 夹具做法：先占住队员的事件循环，把解挂事件排在它后面，然后在事件真正执行之前换角，
// 最后放行 holder。（换角只改"权威事实"这一对字段——角色号与 generation——不调用完整换角
// 入口，因为那会在同一条被占住的循环上投递离队广播。）
func TestDoc104QueuedRefusalDoesNotReachASwitchedCharacter(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	state := runtimePartyStateSnapshot(fixture.leader)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(doc104SelectBody())
	if err != nil {
		t.Fatal(err)
	}

	// 队员那条 op16 已经被回 ACK：绑定并拿到这次请求的令牌。
	token := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if token == nil {
		t.Fatal("bind returned no token")
	}
	if err := fixture.service.startGameSessionEvents(context.Background(), fixture.follower); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = fixture.service.stopGameSessionEvents(ctx, fixture.follower)
	})
	loop := fixture.follower.events.loop

	// 占住事件循环：解挂那条包会排在 hold 之后。
	holdEntered := make(chan struct{})
	releaseHold := make(chan struct{})
	go func() {
		_ = fixture.service.callGameSession(context.Background(), fixture.follower, "doc104-test-hold-event-loop", func() error {
			close(holdEntered)
			<-releaseHold
			return nil
		})
	}()
	select {
	case <-holdEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the follower event loop never entered the holder task")
	}

	// 投递解挂，并等它真的排进队列（Submitted 计数器前进 = 事件已入队）。
	submitted := loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
	answered := make(chan bool, 1)
	go func() {
		answered <- fixture.service.answerRoutedPartyEntryOwnerOnRefusal(
			context.Background(), fixture.leader, state, token, request, "doc104_queued_character_switch",
			"entry_level_too_low", currentSelectDungeonEntryLevelTooLowReason, dnfdungeon.ErrEntryLevelTooLow)
	}()
	waitDoc104(t, "the refusal event to be queued on the follower event loop", func() bool {
		return loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] > submitted
	})

	// 排队期间换角。
	func() {
		fixture.follower.dungeon.mu.Lock()
		defer fixture.follower.dungeon.mu.Unlock()
		fixture.follower.selectedCharacterID = 101
		fixture.follower.characterGeneration++
	}()

	close(releaseHold)
	select {
	case delivered := <-answered:
		if delivered {
			t.Fatal("the queued refusal reported a delivered packet after the character switch")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued refusal never returned")
	}
	if steps := doc104Op16Steps(doc104StepsFor(t, fixture, 100)); len(steps) != 0 {
		t.Fatalf("queued refusal reached the switched-away character: %+v", steps)
	}
	// 取令牌在 callback 内、发送之前：身份已经变了 ⇒ 令牌取不到，挂起态原样留着（它属于换角
	// 之前那次请求），也不会发给新角色。
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 100 {
		t.Fatalf("pending=%d want 100 (the switched-away request must survive)", pending)
	}
}

// 本文件是文档段 104「组队时，如果选择的的地图队伍里某个队员没有解锁，会导致返回城镇的
// 选项直接消失，要等倒计时才能自动退出」的回归：
//
//   · 整队进图在**提交之前**被拒绝时，还在等结果的每条会话都必须收到既有形状的拒绝包
//     `{0, reason, 0}`（msg id = op16，普通频道分类），队长那条与队员那条同形；
//   · 队员自己按过进图（它那条 op16 已经被回 ACK）时，这条拒绝包是**迟到**的：必须先有
//     ACK、再有拒绝，且同一个 op16 只回一包（重复应答不成立）；
//   · 换角之后不得把这条拒绝包发给新角色；
//   · 解锁门禁不被绕过：拒绝仍然发生在任何 runtime/场景提交之前，两名会话都没有进图
//     序列（op28/op800/op29）、没有活动 runtime。
//
// 解锁门禁本身由既有用例钉住（`dungeon_select_refusal_test.go` 的等级/疲劳/队伍/索引四档；
// 组队成功进图那条由 `TestPartyDungeonEntryBarrierHoldsEveryMemberOp29UntilAllMembersReachedOp28`
// 钉住）——本文件只钉"拒绝之后要回包、要解挂"这半步，不改门禁判据。

// doc104PartyFixture 造"队长 99 + 队员 100 同队同区域"的两人进图夹具。
// followLevel=9 时 700 这张图（`[minimum required level] 10`）对队员是未解锁的。
// leaderHasTownOrigin=false 时队长没有可用的返回城镇原点（= 提交前的晚期拒绝）。
type doc104PartyFixture struct {
	service  *Service
	leader   *gameSession
	follower *gameSession
	trace    *partyDungeonSyncTrace
}

func newDoc104PartyFixture(t *testing.T, followerLevel int, leaderHasTownOrigin bool) *doc104PartyFixture {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"], "[limit party count]\n1\n", "[limit party count]\n4\n", 1)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	townStats := map[string]int64{
		"fatigue": 100, "town_id": 38, "area_id": 1,
		"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
	}
	leaderStats := townStats
	if !leaderHasTownOrigin {
		// 与 `TestHandleDungeonSelectUpperAnswersTownReturnOriginUnavailable` 同形：
		// 会话没有 op35 快照、角色行也没有持久化落点 ⇒ 返回城镇原点不可用。
		leaderStats = map[string]int64{"fatigue": 100}
	}
	for characterID, level := range map[string]int{"99": 20, "100": followerLevel} {
		stats := townStats
		if characterID == "99" {
			stats = leaderStats
		}
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   "account-1",
			Level:       level,
			Stats:       stats,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: characterID, Slots: map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	trace := &partyDungeonSyncTrace{}
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	leader := &gameSession{conn: &partyDungeonSyncConn{charID: 99, trace: trace}, channel: channel, residentChannel: channel}
	follower := &gameSession{conn: &partyDungeonSyncConn{charID: 100, trace: trace}, channel: channel, residentChannel: channel}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := runtimePartyStateForSyncFixture()
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 99, TownID: 38, AreaID: 1, Session: leader})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 100, TownID: 38, AreaID: 1, Session: follower})
	if leaderHasTownOrigin {
		bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
	}
	bindDungeonSelectorOriginForTestAt(t, service, follower, 38, 1, 450, 234)
	return &doc104PartyFixture{service: service, leader: leader, follower: follower, trace: trace}
}

func doc104SelectBody() []byte {
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], 700)
	return body
}

// pressEnter 让某条会话发一次 op16。队员那次会走"路由给队长"分支（异步），队长那次
// 直接跑整队进图流程。
func (fixture *doc104PartyFixture) pressEnter(t *testing.T, session *gameSession) {
	t.Helper()
	if err := fixture.service.handleDungeonSelectUpper(session, doc104SelectBody()); err != nil {
		t.Fatalf("op16 select dungeon: %v", err)
	}
}

func waitDoc104(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// doc104StepsFor 返回某条会话实际发出的所有上行帧（跨会话全局有序的解码结果）。
func doc104StepsFor(t *testing.T, fixture *doc104PartyFixture, charID uint16) []partyDungeonSyncStep {
	t.Helper()
	var steps []partyDungeonSyncStep
	for _, step := range fixture.trace.orderedSteps(t) {
		if step.CharID == charID {
			steps = append(steps, step)
		}
	}
	return steps
}

// doc104Op16Steps 只留 op16 那一族帧（ACK 与拒绝都是 op16）。
func doc104Op16Steps(steps []partyDungeonSyncStep) []partyDungeonSyncStep {
	var op16 []partyDungeonSyncStep
	for _, step := range steps {
		if step.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) {
			op16 = append(op16, step)
		}
	}
	return op16
}

// doc104PendingCharacterForTest 读挂起态里的角色号（不清）。
func doc104PendingCharacterForTest(session *gameSession) uint16 {
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return session.partyDungeonEntryRoutedPendingCharacter
}

// doc104AssertNoEntrySequence 钉住"解锁门禁没有被绕过"：两条会话上都不允许出现任何
// 进图序列的帧（op28 地牢信息 / op800 场景快照 / op29 start-map），也不允许有活动 runtime。
func doc104AssertNoEntrySequence(t *testing.T, fixture *doc104PartyFixture) {
	t.Helper()
	for _, step := range fixture.trace.orderedSteps(t) {
		switch step.MsgID {
		case currentDungeonInfoNotification, currentDungeonEntryActorSceneSnapshotMsgID, currentDungeonStartNotification:
			t.Fatalf("refused party entry still emitted entry frame char=%d msg=%d body=% x",
				step.CharID, step.MsgID, step.Body)
		}
	}
	for name, session := range map[string]*gameSession{"leader": fixture.leader, "follower": fixture.follower} {
		if sessionHasActiveDungeonRuntime(session) {
			t.Fatalf("%s session has an active dungeon runtime after the refusal", name)
		}
	}
}

// doc104AssertRefusalFrames 钉住一组帧每一帧都是"op16 拒绝"：普通频道分类、msg id = op16、
// body 恰为 `{0, reason, 0}`。
func doc104AssertRefusalFrames(t *testing.T, charID uint16, steps []partyDungeonSyncStep, reason byte) {
	t.Helper()
	for index, step := range steps {
		if step.Classification != dnfproto.DefaultChannelClassification ||
			step.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
			t.Fatalf("char %d frame %d header class=%d msg=%d, want op16 refusal",
				charID, index, step.Classification, step.MsgID)
		}
		if !bytes.Equal(step.Body, []byte{0, reason, 0}) {
			t.Fatalf("char %d frame %d body=% x want {00 %02x 00}", charID, index, step.Body, reason)
		}
	}
}

// TestDoc104RoutedFollowerRefusalAnswersBothWaitingClients 是主回归：队员未解锁、
// **队员自己**按了进图（先拿到 op16 成功 ACK，再等到拒绝）时，队员与队长都拿到既有形状
// 的拒绝包，且没有任何进图提交。
func TestDoc104RoutedFollowerRefusalAnswersBothWaitingClients(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	fixture.pressEnter(t, fixture.follower)
	waitDoc104(t, "the routed leader entry flow to refuse", func() bool {
		return len(doc104StepsFor(t, fixture, 99)) >= 1
	})

	followerSteps := doc104StepsFor(t, fixture, 100)
	if len(followerSteps) != 2 {
		t.Fatalf("follower frames=%d want 2 (op16 ACK then refusal): %+v", len(followerSteps), followerSteps)
	}
	wantAck := upperSuccessBody(buildSelectDungeonBodyForCharacter(100))
	if followerSteps[0].Classification != dnfproto.DefaultChannelClassification ||
		followerSteps[0].MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(followerSteps[0].Body, wantAck) {
		t.Fatalf("follower first frame=%+v body=% x want op16 success ACK % x",
			followerSteps[0], followerSteps[0].Body, wantAck)
	}
	doc104AssertRefusalFrames(t, 100, followerSteps[1:], currentSelectDungeonEntryLevelTooLowReason)

	leaderSteps := doc104StepsFor(t, fixture, 99)
	if len(leaderSteps) != 1 {
		t.Fatalf("leader frames=%d want 1 (the routed flow answers the leader exactly once): %+v",
			len(leaderSteps), leaderSteps)
	}
	doc104AssertRefusalFrames(t, 99, leaderSteps, currentSelectDungeonEntryLevelTooLowReason)

	doc104AssertNoEntrySequence(t, fixture)
	// 挂起态已经被这次请求的令牌消费：同一条流程不会给同一个 op16 补第二包。
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 0 {
		t.Fatalf("follower routed pending survived the answer (char=%d)", pending)
	}
	// 解锁门禁一个字节没动：队员等级还是 9，进图从未提交。
	repositories, ok := fixture.service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	character, found, err := repositories.Character.Load(context.Background(), "100")
	if err != nil || !found || character.Level != 9 {
		t.Fatalf("follower character level=%d found=%t err=%v, want the locked level 9", character.Level, found, err)
	}
	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"reason=party_entry_preflight_failed_before_any_member_commit",
		"failure_class=entry_level_too_low",
		"failure_reason_byte=14",
		"released_awaiting_routed_entry=true",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("refusal log missing %q:\n%s", want, log)
		}
	}
	if !strings.Contains(log, "char_id=100") {
		t.Fatalf("refusal log does not name the awaiting follower:\n%s", log)
	}
}

// TestDoc104LeaderRefusalAnswersLeaderAndLeavesIdleFollowerSilent：队长自己按进图时，
// 队长拿到拒绝包；**没按过进图**的队员一包都不发（挂起态机制不会给无关成员补包）。
func TestDoc104LeaderRefusalAnswersLeaderAndLeavesIdleFollowerSilent(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	fixture.pressEnter(t, fixture.leader)

	leaderSteps := doc104StepsFor(t, fixture, 99)
	if len(leaderSteps) != 1 {
		t.Fatalf("leader frames=%d want 1 refusal: %+v", len(leaderSteps), leaderSteps)
	}
	doc104AssertRefusalFrames(t, 99, leaderSteps, currentSelectDungeonEntryLevelTooLowReason)
	if followerSteps := doc104StepsFor(t, fixture, 100); len(followerSteps) != 0 {
		t.Fatalf("idle follower received %d frames, want none: %+v", len(followerSteps), followerSteps)
	}
	doc104AssertNoEntrySequence(t, fixture)
	if log := testPacketLogText(t, fixture.service); strings.Contains(log, "released_awaiting_routed_entry=true") {
		t.Fatalf("no member was awaiting yet the release answered one:\n%s", log)
	}
}

// TestDoc104RepeatedEntryAttemptsAnswerOncePerRoutedOp16：同一个挂起态只回一包。
// 队员按两次 + 队长按一次 ⇒ 队员恰好两包（每次自己那条 op16 一包）、队长恰好三包
// （两次队长流程 + 一次路由流程各一包）。
func TestDoc104RepeatedEntryAttemptsAnswerOncePerRoutedOp16(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	fixture.pressEnter(t, fixture.follower)
	waitDoc104(t, "the first routed refusal", func() bool {
		return len(doc104StepsFor(t, fixture, 100)) >= 2
	})
	fixture.pressEnter(t, fixture.leader)
	waitDoc104(t, "the leader-triggered refusal", func() bool {
		return len(doc104StepsFor(t, fixture, 99)) >= 2
	})
	fixture.pressEnter(t, fixture.follower)
	waitDoc104(t, "the second routed refusal", func() bool {
		return len(doc104StepsFor(t, fixture, 100)) >= 4
	})

	followerSteps := doc104StepsFor(t, fixture, 100)
	if len(followerSteps) != 4 {
		t.Fatalf("follower frames=%d want 4 (ACK+refusal twice): %+v", len(followerSteps), followerSteps)
	}
	wantAck := upperSuccessBody(buildSelectDungeonBodyForCharacter(100))
	for _, index := range []int{0, 2} {
		if !bytes.Equal(followerSteps[index].Body, wantAck) {
			t.Fatalf("follower frame %d body=% x want the op16 success ACK % x", index, followerSteps[index].Body, wantAck)
		}
	}
	doc104AssertRefusalFrames(t, 100, []partyDungeonSyncStep{followerSteps[1], followerSteps[3]},
		currentSelectDungeonEntryLevelTooLowReason)
	leaderSteps := doc104StepsFor(t, fixture, 99)
	if len(leaderSteps) != 3 {
		t.Fatalf("leader frames=%d want 3 (routed + own + routed): %+v", len(leaderSteps), leaderSteps)
	}
	doc104AssertRefusalFrames(t, 99, leaderSteps, currentSelectDungeonEntryLevelTooLowReason)
	doc104AssertNoEntrySequence(t, fixture)
}

// TestDoc104CharacterSwitchDropsTheRoutedPendingAnswer 钉住换角口径：挂起态绑在
// "角色号 + characterGeneration"上，换角（甚至换回来）之后不得把这条拒绝包发给新角色。
func TestDoc104CharacterSwitchDropsTheRoutedPendingAnswer(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	state := runtimePartyStateSnapshot(fixture.leader)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(doc104SelectBody())
	if err != nil {
		t.Fatal(err)
	}
	answer := func(token *partyDungeonEntryRoutedRequest) bool {
		return fixture.service.answerRoutedPartyEntryOwnerOnRefusal(
			context.Background(), fixture.leader, state, token, request, "doc104_character_switch_probe",
			"entry_level_too_low", currentSelectDungeonEntryLevelTooLowReason, dnfdungeon.ErrEntryLevelTooLow)
	}

	// 正控：同一个角色、同一个 generation ⇒ 这条拒绝包一定补发。
	token := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if token == nil {
		t.Fatal("bind returned no token")
	}
	if !answer(token) {
		t.Fatal("same-character token was not answered")
	}
	doc104AssertRefusalFrames(t, 100, doc104Op16Steps(doc104StepsFor(t, fixture, 100)),
		currentSelectDungeonEntryLevelTooLowReason)

	// 换角（走真实换角入口：generation 前进一步、把旧角色从权威队伍里摘掉），再换回来：
	// 令牌属于换角之前那次请求，两个方向都不补包，也不动这条挂起态。
	stale := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if stale == nil {
		t.Fatal("bind returned no token for the second request")
	}
	fixture.service.bindGameSessionCharacter(fixture.follower, 101)
	if answer(stale) {
		t.Fatal("the refusal was delivered after the character switched away")
	}
	fixture.service.bindGameSessionCharacter(fixture.follower, 100)
	if answer(stale) {
		t.Fatal("the refusal was delivered after switching back to the character")
	}
	// 只有正控那一包：换角路上角色绑定本身也会下发城镇包（op9），所以这里只数 op16。
	if steps := doc104Op16Steps(doc104StepsFor(t, fixture, 100)); len(steps) != 1 {
		t.Fatalf("follower op16 frames=%d want 1 (only the positive control): %+v", len(steps), steps)
	}
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 100 {
		t.Fatalf("pending=%d want 100 (the switched-away request must survive)", pending)
	}
}

// TestDoc104LateEntryRefusalStillAnswersAwaitingFollower 覆盖"晚期错误"：队员自己是
// 解锁的（等级 20），队员那条进图准备**已经通过**，队长那条在提交之前因为会话状态取不到
// 返回城镇原点而拒绝 —— 队友仍然必须拿到同形状的拒绝包。
func TestDoc104LateEntryRefusalStillAnswersAwaitingFollower(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 20, false)
	fixture.pressEnter(t, fixture.follower)
	waitDoc104(t, "the routed leader entry flow to refuse late", func() bool {
		return len(doc104StepsFor(t, fixture, 100)) >= 2
	})

	followerSteps := doc104StepsFor(t, fixture, 100)
	// 队员这条路先收到自己那条 op16 的 ACK，再收到队长那条流程下发的 op15/op27 选择器页
	// （队员进图准备的一部分），最后才是这条"晚期"拒绝包；全程没有 op28/op800/op29。
	op16Steps := doc104Op16Steps(followerSteps)
	if len(op16Steps) != 2 {
		t.Fatalf("follower op16 frames=%d want 2 (ACK then late refusal): %+v", len(op16Steps), followerSteps)
	}
	wantAck := upperSuccessBody(buildSelectDungeonBodyForCharacter(100))
	if !bytes.Equal(op16Steps[0].Body, wantAck) {
		t.Fatalf("follower first op16 frame body=% x want the success ACK % x", op16Steps[0].Body, wantAck)
	}
	doc104AssertRefusalFrames(t, 100, op16Steps[1:], currentSelectDungeonInvalidSelectionStateReason)
	leaderSteps := doc104StepsFor(t, fixture, 99)
	if len(leaderSteps) != 1 {
		t.Fatalf("leader frames=%d want 1 late refusal: %+v", len(leaderSteps), leaderSteps)
	}
	doc104AssertRefusalFrames(t, 99, leaderSteps, currentSelectDungeonInvalidSelectionStateReason)
	doc104AssertNoEntrySequence(t, fixture)
	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"reason=ordinary_dungeon_town_return_origin_unavailable",
		"failure_reason_byte=19",
		"released_awaiting_routed_entry=true",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("late refusal log missing %q:\n%s", want, log)
		}
	}
}

// TestDoc104QueuedAnswerDoesNotDisturbANewerRequestFromTheSameCharacter 钉住"取令牌必须落在
// 队员事件循环的 callback 内、发送之前"：解挂事件还在队列里等待时，队员又按了一次进图
// （同角色、新 seq）—— 旧那次请求已经作废，回调必须一包不发，并把新请求的挂起态原样留给
// 它自己那条流程应答。
func TestDoc104QueuedAnswerDoesNotDisturbANewerRequestFromTheSameCharacter(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 9, true)
	state := runtimePartyStateSnapshot(fixture.leader)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(doc104SelectBody())
	if err != nil {
		t.Fatal(err)
	}
	older := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if older == nil {
		t.Fatal("bind returned no token")
	}
	if err := fixture.service.startGameSessionEvents(context.Background(), fixture.follower); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = fixture.service.stopGameSessionEvents(ctx, fixture.follower)
	})
	loop := fixture.follower.events.loop

	// 占住队员的事件循环，把"应答旧请求"排在它后面。
	holdEntered := make(chan struct{})
	releaseHold := make(chan struct{})
	go func() {
		_ = fixture.service.callGameSession(context.Background(), fixture.follower, "doc104-test-hold-event-loop", func() error {
			close(holdEntered)
			<-releaseHold
			return nil
		})
	}()
	select {
	case <-holdEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the follower event loop never entered the holder task")
	}
	submitted := loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
	answered := make(chan bool, 1)
	go func() {
		answered <- fixture.service.answerRoutedPartyEntryOwnerOnRefusal(
			context.Background(), fixture.leader, state, older, request, "doc104_queued_newer_request_probe",
			"entry_level_too_low", currentSelectDungeonEntryLevelTooLowReason, dnfdungeon.ErrEntryLevelTooLow)
	}()
	waitDoc104(t, "the older answer to be queued on the follower event loop", func() bool {
		return loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] > submitted
	})

	// 排队期间：同角色又按了一次进图（新令牌、新 seq）。
	newer := fixture.service.bindPartyDungeonEntryRoutedPending(fixture.follower)
	if newer == nil {
		t.Fatal("bind returned no token for the newer request")
	}
	close(releaseHold)
	select {
	case delivered := <-answered:
		if delivered {
			t.Fatal("the older answer was delivered although a newer request was bound")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued answer never returned")
	}
	if steps := doc104Op16Steps(doc104StepsFor(t, fixture, 100)); len(steps) != 0 {
		t.Fatalf("the older answer disturbed a newer request: %+v", steps)
	}
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 100 {
		t.Fatalf("pending=%d want 100 (the newer request must survive)", pending)
	}

	// 新请求由它自己的令牌应答：恰好一包。
	if !fixture.service.answerRoutedPartyEntryOwnerOnRefusal(
		context.Background(), fixture.leader, state, newer, request, "doc104_queued_newer_request_probe",
		"entry_level_too_low", currentSelectDungeonEntryLevelTooLowReason, dnfdungeon.ErrEntryLevelTooLow) {
		t.Fatal("the newer request was not answered by its own token")
	}
	doc104AssertRefusalFrames(t, 100, doc104Op16Steps(doc104StepsFor(t, fixture, 100)),
		currentSelectDungeonEntryLevelTooLowReason)
}

// TestDoc104RoutedFlowBackstopAnswersOnAnUnwiredExit 钉住 defer 兜底：队员路由过来的那次请求，
// 如果队长那条流程从一个**没有精确应答**的出口返回（这里用"副本不在 PVF" ⇒
// `prepareDungeonRuntime` 早期失败那一支），队员仍然必须拿到既有形状的拒绝包
// （厂商 default `DungeonUnavailable` = 0x01）。这样就不必依赖"每个 return 点都记得应答"。
func TestDoc104RoutedFlowBackstopAnswersOnAnUnwiredExit(t *testing.T) {
	fixture := newDoc104PartyFixture(t, 20, true)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], 701) // 701 不在合成 PVF 里
	if err := fixture.service.handleDungeonSelectUpper(fixture.follower, body); err != nil {
		t.Fatalf("follower op16: %v", err)
	}
	waitDoc104(t, "the backstop to answer the routed request", func() bool {
		return len(doc104StepsFor(t, fixture, 100)) >= 2
	})
	op16Steps := doc104Op16Steps(doc104StepsFor(t, fixture, 100))
	if len(op16Steps) != 2 {
		t.Fatalf("follower op16 frames=%d want 2 (ACK + backstop refusal): %+v", len(op16Steps), op16Steps)
	}
	wantAck := upperSuccessBody(buildSelectDungeonBodyForCharacter(100))
	if !bytes.Equal(op16Steps[0].Body, wantAck) {
		t.Fatalf("follower first op16 frame body=% x want the success ACK % x", op16Steps[0].Body, wantAck)
	}
	doc104AssertRefusalFrames(t, 100, op16Steps[1:], currentSelectDungeonUnavailableReason)
	doc104AssertNoEntrySequence(t, fixture)
	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"reason=party_entry_routed_flow_exited_without_answer",
		"released_awaiting_routed_entry=true",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("backstop refusal log missing %q:\n%s", want, log)
		}
	}
	if pending := doc104PendingCharacterForTest(fixture.follower); pending != 0 {
		t.Fatalf("pending=%d want 0 (the backstop consumed the routed request)", pending)
	}
}
