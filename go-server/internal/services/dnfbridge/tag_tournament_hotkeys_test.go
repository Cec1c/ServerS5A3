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
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

type tagHotkeyLateChangingRepository struct {
	repo.SettingsRepository
	reads int
}

func (r *tagHotkeyLateChangingRepository) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	v, found, err := r.SettingsRepository.Load(ctx, scope)
	if scope == characterHotkeyConfigScope("60") {
		r.reads++
		if r.reads > 4 && err == nil {
			v = repo.CloneSettings(v)
			v.Values["hotkey_config_hex"] = hex.EncodeToString([]byte("I01\t99\n"))
		}
	}
	return v, found, err
}

func TestTagHotkeysCreatorAndNormalRolesLoadBeforeTagAndRestoreOnReturn(t *testing.T) {
	ctx := context.Background()
	var fixtures []map[string]any
	for _, creatorRole := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(creatorRole), func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			var saved [2]repo.SettingsRecord
			for i, id := range []string{"29", "60"} {
				if i == creatorRole {
					ch, _, _ := g.Character.Load(ctx, id)
					ch.Job, ch.Stats["grow_type"] = "10", 0x20
					if err := g.Character.Save(ctx, ch); err != nil {
						t.Fatal(err)
					}
				}
				config := []byte(fmt.Sprintf("I01\t%d\nI02\t%d\n", 30+i, 40+i))
				r := repo.SettingsRecord{Scope: characterHotkeyConfigScope(id), Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(config)}}
				if err := g.Settings.Save(ctx, r); err != nil {
					t.Fatal(err)
				}
				saved[i], _, _ = g.Settings.Load(ctx, r.Scope)
			}
			tagPlayChallenge(t, s, p, tagtournament.Normal)
			b := p.tagTournamentBattle
			for i, row := range b.prepared.characters {
				bank := byte(0)
				if i == creatorRole {
					bank = 1
				}
				if row.hotkeys == nil || row.hotkeys.bank != bank || row.hotkeys.body[0] != bank {
					t.Fatal("角色键位bank错误", i)
				}
				actor, err := freshCurrentTagTournamentActor(row)
				if err != nil || actor.shieldDeck != ([5]uint32{}) {
					t.Fatal("普通/缔造者误用守护者盾栏", err)
				}
				fixtures = append(fixtures, map[string]any{"body": row.hotkeys.body, "bank": bank, "job": row.job})
			}
			seen := 0
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = tail
				if packet.Header.MsgID == 1394 {
					seen++
					if !bytes.Equal(packet.Body, b.prepared.characters[0].hotkeys.body) {
						t.Fatal("最终落位键位不属于本体")
					}
				}
			}
			if seen != 1 {
				t.Fatal("进图最终落位没有同步键位", seen)
			}
			tagPlayMoveNext(t, s, p)
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			var order []uint16
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = tail
				if packet.Header.MsgID == 1394 || packet.Header.MsgID == 1402 {
					order = append(order, packet.Header.MsgID)
				}
				if packet.Header.MsgID == 1394 && !bytes.Equal(packet.Body, b.prepared.characters[1].hotkeys.body) {
					t.Fatal("切人使用了本体键位")
				}
				if packet.Header.MsgID == 1392 {
					t.Fatal("切人重置了连招")
				}
			}
			if !reflect.DeepEqual(order, []uint16{1394, 1402}) || b.run.Snapshot().Active != 1 {
				t.Fatal("切人前未加载目标角色配置", order)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.closeCurrentTagTournamentForReturn(p); err != nil {
				t.Fatal(err)
			}
			seen = 0
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = tail
				if packet.Header.MsgID == s.currentAccountHotkeyNoticeID() {
					seen++
					if !bytes.Equal(packet.Body, b.prepared.characters[0].hotkeys.body) {
						t.Fatal("离场没有恢复本体bank和正文")
					}
				}
			}
			if seen != 1 || p.selectedCharacterID != 29 || p.characterGeneration != 1 {
				t.Fatal("离场污染身份或缺少恢复通知")
			}
			for _, before := range saved {
				after, _, _ := g.Settings.Load(ctx, before.Scope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("键位投影改写存档")
				}
			}
		})
	}
	if target := os.Getenv("DNFBRIDGE_TAG_HOTKEY_FIXTURES"); target != "" {
		raw, _ := json.MarshalIndent(fixtures, "", "  ")
		if err := os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTagHotkeysInvalidOrChangingProfileRefusesBeforePayment(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"损坏", "空正文", "超容量", "准备期间保存", "地图准备后保存", "读取失败"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			scope := characterHotkeyConfigScope("60")
			r, _, _ := g.Settings.Load(ctx, scope)
			switch scenario {
			case "损坏":
				r.Values["hotkey_config_hex"] = "XX"
			case "空正文":
				r.Values["hotkey_config_hex"] = ""
			case "超容量":
				r.Values["hotkey_config_hex"] = strings.Repeat("41", 0x578)
			case "准备期间保存", "读取失败":
				g.Settings = &tagOptionsChangingRepository{SettingsRepository: g.Settings, scope: scope, fail: scenario == "读取失败"}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			case "地图准备后保存":
				g.Settings = &tagHotkeyLateChangingRepository{SettingsRepository: g.Settings}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			}
			if err := g.Settings.Save(ctx, r); err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Account.Load(ctx, "a")
			character, _, _ := g.Character.Load(ctx, "29")
			if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Account.Load(ctx, "a")
			actual, _, _ := g.Character.Load(ctx, "29")
			if p.tagTournamentBattle != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, actual) {
				t.Fatal("损坏/变化配置仍入场或扣费")
			}
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = tail
				if packet.Header.MsgID == 1389 || packet.Header.MsgID == 1394 {
					t.Fatal("拒绝后仍创建角色")
				}
			}
		})
	}
}

func TestTagBattleConfigurationAndShieldSavesCannotWriteToRootCharacter(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, opcode := range []uint16{currentSaveGameOption2Opcode, currentSaveCharacterOptionOpcode, uint16(dnfenum.CmdPacketChangeDeckInfo)} {
			t.Run(fmt.Sprintf("%s_%d", transport, opcode), func(t *testing.T) {
				s, p, g := tagPlayFixture(t)
				tagPlayChallenge(t, s, p, tagtournament.Normal)
				tagPlayMoveNext(t, s, p)
				if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
					t.Fatal(err)
				}
				before, _, _ := g.Settings.Load(context.Background(), characterHotkeyConfigScope("29"))
				ch, _, _ := g.Character.Load(context.Background(), "29")
				p.conn.(*bufferConn).write.Reset()
				body := append([]byte{0, 2, 0, 0}, bytes.Repeat([]byte{1}, 512)...)
				if opcode == currentSaveGameOption2Opcode {
					config := []byte("I01\t99\n")
					body = make([]byte, 4+len(config))
					binary.LittleEndian.PutUint32(body, uint32(len(config)))
					copy(body[4:], config)
				}
				var handled bool
				var err error
				if transport == "legacy" {
					handled, err = currentGameplayModules.DispatchLegacy(s, p, opcode, body)
				} else {
					handled, err = currentGameplayModules.DispatchUpper(s, p, opcode, 1, body)
				}
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
				if opcode == uint16(dnfenum.CmdPacketChangeDeckInfo) {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
					if packet.Header.MsgID != opcode || packet.Body[0] != 0 || len(rest) != 0 {
						t.Fatal(packet)
					}
				} else if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("无ACK的配置保存发送了猜测应答")
				}
				after, _, _ := g.Settings.Load(context.Background(), before.Scope)
				actual, _, _ := g.Character.Load(context.Background(), "29")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ch, actual) {
					t.Fatal("备用配置写入本体")
				}
				if opcode == currentSaveGameOption2Opcode {
					p.tagTournamentBattle = nil
					if err := s.handleSaveGameOption2(p, body); err != nil {
						t.Fatal(err)
					}
					control, _, _ := g.Settings.Load(context.Background(), before.Scope)
					if control.Values["hotkey_config_hex"] != hex.EncodeToString(body[4:]) {
						t.Fatal("有效对照正文未走正常保存")
					}
				}
			})
		}
	}
}

type tagHotkeyMissingRepository struct{ repo.SettingsRepository }

func (r tagHotkeyMissingRepository) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	if scope == characterHotkeyConfigScope("29") || scope == characterHotkeyConfigScope("60") {
		return repo.SettingsRecord{}, false, nil
	}
	return r.SettingsRepository.Load(ctx, scope)
}

func TestRealPVFTagHotkeyDefaultsUseEachNativeJobBankReadOnly(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	s, p, g, roster := tagCharacterProjectionFixture(t)
	s.options.pvfPath = path
	roster[1].Job, roster[1].Stats["grow_type"] = "10", 0x20
	if err := g.Character.Save(context.Background(), roster[1]); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Settings.Load(context.Background(), characterHotkeyConfigScope("60"))
	g.Settings = tagHotkeyMissingRepository{g.Settings}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		if row.hotkeys == nil || row.hotkeys.found || row.hotkeys.bank != byte(i) || len(row.hotkeys.body) <= 5 {
			t.Fatal("未使用角色原生默认bank", i)
		}
	}
	if bytes.Equal(rows[0].hotkeys.body[5:], rows[1].hotkeys.body[5:]) {
		t.Fatal("普通与缔造者默认配置混用")
	}
	after, _, _ := g.Settings.(tagHotkeyMissingRepository).SettingsRepository.Load(context.Background(), before.Scope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("无存档默认值写入仓储")
	}
}
