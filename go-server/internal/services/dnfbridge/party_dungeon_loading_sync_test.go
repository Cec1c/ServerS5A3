package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是「组队进图读条同步」的回归夹具，钉住两条厂商口径：
//
//	① `all-members-through-op28-before-any-op800-op29`（进场屏障）
//	   —— 任何成员的 op29 之前，**所有**成员都必须已经收到 op28；每个成员的 op29
//	   之前各收一条 op800 场景 actor 快照。
//	② `party_dungeon_shared_loading_barrier`（全员读条屏障）
//	   —— 只有**最后一名**成员上报 op37 之后，两个人才一起拿到落位（op120/op3）与
//	   op37 主状态。
//
// 两条屏障都带 `DNFBRIDGE_PARTY_DUNGEON_*` 开关，`legacy` 档必须逐字节回到改动前
// （见 TestPartyDungeonLoadingSyncLegacyEnvRestoresPreFixPacketOrder）。

// partyDungeonSyncChunk 是某个会话一次 conn.Write 的原始字节（= 一个完整上行包）。
type partyDungeonSyncChunk struct {
	CharID uint16
	Data   []byte
}

// partyDungeonSyncTrace 按**跨会话的全局先后**记录两个会话实际发出的包。
// 这是"谁先收到可以进图的信号"的唯一可观察判据，所以必须全局有序而不是按连接分开。
type partyDungeonSyncTrace struct {
	mu     sync.Mutex
	chunks []partyDungeonSyncChunk
}

func (trace *partyDungeonSyncTrace) record(charID uint16, data []byte) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.chunks = append(trace.chunks, partyDungeonSyncChunk{
		CharID: charID,
		Data:   append([]byte(nil), data...),
	})
	trace.mu.Unlock()
}

func (trace *partyDungeonSyncTrace) snapshot() []partyDungeonSyncChunk {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]partyDungeonSyncChunk(nil), trace.chunks...)
}

// partyDungeonSyncStep 是解出来的"某会话发出的某个上行包"。
type partyDungeonSyncStep struct {
	CharID         uint16
	Classification byte
	MsgID          uint16
	Body           []byte
}

// orderedSteps 把原始 chunk 解成全局有序的包序（每个 chunk = 一个完整上行包）。
func (trace *partyDungeonSyncTrace) orderedSteps(t *testing.T) []partyDungeonSyncStep {
	t.Helper()
	chunks := trace.snapshot()
	steps := make([]partyDungeonSyncStep, 0, len(chunks))
	for _, chunk := range chunks {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, chunk.Data)
		if len(rest) != 0 {
			t.Fatalf("char %d wrote more than one frame in a single write: %x (rest %x)",
				chunk.CharID, chunk.Data, rest)
		}
		steps = append(steps, partyDungeonSyncStep{
			CharID:         chunk.CharID,
			Classification: packet.Header.Classification,
			MsgID:          packet.Header.MsgID,
			Body:           append([]byte(nil), packet.Body...),
		})
	}
	return steps
}

// firstIndex 返回第一个满足条件的包在全局序里的下标（找不到 = -1）。
func firstPartyDungeonSyncStep(
	steps []partyDungeonSyncStep,
	charID uint16,
	msgID uint16,
	predicate func(partyDungeonSyncStep) bool,
) int {
	for index, step := range steps {
		if step.CharID != charID || step.MsgID != msgID {
			continue
		}
		if predicate == nil || predicate(step) {
			return index
		}
	}
	return -1
}

// partyDungeonSyncConn 是"会记全局包序"的连接（其余行为与 bufferConn 相同）。
type partyDungeonSyncConn struct {
	mu     sync.Mutex
	charID uint16
	trace  *partyDungeonSyncTrace
	raw    bytes.Buffer
}

func (c *partyDungeonSyncConn) Read(_ []byte) (int, error) { return 0, io.EOF }

func (c *partyDungeonSyncConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	c.raw.Write(data)
	c.mu.Unlock()
	c.trace.record(c.charID, data)
	return len(data), nil
}

func (c *partyDungeonSyncConn) Close() error                       { return nil }
func (c *partyDungeonSyncConn) LocalAddr() net.Addr                { return fakeAddr("local") }
func (c *partyDungeonSyncConn) RemoteAddr() net.Addr               { return fakeAddr("remote") }
func (c *partyDungeonSyncConn) SetDeadline(_ time.Time) error      { return nil }
func (c *partyDungeonSyncConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *partyDungeonSyncConn) SetWriteDeadline(_ time.Time) error { return nil }

func (c *partyDungeonSyncConn) frames() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.raw.Bytes()...)
}

// partyDungeonSyncFixture 造"两人同队 + 两台会记包序的会话"的完整进图夹具
// （PVF/怪物表与 dungeon_entry_packets_test.go 的组队用例同源）。
func partyDungeonSyncFixture(t *testing.T) (
	*Service,
	*gameSession,
	*gameSession,
	*partyDungeonSyncConn,
	*partyDungeonSyncConn,
	*partyDungeonSyncTrace,
) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[limit party count]\n1\n", "[limit party count]\n4\n", 1)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   "account-1",
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
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
	leaderConn := &partyDungeonSyncConn{charID: 99, trace: trace}
	followerConn := &partyDungeonSyncConn{charID: 100, trace: trace}
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	leader := &gameSession{conn: leaderConn, channel: channel, residentChannel: channel, selectedCharacterID: 99}
	follower := &gameSession{conn: followerConn, channel: channel, residentChannel: channel, selectedCharacterID: 100}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := runtimePartyStateForSyncFixture()
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 99, TownID: 38, AreaID: 1, Session: leader})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 100, TownID: 38, AreaID: 1, Session: follower})
	bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, service, follower, 38, 1, 450, 234)
	return service, leader, follower, leaderConn, followerConn, trace
}

func runtimePartyStateForSyncFixture() alignedcmd.PartyState {
	return alignedcmd.PartyState{
		PartyID: 99,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
}

// runPartyDungeonSyncEntry 走一次正常的组队 op16 进图。
func runPartyDungeonSyncEntry(t *testing.T, service *Service, leader *gameSession) {
	t.Helper()
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], 700)
	if err := service.handleDungeonSelectUpper(leader, body); err != nil {
		t.Fatal(err)
	}
}

// op800 的厂商夹具（`D:\DNF90-test\server\runtime\logs\packet_log.txt` :5389/:5393）。
const partyDungeonSyncVendorActorSceneSnapshotHex = dungeonEntryBarrierVendorBodyHex

// TestPartyDungeonEntryActorSceneSnapshotMatchesVendorFixture 钉住 op800 barrier 释放包
// 的逐字节形状（厂商两条 op800 body 完全相同）。
func TestPartyDungeonEntryActorSceneSnapshotMatchesVendorFixture(t *testing.T) {
	want, err := hex.DecodeString(partyDungeonSyncVendorActorSceneSnapshotHex)
	if err != nil {
		t.Fatal(err)
	}
	got := buildDungeonEntryActorSceneSnapshotBody([]uint16{1, 2})
	if !bytes.Equal(got, want) {
		t.Fatalf("dungeon op800 body=% X want vendor % X", got, want)
	}
	// reader 形状：u8 count + count * { u16 actor_key, u8 child_count }。
	if got[0] != 2 || binary.LittleEndian.Uint16(got[1:3]) != 1 || got[3] != 0 ||
		binary.LittleEndian.Uint16(got[4:6]) != 2 || got[6] != 0 {
		t.Fatalf("dungeon op800 body layout=% X", got)
	}
	// 空队不发（避免造出厂商没有样本的 count=0 形状）。
	if body := buildDungeonEntryActorSceneSnapshotBody(nil); len(body) != 1 || body[0] != 0 {
		t.Fatalf("empty dungeon op800 body=% X", body)
	}
}

// TestPartyDungeonEntryBarrierHoldsEveryMemberOp29UntilAllMembersReachedOp28 是本轮
// 缺陷 A 的主回归：**任何**成员的 op29 之前，所有成员都必须已经收到 op28；
// 且每个成员的 op29 之前各有一条 op800。
func TestPartyDungeonEntryBarrierHoldsEveryMemberOp29UntilAllMembersReachedOp28(t *testing.T) {
	service, leader, _, _, _, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)

	steps := trace.orderedSteps(t)
	leaderOp28 := firstPartyDungeonSyncStep(steps, 99, currentDungeonInfoNotification, nil)
	followerOp28 := firstPartyDungeonSyncStep(steps, 100, currentDungeonInfoNotification, nil)
	leaderOp800 := firstPartyDungeonSyncStep(steps, 99, currentDungeonEntryActorSceneSnapshotMsgID, nil)
	followerOp800 := firstPartyDungeonSyncStep(steps, 100, currentDungeonEntryActorSceneSnapshotMsgID, nil)
	leaderOp29 := firstPartyDungeonSyncStep(steps, 99, currentDungeonStartNotification, nil)
	followerOp29 := firstPartyDungeonSyncStep(steps, 100, currentDungeonStartNotification, nil)
	for name, index := range map[string]int{
		"leader_op28":    leaderOp28,
		"follower_op28":  followerOp28,
		"leader_op800":   leaderOp800,
		"follower_op800": followerOp800,
		"leader_op29":    leaderOp29,
		"follower_op29":  followerOp29,
	} {
		if index < 0 {
			t.Fatalf("missing %s in the party dungeon entry stream", name)
		}
	}
	lastOp28 := leaderOp28
	if followerOp28 > lastOp28 {
		lastOp28 = followerOp28
	}
	firstOp800 := leaderOp800
	if followerOp800 < firstOp800 {
		firstOp800 = followerOp800
	}
	firstOp29 := leaderOp29
	if followerOp29 < firstOp29 {
		firstOp29 = followerOp29
	}
	if lastOp28 > firstOp800 || lastOp28 > firstOp29 {
		t.Fatalf("all-members-through-op28-before-any-op800-op29 violated: "+
			"leader_op28=%d follower_op28=%d first_op800=%d first_op29=%d",
			leaderOp28, followerOp28, firstOp800, firstOp29)
	}
	for _, member := range []struct {
		name  string
		op800 int
		op29  int
		op28  int
	}{
		{"leader", leaderOp800, leaderOp29, leaderOp28},
		{"follower", followerOp800, followerOp29, followerOp28},
	} {
		if member.op28 > member.op800 || member.op800 > member.op29 {
			t.Fatalf("%s per-member order must be op28 → op800 → op29, got %d/%d/%d",
				member.name, member.op28, member.op800, member.op29)
		}
	}
	// op800 的 body 必须是"本房间这一队人的 actor key 全列一遍"的 reader 形状
	// （厂商夹具 [1,2] ⇒ `02 01 00 00 02 00 00`，见
	// TestPartyDungeonEntryActorSceneSnapshotMatchesVendorFixture）；
	// 本夹具的 char id 是 99/100，所以 object key 就是 99/100，两个收包者相同。
	wantBody := buildDungeonEntryActorSceneSnapshotBody([]uint16{
		currentSceneActorObjectKey(99),
		currentSceneActorObjectKey(100),
	})
	for _, step := range steps {
		if step.MsgID != currentDungeonEntryActorSceneSnapshotMsgID {
			continue
		}
		if !bytes.Equal(step.Body, wantBody) {
			t.Fatalf("char %d op800 body=% X want % X", step.CharID, step.Body, wantBody)
		}
		if step.Body[0] != 2 ||
			binary.LittleEndian.Uint16(step.Body[1:3]) != 99 || step.Body[3] != 0 ||
			binary.LittleEndian.Uint16(step.Body[4:6]) != 100 || step.Body[6] != 0 {
			t.Fatalf("char %d op800 body layout=% X want count=2 keys=[99 100] child=0",
				step.CharID, step.Body)
		}
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-entry-member-staged-at-op28",
		"game-party-dungeon-entry-barrier-released",
		"packet_order=" + dungeonEntryBarrierVendorPacketOrder,
		"game-party-dungeon-entry-actor-scene-snapshot-send",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("entry barrier log missing %q:\n%s", want, log)
		}
	}
}

// TestPartyDungeonSharedLoadingBarrierReleasesBothMembersAfterTheLastOp37 是本轮缺陷 B
// 的主回归：先到的那名成员**不会**独自进图 —— 落位（op120）与 op37 主状态都要等
// 最后一名成员上报 op37 才一起下发。
func TestPartyDungeonSharedLoadingBarrierReleasesBothMembersAfterTheLastOp37(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()

	service, leader, follower, leaderConn, followerConn, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)
	if !service.partyDungeonPostStartMapPlacementPending(leader) ||
		!service.partyDungeonPostStartMapPlacementPending(follower) {
		t.Fatal("dungeon entry must defer both members' post-start-map placement")
	}
	// 进图本身还没落位：两个人都不该已经收到 op120（msg_id=120 / body_len=2）。
	before := trace.orderedSteps(t)
	for _, step := range before {
		if step.MsgID == uint16(dnfenum.CmdPacketRequestBlacklist) && len(step.Body) == 2 {
			t.Fatalf("char %d received the post-start-map op120 before the shared loading barrier released",
				step.CharID)
		}
	}

	// 只到一名成员：屏障不放行，两个人都不该拿到 op37 主状态。
	if err := service.sendFinishLoadingStatus(leader, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatal(err)
	}
	afterFirst := trace.orderedSteps(t)
	if index := firstPartyDungeonSyncStep(afterFirst, 99, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
		return step.Classification == 0
	}); index >= 0 {
		t.Fatal("the first member received its op37 main state before the second member reported op37")
	}
	if index := firstPartyDungeonSyncStep(afterFirst, 100, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
		return step.Classification == 0
	}); index >= 0 {
		t.Fatal("the peer received its op37 main state while its own op37 request had not arrived yet")
	}
	// op37 的 ACK（classification=1）必须**立刻**回，不能也被压住。
	if index := firstPartyDungeonSyncStep(afterFirst, 99, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
		return step.Classification == 1
	}); index < 0 {
		t.Fatal("the shared loading barrier must keep answering the op37 status ACK immediately")
	}

	// 最后一名成员到齐：两个人一起拿到落位 + op37 主状态。
	if err := service.sendFinishLoadingStatus(follower, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatal(err)
	}
	afterSecond := trace.orderedSteps(t)
	leaderOp37 := firstPartyDungeonSyncStep(afterSecond, 99, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
		return step.Classification == 0
	})
	followerOp37 := firstPartyDungeonSyncStep(afterSecond, 100, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
		return step.Classification == 0
	})
	if leaderOp37 < 0 || followerOp37 < 0 {
		t.Fatalf("both members must receive their op37 main state after the last op37, got leader=%d follower=%d",
			leaderOp37, followerOp37)
	}
	for _, pair := range []struct {
		name   string
		charID uint16
		conn   *partyDungeonSyncConn
		op37   int
	}{
		{"leader", 99, leaderConn, leaderOp37},
		{"follower", 100, followerConn, followerOp37},
	} {
		op120 := firstPartyDungeonSyncStep(afterSecond, pair.charID, uint16(dnfenum.CmdPacketRequestBlacklist), func(step partyDungeonSyncStep) bool {
			return len(step.Body) == 2
		})
		if op120 < 0 {
			t.Fatalf("%s never received the deferred post-start-map op120", pair.name)
		}
		if op120 > pair.op37 {
			t.Fatalf("%s order must be op120 → op37 (vendor release order), got op120=%d op37=%d",
				pair.name, op120, pair.op37)
		}
		if len(pair.conn.frames()) == 0 {
			t.Fatalf("%s received nothing", pair.name)
		}
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-post-start-map-placement-deferred",
		"game-party-dungeon-loading-ready",
		"release=false",
		"release=true",
		"game-party-dungeon-loading-released",
		"packet_order=op120-each-stable-slot-op3-all-op37-op30-op19-op14",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("shared loading barrier log missing %q:\n%s", want, log)
		}
	}
	if got := strings.Count(log, "game-party-dungeon-loading-released"); got != 2 {
		t.Fatalf("loading-released events=%d want=2 (one per member)", got)
	}
}

// TestPartyDungeonSharedLoadingBarrierTimesOutAndStillReleasesThePlacement 是"失败放行"
// 回归：谁都没发 op37 时，落位必须在超时后被放行，绝不能永久卡在读条界面。
func TestPartyDungeonSharedLoadingBarrierTimesOutAndStillReleasesThePlacement(t *testing.T) {
	restoreTimeout := partyDungeonLoadingBarrierTimeout
	partyDungeonLoadingBarrierTimeout = 20 * time.Millisecond
	defer func() { partyDungeonLoadingBarrierTimeout = restoreTimeout }()

	service, leader, _, leaderConn, followerConn, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)

	deadline := time.Now().Add(5 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		log = testPacketLogText(t, service)
		// Each release callback logs after writing its placement. Observing both
		// packets does not mean either asynchronous callback has finished yet.
		if partyDungeonSyncConnHasPlacement(t, leaderConn) && partyDungeonSyncConnHasPlacement(t, followerConn) &&
			strings.Count(log, "kind=game-party-dungeon-loading-released ") == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for name, conn := range map[string]*partyDungeonSyncConn{"leader": leaderConn, "follower": followerConn} {
		if !partyDungeonSyncConnHasPlacement(t, conn) {
			t.Fatalf("%s placement was never released after the barrier timeout", name)
		}
	}
	if got := strings.Count(log, "kind=game-party-dungeon-loading-released "); got != 2 {
		t.Fatalf("timeout release events=%d want=2 (one completed callback per member):\n%s", got, log)
	}
}

func partyDungeonSyncConnHasPlacement(t *testing.T, conn *partyDungeonSyncConn) bool {
	t.Helper()
	stream := conn.frames()
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketRequestBlacklist) && len(packet.Body) == 2 {
			return true
		}
		stream = rest
	}
	return false
}

// TestPartyDungeonLoadingSyncLegacyEnvRestoresPreFixPacketOrder 是"一键回退"的回归：
// 两条开关都置 legacy 时，包序必须回到改动前 ——
//   - 一条 op800 都不发；
//   - 队长的 op29 出现在队员的 op28 **之前**（原来的串行 fan-out）；
//   - 落位不等人（没有任何 op37 就已经发出）。
func TestPartyDungeonLoadingSyncLegacyEnvRestoresPreFixPacketOrder(t *testing.T) {
	if got := readPartyDungeonEntryBarrierMode("legacy"); got != partyDungeonEntryBarrierLegacy {
		t.Fatalf("legacy entry barrier mode=%v", got)
	}
	if got := readPartyDungeonLoadingBarrierMode(" LEGACY "); got != partyDungeonLoadingBarrierLegacy {
		t.Fatalf("legacy loading barrier mode=%v", got)
	}
	if readPartyDungeonEntryBarrierMode("").String() != partyDungeonEntryBarrierValueVendor ||
		readPartyDungeonLoadingBarrierMode("junk").String() != partyDungeonLoadingBarrierValueVendor {
		t.Fatal("empty/junk env must stay on the vendor default")
	}
	var zero Service
	var nilService *Service
	if zero.partyDungeonEntryBarrier() != partyDungeonEntryBarrierVendor ||
		nilService.partyDungeonEntryBarrier() != partyDungeonEntryBarrierVendor ||
		zero.partyDungeonLoadingBarrier() != partyDungeonLoadingBarrierVendor ||
		nilService.partyDungeonLoadingBarrier() != partyDungeonLoadingBarrierVendor {
		t.Fatal("zero-value/nil service must default to the vendor barrier modes")
	}

	service, leader, follower, leaderConn, followerConn, trace := partyDungeonSyncFixture(t)
	service.options.partyDungeonEntryBarrier = partyDungeonEntryBarrierLegacy
	service.options.partyDungeonLoadingBarrier = partyDungeonLoadingBarrierLegacy
	runPartyDungeonSyncEntry(t, service, leader)

	steps := trace.orderedSteps(t)
	leaderOp29 := firstPartyDungeonSyncStep(steps, 99, currentDungeonStartNotification, nil)
	followerOp28 := firstPartyDungeonSyncStep(steps, 100, currentDungeonInfoNotification, nil)
	followerOp29 := firstPartyDungeonSyncStep(steps, 100, currentDungeonStartNotification, nil)
	if leaderOp29 < 0 || followerOp28 < 0 || followerOp29 < 0 {
		t.Fatal("legacy run is missing op28/op29 for one of the members")
	}
	if leaderOp29 > followerOp28 {
		t.Fatalf("legacy mode must keep the pre-fix serial fan-out (leader op29 before follower op28): leader_op29=%d follower_op28=%d",
			leaderOp29, followerOp28)
	}
	for _, step := range steps {
		if step.MsgID == currentDungeonEntryActorSceneSnapshotMsgID {
			t.Fatalf("legacy mode must not send the op800 barrier release: char %d body=% X", step.CharID, step.Body)
		}
	}
	if !partyDungeonSyncConnHasPlacement(t, leaderConn) || !partyDungeonSyncConnHasPlacement(t, followerConn) {
		t.Fatal("legacy mode must send the post-start-map placement without waiting for op37")
	}
	if service.partyDungeonPostStartMapPlacementPending(leader) ||
		service.partyDungeonPostStartMapPlacementPending(follower) {
		t.Fatal("legacy mode must not leave a deferred placement behind")
	}
}
