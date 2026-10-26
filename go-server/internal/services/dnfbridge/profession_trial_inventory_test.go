package dnfbridge

import (
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestProfessionTrialMainInventoryRequiresSuccessfulWrite(t *testing.T) {
	s, p, _ := newBackToVillageRuntime(t)
	conn := p.conn.(*bufferConn)
	want := errors.New("测试主背包写入失败")
	p.conn = &failNthDungeonWriteConn{failAt: 1, err: want}
	body := buildCurrentItemListBody(0, nil, repository.CharacterContainerState{})
	if err := s.sendCurrentSceneItemList(p, 13, body); !errors.Is(err, want) || p.selectedMainInventoryCharacterID != 0 {
		t.Fatal("失败写入被当作主背包已有对象", err)
	}
	p.conn = conn
	if err := s.sendCurrentSceneItemList(p, 13, buildCurrentItemListBody(1, nil, repository.CharacterContainerState{})); err != nil {
		t.Fatal(err)
	}
	if p.selectedMainInventoryCharacterID != 0 {
		t.Fatal("其它容器被当作主背包已有对象")
	}
	if err := s.sendCurrentSceneItemList(p, 13, body); err != nil {
		t.Fatal(err)
	}
	if p.selectedMainInventoryCharacterID != p.selectedCharacterID {
		t.Fatal("成功主背包写入没有记录当前角色归属")
	}
	p.selectedItemListBootstrapCharacterID = p.selectedCharacterID
	resetDungeonEntrySceneGates(p)
	if p.selectedItemListBootstrapCharacterID != 0 || p.selectedMainInventoryCharacterID != p.selectedCharacterID {
		t.Fatal("入图门重置丢失全局主背包对象归属")
	}
	p.selectedCharacterID = 0
	resetDungeonEntrySceneGates(p)
	if p.selectedMainInventoryCharacterID != 0 {
		t.Fatal("返回选角后仍持有旧角色的主背包回执")
	}
}

func TestProfessionTrialInventoryDoesNotSuppressOrdinaryOrColdBootstrap(t *testing.T) {
	for _, cold := range []bool{false, true} {
		s, p, runtime := newBackToVillageRuntime(t)
		if !cold {
			p.selectedMainInventoryCharacterID = p.selectedCharacterID
		}
		conn := p.conn.(*bufferConn)
		conn.write.Reset()
		scene, ok := runtime.Session.Scene()
		if !ok {
			t.Fatal("普通副本缺少场景")
		}
		if err := s.sendCurrentProfessionTrialInventoryBootstrap(p, runtime, scene, "普通副本必须交付全部容器"); err != nil {
			t.Fatal(err)
		}
		_, lists, _ := requireCurrentSelectInventoryBootstrapPackets(t, conn.write.Bytes())
		if _, ok := lists[0]; !ok {
			t.Fatal("普通或冷入口丢失主背包")
		}
	}
}

func TestProfessionTrialPreservedInventoryWriteRetry(t *testing.T) {
	s, p, _ := newBackToVillageRuntime(t)
	conn := p.conn.(*bufferConn)
	p.selectedMainInventoryCharacterID = p.selectedCharacterID
	want := errors.New("测试其它容器写入失败")
	p.conn = &failNthDungeonWriteConn{failAt: 2, err: want}
	if err := s.sendCurrentSelectInventoryBootstrapWithMainPreserved(p, "体验其它容器写失败", true); !errors.Is(err, want) {
		t.Fatal("没有命中其它容器写失败", err)
	}
	if p.selectedItemListBootstrapCharacterID != 0 || p.selectedMainInventoryCharacterID != p.selectedCharacterID {
		t.Fatal("写失败错误提交全容器门或丢失主背包归属")
	}
	p.conn = conn
	conn.write.Reset()
	if err := s.sendCurrentSelectInventoryBootstrapWithMainPreserved(p, "体验其它容器重试", true); err != nil {
		t.Fatal(err)
	}
	seen := make(map[byte]bool)
	stream := conn.write.Bytes()
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		if packet.Header.Classification == 0 && packet.Header.MsgID == 13 && len(packet.Body) > 0 {
			seen[packet.Body[0]] = true
		}
	}
	for _, list := range loginEquipmentBootstrapListTypes() {
		if seen[list] != (list != 0) {
			t.Fatalf("重试容器不符：list=%d seen=%v", list, seen)
		}
	}
	if p.selectedItemListBootstrapCharacterID != p.selectedCharacterID {
		t.Fatal("重试成功未提交其余容器门")
	}
	// 没有真实主背包写出回执时，即使调用方请求保留，也必须交付list0。
	p.selectedItemListBootstrapCharacterID = 0
	p.selectedMainInventoryCharacterID = 0
	conn.write.Reset()
	if err := s.sendCurrentSelectInventoryBootstrapWithMainPreserved(p, "无主背包回执冷入口", true); err != nil {
		t.Fatal(err)
	}
	_, lists, _ := requireCurrentSelectInventoryBootstrapPackets(t, conn.write.Bytes())
	if _, ok := lists[0]; !ok {
		t.Fatal("无主背包回执仍省略了主背包")
	}
}
