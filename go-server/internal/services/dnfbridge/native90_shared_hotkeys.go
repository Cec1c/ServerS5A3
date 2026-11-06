package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A full keyboard configuration belongs to the authenticated account; the
// existing packet writer still selects the current character's native bank.
var native90SharedHotkeysMu sync.Mutex

func native90SharedHotkeyScope(account string) string {
	return "account:" + strings.TrimSpace(account) + ":native90_hotkeys"
}

// Run the existing per-character handler first; the extra native-only record
// becomes the durable account-wide owner for subsequent character logins.
func (s *Service) handleNative90SharedHotkeySave(session *gameSession, body []byte) error {
	if err := s.handleSaveGameOption2(session, body); err != nil {
		return err
	}
	blob, ok := lengthPrefixedOptionBlob(body, currentGameOptionBlobMaxLen)
	if !ok || len(blob) >= 0x578 || session == nil || !session.loginPacketSeen || !session.accountFromToken {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return nil
	}
	account := strings.TrimSpace(s.accountIDForSession(session))
	if account == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Settings == nil {
		return nil
	}
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || strings.TrimSpace(c.AccountID) != account || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	native90SharedHotkeysMu.Lock()
	defer native90SharedHotkeysMu.Unlock()
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	err = s.saveSettingsValues(session, native90SharedHotkeyScope(account), map[string]string{
		"hotkey_config_hex": hex.EncodeToString(blob), "source": "current_exe_op198", "source_character_id": c.CharacterID,
	})
	if err != nil {
		s.logGameEvent(session, "game-native90-account-hotkey-save-failed", "error", err)
		return nil
	}
	s.logGameEvent(session, "game-native90-account-hotkey-saved", "char_id", identity.character, "blob_len", len(blob))
	return nil
}

// Before an account-wide row exists, recover the newest complete, explicitly
// saved op198 row belonging to this account. No data
// is copied or changed by this lookup.
func (s *Service) loadNative90SharedHotkeys(ctx context.Context, repos repo.Group, account string) ([]byte, string, error) {
	native90SharedHotkeysMu.Lock()
	defer native90SharedHotkeysMu.Unlock()
	if repos.Settings == nil || repos.Character == nil || account == "" {
		return nil, "", fmt.Errorf("native90 shared hotkey owner unavailable")
	}
	shared, found, err := repos.Settings.Load(ctx, native90SharedHotkeyScope(account))
	if err != nil {
		return nil, "", err
	}
	if found && shared.Values["source"] == "current_exe_op198" {
		if data := decodeSettingsHex(shared.Values["hotkey_config_hex"]); len(data) > 0 && len(data) < 0x578 {
			return data, "account_native90_op198", nil
		}
		return nil, "", fmt.Errorf("native90 shared hotkey record invalid")
	}
	chars, err := repos.Character.ListByAccount(ctx, account, repo.DefaultCharacterSlotLimit)
	if err != nil {
		return nil, "", err
	}
	var latest time.Time
	var selected []byte
	var owner string
	for _, c := range chars {
		if strings.TrimSpace(c.AccountID) != account || strings.TrimSpace(c.CharacterID) == "" {
			continue
		}
		record, ok, err := repos.Settings.Load(ctx, characterHotkeyConfigScope(c.CharacterID))
		if err != nil {
			return nil, "", err
		}
		if !ok || record.Values["source"] != "current_exe_op198" {
			continue
		}
		data := decodeSettingsHex(record.Values["hotkey_config_hex"])
		if len(data) == 0 || len(data) >= 0x578 {
			continue
		}
		updated := record.UpdatedAt
		if t, e := time.Parse(time.RFC3339Nano, record.Values["updated_at"]); e == nil {
			updated = t
		}
		if selected == nil || updated.After(latest) || updated.Equal(latest) && c.CharacterID < owner {
			selected, latest, owner = data, updated, c.CharacterID
		}
	}
	if selected == nil {
		return nil, "", nil
	}
	return selected, "account_latest_owned_character_op198", nil
}
