package dnfbridge

import (
	"context"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const nativeExtractionBatchLimit = 127

// 本次事务成功后才投影；只存同一会话事件的逐件结果，不跨事件缓存玩家资产。
type nativeExtractionOutcome struct {
	request   currentExpertJobExtractionRequest
	result    currentExpertJobExtractionResult
	refresh   []byte
	character dnfrepo.CharacterRecord
	err       error
}
type nativeExtractionReplay struct {
	items []nativeExtractionOutcome
	next  int
}

func (r *nativeExtractionReplay) take(request currentExpertJobExtractionRequest) (*nativeExtractionOutcome, error) {
	if r.next >= len(r.items) || r.items[r.next].request != request {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	item := &r.items[r.next]
	r.next++
	return item, item.err
}

func nativeExtractionPacket(packet dnfproto.LatestGameStreamPacket) (currentExpertJobExtractionRequest, nativeResultDeliveryKey, bool) {
	key, ok := nativeResultRequestKey(packet)
	if !ok || key.opcode != 416 {
		return currentExpertJobExtractionRequest{}, key, false
	}
	parsed, err := dnfproto.ParseLegacyGamePacket(packet.Data)
	if err != nil {
		return currentExpertJobExtractionRequest{}, key, false
	}
	request, err := parseCurrentExpertJobExtractionRequest(normalizeLegacyGameBody(parsed.Header.Type, parsed.Body))
	return request, key, err == nil
}
func nativeExtractionPrefix(packets []dnfproto.LatestGameStreamPacket) int {
	var first nativeResultDeliveryKey
	for i, packet := range packets {
		if i >= nativeExtractionBatchLimit {
			return i
		}
		_, key, ok := nativeExtractionPacket(packet)
		if !ok || i > 0 && key != first {
			return i
		}
		first = key
	}
	return len(packets)
}

// 只排空已有读块，不增加定时或等待后续用户操作；坏帧/EOF交回原循环。
func (s *Service) collectNativeExtractionReadyPackets(p *gameSession, ahead *nativeResultReadAhead, packets []dnfproto.LatestGameStreamPacket, remaining []byte) ([]dnfproto.LatestGameStreamPacket, []byte, *nativeResultReadChunk) {
	if ahead == nil || len(packets) == 0 || nativeExtractionPrefix(packets) != len(packets) {
		return packets, remaining, nil
	}
	for i := 0; i < 16 && len(packets) < nativeExtractionBatchLimit; i++ {
		chunk, ready := ahead.tryNext()
		if !ready {
			break
		}
		if chunk.err != nil {
			return packets, remaining, &chunk
		}
		joined := append(append([]byte(nil), remaining...), chunk.data...)
		more, tail, skipped, err := dnfproto.SplitLatestGameStream(joined, s.options.maxPacketBytes)
		if err != nil || skipped != 0 {
			return packets, remaining, &chunk
		}
		s.logCurrentGameReadWait(p, chunk)
		if len(chunk.data) > 0 {
			s.logGamePacket(p, "RECV", "game-raw", chunk.data)
		}
		packets, remaining = append(packets, more...), tail
		if nativeExtractionPrefix(packets) != len(packets) {
			break
		}
	}
	return packets, remaining, nil
}

func (s *Service) nativeExtractionBatchAllowed(p *gameSession) bool {
	if s.options.currentClientVersion != "official-24.9" || !nativeResultDeliveryEnabled() || !currentExpertJobExpNotifyEnabled() || p == nil ||
		p.channel.ID == 0 || p.nativeExtractionReplay != nil || p.nativeNPCDisjointReplay != nil || p.dungeon.runtime != nil || p.channelSceneBatch != nil ||
		p.hasOp19QuietPending() || loginActorGenerationRebuildArmed(p) || s.isCurrentTagTournamentChannel(p) {
		return false
	}
	_, bound := s.boundGameSessionCharacterSnapshot(p)
	return bound
}

// 合法提取只涉及list0来源删除和材料/共享仓库槽；使用原op14构造器及原空槽哨兵。
func nativeExtractionRefreshBody(assets *dnfexpertjob.Assets, request currentExpertJobExtractionRequest, result currentExpertJobExtractionResult) []byte {
	slots := []int16{request.TargetSlot}
	for _, material := range result.Materials {
		slots = append(slots, material.Slot)
	}
	entries := make([]currentItemListEntry, 0, len(slots))
	seen := map[int16]bool{}
	for _, slot := range slots {
		if seen[slot] {
			continue
		}
		seen[slot] = true
		key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)
		stack, found := assets.Inventory.Slots[key]
		if dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, slot) {
			stack, found = assets.AccountInventory.Slots[key]
		}
		var entry currentItemListEntry
		if found && stack.ItemID > 0 && stack.Count >= 0 {
			entry = currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
		} else {
			entry.patchCore(slot, math.MaxUint32, 0)
		}
		entries = append(entries, entry)
	}
	return buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, entries)
}

func (s *Service) commitNativeExtractionRequests(ctx context.Context, p *gameSession, requests []currentExpertJobExtractionRequest) ([]nativeExtractionOutcome, error) {
	outcomes := make([]nativeExtractionOutcome, len(requests))
	for i, request := range requests {
		outcomes[i].request = request
	}
	jobs, items, owner, accountID, characterID, err := s.currentExpertJobMutationContext(p)
	if err != nil {
		return outcomes, err
	}
	err = owner.Extract(ctx, dnfexpertjob.Command{AccountID: accountID, CharacterID: characterID, UpdatedAt: s.gameplayNow(), Project: func(assets *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
		var dirty dnfexpertjob.Changes
		for i, request := range requests {
			if err := ctx.Err(); err != nil {
				return dnfexpertjob.Changes{}, err
			}
			character := dnfrepo.CloneCharacter(*assets.Character)
			inventory := dnfrepo.CloneInventory(*assets.Inventory)
			account := dnfrepo.CloneAccountInventory(*assets.AccountInventory)
			if account.Slots == nil {
				account.Slots = map[string]dnfrepo.ItemStack{}
			}
			trial := &dnfexpertjob.Assets{Character: &character, Inventory: &inventory, AccountInventory: &account}
			result, changes, err := projectCurrentExpertJobExtraction(trial, jobs, items, request)
			if err != nil {
				outcomes[i].err = err
				continue
			}
			outcomes[i].result = result
			outcomes[i].refresh = nativeExtractionRefreshBody(trial, request, result)
			if result.ExperienceGain != 0 || result.LevelChanged {
				outcomes[i].character = dnfrepo.CloneCharacter(character)
			}
			*assets.Character, *assets.Inventory, *assets.AccountInventory = character, inventory, account
			dirty.Character = dirty.Character || changes.Character
			dirty.Inventory = dirty.Inventory || changes.Inventory
			dirty.AccountInventory = dirty.AccountInventory || changes.AccountInventory
			dirty.ExpertExperience = dirty.ExpertExperience || changes.ExpertExperience
		}
		return dirty, nil
	}})
	return outcomes, err
}

func (s *Service) sendNativeExtractionProjection(p *gameSession, item *nativeExtractionOutcome) error {
	s.logPacketEvent("game-aligned-command-item-slot-refresh-send", "conn_id", p.connID, "operation", "expert_job_extraction_changed_slots_after_ack", "char_id", p.selectedCharacterID,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember), "classification", 0, "list_type", 0, "body_len", len(item.refresh), "sequence", "durable_owner_then_class1_ack_then_committed_projection_op14_changed_rows")
	if err := s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketWalkoutPartyMember), item.refresh, 0); err != nil {
		return err
	}
	if item.result.ExperienceGain != 0 || item.result.LevelChanged {
		if item.result.LevelChanged {
			return s.sendCurrentExpertJobInfoForCharacter(p, item.character, true)
		}
		s.notifyCurrentExpertJobExperience(p, item.character, item.result.JobType, max(int64(0), item.character.Stats["expert_job_exp"]))
	}
	return nil
}

func (s *Service) handleNativeExtractionBatch(p *gameSession, packets []dnfproto.LatestGameStreamPacket) (bool, error) {
	if len(packets) < 2 || nativeExtractionPrefix(packets) != len(packets) || !s.nativeExtractionBatchAllowed(p) {
		return false, nil
	}
	started := time.Now()
	if err := s.prepareNativeResultDelivery(p, packets[0]); err != nil {
		return true, err
	}
	requests := make([]currentExpertJobExtractionRequest, len(packets))
	for i, packet := range packets {
		requests[i], _, _ = nativeExtractionPacket(packet)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	outcomes, err := s.commitNativeExtractionRequests(ctx, p, requests)
	if err != nil {
		for i := range outcomes {
			outcomes[i] = nativeExtractionOutcome{request: requests[i], err: err}
		}
	}
	settled := time.Since(started)
	replay := &nativeExtractionReplay{items: outcomes}
	p.nativeExtractionReplay = replay
	defer func() { p.nativeExtractionReplay = nil }()
	for i, packet := range packets {
		if i > 0 {
			if err := s.prepareNativeResultDelivery(p, packet); err != nil {
				return true, err
			}
		}
		if err := s.handleGameStreamPacket(p, packet); err != nil {
			return true, err
		}
	}
	if replay.next != len(outcomes) {
		return true, dnfexpertjob.ErrOwnerUnavailable
	}
	s.logGameEvent(p, "原生提取连续请求批量结算", "请求数", len(packets), "结算毫秒", settled.Milliseconds(), "总毫秒", time.Since(started).Milliseconds(), "事务成功", err == nil)
	return true, nil
}
