package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func absoluteAvatarFixture(count int, grade int64) (bridgePVFSource, map[string]dnfrepo.ItemStack, []byte) {
	s := currentAvatarCompoundTestPVF()
	s["stackable/stackable.lst"] += "77 `dream.stk`\n"
	s["stackable/dream.stk"] = fmt.Sprintf("[stackable type] `[etc]` 0\n[action type] `[absolute bind cube]` 91 %d 2 0 0\n[/action type]\n[usable job] `[swordman]`\n[/usable job]\n", count)
	s[currentAvatarCompoundTestConfigPath] = strings.Replace(s[currentAvatarCompoundTestConfigPath], "[upper grade]\n2", "[upper grade]\n"+fmt.Sprint(grade), 1)
	s["equipment/avatar_hat_a.equ"] = fmt.Sprintf("[equipment type] `[hat avatar]`\n[grade] %d\n", grade)
	s[currentAbsoluteAvatarMapPath] = "[absolute bind cube]\n91\n[absolute bind plus rare]\n`[swordman]` `hat` 41003\n[/absolute bind plus rare]\n[/absolute bind cube]\n"
	slots := map[string]dnfrepo.ItemStack{"0:9": {ItemID: 77, Count: 2}}
	b := make([]byte, 144)
	binary.LittleEndian.PutUint16(b[13:], 9)
	binary.LittleEndian.PutUint32(b[16:], 41003)
	binary.LittleEndian.PutUint16(b[20:], 4)
	binary.LittleEndian.PutUint16(b[22:], uint16(count))
	for i := 0; i < count; i++ {
		slots[fmt.Sprintf("1:%d", 40+i)] = dnfrepo.ItemStack{ItemID: 41001, Count: 1}
		binary.LittleEndian.PutUint16(b[24+6*i:], uint16(40+i))
		binary.LittleEndian.PutUint32(b[26+6*i:], 41001)
	}
	return s, slots, b
}

func TestCurrentAbsoluteAvatarPVFCountGradeAndNativeReply(t *testing.T) {
	for _, n := range []int{3, 8} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, slots, b := absoluteAvatarFixture(n, 4) // 用变化的数量、品质和套装键证明不写死8/2/4。
			repos, cat := mustAvatarCompoundLiveAssets(t, s, "0", slots)
			svc := currentAvatarCompoundTestService(repos, cat)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBindPlus), b); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 1119 || ack.Header.Classification != 1 || len(ack.Body) != 55 || ack.Body[0] != 1 {
				t.Fatalf("原生回包=%+v %x", ack.Header, ack.Body)
			}
			refresh, rest := splitGameServerUpperPacket(t, trailing)
			if refresh.Header.MsgID != 14 || refresh.Header.Classification != 0 || len(rest) != 0 || refresh.Body[0] != 0 || binary.LittleEndian.Uint16(refresh.Body[1:]) != 1 || binary.LittleEndian.Uint16(refresh.Body[3:]) != 9 || binary.LittleEndian.Uint32(refresh.Body[5:]) != 77 || binary.LittleEndian.Uint32(refresh.Body[9:]) != 1 {
				t.Fatalf("梦想只应更新实际合成器槽，不能重建原生结果装扮: %x", trailing)
			}
			// 独立按原生偏移读回，不以生产builder自身充当期望。
			p := ack.Body[1:]
			if binary.LittleEndian.Uint16(p[4:]) != 40 || binary.LittleEndian.Uint32(p[6:]) != 41003 || binary.LittleEndian.Uint16(p[10:]) != 4 || binary.LittleEndian.Uint16(p[12:]) != 1 {
				t.Fatalf("原生字段错位: %x", p)
			}
			for i := 0; i < 8; i++ {
				want := int16(-1)
				if i < n {
					want = int16(40 + i)
				}
				if int16(binary.LittleEndian.Uint16(p[14+i*2:])) != want {
					t.Fatal("消耗槽错误")
				}
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if after.Slots["0:9"].Count != 1 || after.Slots["1:40"].ItemID != 41003 || after.Slots["1:40"].Extra["ext_data0"] != "4" || len(after.Slots) != 2 {
				t.Fatalf("扣料或产出错误: %+v", after.Slots)
			}
			conn.write.Reset()
			if err := svc.handleCurrentAbsoluteAvatar(session, b); err != nil {
				t.Fatal(err)
			}
			again, _, _ := repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(after.Slots, again.Slots) {
				t.Fatal("重复请求再次扣料")
			}
		})
	}
}

func TestCurrentAbsoluteAvatarRejectsWithoutAssetsChanging(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(bridgePVFSource, map[string]dnfrepo.ItemStack, []byte) []byte
	}{
		{"短包", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte { return b[:72] }},
		{"重复材料槽", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			copy(b[30:32], b[24:26])
			return b
		}},
		{"缺材料", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte { delete(m, "1:47"); return b }},
		{"伪造材料", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			binary.LittleEndian.PutUint32(b[26:], 41002)
			return b
		}},
		{"品质不符", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			s["equipment/avatar_hat_a.equ"] = "[equipment type] `[hat avatar]`\n[grade] 1\n"
			return b
		}},
		{"普通合成器", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			m["0:9"] = dnfrepo.ItemStack{ItemID: 21, Count: 2}
			return b
		}},
		{"跨套产出", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			binary.LittleEndian.PutUint32(b[16:], 41004)
			return b
		}},
		{"跨职业", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			s["stackable/dream.stk"] = strings.ReplaceAll(s["stackable/dream.stk"], "[swordman]", "[mage]")
			return b
		}},
		{"PVF数量变化", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			s["stackable/dream.stk"] = strings.ReplaceAll(s["stackable/dream.stk"], "91 8 2", "91 7 2")
			return b
		}},
		{"缺产出配置", func(s bridgePVFSource, m map[string]dnfrepo.ItemStack, b []byte) []byte {
			delete(s, currentAbsoluteAvatarMapPath)
			return b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m, b := absoluteAvatarFixture(8, 2)
			b = tc.alter(s, m, b)
			repos, cat := mustAvatarCompoundLiveAssets(t, s, "0", m)
			before, _, _ := repos.Inventory.Load(context.Background(), "19")
			conn := &bufferConn{}
			svc := currentAvatarCompoundTestService(repos, cat)
			if err := svc.handleCurrentAbsoluteAvatar(&gameSession{conn: conn, selectedCharacterID: 19}, b); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 0x16}) || len(trailing) != 0 {
				t.Fatalf("拒绝回包 %x", conn.write.Bytes())
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatal("拒绝后资产改变")
			}
		})
	}
}

type absoluteAvatarFailingAssets struct {
	base dnfrepo.AccountCharacterAssetUnitOfWork
}

func (f absoluteAvatarFailingAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return f.base.WithinAccountCharacterAssets(ctx, account, character, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return errors.New("模拟提交失败")
	})
}

func TestCurrentAbsoluteAvatarCommitFailureRollsBackAllAssets(t *testing.T) {
	s, m, b := absoluteAvatarFixture(8, 2)
	repos, cat := mustAvatarCompoundLiveAssets(t, s, "0", m)
	before, _, _ := repos.Inventory.Load(context.Background(), "19")
	repos.AccountAssets = absoluteAvatarFailingAssets{repos.AccountAssets}
	svc := currentAvatarCompoundTestService(repos, cat)
	conn := &bufferConn{}
	if err := svc.handleCurrentAbsoluteAvatar(&gameSession{conn: conn, selectedCharacterID: 19}, b); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 0x16}) || len(trailing) != 0 {
		t.Fatal("事务失败仍通知成功")
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before.Slots, after.Slots) {
		t.Fatal("事务失败没有整体回滚")
	}
}

func TestCurrentAbsoluteAvatarZeroKeyMustHaveMatchingPVFOutput(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key, mapped int
		target      uint32
		wantSuccess bool
	}{
		{"零键合法", 0, 0, 41003, true},
		{"负键拒绝", -1, -1, 41003, false},
		{"零键未声明拒绝", 0, 1, 41003, false},
		{"零键跨套目标拒绝", 0, 0, 41004, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m, b := absoluteAvatarFixture(3, 4)
			s["stackable/dream.stk"] = strings.ReplaceAll(s["stackable/dream.stk"], "91 3 2", fmt.Sprintf("%d 3 2", tc.key))
			s[currentAbsoluteAvatarMapPath] = strings.ReplaceAll(s[currentAbsoluteAvatarMapPath], "\n91\n", fmt.Sprintf("\n%d\n", tc.mapped))
			binary.LittleEndian.PutUint32(b[16:], tc.target)
			repos, cat := mustAvatarCompoundLiveAssets(t, s, "0", m)
			before, _, _ := repos.Inventory.Load(context.Background(), "19")
			conn := &bufferConn{}
			if err := currentAvatarCompoundTestService(repos, cat).handleCurrentAbsoluteAvatar(&gameSession{conn: conn, selectedCharacterID: 19}, b); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if (ack.Body[0] == 1) != tc.wantSuccess {
				t.Fatalf("零键处理结果错误：%x", ack.Body)
			}
			if tc.wantSuccess {
				if len(after.Slots) != 2 || after.Slots["0:9"].Count != 1 || after.Slots["1:40"].ItemID != int64(tc.target) {
					t.Fatalf("零键合成资产错误：%+v", after.Slots)
				}
			} else if !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatal("无效套装或产出改变了库存")
			}
		})
	}
}
