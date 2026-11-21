package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentRaidClearNoticeUsesNativeTeamAndSoloBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode byte
		solo bool
		kind uint32
	}{
		{"安徒恩团队", 1, false, 0}, {"卢克团队", 2, false, 1}, {"超时空团队", 3, false, 2},
		{"安徒恩单人", 1, true, 0}, {"卢克单人", 2, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := runtimeRaidState{
				LeaderID: 19, NameBytes: []byte{0xb9, 0xa5, 0xbc, 0xe1, 0xb6, 0xd3}, // 攻坚队
				Members: []runtimeRaidMemberState{{CharID: 19, Name: "团长"}},
				Battle:  &runtimeRaidBattle{Mode: tc.mode, Single: tc.solo},
			}
			want := binary.LittleEndian.AppendUint32(nil, tc.kind)
			if tc.solo {
				want = binary.LittleEndian.AppendUint32(want, 0)
			} else {
				want = binary.LittleEndian.AppendUint32(want, 6)
				want = append(want, state.NameBytes...)
			}
			want = append(want, 4, 0, 0, 0, 0xcd, 0xc5, 0xb3, 0xa4) // 团长
			if tc.solo {
				want = append(want, 1, 0)
			} else {
				want = append(want, 0, 0)
			}
			got := prepareCurrentRaidClearNotice(state)
			if !bytes.Equal(got, want) {
				t.Fatalf("播报包体=%x，期望=%x", got, want)
			}
		})
	}
	if _, err := raid.BuildRaidClearAnnouncementBody(1, bytes.Repeat([]byte{'a'}, 256), []byte("leader"), false); err == nil {
		t.Fatal("客户端拒绝读取的长度仍生成了播报")
	}
}

func TestCurrentRaidClearNoticeShortensLongTeamNameAtCharacterBoundary(t *testing.T) {
	const limit = raid.RaidClearAnnouncementNameMaxBytes
	// 254 编码字节、253 个 UTF-16 单元，给最后一个双单元字符保留合法输入空间。
	prefixText := strings.Repeat("x", limit-3) + "团"
	prefix, err := simplifiedchinese.GB18030.NewEncoder().String(prefixText)
	if err != nil {
		t.Fatal(err)
	}
	for _, last := range []string{"团", "𠮷"} {
		name, err := simplifiedchinese.GB18030.NewEncoder().String(prefixText + last)
		if err != nil || !currentRaidNameValid([]byte(name)) || len(name) <= limit {
			t.Fatalf("团名夹具没有覆盖截断边界：%q 长度=%d 错误=%v", last, len(name), err)
		}
		state := runtimeRaidState{
			LeaderID: 19, NameBytes: []byte(name),
			Members: []runtimeRaidMemberState{{CharID: 19, Name: "团长"}},
			Battle:  &runtimeRaidBattle{Mode: 2},
		}
		body := prepareCurrentRaidClearNotice(state)
		want, err := raid.BuildRaidClearAnnouncementBody(2, []byte(prefix), []byte{0xcd, 0xc5, 0xb3, 0xa4}, false)
		if err != nil || !bytes.Equal(body, want) || string(state.NameBytes) != name {
			t.Fatalf("长团名播报没有保留完整字符，字符=%q 包体=%x", last, body)
		}
	}
}

func TestRealPVFRaidClearNoticeRequiresCommitAndReachesAllChannelsOnce(t *testing.T) {
	f, state, now := actualFinalAntonWeeklyFixture(t)
	s := f.service
	channel := state.Battle.ChannelID
	newObserver := func(id uint16, onChannel int) *bufferConn {
		conn := &bufferConn{}
		s.bindGameSessionCharacter(&gameSession{conn: conn, channel: channelcatalog.Channel{ID: onChannel}}, id)
		return conn
	}
	observer := newObserver(31, channel)
	replaced := newObserver(32, channel)
	otherChannel := newObserver(32, channel+256)
	thirdChannel := newObserver(33, channel+1)
	recipients := []*bufferConn{observer, otherChannel, thirdChannel}
	instanceID := state.Battle.InstanceID
	s.publishCurrentRaidClearNotice(instanceID)
	for _, conn := range recipients {
		if conn.write.Len() != 0 {
			t.Fatal("终局尚未提交就播报")
		}
	}
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	if done, errs := s.commitCurrentRaidWeeklyIntents(now); len(done) != 0 || len(errs) != 1 {
		t.Fatal("未复现终局事务回滚", done, errs)
	}
	s.publishCurrentRaidClearNotice(instanceID)
	for _, conn := range recipients {
		if conn.write.Len() != 0 {
			t.Fatal("终局事务回滚仍播报")
		}
	}
	delete(s.raids, state.RaidKey)
	f.repos.CharacterTrade = original
	if done, errs := s.commitCurrentRaidWeeklyIntents(now.Add(2 * time.Second)); len(done) != 1 || len(errs) != 0 {
		t.Fatal("终局事务重试失败", done, errs)
	}
	var publish sync.WaitGroup
	for range 2 {
		publish.Go(func() { s.publishCurrentRaidClearNotice(instanceID) })
	}
	publish.Wait()
	for _, conn := range recipients {
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.Classification != 0 || packet.Header.MsgID != 626 || len(rest) != 0 || !bytes.Equal(packet.Body, prepareCurrentRaidClearNotice(state)) {
			t.Fatal("在线观察者没有收到一次冻结的原生播报", packet, rest)
		}
	}
	if replaced.write.Len() != 0 {
		t.Fatal("原生播报发送到了旧会话")
	}
}
