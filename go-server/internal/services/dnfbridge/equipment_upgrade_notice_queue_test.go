package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/platform/eventloop"
)

func blockEquipmentNoticePeer(t *testing.T, s *Service, peer *gameSession) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	release, entered := make(chan struct{}), make(chan struct{})
	if err := s.postGameSessionCharacterEvent(peer, "占用接收事件", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("接收事件未启动")
	}
	closed := false
	finish := func() {
		if !closed {
			close(release)
			closed = true
		}
	}
	t.Cleanup(func() {
		finish()
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := s.stopGameSessionEvents(stopCtx, peer); err != nil {
			t.Error(err)
		}
	})
	return finish
}

func queueTestEquipmentNotice() *alignedcmd.EquipmentUpgradeNotice {
	return &alignedcmd.EquipmentUpgradeNotice{Kind: "reinforce", ItemID: 700, Level: 12, Succeeded: true, Body: []byte{1, 1, 1, 0, 0xbc, 2, 0, 0, 12, 0}}
}

func TestEquipmentUpgradeNoticeFullQueueDoesNotStallSourceOrOtherPeers(t *testing.T) {
	s, source, peer, _, blocked := newCurrentSocialPeerTest(t)
	blockEquipmentNoticePeer(t, s, peer)
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := s.postGameSessionCharacterEvent(peer, "填满接收队列", peer.selectedCharacterID, peer.characterGeneration, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if peer.events.loop.QueueLengths().Medium != 512 {
		t.Fatal("未使用真实512容量")
	}
	if err := s.postCurrentEquipmentNotice(boundGameSessionCharacter{session: peer, character: peer.selectedCharacterID, generation: peer.characterGeneration}, func() error { return nil }); !errors.Is(err, eventloop.ErrQueueFull) {
		t.Fatalf("满队列未立即拒绝播报：%v", err)
	}
	healthyConn := &bufferConn{}
	healthy := &gameSession{conn: healthyConn, channel: channelcatalog.Channel{ID: 42}, accountID: "peer"}
	s.bindGameSessionCharacter(healthy, 6)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 6, TownID: 2, AreaID: 1, Session: healthy})
	sourceCtx, cancel := context.WithCancel(context.Background())
	source.ctx = sourceCtx
	if err := s.startGameSessionEvents(sourceCtx, source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = s.stopGameSessionEvents(ctx, source)
	})
	done := make(chan struct{})
	if err := s.postGameSessionCharacterEvent(source, "装备播报", source.selectedCharacterID, source.characterGeneration, func() error { s.broadcastCurrentEquipmentUpgradeNotice(source, queueTestEquipmentNotice()); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.postGameSessionCharacterEvent(source, "后续无关事件", source.selectedCharacterID, source.characterGeneration, func() error { close(done); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("接收者满队列仍阻塞操作者")
	}
	packet, rest := splitGameServerUpperPacket(t, healthyConn.write.Bytes())
	if packet.Header.MsgID != 12 || len(rest) != 0 {
		t.Fatal("满队列阻断其他同频道接收者")
	}
	if blocked.write.Len() != 0 {
		t.Fatal("满队列仍直接向接收连接写入")
	}
}

func TestEquipmentUpgradeNoticeQueuedDropsAfterCharacterChange(t *testing.T) {
	s, source, peer, _, conn := newCurrentSocialPeerTest(t)
	release := blockEquipmentNoticePeer(t, s, peer)
	s.broadcastCurrentEquipmentUpgradeNotice(source, queueTestEquipmentNotice())
	if peer.events.loop.QueueLengths().Medium != 1 {
		t.Fatal("播报未入队")
	}
	s.bindGameSessionCharacter(peer, 9)
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.callGameSession(ctx, peer, "等待旧播报消费", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("旧角色播报泄露到换角后的连接")
	}
}
