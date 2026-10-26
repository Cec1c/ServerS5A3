package dnfbridge

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentAvatarCompoundServerParameterPath = "etc/serverparameter.etc"

// materialRateBonus 按职业表声明的合成器角色读取当前PVF中的万分比加成。
// 参考86的加法算法，数值和道具编号均不采用86源码里的固定常量。
// 优先使用选中池的角色，保留既有跨可用变体的材料准入范围；动作类钻石由
// 调用方在验证资格后传入选中表的master编号。普通合成不依赖加成文件。
func (c currentAvatarCompoundConfig) materialRateBonus(source dnfpvf.Source, selected currentAvatarCompoundVariant, cubeItemID uint32) (int64, string, error) {
	role := selected.MaterialRoles[cubeItemID]
	if role == "" {
		for _, variant := range c.Variants {
			candidate := variant.MaterialRoles[cubeItemID]
			if candidate == "" {
				continue
			}
			if role != "" && role != candidate {
				return 0, "", fmt.Errorf("%w: 合成器%d在可用配置中角色冲突", errCurrentAvatarCompoundRequestInvalid, cubeItemID)
			}
			role = candidate
		}
	}
	var field string
	switch role {
	case "material":
		return 0, role, nil
	case "material_upper":
		field = "upper bind cube rate bonus"
	case "material_master":
		field = "master bind cube rate bonus"
	default:
		return 0, role, fmt.Errorf("%w: 合成器%d没有有效的概率角色", errCurrentAvatarCompoundRequestInvalid, cubeItemID)
	}
	if source == nil {
		return 0, role, fmt.Errorf("%w: [%s]的PVF来源不可用", errCurrentAvatarCompoundRequestInvalid, field)
	}
	text, err := source.ReadText(currentAvatarCompoundServerParameterPath)
	if err != nil {
		return 0, role, fmt.Errorf("读取合成加成%s [%s]: %w", currentAvatarCompoundServerParameterPath, field, err)
	}
	document, err := dnfpvf.Parse(currentAvatarCompoundServerParameterPath, text)
	if err != nil {
		return 0, role, fmt.Errorf("解析合成加成%s [%s]: %w", currentAvatarCompoundServerParameterPath, field, err)
	}
	var tokens []dnfpvf.Token
	found := false
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), field) {
			continue
		}
		if found || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
			return 0, role, fmt.Errorf("%w: %s的[%s]重复或段落无效", errCurrentAvatarCompoundRequestInvalid, currentAvatarCompoundServerParameterPath, field)
		}
		tokens = document.Tokens[section.Start:section.End]
		found = true
	}
	if !found || len(tokens) != 1 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int < 0 {
		return 0, role, fmt.Errorf("%w: %s的[%s]须为单个非负整数", errCurrentAvatarCompoundRequestInvalid, currentAvatarCompoundServerParameterPath, field)
	}
	return tokens[0].Int, role, nil
}

// bonusRateWithMaterial 先算材料品质概率，再叠加PVF加成并限制在0..10000。
// 先比较剩余额度，避免PVF极大非负整数相加溢出。
func (v currentAvatarCompoundVariant) bonusRateWithMaterial(part string, grade1 int64, grade2 int64, materialBonus int64) int64 {
	rate := v.bonusRateFor(part, grade1, grade2)
	if materialBonus <= 0 {
		return rate
	}
	if materialBonus >= currentAvatarCompoundRateScale-rate {
		return currentAvatarCompoundRateScale
	}
	return rate + materialBonus
}
