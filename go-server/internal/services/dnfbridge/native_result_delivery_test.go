package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func nativeResultTestPacket(op uint16, body []byte) dnfproto.LatestGameStreamPacket {
	raw := make([]byte, 13+len(body))
	raw[0] = 1
	binary.LittleEndian.PutUint16(raw[1:3], op)
	binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
	copy(raw[13:], body)
	return dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: raw}
}

func TestNativeResultKeysCoverFourJobsAndSeparateTools(t *testing.T) {
	for _, kind := range []byte{1, 2, 4} {
		one, ok := nativeResultRequestKey(nativeResultTestPacket(416, []byte{kind, 76, 0, 0, 9, 0}))
		two, ok2 := nativeResultRequestKey(nativeResultTestPacket(416, []byte{kind, 76, 0, 0, 10, 0}))
		other, ok3 := nativeResultRequestKey(nativeResultTestPacket(416, []byte{kind, 77, 0, 0, 10, 0}))
		if !ok || !ok2 || !ok3 || one != two || one == other {
			t.Fatal("三种提取必须保留来源槽，并按提取器分开", kind)
		}
	}
	for _, op := range []uint16{26, 235} {
		body := currentDisjointItemTestBody(9, 0, -1, 317)
		if op == 235 {
			body = []byte{5, 0, 9, 0, 0}
		}
		if _, ok := nativeResultRequestKey(nativeResultTestPacket(op, body)); !ok {
			t.Fatal("NPC或分解师原生请求未识别", op)
		}
	}
	for _, packet := range []dnfproto.LatestGameStreamPacket{
		nativeResultTestPacket(416, []byte{3, 76, 0, 0, 9, 0}),
		nativeResultTestPacket(416, []byte{2, 76, 0, 0, 76, 0}),
		nativeResultTestPacket(416, []byte{2, 76, 0, 0, 255, 255, 'B', '4', '1', 1, 1, 9, 0}),
		nativeResultTestPacket(35, []byte{0}),
		{Kind: dnfproto.LatestGameStreamUpper},
	} {
		if _, ok := nativeResultRequestKey(packet); ok {
			t.Fatal("不应把非原生单件请求纳入连续写回")
		}
	}
}

type nativeResultBlockingAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	once             sync.Once
	entered, release chan struct{}
}

func (b *nativeResultBlockingAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, run func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, run)
}

type nativeResultRecordingConn struct {
	net.Conn
	mu     sync.Mutex
	writes [][]byte
}

func (c *nativeResultRecordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func TestNativeResultRealInputLoopDrainsFragmentsAndKeepsFailedItem(t *testing.T) {
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
	repos, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}, "0:67": {ItemID: 5000, Count: 1}})
	blocked := &nativeResultBlockingAssets{AccountCharacterAssetUnitOfWork: repos.AccountAssets, entered: make(chan struct{}), release: make(chan struct{})}
	repos.AccountAssets = blocked
	s := currentDisjointTestService(repos, catalog)
	s.options.currentClientVersion = "official-24.9"
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	conn := &nativeResultRecordingConn{Conn: server}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &gameSession{conn: conn, ctx: ctx, selectedCharacterID: 19, accountID: "account-1"}
	finished := make(chan struct{})
	go func() { defer close(finished); s.handleGameStreamInput(ctx, p) }()
	packets := make([]dnfproto.LatestGameStreamPacket, 3)
	for i, slot := range []int16{65, 66, 67} {
		packets[i] = nativeResultTestPacket(26, currentDisjointItemTestBody(slot, 0, -1, 317))
	}
	if _, err := client.Write(packets[0].Data); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("真实资产事务未开始")
	}
	rest := append(append([]byte(nil), packets[1].Data...), packets[2].Data...)
	for _, fragment := range [][]byte{rest[:3], rest[3:19], rest[19:]} {
		if _, err := client.Write(fragment); err != nil {
			t.Fatal(err)
		}
	}
	// 没有收到第一件ACK，后两件仍然可以完整进入读取队列。
	close(blocked.release)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var wire []byte
	for {
		buffer := make([]byte, 4096)
		n, err := client.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, buffer[:n]...)
		if len(splitAllCurrentGameServerUpperPackets(t, wire)) >= 3 {
			break
		}
	}
	acks := splitAllCurrentGameServerUpperPackets(t, wire)
	if len(acks) != 3 || acks[0].Body[0] != 1 || acks[1].Body[0] != 0 || acks[2].Body[0] != 1 {
		t.Fatalf("逐件成功/拒绝应答改变：%+v", acks)
	}
	conn.mu.Lock()
	writeCount := len(conn.writes)
	conn.mu.Unlock()
	if writeCount != 1 {
		t.Fatalf("拆包后的连续结果被逐件提前发送：%d", writeCount)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("输入循环或预读协程未退出")
	}
	inventory, _, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := inventory.Slots["0:65"]; exists {
		t.Fatal("第一件未提交")
	}
	if _, exists := inventory.Slots["0:67"]; exists {
		t.Fatal("第三件未提交")
	}
	account, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
	if account.Slots["0:358"].Count != 4 {
		t.Fatal("连续写回改变了资产数量", account.Slots)
	}
}

func TestNativeResultDeliveryPreservesDprotoAndFlushesAtOtherCommand(t *testing.T) {
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	p.selectedCharacterID = 19
	c := &channelSceneWriteConn{}
	p.conn = c
	provider := &testDprotoSession{}
	p.dproto = provider
	packet := nativeResultTestPacket(416, []byte{2, 76, 0, 0, 9, 0})
	for i := 0; i < 3; i++ {
		if err := s.prepareNativeResultDelivery(p, packet); err != nil {
			t.Fatal(err)
		}
		if err := s.sendGameUpperRawClass(p, 416, []byte{1, 0, byte(i)}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.writes) != 0 {
		t.Fatal("准备期间提前发送")
	}
	if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(35, []byte{0})); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 1 || p.nativeResultDelivery != nil || len(provider.encodeInputs) != 3 {
		t.Fatal("其他指令没有先释放原生结果")
	}
	wire := c.writes[0]
	for _, inner := range provider.encodeInputs {
		n := int(binary.LittleEndian.Uint32(wire[3:7]))
		outer, err := dnfproto.ParseChannelPacketUnchecked(wire[:n])
		if err != nil || !bytes.Equal(outer.Body, inner) {
			t.Fatal("保护层字节或顺序改变", err)
		}
		wire = wire[n:]
	}
	if len(wire) != 0 {
		t.Fatal("保护层存在多余字节")
	}
}

func TestNativeResultDeliveryShortWriteAndCapacity(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "容量释放", true: "写入中断"}[short], func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			c := &channelSceneWriteConn{short: short}
			p.conn = c
			p.nativeResultDelivery = &nativeResultDelivery{wire: []byte{1, 2, 3}, started: time.Now()}
			if short {
				err := s.finishNativeResultDelivery(p, "测试")
				if !errors.Is(err, io.ErrShortWrite) || !c.closed || p.nativeResultDelivery != nil {
					t.Fatal("短写未中止连接", err)
				}
			} else {
				p.wireMu.Lock()
				_, err := s.writeNativeResultDeliveryLocked(p, make([]byte, nativeResultDeliveryLimit))
				p.wireMu.Unlock()
				if err != nil || len(c.writes) != 2 || !bytes.Equal(c.writes[0], []byte{1, 2, 3}) || p.nativeResultDelivery != nil {
					t.Fatal("容量边界丢失先前已提交应答", err)
				}
			}
		})
	}
}

func TestNativeResultReadAheadTimeoutResumesOnlyAfterConsumer(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	r := newNativeResultReadAhead(server)
	defer r.stop()
	_ = server.SetReadDeadline(time.Now())
	chunk := r.next(context.Background())
	var nerr net.Error
	if !errors.As(chunk.err, &nerr) || !nerr.Timeout() {
		t.Fatal("没有保留读超时", chunk.err)
	}
	_ = server.SetReadDeadline(time.Time{})
	r.changeDeadline(func() { _ = server.SetReadDeadline(time.Time{}) })
	if !r.obsoleteTimeout(chunk) {
		t.Fatal("已清除的截止仍可能关闭连接或结算新状态")
	}
	written := make(chan error, 1)
	go func() { _, err := client.Write([]byte{7, 8}); written <- err }()
	chunk = r.next(context.Background())
	if chunk.err != nil || !bytes.Equal(chunk.data, []byte{7, 8}) {
		t.Fatal("同一次过期截止被重复读取或恢复后丢字节", chunk.err, chunk.data)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestNativeResultSingleRepliesImmediatelyInBothModes(t *testing.T) {
	for _, mode := range []string{"on", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", mode)
			repos, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}, "0:66": {ItemID: 5000, Count: 1}})
			s := currentDisjointTestService(repos, catalog)
			s.options.currentClientVersion = "official-24.9"
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			c := &nativeResultRecordingConn{Conn: server}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &gameSession{conn: c, ctx: ctx, selectedCharacterID: 19, accountID: "account-1"}
			if err := s.startGameSessionEvents(ctx, p); err != nil {
				t.Fatal(err)
			}
			defer stopTestGameSessionEvents(t, s, p)
			finished := make(chan struct{})
			go func() { defer close(finished); s.handleGameStreamInput(ctx, p) }()
			for index, slot := range []int16{65, 66, 65} {
				_ = client.SetDeadline(time.Now().Add(3 * time.Second))
				packet := nativeResultTestPacket(26, currentDisjointItemTestBody(slot, 0, -1, 317))
				if _, err := client.Write(packet.Data); err != nil {
					t.Fatal(err)
				}
				// 只有收到本件结果才发送下一件，验证不存在等下一次操作的死等。
				header := make([]byte, 7)
				if _, err := io.ReadFull(client, header); err != nil {
					t.Fatal("单件结果等待了下一次请求", err)
				}
				wire := make([]byte, int(binary.LittleEndian.Uint32(header[3:7])))
				copy(wire, header)
				if _, err := io.ReadFull(client, wire[7:]); err != nil {
					t.Fatal(err)
				}
				ack, err := dnfproto.ParseChannelPacketUnchecked(wire)
				want := byte(1)
				if index == 2 {
					want = 0
				}
				if err != nil || ack.Header.MsgID != 26 || ack.Body[0] != want {
					t.Fatal("单件或重放拒绝的应答改变", err, ack.Body)
				}
			}
			cancel()
			_ = client.Close()
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("读循环未退出")
			}
			c.mu.Lock()
			writes := len(c.writes)
			c.mu.Unlock()
			if writes != 3 {
				t.Fatal("不同操作被合并或重复回复", writes)
			}
		})
	}
}

func TestNativeResultOtherNotificationsAndControlRemainImmediate(t *testing.T) {
	for _, mode := range []string{"其他功能", "跨场景mode3", "跨场景mode8", "其他角色mode0", "保护控制"} {
		t.Run(mode, func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			s.options.currentClientVersion = "official-24.9"
			p.selectedCharacterID = 19
			c := &channelSceneWriteConn{}
			p.conn = c
			if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(416, []byte{2, 76, 0, 0, 9, 0})); err != nil {
				t.Fatal(err)
			}
			if err := s.sendGameUpperRawClass(p, 416, []byte{1}, 1); err != nil {
				t.Fatal(err)
			}
			body, opcode := []byte{0}, uint16(1426)
			switch mode {
			case "跨场景mode3":
				opcode, body = 2, []byte{3, 1, 0}
			case "跨场景mode8":
				opcode, body = 2, []byte{8, 1, 0}
			case "其他角色mode0":
				opcode, body = 2, make([]byte, 78)
				body[1] = 1
				binary.LittleEndian.PutUint16(body[76:78], 20)
			}
			if mode == "保护控制" {
				control, err := dnfproto.BuildChannelPacket(dnfproto.DprotoServerControlOpcode, []byte{4}, 9, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.withGameWire(p, func() error { return s.writeDprotoProviderPacketsLocked(p, [][]byte{control}, "回归") }); err != nil {
					t.Fatal(err)
				}
			} else if err := s.sendGameUpperRawClass(p, opcode, body, 0); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 2 || p.nativeResultDelivery != nil {
				t.Fatal("其他消息被分解结果缓冲延迟", len(c.writes))
			}
		})
	}
}

func TestNativeResultLocalExperienceUsesConfiguredHeader(t *testing.T) {
	for _, header := range []string{gameUpperHeaderChannel13, gameUpperHeaderServer16} {
		t.Run(header, func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			s.options.currentClientVersion, s.options.gameUpperHeader = "official-24.9", header
			p.selectedCharacterID = 19
			c := &channelSceneWriteConn{}
			p.conn = c
			if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(416, []byte{2, 76, 0, 0, 9, 0})); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 78)
			body[1] = 1
			binary.LittleEndian.PutUint16(body[76:78], 19)
			if err := s.sendGameUpperRawClass(p, 2, body, 0); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 0 {
				t.Fatal("当前角色经验误判成其他资料")
			}
			if err := s.finishNativeResultDelivery(p, "回归"); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 1 {
				t.Fatal("经验报文缺失")
			}
		})
	}
}
