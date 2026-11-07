package dnfbridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	disjoint "longheng.io/server/internal/modules/dnf/disjoint"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	equip "longheng.io/server/internal/modules/dnf/equip"
	expertjob "longheng.io/server/internal/modules/dnf/expertjob"
	inventory "longheng.io/server/internal/modules/dnf/inventory"
	npcshop "longheng.io/server/internal/modules/dnf/npcshop"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A separate, owner-only player capability. Pickup credentials cannot authorize
// equipment disposal, currency spending, storage moves, or party transitions.
func helperActionCredential(token string) string {
	h := sha256.Sum256([]byte("Arad-Helper-Player/2\n" + token))
	return hex.EncodeToString(h[:])
}

type helperUtilityRequest struct {
	Mode       string `json:"mode"`
	Kind       string `json:"kind"`
	Binding    string `json:"binding"`
	RequestID  string `json:"requestId"`
	Token      string `json:"token"`
	Room       string `json:"room"`
	Direction  string `json:"direction"`
	RarityMask uint8  `json:"rarityMask"`
	ListType   byte   `json:"listType"`
	Slot       int16  `json:"slot"`
	Page       int    `json:"page"`
}
type helperUtilityItem struct {
	ListType byte   `json:"listType"`
	Slot     int16  `json:"slot"`
	ItemID   int64  `json:"itemId"`
	Name     string `json:"name"`
	Count    int64  `json:"count"`
	Rarity   int64  `json:"rarity"`
}
type helperUtilityReply struct {
	OK        bool                `json:"ok"`
	Binding   string              `json:"binding"`
	Room      string              `json:"room"`
	Message   string              `json:"message"`
	Token     string              `json:"token,omitempty"`
	Kind      string              `json:"kind,omitempty"`
	Count     int                 `json:"count"`
	Gold      int64               `json:"gold"`
	Committed bool                `json:"committed"`
	Items     []helperUtilityItem `json:"items,omitempty"`
	Total     int                 `json:"total"`
}
type helperUtilityPlan struct {
	ID, Binding, Kind, Fingerprint string
	MachineFingerprint             string
	Expires                        time.Time
	Slots                          []int16
	SourceList                     byte
	SourceSlot                     int16
	DestinationSlot                int16
	Amount                         int64
	Result                         helperUtilityReply
}
type helperUtilityReceipt struct {
	ID    string
	Reply helperUtilityReply
}
type helperUtilityState struct {
	Plan     *helperUtilityPlan
	Receipts []helperUtilityReceipt
}

var errHelperAssetsChanged = errors.New("背包或装备已变化，请重新预览")

func helperHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func helperSlots(v map[string]repo.ItemStack) map[string]repo.ItemStack {
	if v == nil {
		return map[string]repo.ItemStack{}
	}
	return v
}
func helperBagHash(v repo.InventoryRecord) string { return helperHash(helperSlots(v.Slots)) }
func helperCargoHash(v repo.InventoryRecord) string {
	return helperHash([]any{helperSlots(v.Slots), helperSlots(v.Warehouse)})
}
func helperRepairHash(c repo.CharacterRecord, i repo.InventoryRecord, e repo.EquipmentRecord) string {
	if e.Entries == nil {
		e.Entries = map[string]repo.EquipmentEntry{}
	}
	return helperHash([]any{c.Stats["gold"], helperSlots(i.Slots), e.Entries})
}
func (s *Service) helperActionCaller(secret string) (boundGameSessionCharacter, bool) {
	if len(secret) != 64 {
		return boundGameSessionCharacter{}, false
	}
	if _, e := hex.DecodeString(secret); e != nil {
		return boundGameSessionCharacter{}, false
	}
	d := sha256.Sum256([]byte(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.gameSessions {
		if p != nil && id != 0 && p.selectedCharacterID == id && p.accountFromToken && p.loginPacketSeen && !p.loginRevoked.Load() && (p.ctx == nil || p.ctx.Err() == nil) && subtle.ConstantTimeCompare(d[:], p.helperActionsAccessDigest[:]) == 1 {
			return boundGameSessionCharacter{p, id, p.characterGeneration}, true
		}
	}
	return boundGameSessionCharacter{}, false
}
func (a *accountAPIServer) handleHelperUtility(w http.ResponseWriter, r *http.Request, q helperLootRequest) {
	id, ok := a.service.helperActionCaller(r.Header.Get("X-DNF-Helper"))
	if !ok {
		http.Error(w, "game session unavailable", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out helperUtilityReply
	err := a.service.callGameSession(ctx, id.session, "player-helper-utility", func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, ok := a.service.helperActionCaller(r.Header.Get("X-DNF-Helper"))
		if !ok || current != id {
			return errHelperChanged
		}
		var e error
		out, e = a.service.applyHelperUtility(ctx, id, *q.Operation)
		return e
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(helperUtilityReply{Message: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
func (s *Service) helperUtilityGuard(p *gameSession, kind string) error {
	if s.currentOnlineItemTrade(p.selectedCharacterID) != nil {
		return errors.New("交易期间不能使用内辅资产操作")
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	if r == nil {
		p.dungeon.mu.Unlock()
		// Town has its own completed scene/position boundary. Optional class1
		// op37 must never be required after a normal town login.
		if p.loginPacketSeen {
			if ready, _ := s.currentTownEnterSelectReady(p); !ready {
				return errors.New("请等待城镇场景就绪")
			}
			p.townMu.Lock()
			exited := p.channelExitAcknowledged
			p.townMu.Unlock()
			if exited {
				return errHelperChanged
			}
		}
		return nil
	}
	defer p.dungeon.mu.Unlock()
	if p.loginPacketSeen && !p.finishLoadingGateSnapshot().PostFinish {
		return errors.New("请等待副本场景加载完成")
	}
	if r.Session == nil || r.townReturnPending || r.deathReturnWaiting {
		return errors.New("加载、回城或死亡期间暂不可操作")
	}
	if kind == "native-warehouse" && !dungeonRuntimeOwnsCharacter(r, p.selectedCharacterID) {
		return errHelperChanged
	}
	if kind == "warehouse" || kind == "deposit" || kind == "withdraw" {
		return errors.New("请在城镇使用远程仓库")
	}
	if kind == "sell" || kind == "disjoint" {
		scene, ok := r.Session.Scene()
		if !ok || !scene.Cleared {
			return errors.New("请先清空当前房间，再出售或分解")
		}
	}
	return nil
}
func (s *Service) helperUtilityRecords(ctx context.Context, id boundGameSessionCharacter) (repo.Group, repo.InventoryRecord, error) {
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil {
		return g, repo.InventoryRecord{}, errors.New("角色仓储不可用")
	}
	v, found, e := g.Inventory.Load(ctx, strconv.Itoa(int(id.character)))
	if e != nil {
		return g, v, e
	}
	if !found {
		return g, v, errors.New("角色背包不存在")
	}
	return g, v, nil
}
func helperDisposalProtected(stack repo.ItemStack) bool {
	if currentNPCShopItemLocked(stack) || stack.Extra[repo.SortLockExtraKey] == "1" {
		return true
	}
	for _, key := range []string{"upgrade_level", "upgrade", "reinforce", "amplify_level", "amplify_value", "amplify_type", "forge_level", "forging_level", "enchant_card_id", "pet_enchant_card_item_id"} {
		if v := strings.TrimSpace(stack.Extra[key]); v != "" && v != "0" {
			return true
		}
	}
	row := currentItemListEntryFromStack(0, 9, stack)
	return row.data[0x0a] != 0 || binary.LittleEndian.Uint32(row.data[0x0e:0x12]) != 0 || row.data[0x13] != 0 || binary.LittleEndian.Uint16(row.data[0x14:0x16]) != 0
}
func helperDisposableType(t string) bool {
	switch t {
	case "[weapon]", "[coat]", "[pants]", "[shoulder]", "[waist]", "[shoes]", "[amulet]", "[wrist]", "[ring]", "[support]", "[magic stone]", "[earring]":
		return true
	}
	return false
}
func (s *Service) helperDisposalItems(v repo.InventoryRecord, mask uint8) ([]helperUtilityItem, error) {
	if mask == 0 || mask > 127 {
		return nil, errors.New("请在面板勾选并保存需要处理的装备品级")
	}
	c, e := s.currentPVFItemCatalog()
	if e != nil {
		return nil, e
	}
	var rows []helperUtilityItem
	for key, stack := range v.Slots {
		var list, slot int
		if _, e := fmt.Sscanf(key, "%d:%d", &list, &slot); e != nil || list != 0 || slot < 9 || slot > 32767 || repo.IsAccountSharedInventorySlot(0, int16(slot)) || stack.ItemID <= 0 || stack.Count != 1 || helperDisposalProtected(stack) {
			continue
		}
		def, e := c.ResolveItem(uint32(stack.ItemID))
		if e != nil || def.Kind != dungeonDropItemEquipment || !helperDisposableType(def.EquipmentType) || def.Rarity < 0 || def.Rarity > 6 || mask&(1<<uint(def.Rarity)) == 0 {
			continue
		}
		rows = append(rows, helperUtilityItem{Slot: int16(slot), ItemID: stack.ItemID, Name: def.Name, Count: 1, Rarity: def.Rarity})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slot < rows[j].Slot })
	if len(rows) > 72 {
		rows = rows[:72]
	}
	return rows, nil
}
func (s *Service) applyHelperUtility(ctx context.Context, id boundGameSessionCharacter, q helperUtilityRequest) (helperUtilityReply, error) {
	p := id.session
	if p == nil || p.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(id) {
		return helperUtilityReply{}, errHelperChanged
	}
	binding := helperBinding(id)
	out := helperUtilityReply{OK: true, Binding: binding}
	if q.Mode == "status" {
		out.Room = s.helperRoomStamp(p)
		out.Message = "便捷功能已就绪"
		if e := s.helperUtilityGuard(p, "status"); e != nil {
			out.Message = e.Error()
		}
		return out, nil
	}
	if q.Binding != binding || !validDiagnosticID(q.RequestID) {
		return out, errHelperChanged
	}
	for _, receipt := range p.helperUtility.Receipts {
		if receipt.ID == q.RequestID {
			return receipt.Reply, nil
		}
	}
	if q.Mode == "room" {
		var err error
		out, err = s.helperMoveRoom(p, id, q)
		if err != nil {
			return out, err
		}
		s.helperRemember(p, q.RequestID, out)
		return out, nil
	}
	if q.Mode == "warehouse-open" {
		if err := s.helperUtilityGuard(p, "native-warehouse"); err != nil {
			return out, err
		}
		if _, _, err := s.helperUtilityRecords(ctx, id); err != nil {
			return out, err
		}
		out.Kind = "native-warehouse"
		out.Message = "角色仓库已就绪"
		return out, nil
	}
	if q.Mode == "warehouse" {
		return s.helperWarehouse(ctx, id, q, out)
	}
	if q.Mode == "preview" {
		if err := s.helperUtilityGuard(p, q.Kind); err != nil {
			return out, err
		}
		g, inv, e := s.helperUtilityRecords(ctx, id)
		if e != nil {
			return out, e
		}
		plan := &helperUtilityPlan{ID: launcherNonce(), Binding: binding, Kind: q.Kind, Expires: s.gameplayNow().Add(30 * time.Second), Fingerprint: helperBagHash(inv)}
		out.Kind = q.Kind
		switch q.Kind {
		case "sell", "disjoint":
			rows, e := s.helperDisposalItems(inv, q.RarityMask)
			if e != nil {
				return out, e
			}
			if len(rows) == 0 {
				return out, errors.New("没有符合品级且未受保护的装备")
			}
			out.Items = rows
			out.Count = len(rows)
			for _, row := range rows {
				plan.Slots = append(plan.Slots, row.Slot)
			}
			if q.Kind == "sell" {
				shop, e := s.currentNPCShopCatalog()
				if e != nil {
					return out, e
				}
				items, e := s.currentPVFItemCatalog()
				if e != nil {
					return out, e
				}
				for _, row := range rows {
					price, e := resolveCurrentNPCShopActorPricing(shop, items, uint32(row.ItemID), 0)
					if e != nil {
						return out, e
					}
					out.Gold += price.SellGold
				}
				out.Message = fmt.Sprintf("出售%d件装备，获得%d金币。锁定、强化、增幅、附魔装备均已排除。", out.Count, out.Gold)
			} else {
				machine, e := s.helperCurrentDisjointer(p)
				if e != nil {
					return out, e
				}
				plan.MachineFingerprint = helperMachineHash(machine)
				if machine != nil {
					if p.selectedCharacterID != machine.OwnerCharacterID {
						out.Gold = -machine.Cost * int64(len(rows))
					}
					out.Message = fmt.Sprintf("分解%d件装备，使用当前已打开的%d级玩家分解机（角色%d），费用%d金币。", out.Count, machine.MachineGrade, machine.OwnerCharacterID, -out.Gold)
				} else {
					cat, e := s.currentPVFItemCatalog()
					if e != nil {
						return out, e
					}
					cfg, e := cat.currentDisjointPVFConfig()
					if e != nil {
						return out, e
					}
					for _, row := range rows {
						if _, e := currentPlanDisjointItemSource(cat, &inv, 0, row.Slot, currentEquipmentDisjointResolver(cfg)); e != nil {
							return out, e
						}
					}
					out.Message = fmt.Sprintf("分解%d件装备，使用系统分解规则。锁定和打造装备已排除。", out.Count)
				}
			}

		case "repair":
			c, _, e := g.Character.Load(ctx, strconv.Itoa(int(id.character)))
			if e != nil {
				return out, e
			}
			eq, _, e := g.Equipment.Load(ctx, strconv.Itoa(int(id.character)))
			if e != nil {
				return out, e
			}
			plan.Fingerprint = helperRepairHash(c, inv, eq)
			res, e := s.helperRepair(ctx, id, plan, true)
			if e != nil {
				return out, e
			}
			out.Count = res.RepairedCount
			out.Gold = -res.Cost
			if out.Count == 0 {
				return out, errors.New("装备耐久已满，无需维修")
			}
			out.Message = fmt.Sprintf("维修%d件装备，花费%d金币（原快捷维修价格）。", out.Count, res.Cost)
		case "deposit", "withdraw":
			if q.Slot < 0 {
				return out, errors.New("请选择物品")
			}
			source := inv.Slots
			list := byte(0)
			if q.Kind == "withdraw" {
				source = inv.Warehouse
				list = 2
			}
			stack, ok := source[fmt.Sprintf("%d:%d", list, q.Slot)]
			if !ok || stack.ItemID <= 0 || stack.Count <= 0 || stack.Count > 2147483647 {
				return out, errors.New("物品已变化，请刷新仓库")
			}
			plan.Fingerprint = helperCargoHash(inv)
			plan.SourceList = list
			plan.SourceSlot = q.Slot
			plan.Amount = stack.Count
			plan.DestinationSlot = 0
			if q.Kind == "withdraw" {
				policy, e := s.currentInventoryMovePlacement(stack)
				if e != nil {
					return out, e
				}
				plan.DestinationSlot = policy.Start
			}
			out.Count = 1
			out.Message = fmt.Sprintf("将所选物品整组%s；遵守原仓库容量、绑定与锁定限制。", map[bool]string{true: "取出", false: "存入"}[list == 2])
		default:
			return out, errors.New("不支持的内辅操作")
		}
		out.Token = plan.ID
		plan.Result = out
		p.helperUtility.Plan = plan
		return out, nil
	}
	if q.Mode != "commit" {
		return out, errors.New("无效操作")
	}
	plan := p.helperUtility.Plan
	if plan == nil || plan.Binding != binding || q.Token != plan.ID || !s.gameplayNow().Before(plan.Expires) {
		return out, errors.New("确认已过期，请重新预览")
	}
	if err := s.helperUtilityGuard(p, plan.Kind); err != nil {
		return out, err
	}
	reservation, e := s.reserveCurrentCharacterAssets(ctx, id, s.accountIDForSession(p))
	if e != nil {
		return out, e
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	out = plan.Result
	out.Token = ""
	out.Committed = true
	out.Items = nil
	var refresh []alignedcmd.ItemSlotRefresh
	var repairedWorn []int16
	switch plan.Kind {
	case "sell":
		e = s.helperSell(ctx, id, plan)
		for _, slot := range plan.Slots {
			refresh = append(refresh, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: slot})
		}
	case "disjoint":
		var res currentDisjointBatchResult
		res, e = s.helperDisjoint(ctx, id, plan)
		if e == nil {
			refresh = currentDisjointBatchSlotRefreshes(res)
			_ = s.sendCurrentDisjointEpicPieces(p, res.EpicPieces)
		}
	case "repair":
		var res equip.RepairResult
		res, e = s.helperRepair(ctx, id, plan, false)
		if e == nil {
			repairedWorn = res.RepairedEquipmentSlots
			for _, slot := range res.RepairedInventorySlots {
				refresh = append(refresh, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: slot})
			}
		}
	case "deposit", "withdraw":
		var res inventory.MoveResult
		res, e = s.helperCargoMove(ctx, id, plan)
		if e == nil {
			refresh = []alignedcmd.ItemSlotRefresh{{ListType: res.SourceListType, SlotIndex: res.SourceSlotIndex}, {ListType: res.DestinationListType, SlotIndex: res.DestinationSlotIndex}}
		}
	}
	if e != nil {
		return helperUtilityReply{}, e
	}
	p.helperUtility.Plan = nil
	out.Message = "已完成：" + out.Message
	s.helperRemember(p, q.RequestID, out)
	// Absolute committed projections only. An unsolicited NPC-window RPC ACK can
	// refer to a UI the player never opened, so helper calls do not fabricate it.
	refreshErr := s.sendRepairedWornEquipmentSlots(p, "helper-utility-repair", repairedWorn)
	if e = s.sendSelectedIncrementalItemSlotRefreshes(p, "helper-utility", refresh); refreshErr == nil {
		refreshErr = e
	}
	if refreshErr != nil {
		out.Message += "（显示刷新失败，请重登核对；不会重复执行）"
	}
	if plan.Kind == "sell" || plan.Kind == "repair" || plan.Kind == "disjoint" {
		g, ok := s.repositoryGroup()
		if ok {
			c, _, _ := g.Character.Load(ctx, strconv.Itoa(int(id.character)))
			_ = s.sendGameUpperRawClass(p, 14, buildCurrentGoldStateBody(c.Stats["gold"]), 0)
		}
	}
	s.helperRemember(p, q.RequestID, out)
	return out, nil
}
func (s *Service) helperRemember(p *gameSession, id string, out helperUtilityReply) {
	r := p.helperUtility.Receipts
	for n, old := range r {
		if old.ID == id {
			r[n].Reply = out
			return
		}
	}
	if len(r) >= 8 {
		r = r[len(r)-7:]
	}
	p.helperUtility.Receipts = append(r, helperUtilityReceipt{id, out})
}
func (s *Service) helperSell(ctx context.Context, id boundGameSessionCharacter, plan *helperUtilityPlan) error {
	p := id.session
	account, char, _, owner, e := s.currentNPCShopMutationOwner(p)
	if e != nil {
		return e
	}
	shop, e := s.currentNPCShopCatalog()
	if e != nil {
		return e
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return e
	}
	return owner.Mutate(ctx, npcshop.Command{AccountID: account, CharacterID: char, Project: func(a *npcshop.Assets) (npcshop.Changes, error) {
		if helperBagHash(*a.Inventory) != plan.Fingerprint {
			return npcshop.Changes{}, errHelperAssetsChanged
		}
		for _, slot := range plan.Slots {
			if _, e := projectCurrentNPCShopSell(a, shop, items, currentNPCShopSellRequest{ListType: 0, Slot: slot, Count: 1}); e != nil {
				return npcshop.Changes{}, e
			}
		}
		return npcshop.Changes{Character: true, Inventory: true}, nil
	}})
}

// The server's entered-store context is authoritative. Never switch a failed
// player machine operation to free system disassembly.
func (s *Service) helperCurrentDisjointer(p *gameSession) (*currentExpertJobStore, error) {
	s.expertJobStoreMu.Lock()
	owner := s.expertJobVisitors[p]
	own := s.expertJobStores[p.selectedCharacterID]
	if owner == 0 && own != nil && own.Kind == currentExpertJobDisjointStoreKind && own.OwnerSession == p {
		owner = p.selectedCharacterID
	}
	s.expertJobStoreMu.Unlock()
	if owner == 0 {
		// A disjointer may use the personal profession machine without opening
		// a public shop. An entered peer store always wins over this fallback.
		if !currentExpertJobSessionInDungeon(p) {
			g, available := s.repositoryGroup()
			if !available || g.Character == nil {
				return nil, errors.New("无法核对分解师状态")
			}
			c, found, e := g.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
			if e != nil {
				return nil, e
			}
			if found && c.Stats["expert_job_type"] == int64(expertjob.DisjointerType) {
				return s.currentPersonalDisjointerContext(p, p.selectedCharacterID)
			}
		}
		return nil, nil
	}
	machine, ok := s.currentExpertJobEnteredStore(p, owner, currentExpertJobDisjointStoreKind)
	if !ok && owner == p.selectedCharacterID && own != nil { // The placer owns this live machine even before entering its UI.
		s.expertJobStoreMu.Lock()
		live := s.expertJobStores[owner]
		if live != nil && live.OwnerSession == p && live.Kind == currentExpertJobDisjointStoreKind {
			machine = cloneCurrentExpertJobStore(live)
			ok = true
		}
		s.expertJobStoreMu.Unlock()
	}
	if !ok {
		return nil, errors.New("当前分解机已关闭或不可用，请重新打开")
	}
	visitor, present := s.onlinePlayers.PlayerForCharacter(p.selectedCharacterID)
	if !present || visitor.Session != p || visitor.ChannelID != machine.ChannelID || visitor.Instance != machine.Instance || visitor.TownID != machine.TownID || visitor.AreaID != machine.AreaID {
		return nil, errors.New("当前分解机不在本角色所在区域")
	}
	return machine, nil
}
func helperMachineHash(m *currentExpertJobStore) string {
	if m == nil {
		return "system"
	}
	conn := ""
	if m.OwnerSession != nil {
		conn = m.OwnerSession.connID
	}
	return helperHash([]any{m.OwnerCharacterID, conn, m.MachineGrade, m.Cost, m.ChannelID, m.TownID, m.AreaID, m.Instance})
}
func (s *Service) helperDisjoint(ctx context.Context, id boundGameSessionCharacter, plan *helperUtilityPlan) (currentDisjointBatchResult, error) {
	s.expertJobStoreOpMu.Lock()
	defer s.expertJobStoreOpMu.Unlock()
	machine, e := s.helperCurrentDisjointer(id.session)
	if e != nil {
		return currentDisjointBatchResult{}, e
	}
	if helperMachineHash(machine) != plan.MachineFingerprint {
		return currentDisjointBatchResult{}, errors.New("分解机已变化，请重新执行")
	}
	cat, e := s.currentPVFItemCatalog()
	if e != nil {
		return currentDisjointBatchResult{}, e
	}
	if machine == nil {
		cfg, e := cat.currentDisjointPVFConfig()
		if e != nil {
			return currentDisjointBatchResult{}, e
		}
		return s.commitCurrentDisjointBatch(ctx, id.session, cat, plan.Slots, 0, currentEquipmentDisjointResolver(cfg), func(a *disjoint.Assets) error {
			if helperBagHash(*a.Inventory) != plan.Fingerprint {
				return errHelperAssetsChanged
			}
			return nil
		})
	}
	requests := make([]currentExpertJobStoreDisjointRequest, 0, len(plan.Slots))
	for _, slot := range plan.Slots {
		requests = append(requests, currentExpertJobStoreDisjointRequest{OwnerID: machine.OwnerCharacterID, TargetSlot: slot, TargetList: 0})
	}
	result, e := s.commitCurrentExpertJobStoreDisjointBatch(ctx, id.session, machine, requests, func(a *expertjob.StoreDisjointAssets) error {
		if helperBagHash(*a.RequesterInventory) != plan.Fingerprint {
			return errHelperAssetsChanged
		}
		return nil
	})
	if e != nil {
		return currentDisjointBatchResult{}, e
	}
	s.updateCurrentExpertJobStoreMachine(machine, result.Endurance, machine.MachineGrade)
	// Native owner's income/endurance/experience projections, never a forged requester NPC ACK.
	if owner := machine.OwnerSession; owner != nil {
		if owner != id.session {
			w := packetWriter{}
			w.writeByte(1)
			w.writeUint32(uint32(result.OwnerGold))
			w.writeUint32(uint32(result.Endurance))
			for range requests {
				_ = s.sendGameUpperRawClass(owner, currentExpertJobDisjointOwnerNotification, w.bytes(), 0)
			}
			_ = s.sendGameUpperRawClass(owner, 14, buildCurrentGoldStateBody(result.OwnerGold), 0)
		}
		_ = s.sendCurrentExpertJobInfoFromRepository(owner, expertjob.DisjointerType, true)
	}
	// Reuse the existing absolute item-slot refresh builder for materials.
	out := currentDisjointBatchResult{EpicPieces: result.EpicPieceBalances, GoldAfter: result.RequesterGold, GoldChanged: true}
	for _, slot := range plan.Slots {
		out.Items = append(out.Items, currentDisjointBatchItem{Slot: slot})
	}
	for _, material := range result.Materials {
		out.Items[0].Rewards = append(out.Items[0].Rewards, currentDisjointRewardSlot{Slot: material.Slot, ItemID: material.ItemID, Count: material.Count})
	}
	return out, nil
}
func (s *Service) helperRepair(ctx context.Context, id boundGameSessionCharacter, plan *helperUtilityPlan, preview bool) (equip.RepairResult, error) {
	g, _ := s.repositoryGroup()
	o, e := equip.NewOwner(g)
	if e != nil {
		return equip.RepairResult{}, e
	}
	resolve, e := s.alignedRepairCostResolverForCommand(dnfenum.CmdPacketRepairEquipment)
	if e != nil {
		return equip.RepairResult{}, e
	}
	return o.RepairAll(ctx, equip.RepairCommand{SelectedCharacterID: id.character, AccountID: s.accountIDForSession(id.session), SlotIndex: -1, QuickRepair: true, PreviewOnly: preview, ValidateSnapshot: func(c repo.CharacterRecord, i repo.InventoryRecord, eq repo.EquipmentRecord) error {
		if helperRepairHash(c, i, eq) != plan.Fingerprint {
			return errHelperAssetsChanged
		}
		return nil
	}}, func(item int64) (alignedcmd.RepairCostEvidence, error) {
		s.repairCostMu.Lock()
		defer s.repairCostMu.Unlock()
		return resolve(item)
	})
}
func (s *Service) helperCargoMove(ctx context.Context, id boundGameSessionCharacter, plan *helperUtilityPlan) (inventory.MoveResult, error) {
	g, _ := s.repositoryGroup()
	o, e := inventory.NewOwner(g)
	if e != nil {
		return inventory.MoveResult{}, e
	}
	dest := byte(2)
	if plan.SourceList == 2 {
		dest = 0
	}
	return o.Move(ctx, inventory.Command{SelectedCharacterID: id.character, AccountID: s.accountIDForSession(id.session), SourceListType: plan.SourceList, SourceSlotIndex: plan.SourceSlot, DestinationListType: dest, DestinationSlotIndex: plan.DestinationSlot, MoveCount: int32(plan.Amount), ItemPlacement: s.currentInventoryMovePlacement, CargoInsert: s.currentInventoryCargoInsert, ValidateMoveSnapshot: func(i repo.InventoryRecord) error {
		if helperCargoHash(i) != plan.Fingerprint {
			return errHelperAssetsChanged
		}
		return nil
	}})
}
func (s *Service) helperWarehouse(ctx context.Context, id boundGameSessionCharacter, q helperUtilityRequest, out helperUtilityReply) (helperUtilityReply, error) {
	if e := s.helperUtilityGuard(id.session, "warehouse"); e != nil {
		return out, e
	}
	_, v, e := s.helperUtilityRecords(ctx, id)
	if e != nil {
		return out, e
	}
	if q.ListType != 0 && q.ListType != 2 {
		return out, errors.New("仓库类型无效")
	}
	if q.Page < 0 || q.Page > 20 {
		return out, errors.New("页码无效")
	}
	m := v.Slots
	if q.ListType == 2 {
		m = v.Warehouse
	}
	cat, e := s.currentPVFItemCatalog()
	if e != nil {
		return out, e
	}
	var rows []helperUtilityItem
	for key, stack := range m {
		var list, slot int
		if _, e := fmt.Sscanf(key, "%d:%d", &list, &slot); e != nil || byte(list) != q.ListType || slot < 0 || slot > 32767 || list == 0 && slot < 9 || repo.IsAccountSharedInventorySlot(byte(list), int16(slot)) {
			continue
		}
		def, e := cat.ResolveItem(uint32(stack.ItemID))
		if e != nil {
			continue
		}
		rows = append(rows, helperUtilityItem{ListType: byte(list), Slot: int16(slot), ItemID: stack.ItemID, Name: def.Name, Count: stack.Count, Rarity: def.Rarity})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slot < rows[j].Slot })
	out.Total = len(rows)
	start := q.Page * 48
	if start > len(rows) {
		start = len(rows)
	}
	end := start + 48
	if end > len(rows) {
		end = len(rows)
	}
	out.Items = rows[start:end]
	out.Message = "远程角色仓库：整组存取，遵守原容量与物品限制。"
	return out, nil
}
func (s *Service) helperRoomStamp(p *gameSession) string {
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	r := p.dungeon.runtime
	if r == nil || r.Session == nil {
		return ""
	}
	scene, ok := r.Session.Scene()
	if !ok {
		return ""
	}
	return helperHash([]any{r.Dungeon.ID, r.MazeIndex, r.Seed, scene.Coordinate, scene.Map.Map.ID})
}
func (s *Service) helperMoveRoom(p *gameSession, id boundGameSessionCharacter, q helperUtilityRequest) (helperUtilityReply, error) {
	out := helperUtilityReply{OK: true, Binding: helperBinding(id)}
	if e := s.helperUtilityGuard(p, "room"); e != nil {
		return out, e
	}
	if s.isCurrentTagTournamentChannel(p) {
		return out, errors.New("该场景暂不支持顺图")
	}
	if q.Room == "" || q.Room != s.helperRoomStamp(p) {
		return out, errors.New("房间已变化，请重试")
	}
	party := s.runtimePartyStateForSession(p)
	if isNonLeaderPartyMember(party, id.character) {
		return out, errors.New("请由队长使用顺图")
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	if r == nil || r.Session == nil || r.Room == nil || r.deathReturnWaiting || r.townReturnPending {
		p.dungeon.mu.Unlock()
		return out, errors.New("当前不可换房")
	}
	scene, ok := r.Session.Scene()
	if !ok || !scene.Cleared {
		p.dungeon.mu.Unlock()
		return out, errors.New("请先清空当前房间")
	}
	x, y := scene.Coordinate.X, scene.Coordinate.Y
	switch q.Direction {
	case "left":
		x--
	case "right":
		x++
	case "up":
		y--
	case "down":
		y++
	default:
		p.dungeon.mu.Unlock()
		return out, errors.New("方向无效")
	}
	if x < 0 || x > 255 || y < 0 || y > 255 {
		p.dungeon.mu.Unlock()
		return out, errors.New("该方向没有可通行出口")
	}
	req := dungeoncmd.MoveMapRequest{NextX: byte(x), NextY: byte(y)}
	_, e := s.planCurrentDungeonMove(r, req)
	p.dungeon.mu.Unlock()
	if e != nil {
		return out, errors.New("该方向暂不能通行")
	}
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0], body[1] = byte(x), byte(y)
	if e := s.handleDungeonMoveMapFrom(p, body, nil, true, "helper_utility"); e != nil {
		return out, e
	}
	out.Room = s.helperRoomStamp(p)
	if out.Room == q.Room {
		return out, errors.New("队伍或房间状态不满足换房条件")
	}
	out.Message = "已按所选方向换房，队员沿用队伍加载流程。"
	out.Committed = true
	return out, nil
}
