package equipmentservice

import (
	"fmt"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
	"strings"
)

// 已有真档允许下一节隐式结束上一节；校验数值、完整行和重复定义，
// 不把合法零权重或-1占位符改成价格，也不从损坏表跳过半行继续扣费。
func integerRows(tokens []pvf.Token, name string, width, leading int) ([]int64, error) {
	values := make([]int64, 0, len(tokens))
	for _, t := range tokens {
		if t.Kind == pvf.TokenSection && strings.HasPrefix(strings.TrimSpace(t.Value), "/") {
			continue
		}
		if t.Kind != pvf.TokenInt {
			return nil, fmt.Errorf("%w: %s含非整数行", ErrRules, name)
		}
		values = append(values, t.Int)
	}
	if len(values) < leading || (len(values)-leading)%width != 0 {
		return nil, fmt.Errorf("%w: %s行长度不完整", ErrRules, name)
	}
	return values, nil
}
func uniqueSection(tokens []pvf.Token, name string) ([]pvf.Token, bool, error) {
	count := 0
	for _, t := range tokens {
		if isPVFSectionToken(t, name) {
			count++
		}
	}
	if count > 1 {
		return nil, false, fmt.Errorf("%w: %s重复", ErrRules, name)
	}
	return sectionPVFTokens(tokens, name), count == 1, nil
}
func validateRanges(values []int64, name string, maxWeight int64) error {
	for n := 0; n < len(values); n += 3 {
		low, high, weight := values[n], values[n+1], values[n+2]
		if low <= 0 || high < low || weight <= 0 || maxWeight > 0 && weight > maxWeight || n > 0 && low <= values[n-2] {
			return fmt.Errorf("%w: %s区间或权重无效", ErrRules, name)
		}
	}
	return nil
}
func validateRarityKeys(values []int64, width int, name string) error {
	seen := map[int64]bool{}
	for n := 0; n < len(values); n += width {
		key := values[n]
		if key < 0 || key > 6 || seen[key] {
			return fmt.Errorf("%w: %s品质键无效或重复", ErrRules, name)
		}
		seen[key] = true
	}
	return nil
}
func validateResealDocument(d *pvf.Document) error {
	for _, name := range []string{"grade", "reseal count limit", "reseal soul index"} {
		tokens, present, err := uniqueSection(d.Tokens, name)
		if err != nil {
			return err
		}
		if !present || len(tokens) == 0 {
			return fmt.Errorf("%w: 缺少%s", ErrRules, name)
		}
		width := 3
		if name == "reseal soul index" {
			width = 2
		}
		values, err := integerRows(tokens, name, width, 0)
		if err != nil {
			return err
		}
		if width == 3 {
			maxWeight := int64(0)
			if name == "reseal count limit" {
				maxWeight = 7
			}
			if err = validateRanges(values, name, maxWeight); err != nil {
				return err
			}
		} else {
			if err = validateRarityKeys(values, 2, name); err != nil {
				return err
			}
			for n := 1; n < len(values); n += 2 {
				if values[n] <= 0 || values[n] > math.MaxUint32 {
					return fmt.Errorf("%w: 灵魂编号无效", ErrRules)
				}
			}
		}
	}
	for _, name := range []string{"rarity weight", "extended rarity weight", "soul cost weight", "soul reseal constant"} {
		tokens, present, err := uniqueSection(d.Tokens, name)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("%w: 缺少%s", ErrRules, name)
		}
		count := 0
		for _, t := range tokens {
			if t.Kind == pvf.TokenSection && strings.HasPrefix(strings.TrimSpace(t.Value), "/") {
				continue
			}
			if t.Kind != pvf.TokenInt && t.Kind != pvf.TokenFloat {
				return fmt.Errorf("%w: %s含非数值", ErrRules, name)
			}
			count++
		}
		values := d.Numbers(name)
		if count != 7 || len(values) != 7 {
			return fmt.Errorf("%w: %s必须为7个品质权重", ErrRules, name)
		}
		for _, v := range values {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("%w: %s含非法权重", ErrRules, name)
			}
		}
	}
	return nil
}
func validateTransferDocument(d *pvf.Document) error {
	types := map[int64]bool{}
	for n := 0; n < len(d.Tokens); n++ {
		if !isPVFSectionToken(d.Tokens[n], "stone type") {
			continue
		}
		end := n + 1
		for end < len(d.Tokens) && !isPVFSectionToken(d.Tokens[end], "/stone type") {
			end++
		}
		if end == len(d.Tokens) {
			return fmt.Errorf("%w: 跨界类型未闭合", ErrRules)
		}
		group := d.Tokens[n+1 : end]
		if len(group) == 0 || group[0].Kind != pvf.TokenInt || group[0].Int < 0 || types[group[0].Int] {
			return fmt.Errorf("%w: 跨界类型无效或重复", ErrRules)
		}
		types[group[0].Int] = true
		for _, s := range []struct {
			name           string
			width, leading int
		}{
			{"cargo transport stone grade", 3, 0}, {"cargo transport stone rarity weight", 2, 0}, {"cargo transport stone soul index", 2, 0}, {"cargo transport stone soul weight", 5, 0}, {"cargo transport stone element crystals cost weight", 5, 1}, {"cargo transport stone particular cost", 4, 0}, {"cargo transport stone epic piece cost weight", 4, 0}, {"cargo transport stone chronicle cost", 1, 0},
			{"cargo transport stone include index", 1, 0}, {"cargo transport stone except index", 1, 0},
		} {
			tokens, present, err := uniqueSection(group, s.name)
			if err != nil {
				return err
			}
			if !present {
				continue
			}
			values, err := integerRows(tokens, s.name, s.width, s.leading)
			if err != nil {
				return err
			}
			if len(values) == 0 && s.name != "cargo transport stone include index" && s.name != "cargo transport stone except index" {
				return fmt.Errorf("%w: %s为空", ErrRules, s.name)
			}
			switch s.name {
			case "cargo transport stone grade":
				if err = validateRanges(values, s.name, 0); err != nil {
					return err
				}
			case "cargo transport stone rarity weight", "cargo transport stone soul index", "cargo transport stone soul weight":
				if err = validateRarityKeys(values, s.width, s.name); err != nil {
					return err
				}
				if s.name == "cargo transport stone soul index" {
					for n := 1; n < len(values); n += 2 {
						if values[n] <= 0 || values[n] > math.MaxUint32 {
							return fmt.Errorf("%w: 跨界灵魂编号无效", ErrRules)
						}
					}
				}
			case "cargo transport stone particular cost":
				for n := 0; n < len(values); n += 4 {
					if values[n] < 0 || values[n] > 6 || values[n+1] <= 0 || values[n+2] <= 0 || values[n+2] > math.MaxUint32 || values[n+3] <= 0 {
						return fmt.Errorf("%w: 跨界额外材料费用无效", ErrRules)
					}
				}
			case "cargo transport stone epic piece cost weight", "cargo transport stone chronicle cost":
				if len(values) != s.width {
					return fmt.Errorf("%w: %s必须是单行", ErrRules, s.name)
				}
			case "cargo transport stone include index", "cargo transport stone except index":
				for _, id := range values {
					if id <= 0 || id > math.MaxUint32 {
						return fmt.Errorf("%w: 跨界物品名单编号无效", ErrRules)
					}
				}
			case "cargo transport stone element crystals cost weight":
				if values[0] <= 0 || values[0] > math.MaxUint32 {
					return fmt.Errorf("%w: 结晶编号无效", ErrRules)
				}
				if err = validateRarityKeys(values[1:], 5, s.name); err != nil {
					return err
				}
			}
		}
		n = end
	}
	return nil
}
