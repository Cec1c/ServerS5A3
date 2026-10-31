package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentTagTournamentHotkeyNotification uint16 = 1394

type currentTagTournamentHotkeyProjection struct {
	source repo.SettingsRecord
	found  bool
	bank   byte
	body   []byte
}

// 1456210保存现用bank，临时选正文bank，22616B0读取完整配置，再恢复bank。
// 14572E0在Tag完成后依据真实job选0/1；1394的首字节是bank，不是队员序号。
func (s *Service) prepareCurrentTagTournamentHotkeys(ctx context.Context, g repo.Group, row *currentTagTournamentCharacterProjection) error {
	if g.Settings == nil {
		return fmt.Errorf("帝国竞技场缺少独立键位仓储")
	}
	scope := characterHotkeyConfigScope(row.character.CharacterID)
	r, found, err := g.Settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	var config []byte
	if found {
		if r.Scope != scope {
			return fmt.Errorf("帝国竞技场键位不属于该队员")
		}
		config, err = hex.DecodeString(r.Values["hotkey_config_hex"])
		if err != nil {
			return fmt.Errorf("帝国竞技场键位存档编码损坏")
		}
	} else {
		config, err = s.nativeDefaultConfig(ctx, row.job == csharpCreatorMageJob)
		if err != nil {
			return err
		}
	}
	if len(config) == 0 || len(config) >= 0x578 {
		return fmt.Errorf("帝国竞技场键位正文为空或超出原生容量")
	}
	bank := byte(0)
	if row.job == csharpCreatorMageJob {
		bank = 1
	}
	row.hotkeys = &currentTagTournamentHotkeyProjection{source: repo.CloneSettings(r), found: found, bank: bank, body: buildAccountHotkeyNoticeBody(bank, config)}
	return nil
}

func currentTagTournamentHotkeySourceCurrent(ctx context.Context, g repo.Group, row currentTagTournamentCharacterProjection) error {
	if row.hotkeys == nil || g.Settings == nil {
		return fmt.Errorf("帝国竞技场缺少该队员的独立键位投影")
	}
	r, found, err := g.Settings.Load(ctx, characterHotkeyConfigScope(row.character.CharacterID))
	if err != nil {
		return err
	}
	if found != row.hotkeys.found || !reflect.DeepEqual(r, row.hotkeys.source) {
		return fmt.Errorf("帝国竞技场准备期间键位存档变化，请重试")
	}
	return nil
}

func (s *Service) sendCurrentTagTournamentHotkeys(p *gameSession, b *currentTagTournamentBattle, role int) error {
	if b.prepared == nil {
		return nil
	}
	if role < 0 || role >= len(b.prepared.characters) {
		return fmt.Errorf("帝国竞技场键位队员越界")
	}
	row := b.prepared.characters[role]
	if row.hotkeys == nil || len(row.hotkeys.body) == 0 {
		return fmt.Errorf("帝国竞技场缺少已冻结的键位正文")
	}
	return s.sendGameUpperRawClass(p, currentTagTournamentHotkeyNotification, row.hotkeys.body, 0)
}
