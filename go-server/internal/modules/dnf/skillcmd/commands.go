// 本文件把 op331/op332 技能指令自定义落库，并把已保存的指令投影给技能信息包。
//
// 现场证据（HK dnf90.out 2026-09-25 18:16–18:19，角色 14 / dnf:10013 连续 8 次）：
// 每一次 op331 都只走到 `skill_command_customizing ... response_allowed=false ...
// success ACK is blocked`，既没有落库也没有回包，玩家看到的就是"指令改了不生效"。
//
// 参考实现（86JP/A21 `Network/Handlers/SkillHandler.cs`）：
//   - `Handle_CHANGE_SKILL_COMMAND`：先回 `0x01 + 原样回显请求体`（class1/0x014B），
//     再把每个技能的指令字节按技能号写库；
//   - `Handle_RESET_ALL_SKILL_COMMANDS`（op332）：回 `0x01` 并清空全部指令。
//
// 存储复用既有 settings 家族（`dnf_settings` / `dnf_setting_values`），不新增表：
// scope = `character:<id>:skill_commands`，entry_key = `command_<skillID>`，
// entry_value = 指令字节的十六进制；另存 `command_count` / `source` / `updated_at`。
package skillcmd

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// SkillCommandScope 是单个角色技能指令配置的 settings scope。
func SkillCommandScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":skill_commands"
}

func skillCommandEntryKey(skillID uint16) string {
	return "command_" + strconv.FormatUint(uint64(skillID), 10)
}

// isSkillCommandEntryKey keeps the metadata keys (`command_count`, `source`,
// `updated_at`) out of the per-skill vector.
func isSkillCommandEntryKey(key string) bool {
	if !strings.HasPrefix(key, "command_") {
		return false
	}
	_, err := strconv.ParseUint(strings.TrimPrefix(key, "command_"), 10, 16)
	return err == nil
}

// SkillCommandSettings is the persisted per-skill custom command vector.
type SkillCommandSettings struct {
	CharacterID string
	Commands    map[uint16][]byte
	UpdatedAt   time.Time
}

// LoadSkillCommands 读取角色已保存的技能指令；没有记录时返回 found=false。
// 传 nil settings 仓储时返回 found=false 而不是错误：技能信息投影必须能在
// 内存仓储/裁剪过的测试仓储下继续工作。
func LoadSkillCommands(ctx context.Context, repos dnfrepo.Group, characterID string) (SkillCommandSettings, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || repos.Settings == nil {
		return SkillCommandSettings{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, found, err := repos.Settings.Load(ctx, SkillCommandScope(characterID))
	if err != nil || !found {
		return SkillCommandSettings{}, false, err
	}
	commands := make(map[uint16][]byte)
	for key, value := range record.Values {
		if !isSkillCommandEntryKey(key) {
			continue
		}
		skillID, _ := strconv.ParseUint(strings.TrimPrefix(key, "command_"), 10, 16)
		decoded, err := hex.DecodeString(strings.TrimSpace(value))
		if err != nil || len(decoded) == 0 {
			continue
		}
		commands[uint16(skillID)] = decoded
	}
	return SkillCommandSettings{
		CharacterID: characterID,
		Commands:    commands,
		UpdatedAt:   record.UpdatedAt,
	}, true, nil
}

// SkillCommandMutationResult 描述一次指令自定义/清空的结果。
type SkillCommandMutationResult struct {
	CharacterID string
	Applied     []SkillCommandRecord
	Total       int
	Cleared     int
}

// ApplySkillCommands 持久化 op331 的每个 (skillID, command bytes) 记录。
// 技能号在该角色命名空间内必须是已学技能：未学技能的指令不会生效，落库只会
// 在下一次技能信息投影里变成一个孤儿键，因此这里显式拒绝。
func (o *Owner) ApplySkillCommands(ctx context.Context, cmd Command) (SkillCommandMutationResult, error) {
	if o == nil || o.skills == nil {
		return SkillCommandMutationResult{}, ErrOwnerUnavailable
	}
	if o.settings == nil {
		return SkillCommandMutationResult{}, ErrSkillCommandStorageUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return SkillCommandMutationResult{}, ErrCharacterRequired
	}
	if len(cmd.SkillCommandRecords) == 0 {
		return SkillCommandMutationResult{}, ErrSkillCommandEmpty
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return SkillCommandMutationResult{}, err
	}
	if !ok {
		return SkillCommandMutationResult{}, ErrCharacterNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return SkillCommandMutationResult{}, ErrCharacterOwner
	}
	record, ok, err := o.skills.Load(ctx, characterID)
	if err != nil {
		return SkillCommandMutationResult{}, err
	}
	if !ok {
		return SkillCommandMutationResult{}, ErrSkillPointLedger
	}
	settings, found, err := o.loadSkillCommandSettings(ctx, characterID)
	if err != nil {
		return SkillCommandMutationResult{}, err
	}
	if !found {
		settings = dnfrepo.SettingsRecord{Scope: SkillCommandScope(characterID), Values: map[string]string{}}
	}
	settings = dnfrepo.CloneSettings(settings)
	if settings.Values == nil {
		settings.Values = map[string]string{}
	}
	result := SkillCommandMutationResult{CharacterID: characterID}
	for _, entry := range cmd.SkillCommandRecords {
		if state, learned := record.Skills[int64(entry.SkillID)]; !learned || state.Level <= 0 {
			return SkillCommandMutationResult{}, fmt.Errorf("%w: skill=%d", ErrSkillCommandUnlearned, entry.SkillID)
		}
		if err := validateSkillCommandBytes(entry.CommandBytes); err != nil {
			return SkillCommandMutationResult{}, err
		}
		settings.Values[skillCommandEntryKey(entry.SkillID)] = hex.EncodeToString(entry.CommandBytes)
		result.Applied = append(result.Applied, SkillCommandRecord{
			SkillID:      entry.SkillID,
			CommandBytes: append([]byte(nil), entry.CommandBytes...),
		})
	}
	total := 0
	for key := range settings.Values {
		if isSkillCommandEntryKey(key) {
			total++
		}
	}
	settings.Values["command_count"] = strconv.Itoa(total)
	settings.Values["source"] = "current_exe_op331_skill_command_customizing"
	result.Total = total
	if err := o.saveSkillCommandSettings(ctx, settings); err != nil {
		return SkillCommandMutationResult{}, err
	}
	return result, nil
}

// ClearSkillCommands 清空该角色的全部技能指令（op332 "全部默认"）。
func (o *Owner) ClearSkillCommands(ctx context.Context, cmd Command) (SkillCommandMutationResult, error) {
	if o == nil || o.skills == nil {
		return SkillCommandMutationResult{}, ErrOwnerUnavailable
	}
	if o.settings == nil {
		return SkillCommandMutationResult{}, ErrSkillCommandStorageUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return SkillCommandMutationResult{}, ErrCharacterRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return SkillCommandMutationResult{}, err
	}
	if !ok {
		return SkillCommandMutationResult{}, ErrCharacterNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return SkillCommandMutationResult{}, ErrCharacterOwner
	}
	settings, found, err := o.loadSkillCommandSettings(ctx, characterID)
	if err != nil {
		return SkillCommandMutationResult{}, err
	}
	cleared := 0
	values := map[string]string{
		"command_count": "0",
		"source":        "current_exe_op332_skill_command_all_default",
	}
	if found {
		for key := range settings.Values {
			if isSkillCommandEntryKey(key) {
				cleared++
			}
		}
	}
	if err := o.saveSkillCommandSettings(ctx, dnfrepo.SettingsRecord{
		Scope:     SkillCommandScope(characterID),
		Values:    values,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return SkillCommandMutationResult{}, err
	}
	return SkillCommandMutationResult{CharacterID: characterID, Cleared: cleared}, nil
}

func (o *Owner) loadSkillCommandSettings(ctx context.Context, characterID string) (dnfrepo.SettingsRecord, bool, error) {
	record, found, err := o.settings.Load(ctx, SkillCommandScope(characterID))
	if err != nil || !found {
		return dnfrepo.SettingsRecord{}, false, err
	}
	return record, true, nil
}

func (o *Owner) saveSkillCommandSettings(ctx context.Context, record dnfrepo.SettingsRecord) error {
	record.UpdatedAt = time.Now().UTC()
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values["updated_at"] = record.UpdatedAt.Format(time.RFC3339Nano)
	return o.settings.Save(ctx, record)
}

// validateSkillCommandBytes bounds one skill's command vector. The live current
// EXE writes a short direction/attack key sequence (observed lengths 1..5); the
// bound only has to stop a malformed length byte from growing the settings row
// without limit.
func validateSkillCommandBytes(command []byte) error {
	if len(command) == 0 {
		return ErrSkillCommandEmpty
	}
	if len(command) > maxSkillCommandBytes {
		return fmt.Errorf("%w: len=%d max=%d", ErrSkillCommandTooLong, len(command), maxSkillCommandBytes)
	}
	return nil
}

// SortedSkillCommandIDs is a deterministic view for logging and tests.
func SortedSkillCommandIDs(commands map[uint16][]byte) []uint16 {
	ids := make([]uint16, 0, len(commands))
	for skillID := range commands {
		ids = append(ids, skillID)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}
