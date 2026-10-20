package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 独立核验当前 EXE 的消费契约：只允许一份 class1/op36，正文恰为
// success=0、error=7、目标城镇、目标区域；不能夹带成功过图或任务完成通知。
func assertTownQuestRequirementFailure(t *testing.T, session *gameSession, town, area byte, headerSize int) {
	t.Helper()
	packet, rest := splitGameServerUpperPacketWithHeader(t, session.conn.(*bufferConn).write.Bytes(), headerSize)
	want := []byte{0, 7, town, area}
	if packet.Header.Classification != 1 || packet.Header.MsgID != 36 || !bytes.Equal(packet.Body, want) || len(rest) != 0 {
		t.Fatalf("任务门槛失败回包不符：header=%+v body=%x trailing=%x，期望 class1/op36 body=%x", packet.Header, packet.Body, rest, want)
	}
}

func TestTownQuestNoticeRejectsOrdinaryGateWithoutChangingState(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	before, _, _ := repositories.Character.Load(context.Background(), "29")
	if err := service.handleTownSetUserArea(session, buildTownMoveRequest(38, 3, 1, 2, 5)); err != nil {
		t.Fatal(err)
	}
	assertTownQuestRequirementFailure(t, session, 38, 3, dnfproto.GameServerUpperHeaderSize)
	after, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("被拒绝的移动改变了角色：found=%v err=%v", found, err)
	}
	if _, found, err := repositories.Quest.Load(context.Background(), "29"); err != nil || found {
		t.Fatalf("被拒绝的移动写入了任务账：found=%v err=%v", found, err)
	}
}

func TestTownQuestNoticeNativeHeaderKeepsAcceptedQuestUnchanged(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	service.options.gameUpperHeader = gameUpperHeaderServer16
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	quest := dnfrepo.QuestRecord{
		CharacterID: "29",
		States:      map[int64]dnfrepo.QuestState{3156: {Status: "accepted"}},
	}
	if err := repositories.Quest.Save(context.Background(), quest); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repositories.Quest.Load(context.Background(), "29")
	if err := service.handleTownSetUserArea(session, buildTownTeleportArrayRequest(39, 2, 520, 271, 5)); err != nil {
		t.Fatal(err)
	}
	assertTownQuestRequirementFailure(t, session, 39, 2, dnfproto.GameServerUpperHeaderSize16)
	after, found, err := repositories.Quest.Load(context.Background(), "29")
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("已接但未完成的任务被改动：found=%v err=%v", found, err)
	}
}

type townQuestNoticeReadFailure struct {
	dnfrepo.QuestRepository
}

func (r townQuestNoticeReadFailure) Load(context.Context, string) (dnfrepo.QuestRecord, bool, error) {
	return dnfrepo.QuestRecord{}, false, errors.New("测试任务读取失败")
}

func TestTownQuestNoticeDoesNotMisreportRepositoryFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "读取失败", true: "仓储缺失"}[missing], func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			service.options.currentClientVersion = "official-24.9"
			before, _, _ := repositories.Character.Load(context.Background(), "29")
			if missing {
				repositories.Quest = nil
			} else {
				repositories.Quest = townQuestNoticeReadFailure{repositories.Quest}
			}
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
			if err := service.handleTownSetUserArea(session, buildTownTeleportArrayRequest(39, 2, 520, 271, 5)); err != nil {
				t.Fatal(err)
			}
			if got := session.conn.(*bufferConn).write.Bytes(); len(got) != 0 {
				t.Fatalf("任务读取故障被误报为玩家未完成任务：%x", got)
			}
			after, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatalf("任务读取故障改变了角色：found=%v err=%v", found, err)
			}
		})
	}
}

func TestTownQuestNoticeOfficialTextUsesActualMissingQuestWithoutTownName(t *testing.T) {
	for _, test := range []struct {
		name, title, want string
		missingCatalog    bool
	}{
		{name: "任务原名", title: "朝着新的冒险", want: "前往所选区域，需要完成[朝着新的冒险]任务。"},
		{name: "无目录", missingCatalog: true, want: townQuestGenericText},
		{name: "无任务名", want: townQuestGenericText},
		{name: "超长任务名", title: strings.Repeat("任务", 100), want: townQuestGenericText},
		{name: "不能编码的任务名", title: "任务😀", want: townQuestGenericText},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			service.options.currentClientVersion = "official-24.9"
			service.options.gameUpperHeader = gameUpperHeaderServer16
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			if !test.missingCatalog {
				index, err := dnfpvf.Build(context.Background(), questListTestSource{
					dnfquest.DefaultList: "3156 `notice.qst`",
					"n_quest/notice.qst": "[name]\n`" + test.title + "`\n[grade]\n`[epic]`\n",
				}, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
				if err != nil {
					t.Fatal(err)
				}
				service.questCatalog, err = dnfquest.Load(context.Background(), index)
				if err != nil {
					t.Fatal(err)
				}
			}
			quest := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{
				3155: {Status: "completed"}, 3156: {Status: "accepted"},
			}}
			if err := repositories.Quest.Save(context.Background(), quest); err != nil {
				t.Fatal(err)
			}
			beforeQuest, _, _ := repositories.Quest.Load(context.Background(), "29")
			beforeCharacter, _, _ := repositories.Character.Load(context.Background(), "29")
			if err := service.handleTownSetUserArea(session, buildTownTeleportArrayRequest(39, 2, 520, 271, 5)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacketWithHeader(t, session.conn.(*bufferConn).write.Bytes(), 16)
			if ack.Header.Classification != 1 || ack.Header.MsgID != 36 || !bytes.Equal(ack.Body, []byte{0, 0, 39, 2}) {
				t.Fatalf("静默拒绝确认不符：header=%+v body=%x", ack.Header, ack.Body)
			}
			message, trailing := splitGameServerUpperPacketWithHeader(t, rest, 16)
			if message.Header.Classification != 0 || message.Header.MsgID != 12 || len(trailing) != 0 || len(message.Body) < 12 {
				t.Fatalf("系统提示包不符：header=%+v body=%x trailing=%x", message.Header, message.Body, trailing)
			}
			body := message.Body
			length := int(binary.LittleEndian.Uint32(body[4:8]))
			if !bytes.Equal(body[:4], []byte{0, 0xff, 0xff, 0}) || length > 240 || len(body) != length+12 || binary.LittleEndian.Uint32(body[8+length:]) != 0 {
				t.Fatalf("系统文字边界或发送者不符：%x", body)
			}
			decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body[8 : 8+length])
			if err != nil || string(decoded) != test.want {
				t.Fatalf("提示=%q err=%v，期望=%q", decoded, err, test.want)
			}
			afterQuest, _, _ := repositories.Quest.Load(context.Background(), "29")
			afterCharacter, _, _ := repositories.Character.Load(context.Background(), "29")
			if !reflect.DeepEqual(beforeQuest, afterQuest) || !reflect.DeepEqual(beforeCharacter, afterCharacter) {
				t.Fatal("提示文字修改了任务或角色位置")
			}
		})
	}
}

func TestTownQuestNoticeFirstMissingQuestSkipsCompletedPrerequisite(t *testing.T) {
	service, _, repositories := newTownMoveTest(t)
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "29", States: map[int64]dnfrepo.QuestState{3155: {Status: "completed"}, 3156: {Status: "accepted"}},
	}); err != nil {
		t.Fatal(err)
	}
	completed, missing, err := service.townAreaQuestRequirement(context.Background(), repositories, "29", []int64{-1, 3155, 3156})
	if err != nil || completed || missing != 3156 {
		t.Fatalf("首个未完成任务=%d completed=%v err=%v", missing, completed, err)
	}
}
