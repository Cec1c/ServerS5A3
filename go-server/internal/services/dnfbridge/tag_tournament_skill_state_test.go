package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagSkillStateFixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := tagPlayFixture(t)
	for i, id := range []string{"29", "60"} {
		command := []byte{byte(i), 4}
		if err := g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: skillcmd.SkillCommandScope(id), Values: map[string]string{"command_10": hex.EncodeToString(command), "command_count": "1"}}); err != nil {
			t.Fatal(err)
		}
		index := uint16(20 + i)
		if err := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{fmt.Sprintf("0:%d", index): {ItemID: int64(500 + i), Count: 1}}}); err != nil {
			t.Fatal(err)
		}
		// 先经已有拥有者事务登记实物身份，投影阶段不再触发旧坐标绑定迁移。
		if err := buffswap.ApplyCurrent(context.Background(), g, id, p.accountID, buffswap.Command{Skill: 10, Slot: byte(i), List: 0, Index: index}); err != nil {
			t.Fatal(err)
		}
	}
	return s, p, g
}

func TestTagSkillStateProductionSendsIndependentCommandsAndFullBuffItems(t *testing.T) {
	s, p, g := tagSkillStateFixture(t)
	before, _, _ := g.Inventory.Load(context.Background(), "60")
	if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(tagtournament.Normal)}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle == nil {
		t.Fatal("生产挑战未创建")
	}
	state := p.tagTournamentBattle.prepared.skillState
	wantCommands := []byte{2, 0, 1, 6, 10, 0, 1, 2, 0, 4, 1, 1, 6, 10, 0, 2, 2, 1, 4}
	if !bytes.Equal(state.commands, wantCommands) {
		t.Fatal("技能指令未对应各自队员、槽位、等级", state.commands)
	}
	off := 1
	for i, selector := range []byte{0, 1} {
		if len(state.buffSwap)-off < 127 {
			t.Fatal("换装物品截断")
		}
		if state.buffSwap[off] != selector || binary.LittleEndian.Uint16(state.buffSwap[off+1:]) != 10 || state.buffSwap[off+3] != 1 || state.buffSwap[off+4] != byte(i) || state.buffSwap[off+5] != 0 || binary.LittleEndian.Uint16(state.buffSwap[off+6:]) != uint16(20+i) || binary.LittleEndian.Uint32(state.buffSwap[off+10:]) != uint32(500+i) {
			t.Fatal("Buff换装队员、技能、来源或完整物品错误", state.buffSwap)
		}
		off += 127
	}
	if off != len(state.buffSwap) {
		t.Fatal("换装尾部错位", off, len(state.buffSwap))
	}
	seen := map[uint16]int{}
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		seen[packet.Header.MsgID]++
		if packet.Header.MsgID == 1391 && !bytes.Equal(packet.Body, state.commands) || packet.Header.MsgID == 1393 && !bytes.Equal(packet.Body, state.buffSwap) {
			t.Fatal("生产下发正文不同")
		}
		rest = tail
	}
	if seen[1391] != 1 || seen[1393] != 1 || seen[1458] != 1 {
		t.Fatal("创建未发送两种独立资料", seen)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "60")
	if !reflect.DeepEqual(before, after) || p.selectedCharacterID != 29 || p.characterGeneration != 1 {
		t.Fatal("资料投影修改了库存或选中身份")
	}
	if target := os.Getenv("DNFBRIDGE_TAG_SKILL_STATE_FIXTURES"); target != "" {
		raw, _ := json.Marshal(map[string]any{"commands": state.commands, "buff_swap": state.buffSwap, "selectors": []byte{0, 1}})
		if err := os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(p.tagTournamentBattle.prepared.runtime.Request)); err != nil {
		t.Fatal(err)
	}
	seen = map[uint16]int{}
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		seen[packet.Header.MsgID]++
		rest = tail
	}
	if seen[1389] != 0 || seen[1390] != 0 || seen[1391] != 0 || seen[1393] != 0 {
		t.Fatal("进图落位重建了已初始化的帝国角色容器", seen)
	}
}

func TestTagSkillStateDefaultsAndStaleBuffBindingDoNotInventItems(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, p.tagTournamentSentRosterCharactersForTest(t, g))
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.prepareCurrentTagTournamentSkillState(context.Background(), p, rows)
	if err != nil || !bytes.Equal(state.commands, []byte{2, 0, 0, 1, 0}) || !bytes.Equal(state.buffSwap, []byte{2, 0, 0, 0, 0, 1, 0, 0, 0}) {
		t.Fatal("缺存档默认值错误", state, err)
	}
	saved := buffswap.State{WireProfile: buffswap.CurrentWireProfile, Skill: 10, Slots: map[byte]buffswap.Binding{0: {List: 0, Index: 20, ItemID: 999}}}
	raw, _ := json.Marshal(saved)
	if err := g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: buffswap.Scope("60"), Values: map[string]string{"state": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	state, err = s.prepareCurrentTagTournamentSkillState(context.Background(), p, rows)
	if err != nil || !bytes.Equal(state.buffSwap, []byte{2, 0, 0, 0, 0, 1, 10, 0, 0}) {
		t.Fatal("失效绑定被伪造物品或丢失独立技能", state, err)
	}
}

func (p *gameSession) tagTournamentSentRosterCharactersForTest(t *testing.T, g repo.Group) [2]repo.CharacterRecord {
	t.Helper()
	var result [2]repo.CharacterRecord
	for i, id := range []string{"29", "60"} {
		r, ok, err := g.Character.Load(context.Background(), id)
		if err != nil || !ok {
			t.Fatal(err)
		}
		result[i] = r
	}
	return result
}

func TestTagSkillStateRejectsBadOrChangingStoresBeforeFee(t *testing.T) {
	for _, scenario := range []string{"指令损坏", "指令超长", "指令变化", "换装损坏", "换装技能未学"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := tagSkillStateFixture(t)
			ctx := context.Background()
			before, _, _ := g.Character.Load(ctx, "29")
			scope := skillcmd.SkillCommandScope("60")
			switch scenario {
			case "指令损坏", "指令超长":
				r, _, _ := g.Settings.Load(ctx, scope)
				r.Values["command_10"] = "XX"
				if scenario == "指令超长" {
					r.Values["command_10"] = hex.EncodeToString(make([]byte, 33))
				}
				if err := g.Settings.Save(ctx, r); err != nil {
					t.Fatal(err)
				}
			case "指令变化":
				g.Settings = &tagOptionsChangingRepository{SettingsRepository: g.Settings, scope: scope}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			case "换装损坏":
				if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: buffswap.Scope("60"), Values: map[string]string{"state": "{"}}); err != nil {
					t.Fatal(err)
				}
			case "换装技能未学":
				state := buffswap.State{WireProfile: buffswap.CurrentWireProfile, Skill: 30}
				raw, _ := json.Marshal(state)
				if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: buffswap.Scope("60"), Values: map[string]string{"state": string(raw)}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Character.Load(ctx, "29")
			if p.tagTournamentBattle != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("失效资料仍扣费或创建")
			}
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == 1389 || packet.Header.MsgID == 1391 || packet.Header.MsgID == 1393 {
					t.Fatal("失败仍下发创建资料")
				}
				rest = tail
			}
		})
	}
}

func TestTagSkillStateFreezesCurrent1426RegistrationInBothDispatchers(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			s, p, g := tagSkillStateFixture(t)
			tagPlayChallenge(t, s, p, tagtournament.Normal)
			before, _, _ := g.Settings.Load(context.Background(), buffswap.Scope("29"))
			p.conn.(*bufferConn).write.Reset()
			var handled bool
			var err error
			if transport == "legacy" {
				handled, err = currentGameplayModules.DispatchLegacy(s, p, currentBuffSwapRegistrationID, []byte{10, 0, 0, 45, 255, 255})
			} else {
				handled, err = currentGameplayModules.DispatchUpper(s, p, currentBuffSwapRegistrationID, dnfproto.DefaultChannelClassification, []byte{10, 0, 0, 45, 255, 255})
			}
			if !handled || err != nil || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("当前换装登记没有由比赛限制静默接管", handled, err)
			}
			after, _, _ := g.Settings.Load(context.Background(), buffswap.Scope("29"))
			if !reflect.DeepEqual(before, after) {
				t.Fatal("比赛中改写换装配置")
			}
			p.tagTournamentBattle = nil
			if handled, _ := s.rejectCurrentTagTournamentMutation(p, gameplayRequest{Opcode: currentBuffSwapRegistrationID, Classification: dnfproto.DefaultChannelClassification}); handled {
				t.Fatal("休息室仍限制登记")
			}
			p.dungeon.runtime = nil
			if _, err := s.handleCurrentBuffSwap1426Command(p, currentBuffSwapRegistrationID, []byte{10, 0, 0, 45, 255, 255}); err != nil {
				t.Fatal(err)
			}
			after, _, _ = g.Settings.Load(context.Background(), buffswap.Scope("29"))
			if reflect.DeepEqual(before, after) {
				t.Fatal("合法对照请求没有移除换装，限制测试未覆盖真实写入")
			}
		})
	}
}

func TestTagSkillStateAvatarIncludesBothNativeExtensionBlocks(t *testing.T) {
	s, p, g := tagSkillStateFixture(t)
	ctx := context.Background()
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "60", Slots: map[string]repo.ItemStack{"1:50": {ItemID: 601, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	saved := buffswap.State{WireProfile: buffswap.CurrentWireProfile, Skill: 10, Slots: map[byte]buffswap.Binding{13: {List: 1, Index: 50, ItemID: 601}}}
	raw, _ := json.Marshal(saved)
	if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: buffswap.Scope("60"), Values: map[string]string{"state": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.prepareCurrentTagTournamentCharacters(ctx, p, p.tagTournamentSentRosterCharactersForTest(t, g))
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.buffSwap) != 263 || !bytes.Equal(state.buffSwap[len(state.buffSwap)-8:], make([]byte, 8)) {
		t.Fatal("装扮扩展块截断或多写", len(state.buffSwap))
	}
	if target := os.Getenv("DNFBRIDGE_TAG_SKILL_STATE_FIXTURES"); target != "" {
		raw, _ := json.Marshal(map[string]any{"buff_swap": state.buffSwap})
		if err := os.WriteFile(target+".avatar", raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
