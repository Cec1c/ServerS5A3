package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	dnfaura "longheng.io/server/internal/modules/dnf/auraskin"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentOpenAuraSkinSlotRequestSize      = 4
	currentOpenAuraSkinSlotFlagStat         = dnfaura.FlagStat
	currentAuraSkinSilentRestoreSource      = "current_exe_town_ui_lifecycle_native_actor_state"
	currentAuraSkinSlotNativeStateBit  byte = 0x08
)

var errCurrentOpenAuraSkinSlotInvalidRequest = errors.New("current aura skin slot request invalid")

type currentOpenAuraSkinSlotRequest struct {
	SourceSlot int16
}

func (s *Service) handleCurrentOpenAuraSkinSlot(session *gameSession, body []byte) error {
	request, err := decodeCurrentOpenAuraSkinSlotRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-current-open-aura-skin-slot-rejected",
			"body_len", len(body),
			"reason", err.Error())
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenAuraSkinSlot), 4)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logGameEvent(session, "game-current-open-aura-skin-slot-rejected",
			"source_slot", request.SourceSlot,
			"reason", "pvf_catalog_unavailable",
			"error", err.Error())
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenAuraSkinSlot), 4)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := s.commitCurrentOpenAuraSkinSlot(ctx, session, catalog, request)
	if err != nil {
		s.logGameEvent(session, "game-current-open-aura-skin-slot-rejected",
			"source_slot", request.SourceSlot,
			"reason", err.Error())
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenAuraSkinSlot), 4)
	}
	// 当前固定 EXE 的1/op863登记到0x1FD6330，成功应答不读取来源格位。
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketOpenAuraSkinSlot), nil); err != nil {
		return err
	}
	if result.SourceChanged {
		var sourceUpdate currentItemListEntry
		if result.SourceRemoved {
			sourceUpdate.patchCore(result.SourceSlot, math.MaxUint32, 0)
		} else {
			sourceUpdate = currentItemListEntryFromStack(
				dnfrepo.MainInventoryListType,
				result.SourceSlot,
				result.RemainingStack,
			)
		}
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, []currentItemListEntry{sourceUpdate}), 0); err != nil {
			return err
		}
	}
	// 扣材事务提交后，用既有原生角色刷新同步0x08权限位；当前配套没有
	// 识别“AURA”自定义标记的DLL，不能再次把该标记当成功应答发送。
	if err := s.sendSelectedActorMode1AppearanceRefresh(session, "open_aura_skin_slot", "op863_ack_then_material_update_then_native_actor_state"); err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-open-aura-skin-slot-accepted",
		"char_id", session.selectedCharacterID,
		"source_slot", result.SourceSlot,
		"source_item_id", result.SourceItemID,
		"source_pvf_path", result.SourcePVFPath,
		"consumed", result.Consumed,
		"already_open", result.AlreadyOpen,
		"source_update", result.SourceChanged)
	return nil
}

func decodeCurrentOpenAuraSkinSlotRequest(body []byte) (currentOpenAuraSkinSlotRequest, error) {
	if len(body) != currentOpenAuraSkinSlotRequestSize {
		return currentOpenAuraSkinSlotRequest{}, fmt.Errorf("%w: body_len=%d", errCurrentOpenAuraSkinSlotInvalidRequest, len(body))
	}
	slot := binary.LittleEndian.Uint32(body)
	if slot > uint32(math.MaxInt16) {
		return currentOpenAuraSkinSlotRequest{}, fmt.Errorf("%w: source_slot=%d", errCurrentOpenAuraSkinSlotInvalidRequest, slot)
	}
	return currentOpenAuraSkinSlotRequest{SourceSlot: int16(slot)}, nil
}

func (s *Service) commitCurrentOpenAuraSkinSlot(ctx context.Context, session *gameSession, catalog *pvfDungeonDropCatalog, request currentOpenAuraSkinSlotRequest) (dnfaura.Result, error) {
	if s == nil || session == nil || catalog == nil {
		return dnfaura.Result{}, dnfaura.ErrOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return dnfaura.Result{}, dnfaura.ErrOwnerUnavailable
	}
	owner, err := dnfaura.NewOwner(repositories, currentAuraSkinCatalog{catalog: catalog})
	if err != nil {
		return dnfaura.Result{}, err
	}
	return owner.Open(ctx, dnfaura.Command{
		AccountID:           s.accountIDForSession(session),
		SelectedCharacterID: session.selectedCharacterID,
		SourceSlot:          request.SourceSlot,
	})
}

// 冷登录的开启状态由后续USERINFO完整角色状态恢复。此处只核对所属角色
// 并记录状态，不发送未经请求的op863，以免默认锁定角色收到错误/开启效果。
func (s *Service) sendCurrentAuraSkinSlotTownUIReadyState(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	session.auraSkinMu.Lock()
	if session.auraSkinTownUIReadyStateSent {
		session.auraSkinMu.Unlock()
		s.logGameEvent(session, "game-current-aura-skin-slot-state-skipped",
			"source", currentAuraSkinSilentRestoreSource,
			"reason", "already_sent_for_current_town_scene")
		return nil
	}
	session.auraSkinTownUIReadyStateSent = true
	session.auraSkinMu.Unlock()

	resetGate := func() {
		session.auraSkinMu.Lock()
		session.auraSkinTownUIReadyStateSent = false
		session.auraSkinMu.Unlock()
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		resetGate()
		return dnfrepo.ErrRepoMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		resetGate()
		return err
	}
	if !found || strings.TrimSpace(character.CharacterID) != characterID {
		resetGate()
		return fmt.Errorf("aura skin state character %s not found", characterID)
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID != "" && strings.TrimSpace(character.AccountID) != accountID {
		resetGate()
		return fmt.Errorf("aura skin state character %s account mismatch", characterID)
	}
	opened := character.Stats != nil && character.Stats[currentOpenAuraSkinSlotFlagStat] != 0
	s.logGameEvent(session, "game-current-aura-skin-slot-state-ready",
		"source", currentAuraSkinSilentRestoreSource,
		"char_id", session.selectedCharacterID,
		"opened", opened,
		"native_slot_state", currentExtraEquipmentSlotState(character),
		"wire_owner", "后续原生USERINFO角色状态，不发送开启动画应答")
	return nil
}

type currentAuraSkinCatalog struct {
	catalog *pvfDungeonDropCatalog
}

func (c currentAuraSkinCatalog) ResolveItem(itemID uint32) (dnfaura.ItemDefinition, error) {
	if c.catalog == nil {
		return dnfaura.ItemDefinition{}, dnfaura.ErrOwnerUnavailable
	}
	definition, err := c.catalog.ResolveItem(itemID)
	if err != nil {
		return dnfaura.ItemDefinition{}, err
	}
	return dnfaura.ItemDefinition{
		Kind:    string(definition.Kind),
		PVFPath: definition.PVFPath,
	}, nil
}
