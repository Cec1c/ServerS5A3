package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type nativeNPCAssetsTrace struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	transactions, characterLoads, inventoryLoads, accountLoads, inventorySaves, accountSaves int
	failure                                                                                  string
}
type nativeNPCTraceInventory struct {
	dnfrepo.InventoryRepository
	trace *nativeNPCAssetsTrace
}

func (r nativeNPCTraceInventory) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	r.trace.inventoryLoads++
	return r.InventoryRepository.Load(ctx, id)
}
func (r nativeNPCTraceInventory) Save(ctx context.Context, record dnfrepo.InventoryRecord) error {
	r.trace.inventorySaves++
	if r.trace.failure == "背包" {
		return errors.New("注入背包保存失败")
	}
	return r.InventoryRepository.Save(ctx, record)
}

type nativeNPCTraceAccount struct {
	dnfrepo.AccountInventoryRepository
	trace *nativeNPCAssetsTrace
}

func (r nativeNPCTraceAccount) Load(ctx context.Context, id string) (dnfrepo.AccountInventoryRecord, bool, error) {
	r.trace.accountLoads++
	return r.AccountInventoryRepository.Load(ctx, id)
}
func (r nativeNPCTraceAccount) Save(ctx context.Context, record dnfrepo.AccountInventoryRecord) error {
	r.trace.accountSaves++
	if r.trace.failure == "共享仓库" {
		return errors.New("注入共享仓库保存失败")
	}
	return r.AccountInventoryRepository.Save(ctx, record)
}

type nativeNPCTraceCharacter struct {
	dnfrepo.CharacterRepository
	trace *nativeNPCAssetsTrace
}

func (r nativeNPCTraceCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.trace.characterLoads++
	return r.CharacterRepository.Load(ctx, id)
}
func (r *nativeNPCAssetsTrace) WithinAccountCharacterAssets(ctx context.Context, account, character string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	r.transactions++
	return r.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character,
		func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
			if err := apply(nativeNPCTraceAccount{a, r}, nativeNPCTraceCharacter{c, r}, nativeNPCTraceInventory{i, r}, e); err != nil {
				return err
			}
			if r.failure == "提交" {
				return errors.New("注入提交前失败")
			}
			return nil
		})
}

func nativeNPCBatchFixture(t *testing.T, count int) (*Service, *gameSession, dnfrepo.Group, *nativeNPCAssetsTrace, []dnfproto.LatestGameStreamPacket) {
	t.Helper()
	slots := make(map[string]dnfrepo.ItemStack)
	packets := make([]dnfproto.LatestGameStreamPacket, count)
	for i := range count {
		slot := int16(i + 10)
		slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 5000, Count: 1}
		packets[i] = nativeResultTestPacket(26, currentDisjointItemTestBody(slot, 0, -1, 317))
	}
	repos, catalog := mustCurrentDisjointTestAssets(t, slots)
	trace := &nativeNPCAssetsTrace{AccountCharacterAssetUnitOfWork: repos.AccountAssets}
	repos.AccountAssets = trace
	s := currentDisjointTestService(repos, catalog)
	s.options.currentClientVersion = "official-24.9"
	s.options.maxPacketBytes = 1 << 16
	p := &gameSession{conn: &channelSceneWriteConn{}, selectedCharacterID: 19, characterGeneration: 1,
		accountID: "account-1", accountFromToken: true, loginPacketSeen: true}
	p.channel.ID = 10
	s.gameSessions = map[uint16]*gameSession{19: p}
	return s, p, repos, trace, packets
}

func nativeNPCDecodeACKs(t *testing.T, s *Service, wire []byte) []byte {
	t.Helper()
	var success []byte
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
		wire = rest
		if packet.Header.Classification != 1 || packet.Header.MsgID != 26 || len(packet.Body) < 2 {
			t.Fatal("批量改变了原生应答分类或协议", packet)
		}
		success = append(success, packet.Body[0])
	}
	return success
}

func TestNativeNPCBatchUsesOneTransactionAndKeepsEveryACK(t *testing.T) {
	for _, count := range []int{24, 127} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, p, repos, trace, packets := nativeNPCBatchFixture(t, count)
			handled, err := s.handleNativeNPCDisjointBatch(p, packets)
			if err != nil || !handled || p.nativeNPCDisjointReplay != nil {
				t.Fatal(handled, err)
			}
			if trace.transactions != 1 || trace.characterLoads != 1 || trace.inventoryLoads != 1 || trace.accountLoads != 1 || trace.inventorySaves != 1 || trace.accountSaves != 1 {
				t.Fatalf("整批仍重复结算：%+v", trace)
			}
			c := p.conn.(*channelSceneWriteConn)
			if len(c.writes) != 0 {
				t.Fatal("结果未等事务完成集中写回")
			}
			if err := s.finishNativeResultDelivery(p, "批量回归"); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 1 {
				t.Fatal("连续结果被拆开", len(c.writes))
			}
			acks := nativeNPCDecodeACKs(t, s, c.write.Bytes())
			if len(acks) != count {
				t.Fatal("漏掉逐件原生应答", len(acks))
			}
			wire := c.write.Bytes()
			gains := map[int64]int64{}
			for index, result := range acks {
				packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
				wire = rest
				if result != 1 || int(binary.LittleEndian.Uint16(packet.Body[1:3])) != index+10 {
					t.Fatal("成功数或应答顺序改变", index, result)
				}
				for row := 0; row < int(packet.Body[6]); row++ {
					offset := 7 + row*10
					gains[int64(binary.LittleEndian.Uint32(packet.Body[offset+2:offset+6]))] += int64(binary.LittleEndian.Uint32(packet.Body[offset+6 : offset+10]))
				}
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			account, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
			for i := 0; i < count; i++ {
				if stack, ok := bag.Slots[fmt.Sprintf("0:%d", i+10)]; ok && stack.ItemID == 5000 {
					t.Fatal("来源未消耗", i)
				}
			}
			for id, want := range gains {
				got := int64(0)
				for _, stack := range bag.Slots {
					if stack.ItemID == id {
						got += stack.Count
					}
				}
				for _, stack := range account.Slots {
					if stack.ItemID == id {
						got += stack.Count
					}
				}
				if got != want {
					t.Fatal("产物与原生应答不一致", id, got, want)
				}
			}
			t.Logf("%d件：资产事务、角色/背包/共享仓库读取各1次，背包和共享仓库保存各1次，保留%d条ACK", count, count)
		})
	}
}

func TestNativeNPCBatchKeepsPerItemRejectionsAndRollback(t *testing.T) {
	for _, failure := range []string{"", "背包", "共享仓库", "提交"} {
		t.Run("失败="+failure, func(t *testing.T) {
			s, p, repos, trace, packets := nativeNPCBatchFixture(t, 3)
			trace.failure = failure
			// 两件有效，中间缺槽；重复第一件仍应拒绝，不可重复出产物。
			packets = []dnfproto.LatestGameStreamPacket{packets[0], nativeResultTestPacket(26, currentDisjointItemTestBody(80, 0, -1, 317)), packets[0], packets[2]}
			handled, err := s.handleNativeNPCDisjointBatch(p, packets)
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			if err = s.finishNativeResultDelivery(p, "失败回归"); err != nil {
				t.Fatal(err)
			}
			got := nativeNPCDecodeACKs(t, s, p.conn.(*channelSceneWriteConn).write.Bytes())
			want := []byte{1, 0, 0, 1}
			if failure != "" {
				want = []byte{0, 0, 0, 0}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatal("单件拒绝或事务失败应答改变", got, want)
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			account, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
			for _, slot := range []string{"0:10", "0:12"} {
				_, exists := bag.Slots[slot]
				if exists != (failure != "") {
					t.Fatal("来源或回滚不符", failure, slot, bag.Slots)
				}
			}
			if failure != "" && len(account.Slots) > 0 {
				t.Fatal("失败仍交付了产物", account.Slots)
			}
			if trace.transactions != 1 || p.nativeNPCDisjointReplay != nil {
				t.Fatal("事务或会话结果未回收", trace.transactions)
			}
		})
	}
}

func TestNativeNPCBatchBoundariesAndSingleStayOnOriginalPath(t *testing.T) {
	s, p, _, trace, packets := nativeNPCBatchFixture(t, 3)
	other := nativeResultTestPacket(26, currentDisjointItemTestBody(13, 0, -1, 1000))
	if n := nativeNPCDisjointPrefix(append(packets, other)); n != 3 {
		t.Fatal("不同NPC上下文被合并", n)
	}
	for _, replacement := range []dnfproto.LatestGameStreamPacket{
		nativeResultTestPacket(35, []byte{0}),
		nativeResultTestPacket(26, currentDisjointItemTestBody(13, 0, 76, 317)),
		nativeResultTestPacket(416, []byte{2, 76, 0, 0, 13, 0}),
	} {
		if nativeNPCDisjointPrefix([]dnfproto.LatestGameStreamPacket{replacement}) != 0 {
			t.Fatal("其他入口进入NPC整批结算")
		}
	}
	if handled, err := s.handleNativeNPCDisjointBatch(p, packets[:1]); handled || err != nil || trace.transactions != 0 {
		t.Fatal("单件路径改变", handled, err)
	}
	if err := s.prepareNativeResultDelivery(p, packets[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.finishNativeResultDelivery(p, "单件"); err != nil {
		t.Fatal(err)
	}
	if trace.transactions != 1 || len(p.conn.(*channelSceneWriteConn).writes) != 1 {
		t.Fatal("单件不再立即提交与回复")
	}
	for _, state := range []string{"未绑定", "比赛频道", "下线"} {
		s, p, _, trace, packets := nativeNPCBatchFixture(t, 2)
		switch state {
		case "未绑定":
			s.gameSessions = nil
		case "比赛频道":
			p.channel.Type = currentTagTournamentChannelType
		case "下线":
			p.selectedCharacterID = 0
		}
		if handled, err := s.handleNativeNPCDisjointBatch(p, packets); handled || err != nil || trace.transactions != 0 {
			t.Fatal("不合法场景被预结算", state)
		}
	}
}

func TestNativeNPCCollectorPreservesPartialInputAndEOF(t *testing.T) {
	s, p, _, _, packets := nativeNPCBatchFixture(t, 2)
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 3)}
	ahead.ready <- nativeResultReadChunk{data: packets[1].Data[:4]}
	ahead.ready <- nativeResultReadChunk{data: packets[1].Data[4:]}
	ahead.ready <- nativeResultReadChunk{err: io.EOF}
	collected, remaining, held := s.collectNativeNPCReadyPackets(p, ahead, packets[:1], nil)
	if len(collected) != 2 || len(remaining) != 0 || held == nil || !errors.Is(held.err, io.EOF) {
		t.Fatal("碎片或EOF丢失", len(collected), len(remaining), held)
	}
	empty := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk)}
	done := make(chan struct{})
	go func() { s.collectNativeNPCReadyPackets(p, empty, packets[:1], nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没有已到达数据时仍等待合批")
	}
	broken := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	// 非0/1分类且传输帧长度为0，原拆包器会报告跳过；全ff属于待收齐的65535字节帧。
	broken.ready <- nativeResultReadChunk{data: []byte{0xff, 0xff, 0, 0}}
	collected, _, held = s.collectNativeNPCReadyPackets(p, broken, packets[:1], nil)
	if len(collected) != 1 || held == nil {
		t.Fatal("后续坏帧提前丢弃了此前完整请求")
	}
}

type nativeNPCScriptedConn struct {
	chunks                   [][]byte
	index                    int
	beforeRest               <-chan struct{}
	queued, stop, writeReady chan struct{}
	once                     sync.Once
	mu                       sync.Mutex
	wire                     []byte
	writes                   int
}

func (c *nativeNPCScriptedConn) Read(out []byte) (int, error) {
	if c.index == 1 && c.beforeRest != nil {
		select {
		case <-c.beforeRest:
		case <-c.stop:
			return 0, io.EOF
		}
	}
	if c.index < len(c.chunks) {
		data := c.chunks[c.index]
		c.index++
		return copy(out, data), nil
	}
	if c.index == len(c.chunks) {
		c.index++
		close(c.queued)
	}
	<-c.stop
	return 0, io.EOF
}
func (c *nativeNPCScriptedConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	c.wire = append(c.wire, data...)
	c.writes++
	c.mu.Unlock()
	select {
	case c.writeReady <- struct{}{}:
	default:
	}
	return len(data), nil
}
func (c *nativeNPCScriptedConn) Close() error { c.once.Do(func() { close(c.stop) }); return nil }
func (c *nativeNPCScriptedConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10110}
}
func (c *nativeNPCScriptedConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10111}
}
func (c *nativeNPCScriptedConn) SetDeadline(time.Time) error      { return c.Close() }
func (c *nativeNPCScriptedConn) SetReadDeadline(time.Time) error  { return c.Close() }
func (c *nativeNPCScriptedConn) SetWriteDeadline(time.Time) error { return nil }

func TestNativeNPCInputLoopCombinesArrivedFragmentsWithoutWaiting(t *testing.T) {
	s, p, repos, trace, packets := nativeNPCBatchFixture(t, 24)
	blocked := &nativeResultBlockingAssets{AccountCharacterAssetUnitOfWork: repos.AccountAssets, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	defer release()
	repos.AccountAssets = blocked
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	var rest []byte
	for _, packet := range packets[1:] {
		rest = append(rest, packet.Data...)
	}
	c := &nativeNPCScriptedConn{chunks: [][]byte{packets[0].Data, rest[:3], rest[3:19], rest[19:]}, beforeRest: blocked.entered, queued: make(chan struct{}), stop: make(chan struct{}), writeReady: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.Close()
	p.conn, p.ctx = c, ctx
	finished := make(chan struct{})
	go func() { defer close(finished); s.handleGameStreamInput(ctx, p) }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("首件没有进入原事务")
	}
	select {
	case <-c.queued:
	case <-time.After(5 * time.Second):
		t.Fatal("首件处理时后续字节未预读完成")
	}
	release()
	select {
	case <-c.writeReady:
	case <-time.After(5 * time.Second):
		t.Fatal("原生结果未写出")
	}
	c.mu.Lock()
	wire := append([]byte(nil), c.wire...)
	writes := c.writes
	c.mu.Unlock()
	if writes != 1 || len(nativeNPCDecodeACKs(t, s, wire)) != 24 {
		t.Fatal("批量应答拆分或丢失", writes)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("预读或会话未退出")
	}
	if trace.transactions != 2 {
		t.Fatal("已到达23件仍逐件提交", trace.transactions)
	}
}
