package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentChronicleRefineOpcode = uint16(dnfenum.CmdPacketEnchant3rdChronicleItem)

type chronicleCatalogCache struct {
	chronicleCatalog *dnfchronicle.Catalog
}

func chronicleRefineGameplayModule() gameplayModuleDefinition {
	op := currentChronicleRefineOpcode
	return gameplayModuleDefinition{Name: "chronicle-refine", LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, g *gameSession, r gameplayRequest) error {
		return s.handleCurrentChronicleRefine(g, r.Body)
	}}, UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("勇者装备精炼分类拒绝", "370请求必须为class1", (*Service).handleCurrentChronicleRefine)}}
}
func parseCurrentChronicleRefineRequest(body []byte) (dnfchronicle.Request, error) {
	if len(body) != 14 || (body[6] != 0 && body[6] != 3) {
		return dnfchronicle.Request{}, dnfchronicle.ErrMaterial
	}
	r := dnfchronicle.Request{TargetList: body[6], MaterialSlot: int16(binary.LittleEndian.Uint16(body)), MaterialID: int64(binary.LittleEndian.Uint32(body[2:])), TargetSlot: int16(binary.LittleEndian.Uint16(body[7:])), TargetID: int64(binary.LittleEndian.Uint32(body[9:])), Option: body[13]}
	if r.MaterialSlot < 3 || r.TargetSlot < 0 || (r.TargetList == 0 && (r.TargetSlot < 3 || r.MaterialSlot == r.TargetSlot)) || r.MaterialID <= 0 || r.TargetID <= 0 || r.Option > 31 {
		return r, dnfchronicle.ErrMaterial
	}
	return r, nil
}

func (c *pvfDungeonDropCatalog) currentChronicleCatalog() *dnfchronicle.Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chronicleCatalog != nil {
		return c.chronicleCatalog
	}
	refs := make(map[int64]dnfchronicle.Reference, len(c.itemRefs))
	for id, r := range c.itemRefs {
		file := cleanDungeonDropPath(r.path)
		prefix := string(r.kind) + "/"
		if !strings.HasPrefix(file, prefix) {
			file = prefix + file
		}
		refs[int64(id)] = dnfchronicle.Reference{Path: file, Equipment: r.kind == dungeonDropItemEquipment}
	}
	c.chronicleCatalog = dnfchronicle.NewCatalog(c.source, refs)
	return c.chronicleCatalog
}

// 当前EXE 0x1fdb7d0在成功和碎装两分支之前均读取全部固定字段。
// 因此成功体也包含保留字节、目标槽及零奖励数，不能照抄86的6字节体。
func buildCurrentChronicleRefineSuccessBody(r dnfchronicle.Result) []byte {
	body := make([]byte, 10+len(r.Rewards)*10)
	body[0] = 1
	binary.LittleEndian.PutUint16(body[1:], uint16(r.Request.MaterialSlot))
	binary.LittleEndian.PutUint16(body[3:], uint16(r.MaterialRemaining))
	if r.Succeeded {
		body[5] = 1
	}
	targetSlot := uint16(r.Request.TargetSlot)
	if !r.Succeeded && r.Request.TargetList == 3 {
		// 现用EXE 0x1fdb8f4无视容器，按此字段删除主背包槽。
		// 0xffff经0x25ee9e1容量检查直接返回；真正穿戴目标由结果前的
		// list3/op14清除，避免误删同号背包再补回而触发“获得物品”。
		targetSlot = math.MaxUint16
	}
	binary.LittleEndian.PutUint16(body[7:], targetSlot)
	body[9] = byte(len(r.Rewards))
	body[6] = r.Request.TargetList
	for i, row := range r.Rewards {
		offset := 10 + i*10
		binary.LittleEndian.PutUint16(body[offset:], uint16(row.Slot))
		binary.LittleEndian.PutUint32(body[offset+2:], row.ItemID)
		binary.LittleEndian.PutUint32(body[offset+6:], row.Count)
	}
	return body
}

func (s *Service) handleCurrentChronicleRefine(session *gameSession, body []byte) error {
	reject := func(err error) error {
		code := dnfchronicle.FailureCode(err)
		if errors.Is(err, errDungeonPickupInventoryFull) {
			code = 0x15
		}
		s.logGameEvent(session, "勇者装备精炼拒绝", "op", currentChronicleRefineOpcode, "原因", err, "错误码", code)
		return s.sendGameUpperFailure(session, currentChronicleRefineOpcode, code)
	}
	request, err := parseCurrentChronicleRefineRequest(body)
	if err != nil {
		return reject(err)
	}
	if session == nil || session.selectedCharacterID == 0 {
		return reject(dnfchronicle.ErrOwner)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject(dnfchronicle.ErrOwner)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	material, err := items.ResolveItem(uint32(request.MaterialID))
	if err != nil || material.Kind != dungeonDropItemStackable {
		return reject(dnfchronicle.ErrMaterial)
	}
	owner, err := dnfchronicle.NewOwner(repos)
	if err != nil {
		return reject(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.Refine(ctx, s.currentChronicleRefineCommand(session, items, material, request))
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "勇者装备精炼已提交", "op", currentChronicleRefineOpcode, "装备ID", request.TargetID, "装备槽", request.TargetSlot, "气息ID", request.MaterialID, "选项", request.Option, "精炼成功", result.Succeeded, "属性数", result.Options, "成功阈值", result.Probability, "随机值", result.Roll, "气息剩余", result.MaterialRemaining, "奖励行数", len(result.Rewards))
	return s.sendCurrentChronicleRefineResult(session, result)
}

func (s *Service) sendCurrentChronicleRefineResult(session *gameSession, result dnfchronicle.Result) error {
	request := result.Request
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(result.ChangedSlots))
	for _, slot := range result.ChangedSlots {
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: slot})
	}
	var wornSlots []int16
	if request.TargetList == 3 {
		wornSlots = []int16{request.TargetSlot}
	}
	return s.sendAlignedUpperResponses(session, alignedcmd.Result{
		Operation: "勇者装备精炼结果刷新", WornEquipmentSlotRefreshes: wornSlots,
		UpperResponses:    []alignedcmd.UpperResponse{{MsgID: currentChronicleRefineOpcode, Body: buildCurrentChronicleRefineSuccessBody(result), Classification: dnfproto.DefaultChannelClassification, AllowCodec: true}},
		ItemSlotRefreshes: refreshes,
	})
}

func (s *Service) currentChronicleRefineCommand(session *gameSession, items *pvfDungeonDropCatalog, material dungeonDropItemDefinition, request dnfchronicle.Request) dnfchronicle.Command {
	return dnfchronicle.Command{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), Request: request, UpdatedAt: s.gameplayNow(), Catalog: items.currentChronicleCatalog(), MaterialPolicy: currentPVFItemPlacementPolicy(material),
		Identity: func(c dnfrepo.CharacterRecord) (byte, byte, error) {
			job, ok := characterJobByte(c)
			if !ok {
				return 0, 0, dnfchronicle.ErrOwner
			}
			return job, characterGrowTypeByte(c) & 15, nil
		},
		State: currentChronicleItemState, SetOptions: currentSetChronicleOptions,
		WornTarget: currentWornChronicleTarget, WornUpdate: currentWornChronicleUpdate,
		FailureRewards: func(e dnfchronicle.Equipment, state dnfchronicle.State, m dnfchronicle.Material) ([]dnfchronicle.Reward, error) {
			config, err := loadCurrentDisjointPVFConfig(items.source)
			if err != nil {
				return nil, err
			}
			document, err := parseDungeonCardPVFDocument(items.source, e.Path)
			if err != nil {
				return nil, err
			}
			rewards, err := currentEquipmentDisjointRewards(config, document)
			if err != nil {
				return nil, err
			}
			currentDisjointMergeReward(&rewards, currentDisjointReward{ItemID: uint32(m.FragmentID), Count: uint32(state.Reinforce) + 1})
			out := make([]dnfchronicle.Reward, len(rewards))
			for i, row := range rewards {
				out[i] = dnfchronicle.Reward{ItemID: row.ItemID, Count: row.Count}
			}
			return out, nil
		},
		GrantRewards: func(inv *dnfrepo.InventoryRecord, acc *dnfrepo.AccountInventoryRecord, rewards []dnfchronicle.Reward) ([]dnfchronicle.RewardSlot, bool, error) {
			inputs := make([]currentDisjointReward, len(rewards))
			for i, r := range rewards {
				inputs[i] = currentDisjointReward{ItemID: r.ItemID, Count: r.Count}
			}
			rows, dirty, err := currentGrantDisjointRewards(inv, acc, items, inputs)
			if err != nil {
				return nil, false, err
			}
			out := make([]dnfchronicle.RewardSlot, len(rows))
			for i, row := range rows {
				out[i] = dnfchronicle.RewardSlot{Slot: row.Slot, ItemID: row.ItemID, Count: row.Granted}
			}
			return out, dirty, nil
		}, Encode: func(slot int16, stack dnfrepo.ItemStack) dnfrepo.ItemStack {
			e := currentItemListEntryFromStack(0, slot, stack)
			stack.RawEntry = append([]byte(nil), e.data[:]...)
			return stack
		}}
}

func currentChronicleItemState(stack dnfrepo.ItemStack) (dnfchronicle.State, error) {
	options, err := currentChronicleOptions(stack.Extra, stack.RawEntry)
	if err != nil {
		return dnfchronicle.State{}, err
	}
	entry := currentItemListEntryFromStack(0, 9, stack)
	state := dnfchronicle.State{Options: options, Locked: currentNPCShopItemLocked(stack), Durability: binary.LittleEndian.Uint16(entry.data[0x0b:0x0d]), Reinforce: entry.data[0x0a] & 31, Unidentified: entry.data[0x13]&0x80 != 0}
	// 显式的0耐久仍是权威值，不能让旧行中的满耐久掩盖它。
	if value, found := sceneInventoryExtraUint(stack.Extra, "durability"); found {
		if value > math.MaxUint16 {
			return state, dnfchronicle.ErrTarget
		}
		state.Durability = uint16(value)
	}
	if sceneInventoryExtraByte(stack.Extra, "amplify_type", "amplification_type")&0x80 != 0 {
		state.Unidentified = true
	}
	if stack.Count > math.MaxInt16 {
		return state, fmt.Errorf("%w: 气息数量超出当前结果字段", dnfchronicle.ErrMaterial)
	}
	return state, nil
}
