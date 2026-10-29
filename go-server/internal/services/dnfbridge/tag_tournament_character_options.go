package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentTagTournamentCharacterOptionNotification uint16 = 1390

type currentTagTournamentCharacterOption struct {
	selector byte
	blob     []byte
}

// 145C940按count/容器位置/u32长度读取到该队员+460的独立0x200对象。
// 缺少存档时长度为0，保留原生构造器状态；不抄当前角色的512字节。
func buildCurrentTagTournamentCharacterOptions(rows []currentTagTournamentCharacterOption) ([]byte, error) {
	if len(rows) != 2 {
		return nil, fmt.Errorf("帝国竞技场角色选项必须对应两名队员")
	}
	var w packetWriter
	w.writeByte(byte(len(rows)))
	seen := map[byte]bool{}
	for _, row := range rows {
		if row.selector > 1 || seen[row.selector] || len(row.blob) != 0 && len(row.blob) != 512 {
			return nil, fmt.Errorf("帝国竞技场角色选项序号重复或正文超过原生容量")
		}
		seen[row.selector] = true
		w.writeByte(row.selector)
		w.writeUint32(uint32(len(row.blob)))
		w.writeBytes(row.blob)
	}
	return w.bytes(), nil
}

func currentTagTournamentSavedCharacterOption(record repo.SettingsRecord, found bool, scope string) ([]byte, error) {
	if !found {
		return nil, nil
	}
	if record.Scope != scope {
		return nil, fmt.Errorf("帝国竞技场角色选项读取到了其他角色记录")
	}
	if frame, exists := record.Values["character_option_body_hex"]; exists {
		if len(frame) != 1032 {
			return nil, fmt.Errorf("帝国竞技场角色选项原帧长度无效")
		}
		raw, err := hex.DecodeString(frame)
		if err != nil || len(raw) < 4 || int(binary.LittleEndian.Uint32(raw)) != len(raw)-4 || len(raw) != 516 {
			return nil, fmt.Errorf("帝国竞技场角色选项原帧损坏")
		}
		return append([]byte(nil), raw[4:]...), nil
	}
	if value, exists := record.Values["character_option_hex"]; exists {
		if len(value) != 1024 {
			return nil, fmt.Errorf("帝国竞技场角色选项旧存档长度无效")
		}
		raw, err := hex.DecodeString(value)
		if err != nil || len(raw) != 512 {
			return nil, fmt.Errorf("帝国竞技场角色选项旧存档损坏")
		}
		return raw, nil
	}
	return nil, fmt.Errorf("帝国竞技场角色选项存在记录但缺少正文")
}

// 只读准备1390，与1389使用同一名单。准备前后重查角色和设置，防止
// 另一连接保存选项或角色变化后仍把旧资料当成一次完整创建快照。
func (s *Service) prepareCurrentTagTournamentCharacterOptions(ctx context.Context, p *gameSession, characters [2]currentTagTournamentCharacterProjection) ([]byte, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if ctx == nil || !bound || !s.isCurrentTagTournamentChannel(p) {
		return nil, fmt.Errorf("帝国竞技场角色选项会话已失效")
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil || g.Settings == nil {
		return nil, fmt.Errorf("帝国竞技场角色选项仓储不可用")
	}
	advertised, _, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return nil, err
	}
	nativeIDs, err := currentTagTournamentNativeRoleIDs(identity.character, [2]repo.CharacterRecord{characters[0].character, characters[1].character})
	if err != nil {
		return nil, err
	}
	var snapshots [2]repo.SettingsRecord
	var exists [2]bool
	var rows []currentTagTournamentCharacterOption
	for i, projection := range characters {
		ch := projection.character
		if ch.CharacterID == "" || ch.AccountID != p.accountID {
			return nil, fmt.Errorf("帝国竞技场角色选项账号不符")
		}
		selector := -1
		for j, row := range advertised {
			if row.CharacterID == ch.CharacterID && row.Name == ch.Name && row.Slot == ch.Slot {
				selector = j
				break
			}
		}
		if selector < 0 || selector >= 255 {
			return nil, fmt.Errorf("帝国竞技场角色选项不在已发队员名单")
		}
		scope := characterOptionScope(ch.CharacterID)
		record, found, err := g.Settings.Load(ctx, scope)
		if err != nil {
			return nil, err
		}
		blob, err := currentTagTournamentSavedCharacterOption(record, found, scope)
		if err != nil {
			return nil, err
		}
		snapshots[i], exists[i] = repo.CloneSettings(record), found
		rows = append(rows, currentTagTournamentCharacterOption{selector: nativeIDs[i], blob: blob})
	}
	for i, projection := range characters {
		ch, found, err := g.Character.Load(ctx, projection.character.CharacterID)
		if err != nil {
			return nil, err
		}
		if !found || !reflect.DeepEqual(ch, projection.character) {
			return nil, fmt.Errorf("帝国竞技场准备角色选项期间角色资料变化")
		}
		r, found, err := g.Settings.Load(ctx, characterOptionScope(ch.CharacterID))
		if err != nil {
			return nil, err
		}
		if found != exists[i] || !reflect.DeepEqual(r, snapshots[i]) {
			return nil, fmt.Errorf("帝国竞技场准备期间角色选项已变化")
		}
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil, fmt.Errorf("帝国竞技场角色选项准备期间会话变化")
	}
	return buildCurrentTagTournamentCharacterOptions(rows)
}
