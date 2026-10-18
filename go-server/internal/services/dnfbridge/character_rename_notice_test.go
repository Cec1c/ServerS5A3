package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentCharacterRenameDuplicateNoticePreservesRightAndAllowsRetry(t *testing.T) {
	for _, source := range []string{"同账号角色", "其它账号角色", "旧名保留"} {
		t.Run(source, func(t *testing.T) {
			fixture := newBridge135Fixture(t, "official-24.9")
			ctx := context.Background()
			if source == "旧名保留" {
				now := fixture.clock.Now().UTC()
				if err := characterdata.ReservePreviousName(ctx, fixture.repos.Settings, bridge135NewName, now, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			} else {
				account := bridge135AccountID
				if source == "其它账号角色" {
					account = "dnf:2"
				}
				if err := fixture.repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "88", AccountID: account, Slot: 0, Name: bridge135NewName}); err != nil {
					t.Fatal(err)
				}
			}
			before := fixture.record(t)
			reserved := bridge135Reservations(t, fixture.repos)
			fixture.dispatch(t, bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1))
			// 当前EXE 01facf00的0x14分支弹出资源683“含有屏蔽字或已存在的角色名”；
			// 0x02则误报数据库问题，不能借用创建角色op5的0x12。
			requireBridge135Ack(t, fixture.conn, []byte{0, 0x14})
			if !reflect.DeepEqual(fixture.record(t), before) || !reflect.DeepEqual(bridge135Reservations(t, fixture.repos), reserved) {
				t.Fatal("重名拒绝改变了角色、改名资格或名字保留记录")
			}
			if _, found, err := fixture.repos.Settings.Load(ctx, "character:"+bridge135CharacterID+":rename"); err != nil || found {
				t.Fatalf("重名拒绝留下成功回执：found=%t err=%v", found, err)
			}
			if len(fixture.service.characterAssetReservations) != 0 {
				t.Fatal("重名拒绝未释放角色改名占用")
			}
			fixture.conn.write.Reset()
			fixture.dispatch(t, bridge135Body(t, 0, bridge135OldName, "另一个新名字", 1))
			requireBridge135Ack(t, fixture.conn, []byte{1})
			if after := fixture.record(t); after.Name != "另一个新名字" || after.Stats[characterdata.RenameEntitlementStat] != 0 {
				t.Fatal("重名后换可用名字未正常使用原有改名资格")
			}
		})
	}
}

type characterRenameNoticeTransactionFailure struct {
	dnfrepo.CharacterCreationUnitOfWork
	err   error
	calls int
}

func (u *characterRenameNoticeTransactionFailure) WithinCharacterNameMutation(context.Context, string, dnfrepo.Group, func(dnfrepo.Group) error) error {
	u.calls++
	return u.err
}

func TestCurrentCharacterRenameNoticeDistinguishesTransactionErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code byte
	}{
		{"事务内重名", dnfrepo.ErrCharacterNameExists, 0x14},
		{"包装后的事务内重名", fmt.Errorf("名字竞争：%w", dnfrepo.ErrCharacterNameExists), 0x14},
		{"普通存储错误保持原提示", errors.New("测试数据库写入失败"), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBridge135Fixture(t, "official-24.9")
			uow := &characterRenameNoticeTransactionFailure{CharacterCreationUnitOfWork: fixture.repos.CharacterCreate, err: test.err}
			fixture.repos.CharacterCreate = uow
			fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fixture.repos, true }
			before := fixture.record(t)
			fixture.dispatch(t, bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1))
			requireBridge135Ack(t, fixture.conn, []byte{0, test.code})
			if uow.calls != 1 || !reflect.DeepEqual(fixture.record(t), before) {
				t.Fatal("未命中事务拒绝或失败后改变了角色")
			}
		})
	}
}
