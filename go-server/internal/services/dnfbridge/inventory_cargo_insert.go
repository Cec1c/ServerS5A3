package dnfbridge

import (
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentInventoryCargoInsert(stack dnfrepo.ItemStack, list byte) error {
	if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 {
		return fmt.Errorf("金库存入物品无效")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return err
	}
	return validateCurrentCargoInsert(stack, list, definition, s.gameplayNow())
}

func validateCurrentCargoInsert(stack dnfrepo.ItemStack, list byte, definition dungeonDropItemDefinition, now time.Time) error {
	container := currentPVFItemTargetContainer(definition)
	if container.ListType == 1 || container.ListType == 7 || (container.ListType == 0 && (container.SlotStart == 177 || container.SlotStart == 289)) {
		return fmt.Errorf("该道具类型不能存入金库")
	}
	expire := stack.ExpireAt
	if expire.IsZero() {
		if timestamp := currentItemListStackExpire(stack); timestamp != 0 {
			expire = time.Unix(int64(timestamp), 0)
		}
	}
	if !expire.IsZero() && !expire.After(now) {
		return fmt.Errorf("到期物品不能存入金库")
	}
	for _, content := range definition.ImpossibleContents {
		label := normalizeMagicBoxPVFType(content)
		if (list == 2 && label == "charac cargo") || (list == 12 && label == "account cargo") {
			return fmt.Errorf("PVF禁止存入此金库")
		}
	}
	attach := normalizeMagicBoxPVFType(definition.AttachType)
	if list == 2 {
		if attach == "trade delete" {
			return fmt.Errorf("交易删除类物品不能存入个人金库")
		}
		return nil
	}
	if list != 12 {
		return fmt.Errorf("未知金库容器: %d", list)
	}
	if currentNPCShopItemLocked(stack) {
		return fmt.Errorf("锁定装备不能存入账号金库")
	}
	if attach != "free" && attach != "account" && attach != "sealing" {
		return fmt.Errorf("物品交易属性不能存入账号金库")
	}
	if strings.TrimSpace(stack.Extra["trade_restriction"]) == "1" {
		return fmt.Errorf("物品实例限制交易，不能存入账号金库")
	}
	return nil
}
