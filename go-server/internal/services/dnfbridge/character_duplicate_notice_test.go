package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/characterdata"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDuplicateCharacterNameNoticeUsesEachNativeOpcode(t *testing.T) {
	for _, source := range []string{"同账号", "其它账号", "改名保留"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			group := testRepositoryGroup()
			store := group.Character.(*fakeCharacterStore)
			const name = "对对对"
			if source == "改名保留" {
				if err := group.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: dnfrepo.CharacterNameMutationScope, Values: map[string]string{characterdata.NameReservationKey(name): strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}); err != nil {
					t.Fatal(err)
				}
			} else {
				account := "dnf:1"
				if source == "其它账号" {
					account = "dnf:2"
				}
				store.records["7"] = dnfrepo.CharacterRecord{CharacterID: "7", AccountID: account, Name: name, Job: "0", Level: 1, Stats: map[string]int64{"gold": 123}}
			}
			before := make(map[string]dnfrepo.CharacterRecord)
			for id, record := range store.records {
				before[id] = dnfrepo.CloneCharacter(record)
			}
			reservation, _, _ := group.Settings.Load(ctx, dnfrepo.CharacterNameMutationScope)
			service := &Service{options: options{accountPrefix: "dnf:"}, repositoryProvider: func() (dnfrepo.Group, bool) { return group, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, accountID: "dnf:1", channel: channelcatalog.Channel{ID: 10}}
			nameRaw, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
			if err != nil {
				t.Fatal(err)
			}
			var nameRequest packetWriter
			nameRequest.writeRawDstr(nameRaw)
			if err := service.handleGameCheckName(session, nameRequest.bytes()); err != nil {
				t.Fatal(err)
			}
			// 当前EXE 01facd00的0x14分支读取中文资源33195“已存在的角色名”；
			// 0被直接忽略，op5的0x12也不能当作op692的错误码。
			assertDuplicateCharacterNamePacket(t, conn, 692, []byte{0, 0x14})
			conn.write.Reset()
			var createRequest packetWriter
			createRequest.writeByte(0)
			createRequest.writeRawDstr(nameRaw)
			if err := service.handleUpperCreateCharacter(session, createRequest.bytes()); err != nil {
				t.Fatal(err)
			}
			// 当前EXE 01fb4040对0x12读取资源683“含有屏蔽字或已存在的角色名”。
			assertDuplicateCharacterNamePacket(t, conn, 5, []byte{0, 0x12})
			if !reflect.DeepEqual(store.records, before) || store.nextID != 1 {
				t.Fatal("重名拒绝改变了角色或分配了新编号")
			}
			after, _, _ := group.Settings.Load(ctx, dnfrepo.CharacterNameMutationScope)
			if !reflect.DeepEqual(after, reservation) {
				t.Fatal("重名提示改变了名字保留记录")
			}
		})
	}
}

func assertDuplicateCharacterNamePacket(t *testing.T, conn *bufferConn, opcode uint16, want []byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 || packet.Header.Classification != dnfproto.DefaultChannelClassification || packet.Header.MsgID != opcode || !bytes.Equal(packet.Body, want) {
		t.Fatalf("重名应答op%d：header=%+v body=%x trailing=%d，期望%x", opcode, packet.Header, packet.Body, len(rest), want)
	}
}

type duplicateCharacterNameCommitFailure struct {
	err   error
	calls int
}

func (u *duplicateCharacterNameCommitFailure) WithinCharacterCreation(context.Context, string, dnfrepo.Group, func(dnfrepo.Group) error) error {
	u.calls++
	return u.err
}

func TestDuplicateCharacterNameNoticeAfterTransactionConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code byte
	}{
		{"事务内重名", fmt.Errorf("名字竞争：%w", dnfrepo.ErrCharacterNameExists), 0x12},
		{"普通存储失败保持原码", errors.New("测试存储失败"), 0x14},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := testRepositoryGroup()
			uow := &duplicateCharacterNameCommitFailure{err: test.err}
			group.CharacterCreate = uow
			service := &Service{options: options{accountPrefix: "dnf:"}, characterStats: testCharacterStatTable(t), repositoryProvider: func() (dnfrepo.Group, bool) { return group, true }}
			prepareTestCharacterInitialization(service, 15)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, accountID: "dnf:1", channel: channelcatalog.Channel{ID: 10}}
			if err := service.handleUpperCreateCharacter(session, buildCreateRequest(15, "hero")); err != nil {
				t.Fatal(err)
			}
			assertDuplicateCharacterNamePacket(t, conn, 5, []byte{0, test.code})
			if uow.calls != 1 || len(group.Character.(*fakeCharacterStore).records) != 0 {
				t.Fatal("没有命中事务拒绝或错误保存了角色")
			}
		})
	}
}
