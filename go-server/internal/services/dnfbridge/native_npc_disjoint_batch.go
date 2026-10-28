package dnfbridge

import (
	"context"
	"time"

	dnfdisjoint "longheng.io/server/internal/modules/dnf/disjoint"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const nativeNPCDisjointBatchLimit = 127

type nativeNPCDisjointOutcome struct {
	request currentDisjointItemRequest
	result  currentDisjointResult
	err     error
}

// 仅在同一会话事件内投影已提交的结果，不跨请求缓存角色或资产。
type nativeNPCDisjointReplay struct {
	items []nativeNPCDisjointOutcome
	next  int
}

func (r *nativeNPCDisjointReplay) take(request currentDisjointItemRequest) (currentDisjointResult, error) {
	if r.next >= len(r.items) || r.items[r.next].request != request {
		return currentDisjointResult{}, errCurrentDisjointUnavailable
	}
	item := r.items[r.next]
	r.next++
	return item.result, item.err
}

func nativeNPCDisjointPacket(packet dnfproto.LatestGameStreamPacket) (currentDisjointItemRequest, nativeResultDeliveryKey, bool) {
	key, ok := nativeResultRequestKey(packet)
	if !ok || key.opcode != 26 {
		return currentDisjointItemRequest{}, key, false
	}
	parsed, err := dnfproto.ParseLegacyGamePacket(packet.Data)
	if err != nil {
		return currentDisjointItemRequest{}, key, false
	}
	request, err := parseCurrentDisjointItemRequest(normalizeLegacyGameBody(parsed.Header.Type, parsed.Body))
	return request, key, err == nil && request.ToolSlot == -1
}

// 只取已到达且连续的同一NPC入口；数量来自实际报文，不猜客户端勾选总数。
func nativeNPCDisjointPrefix(packets []dnfproto.LatestGameStreamPacket) int {
	var first nativeResultDeliveryKey
	for i, packet := range packets {
		if i >= nativeNPCDisjointBatchLimit {
			return i
		}
		_, key, ok := nativeNPCDisjointPacket(packet)
		if !ok || i > 0 && key != first {
			return i
		}
		first = key
	}
	return len(packets)
}

// 非阻塞排空至多16个已有读块。后续坏帧/EOF保留给原循环，先处理此前完整报文。
func (s *Service) collectNativeNPCReadyPackets(p *gameSession, ahead *nativeResultReadAhead,
	packets []dnfproto.LatestGameStreamPacket, remaining []byte,
) ([]dnfproto.LatestGameStreamPacket, []byte, *nativeResultReadChunk) {
	if ahead == nil || len(packets) == 0 || nativeNPCDisjointPrefix(packets) != len(packets) {
		return packets, remaining, nil
	}
	for i := 0; i < 16 && len(packets) < nativeNPCDisjointBatchLimit; i++ {
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
		if nativeNPCDisjointPrefix(packets) != len(packets) {
			break
		}
	}
	return packets, remaining, nil
}

func (s *Service) nativeNPCDisjointBatchAllowed(p *gameSession) bool {
	if s.options.currentClientVersion != "official-24.9" || !nativeResultDeliveryEnabled() || p == nil ||
		p.channel.ID == 0 || p.nativeNPCDisjointReplay != nil || p.dungeon.runtime != nil ||
		p.hasOp19QuietPending() || loginActorGenerationRebuildArmed(p) || s.isCurrentTagTournamentChannel(p) {
		return false
	}
	_, bound := s.boundGameSessionCharacterSnapshot(p)
	return bound
}

func commitNativeNPCDisjointRequests(ctx context.Context, owner *dnfdisjoint.Owner, accountID, characterID string,
	catalog *pvfDungeonDropCatalog, config currentDisjointPVFConfig, requests []currentDisjointItemRequest,
) ([]nativeNPCDisjointOutcome, error) {
	items := make([]nativeNPCDisjointOutcome, len(requests))
	for i, request := range requests {
		items[i].request = request
	}
	resolve := currentEquipmentDisjointResolver(config)
	err := owner.DisjointEquipment(ctx, dnfdisjoint.Command{AccountID: accountID, CharacterID: characterID,
		Project: func(assets *dnfdisjoint.Assets) (dnfdisjoint.Changes, error) {
			changed, accountDirty := false, false
			for i, request := range requests {
				plan, err := currentPlanDisjointItemSource(catalog, assets.Inventory, request.ListType, request.SourceSlot, resolve)
				if err != nil {
					items[i].err = err
					continue
				}
				// 单件校验/落位失败只拒绝该件，丢弃它的临时改动；后续仍用最新成功状态。
				inventory := dnfrepo.CloneInventory(*assets.Inventory)
				account := dnfrepo.CloneAccountInventory(*assets.AccountInventory)
				if account.Slots == nil {
					account.Slots = make(map[string]dnfrepo.ItemStack)
				}
				delete(inventory.Slots, currentCeraShopInventorySlotKey(request.ListType, plan.Slot))
				rewards, dirty, err := currentGrantDisjointRewards(&inventory, &account, catalog, plan.Rewards)
				if err != nil {
					items[i].err = err
					continue
				}
				*assets.Inventory, *assets.AccountInventory = inventory, account
				items[i].result.Rewards = rewards
				changed, accountDirty = true, accountDirty || dirty
			}
			return dnfdisjoint.Changes{Inventory: changed, AccountInventory: accountDirty}, nil
		}})
	return items, currentDisjointMutationError(err)
}

// 资产成功提交后再走原解码、序号观察、命令尾及逐件ACK；失败不会先投影成功。
func (s *Service) handleNativeNPCDisjointBatch(p *gameSession, packets []dnfproto.LatestGameStreamPacket) (bool, error) {
	if len(packets) < 2 || nativeNPCDisjointPrefix(packets) != len(packets) || !s.nativeNPCDisjointBatchAllowed(p) {
		return false, nil
	}
	// 上游收费入口与准入由原单件路径负责，不能在入口拒绝之前预先提交资产。
	for _, packet := range packets {
		request, _, _ := nativeNPCDisjointPacket(packet)
		if !s.currentDisjointEntryAllowed(p, request.Context) || currentDelilahDisjointFee(request.Context, 1) != 0 {
			return false, nil
		}
	}
	started := time.Now()
	if err := s.prepareNativeResultDelivery(p, packets[0]); err != nil {
		return true, err
	}
	requests := make([]currentDisjointItemRequest, len(packets))
	items := make([]nativeNPCDisjointOutcome, len(packets))
	for i, packet := range packets {
		requests[i], _, _ = nativeNPCDisjointPacket(packet)
		items[i].request = requests[i]
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	var config currentDisjointPVFConfig
	if err == nil {
		config, err = catalog.currentDisjointPVFConfig()
	}
	if err == nil {
		accountID, characterID, owner, ownerErr := s.currentDisjointMutationOwner(p)
		err = ownerErr
		if err == nil {
			items, err = commitNativeNPCDisjointRequests(ctx, owner, accountID, characterID, catalog, config, requests)
		}
	}
	if err != nil {
		for i := range items {
			items[i].err = err
			items[i].result = currentDisjointResult{}
		}
	}
	settled := time.Since(started)
	replay := &nativeNPCDisjointReplay{items: items}
	p.nativeNPCDisjointReplay = replay
	defer func() { p.nativeNPCDisjointReplay = nil }()
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
	if replay.next != len(items) {
		return true, errCurrentDisjointUnavailable
	}
	s.logGameEvent(p, "原生NPC连续分解批量结算", "请求数", len(packets), "结算毫秒", settled.Milliseconds(),
		"总毫秒", time.Since(started).Milliseconds(), "事务成功", err == nil)
	return true, nil
}
