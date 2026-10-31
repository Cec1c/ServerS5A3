package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestEquipmentUpgradeNoticeCurrentChannelNativeAndRemoteArea(t *testing.T) {
	s, source, _, self, sameArea := newCurrentSocialPeerTest(t)
	remote, foreign, highChannel := &bufferConn{}, &bufferConn{}, &bufferConn{}
	for _, tc := range []struct {
		id      uint16
		channel int
		conn    *bufferConn
	}{{6, 42, remote}, {7, 43, foreign}, {8, 298, highChannel}} {
		p := &gameSession{conn: tc.conn, channel: channelcatalog.Channel{ID: tc.channel}, accountID: "notice-peer"}
		s.bindGameSessionCharacter(p, tc.id)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: tc.id, TownID: 2, AreaID: 1, Session: p})
	}
	self.write.Reset()
	sameArea.write.Reset()
	body := []byte{1, 1, 1, 0, 0xbc, 2, 0, 0, 12, 0}
	fact := &alignedcmd.EquipmentUpgradeNotice{Kind: "reinforce", ItemID: 700, Level: 12, Succeeded: true, Body: body}
	err := s.sendAlignedUpperResponses(source, alignedcmd.Result{UpperResponses: []alignedcmd.UpperResponse{{MsgID: 0x50, Classification: 1, Body: []byte{1}}, {MsgID: 86, Classification: 0, Body: body}}, EquipmentUpgradeNotice: fact})
	if err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, self.write.Bytes())
	if ack.Header.MsgID != 0x50 {
		t.Fatal("播报抢先于资产应答")
	}
	native, rest := splitGameServerUpperPacket(t, rest)
	if native.Header.MsgID != 86 || !bytes.Equal(native.Body, body) || len(rest) != 0 {
		t.Fatal("本人原生播报重复或变化")
	}
	peer, rest := splitGameServerUpperPacket(t, sameArea.write.Bytes())
	if peer.Header.MsgID != 86 || !bytes.Equal(peer.Body, body) || len(rest) != 0 {
		t.Fatal("同屏原生播报缺失或重复")
	}
	text, rest := splitGameServerUpperPacket(t, remote.write.Bytes())
	if text.Header.MsgID != 12 || text.Body[0] != 0 || len(rest) != 0 {
		t.Fatal("同频道异区播报缺失")
	}
	n := int(binary.LittleEndian.Uint32(text.Body[4:8]))
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(text.Body[8 : 8+n])
	if err != nil || !bytes.Contains(decoded, []byte("强化 +12 成功")) {
		t.Fatalf("频道播报正文=%s err=%v", decoded, err)
	}
	if foreign.write.Len() != 0 || highChannel.write.Len() != 0 {
		t.Fatal("播报泄露到其他频道或截断后的相同频道")
	}
}

func TestEquipmentUpgradeNoticeSuppressesStaleAndUncommitted(t *testing.T) {
	s, source, peer, self, target := newCurrentSocialPeerTest(t)
	self.write.Reset()
	target.write.Reset()
	s.broadcastCurrentEquipmentUpgradeNotice(source, nil)
	if target.write.Len() != 0 {
		t.Fatal("无提交事实发出播报")
	}
	peer.loginRevoked.Store(true)
	s.broadcastCurrentEquipmentUpgradeNotice(source, &alignedcmd.EquipmentUpgradeNotice{ItemID: 700, Body: make([]byte, 10)})
	if target.write.Len() != 0 {
		t.Fatal("被接管连接收到播报")
	}
	peer.loginRevoked.Store(false)
	source.loginRevoked.Store(true)
	s.broadcastCurrentEquipmentUpgradeNotice(source, &alignedcmd.EquipmentUpgradeNotice{ItemID: 700, Body: make([]byte, 10)})
	if target.write.Len() != 0 {
		t.Fatal("被接管来源连接继续发起播报")
	}
	source.loginRevoked.Store(false)
	repos, _ := s.repositoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "other", Name: "not-owner"}); err != nil {
		t.Fatal(err)
	}
	// 异区文本不能采信不匹配账号的角色名称。
	if body := s.currentEquipmentNoticeText(source, &alignedcmd.EquipmentUpgradeNotice{ItemID: 700}); body != nil {
		t.Fatal("接受错误角色名归属")
	}
}

func TestEquipmentUpgradeNoticeReplacedPresenceUsesSystemText(t *testing.T) {
	s, source, _, _, old := newCurrentSocialPeerTest(t)
	conn := &bufferConn{}
	replacement := &gameSession{conn: conn, channel: source.channel, accountID: "dnf:2"}
	s.bindGameSessionCharacter(replacement, 5)
	s.promoteResidentGameSession(replacement, 5)
	// 在线区域表尚未被新连接重建，不能据旧连接的同屏记录发送原生角色播报。
	s.broadcastCurrentEquipmentUpgradeNotice(source, &alignedcmd.EquipmentUpgradeNotice{Kind: "forging", ItemID: 700, Level: 5, Succeeded: true, Body: make([]byte, 11)})
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 12 || len(rest) != 0 || old.write.Len() != 0 {
		t.Fatal("旧区域记录导致错误原生播报或旧连接重复投递")
	}
}
