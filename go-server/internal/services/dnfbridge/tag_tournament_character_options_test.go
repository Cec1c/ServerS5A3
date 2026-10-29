package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type tagOptionsChangingRepository struct {
	repo.SettingsRepository
	scope string
	reads int
	fail  bool
}

func (r *tagOptionsChangingRepository) Load(ctx context.Context, key string) (repo.SettingsRecord, bool, error) {
	v, ok, err := r.SettingsRepository.Load(ctx, key)
	if key == r.scope {
		r.reads++
		if r.fail {
			return repo.SettingsRecord{}, false, errors.New("隔离角色选项读取失败")
		}
		if r.reads > 1 {
			v = repo.CloneSettings(v)
			v.Values["另一连接保存"] = "变化"
		}
	}
	return v, ok, err
}

func TestTagCharacterOptionsUseIndependentSavedBodiesAndNativeDefault(t *testing.T) {
	s, p, g, roster := tagCharacterProjectionFixture(t)
	ctx := context.Background()
	for i, id := range []string{"29", "60"} {
		blob := bytes.Repeat([]byte{byte(0x31 + i)}, 512)
		values := map[string]string{"character_option_hex": hex.EncodeToString(blob)}
		if i == 1 {
			frame := append([]byte{0, 2, 0, 0}, blob...)
			values = map[string]string{"character_option_body_hex": hex.EncodeToString(frame)}
		}
		if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: characterOptionScope(id), Values: values}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.prepareCurrentTagTournamentCharacters(ctx, p, roster)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := s.prepareCurrentTagTournamentCharacterOptions(ctx, p, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1+2*(1+4+512) || wire[0] != 2 || wire[1] != 0 || wire[518] != 1 || binary.LittleEndian.Uint32(wire[2:6]) != 512 || !bytes.Equal(wire[6:518], bytes.Repeat([]byte{0x31}, 512)) || !bytes.Equal(wire[523:], bytes.Repeat([]byte{0x32}, 512)) {
		t.Fatal("角色选项复制、紧凑序号或长度错位")
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("只读准备发送了创建通知")
	}
	if target := os.Getenv("DNFBRIDGE_TAG_OPTION_FIXTURES"); target != "" {
		raw, _ := json.Marshal([]map[string]any{{"body": wire, "selectors": []byte{0, 1}, "blobs": [][]byte{wire[6:518], wire[523:]}}})
		if err = os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, p, _, roster = tagCharacterProjectionFixture(t)
	rows, err = s.prepareCurrentTagTournamentCharacters(ctx, p, roster)
	if err != nil {
		t.Fatal(err)
	}
	wire, err = s.prepareCurrentTagTournamentCharacterOptions(ctx, p, rows)
	if err != nil || !bytes.Equal(wire, []byte{2, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}) {
		t.Fatal("无存档应保留两名角色各自原生默认对象", wire, err)
	}
}

func TestTagCharacterOptionsRejectCorruptSnapshotsBeforePayment(t *testing.T) {
	for _, scenario := range []string{"损坏十六进制", "超长正文", "原帧截断", "原帧多余尾巴", "缺少正文", "准备期间保存", "读取失败", "角色变化"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g, roster := tagCharacterProjectionFixture(t)
			ctx := context.Background()
			rows, err := s.prepareCurrentTagTournamentCharacters(ctx, p, roster)
			if err != nil {
				t.Fatal(err)
			}
			blob := bytes.Repeat([]byte{0x55}, 512)
			values := map[string]string{"character_option_hex": hex.EncodeToString(blob)}
			switch scenario {
			case "损坏十六进制":
				values["character_option_hex"] = "XX"
			case "超长正文":
				values["character_option_hex"] = hex.EncodeToString(append(blob, 1))
			case "原帧截断":
				values["character_option_body_hex"] = "0002000055"
			case "原帧多余尾巴":
				values["character_option_body_hex"] = hex.EncodeToString(append(append([]byte{0, 2, 0, 0}, blob...), 1))
			case "缺少正文":
				values = map[string]string{}
			case "准备期间保存", "读取失败":
				g.Settings = &tagOptionsChangingRepository{SettingsRepository: g.Settings, scope: characterOptionScope("60"), fail: scenario == "读取失败"}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			case "角色变化":
				ch := repo.CloneCharacter(rows[1].character)
				ch.Name = "修改后的队员"
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: characterOptionScope("60"), Values: values}); err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Account.Load(ctx, "a")
			if wire, err := s.prepareCurrentTagTournamentCharacterOptions(ctx, p, rows); err == nil || len(wire) != 0 {
				t.Fatal("损坏或变化的设置被当作完整快照")
			}
			after, _, _ := g.Account.Load(ctx, "a")
			if !reflect.DeepEqual(before, after) || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("准备失败改写账号或发包")
			}
		})
	}
}

func TestTagCharacterOptionsNativeCapacityAndSelectors(t *testing.T) {
	for _, rows := range [][]currentTagTournamentCharacterOption{nil, {{selector: 0}}, {{selector: 0}, {selector: 0}}, {{selector: 255}, {selector: 2}}, {{selector: 0, blob: make([]byte, 513)}, {selector: 2}}, {{selector: 0, blob: make([]byte, 511)}, {selector: 2}}} {
		if wire, err := buildCurrentTagTournamentCharacterOptions(rows); err == nil || len(wire) != 0 {
			t.Fatal("原生512字节对象允许越界写入", len(wire))
		}
	}
}
