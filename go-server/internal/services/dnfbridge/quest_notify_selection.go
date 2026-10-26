package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentSaveQuestNotifyOpcode uint16 = uint16(dnfenum.CmdPacketSaveQuestNotify)
	currentQuestNotifySlotCount         = 4
)

func characterQuestNotifyScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":quest_notify"
}

// cd3 EXE 1F00A50 writes op506 as u8 count followed by positive i32 IDs.
// Its add/remove paths and the four-u32 select reader 1CB2A00 share +0x24C.
func decodeCurrentQuestNotifySelection(body []byte) ([]uint32, bool) {
	if len(body) == 0 || int(body[0]) > currentQuestNotifySlotCount || len(body) != 1+4*int(body[0]) {
		return nil, false
	}
	ids := make([]uint32, 0, int(body[0]))
	seen := make(map[uint32]bool, int(body[0]))
	for offset := 1; offset < len(body); offset += 4 {
		id := binary.LittleEndian.Uint32(body[offset : offset+4])
		if id == 0 || id > 0x7fffffff || seen[id] {
			return nil, false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, true
}

// The native select reader refreshes its tracker while consuming the ACK.
// Ignore entry reports until this character's scene is acknowledged; a ready
// character's explicit count=0 is a real cancellation and must be persisted.
func currentQuestNotifySaveWindowOpen(session *gameSession) bool {
	if session == nil || session.selectedCharacterID == 0 {
		return false
	}
	selected := session.selectedCharacterID
	session.townMu.Lock()
	townReady := session.townSceneReadyCharacterID == selected
	session.townMu.Unlock()
	if townReady {
		return true
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	dungeonReady := dungeonRuntimeOwnsCharacter(runtime, selected)
	session.dungeon.mu.Unlock()
	return dungeonReady
}

func currentQuestNotifyActive(catalog *dnfquest.Catalog, quests dnfrepo.QuestRecord, id uint32) bool {
	if catalog == nil {
		return false
	}
	if _, known := catalog.Find(int64(id)); !known {
		return false
	}
	state, exists := quests.States[int64(id)]
	if !exists {
		state, exists = quests.Progress[int64(id)]
	}
	// Progress zero means achieved and awaiting hand-in, still an active quest.
	return exists && strings.EqualFold(strings.TrimSpace(state.Status), "active")
}

func (s *Service) handleSaveCurrentQuestNotify(session *gameSession, body []byte) error {
	if err := s.saveCurrentQuestNotifySelection(session, body); err != nil {
		// This native preference push has no response; repository/resource
		// failures preserve the old preference without closing the connection.
		s.logGameEvent(session, "game-quest-notify-save-failed", "原因", err)
	}
	return nil
}

func (s *Service) saveCurrentQuestNotifySelection(session *gameSession, body []byte) error {
	ids, valid := decodeCurrentQuestNotifySelection(body)
	if !valid {
		s.logGameEvent(session, "game-quest-notify-save-blocked", "原因", "invalid_native_shape", "body_len", len(body))
		return nil
	}
	if !currentQuestNotifySaveWindowOpen(session) {
		s.logGameEvent(session, "game-quest-notify-save-deferred", "原因", "character_scene_not_ready", "quest_ids", ids)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil || repos.Character == nil || repos.Quest == nil {
		return errSettingsRepositoryUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repos.Character.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("读取任务追踪所属角色: %w", err)
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if !found || accountID == "" || strings.TrimSpace(character.AccountID) != accountID {
		s.logGameEvent(session, "game-quest-notify-save-blocked", "原因", "character_owner_unresolved", "char_id", characterID)
		return nil
	}
	if len(ids) > 0 {
		quests, hasQuests, loadErr := repos.Quest.Load(ctx, characterID)
		if loadErr != nil {
			return fmt.Errorf("读取任务追踪任务状态: %w", loadErr)
		}
		catalog, catalogErr := s.loadQuestCatalog(ctx)
		if catalogErr != nil {
			return fmt.Errorf("读取任务追踪PVF: %w", catalogErr)
		}
		for _, id := range ids {
			if !hasQuests || !currentQuestNotifyActive(catalog, quests, id) {
				s.logGameEvent(session, "game-quest-notify-save-blocked", "原因", "quest_not_known_active", "char_id", characterID, "quest_id", id)
				return nil
			}
		}
	}
	if err := s.saveCharacterSettingsValues(session, characterQuestNotifyScope(characterID), map[string]string{
		"selection_hex": hex.EncodeToString(body),
		"source":        "current_exe_op506",
	}); err != nil {
		return fmt.Errorf("保存任务追踪勾选: %w", err)
	}
	s.logGameEvent(session, "game-quest-notify-saved", "char_id", characterID, "quest_ids", ids)
	return nil
}

func (s *Service) loadCurrentQuestNotifySelection(ctx context.Context, characterID string, quests dnfrepo.QuestRecord, hasQuests bool) ([]uint32, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil, errSettingsRepositoryUnavailable
	}
	record, found, err := repos.Settings.Load(ctx, characterQuestNotifyScope(characterID))
	if err != nil {
		return nil, fmt.Errorf("读取任务追踪勾选: %w", err)
	}
	if !found {
		return nil, nil
	}
	body, err := hex.DecodeString(record.Values["selection_hex"])
	if err != nil {
		return nil, fmt.Errorf("解析任务追踪存档: %w", err)
	}
	ids, valid := decodeCurrentQuestNotifySelection(body)
	if !valid {
		return nil, errors.New("任务追踪存档形状无效")
	}
	if len(ids) == 0 || !hasQuests {
		return nil, nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取任务追踪PVF: %w", err)
	}
	active := make([]uint32, 0, len(ids))
	for _, id := range ids {
		if currentQuestNotifyActive(catalog, quests, id) {
			active = append(active, id)
		}
	}
	// Project away completed, abandoned or removed definitions without rewriting
	// the durable selection merely because this login's task set changed.
	return active, nil
}

func projectCurrentSelectAckQuestNotify(body []byte, ids []uint32) ([]byte, error) {
	if len(ids) > currentQuestNotifySlotCount {
		return nil, errors.New("任务追踪超过四槽")
	}
	_, offset, ok := currentSelectAckIntermediateState(body)
	if !ok {
		return nil, errors.New("选角任务追踪字段布局无效")
	}
	projected := append([]byte(nil), body...)
	for index := 0; index < currentQuestNotifySlotCount; index++ {
		id := uint32(0)
		if index < len(ids) {
			id = ids[index]
		}
		binary.LittleEndian.PutUint32(projected[offset+index*4:offset+(index+1)*4], id)
	}
	return projected, nil
}
