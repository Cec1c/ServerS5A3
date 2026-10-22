package guildcompound

import (
	"errors"
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	compoundPath    = "etc/guilddungeoncompound.etc"
	compoundGemKey  = "compound info"
	compoundFlagKey = "guild flag compound info"
)

var (
	ErrCompoundCatalogUnavailable = errors.New("guild compound catalog is unavailable")
	ErrCompoundCatalogInvalid     = errors.New("guild compound catalog is malformed")
)

// 2026-10-22 用户最新确认的运营规则：
//   - 合法输入必定产出一件物品；PVF 第三列的历史概率不再作为失败依据。
//   - 守护珠恰好放入 4 颗同 ID 时，升为同族下一品级；其余合法输入从下一品级
//     中随机产出。
//   - 勋章从下一品级中随机产出。
//   - 输入材料必须同品级且与请求品级一致，产出使用下一档，不允许混档输入。
//
// 本文件只负责配方和件数。第三列仍按原表读取，供旧数据核对；事务层不再用它
// 决定成败。扣料及产物选择由合成事务层执行。

// CompoundKind 区分守护珠和勋章两条配方阶梯。
type CompoundKind byte

const (
	CompoundGem CompoundKind = iota
	CompoundFlag
)

func (k CompoundKind) String() string {
	if k == CompoundFlag {
		return "flag"
	}
	return "gem"
}

// CompoundMinMaterialCount 是公会合成允许的最少投入件数。
const CompoundMinMaterialCount uint32 = 2

// CompoundRecipe 是 PVF 中一行 grade、最大件数和历史每件概率。
type CompoundRecipe struct {
	// Rarity 是输入物品所在品级，对应 PVF 每行第一列。
	Rarity uint32
	// MaxMaterialCount 是该档最多允许投入的件数。
	MaxMaterialCount uint32
	// RatePerMaterial 是每件材料贡献的万分率。
	RatePerMaterial uint32
}

// Compoundable 报告该档是否具有正向成功率且件数范围符合 2..4。
func (r CompoundRecipe) Compoundable() bool {
	return r.RatePerMaterial > 0 &&
		r.MaxMaterialCount >= CompoundMinMaterialCount &&
		r.MaxMaterialCount <= MaxMaterialCount
}

// AcceptsMaterialCount 报告该档是否接受这个投入件数。
func (r CompoundRecipe) AcceptsMaterialCount(count uint32) bool {
	return r.Compoundable() &&
		count >= CompoundMinMaterialCount &&
		count <= MaxMaterialCount &&
		count <= r.MaxMaterialCount
}

// SuccessRateFor 返回旧 PVF 表第三列派生的万分率，最高为 10000。
// 当前合成事务不再调用它决定成败；合法输入必定产出。保留它是为了继续核对
// 原表值和历史配置，不要把它重新接回失败判定。
func (r CompoundRecipe) SuccessRateFor(count uint32) uint32 {
	if !r.AcceptsMaterialCount(count) {
		return 0
	}
	// 先做除法判断，避免调用方构造的任意 RatePerMaterial 在乘法时溢出。
	if r.RatePerMaterial > CompoundRateDenominator/count {
		return CompoundRateDenominator
	}
	rate := r.RatePerMaterial * count
	if rate > CompoundRateDenominator {
		return CompoundRateDenominator
	}
	return rate
}

// CompoundLadder 是按 grade 严格升序保存的配方阶梯。
type CompoundLadder struct {
	rows []CompoundRecipe
}

// Recipe 返回指定 grade 的配方。
func (l CompoundLadder) Recipe(rarity uint32) (CompoundRecipe, bool) {
	for _, row := range l.rows {
		if row.Rarity == rarity {
			return row, true
		}
	}
	return CompoundRecipe{}, false
}

// NextRarity 返回阶梯中的下一档。0 率末档或没有下一档时返回 false。
func (l CompoundLadder) NextRarity(rarity uint32) (uint32, bool) {
	for index, row := range l.rows {
		if row.Rarity != rarity {
			continue
		}
		if !row.Compoundable() || index+1 >= len(l.rows) {
			return 0, false
		}
		return l.rows[index+1].Rarity, true
	}
	return 0, false
}

// Rarities 按升序返回阶梯中的全部 grade。
func (l CompoundLadder) Rarities() []uint32 {
	out := make([]uint32, 0, len(l.rows))
	for _, row := range l.rows {
		out = append(out, row.Rarity)
	}
	return out
}

// CompoundCatalog 同时保存守护珠与勋章配方。
type CompoundCatalog struct {
	gem  CompoundLadder
	flag CompoundLadder
}

// Ladder 返回指定合成类型的配方阶梯。
func (c CompoundCatalog) Ladder(kind CompoundKind) CompoundLadder {
	if kind == CompoundFlag {
		return c.flag
	}
	return c.gem
}

// LoadCompoundCatalog 从 etc/guilddungeoncompound.etc 读取两条配方阶梯。
func LoadCompoundCatalog(source dnfpvf.Source) (CompoundCatalog, error) {
	if source == nil {
		return CompoundCatalog{}, ErrCompoundCatalogUnavailable
	}
	text, err := source.ReadText(compoundPath)
	if err != nil {
		return CompoundCatalog{}, fmt.Errorf("read %s: %w", compoundPath, err)
	}
	document, err := dnfpvf.Parse(compoundPath, text)
	if err != nil {
		return CompoundCatalog{}, fmt.Errorf("parse %s: %w", compoundPath, err)
	}
	gem, err := loadCompoundLadder(document, compoundGemKey, "/compound info")
	if err != nil {
		return CompoundCatalog{}, err
	}
	// PVF 原文的结束标记实际拼作 "[/guid flag compound info]"。
	flag, err := loadCompoundLadder(document, compoundFlagKey, "/guid flag compound info")
	if err != nil {
		return CompoundCatalog{}, err
	}
	return CompoundCatalog{gem: gem, flag: flag}, nil
}

func loadCompoundLadder(document *dnfpvf.Document, key, closing string) (CompoundLadder, error) {
	values, err := compoundSectionInts(document, key, closing)
	if err != nil {
		return CompoundLadder{}, err
	}
	if len(values) < 3 || len(values)%3 != 0 {
		return CompoundLadder{}, fmt.Errorf("%w: %s has %d values", ErrCompoundCatalogInvalid, key, len(values))
	}

	rows := make([]CompoundRecipe, 0, len(values)/3)
	for index := 0; index < len(values); index += 3 {
		rarity, count, rate := values[index], values[index+1], values[index+2]
		if rarity < 0 || count < 0 || rate < 0 ||
			rarity > math.MaxUint32 || count > math.MaxUint32 || rate > math.MaxUint32 {
			return CompoundLadder{}, fmt.Errorf("%w: %s row %d has a negative or oversized value", ErrCompoundCatalogInvalid, key, index/3)
		}
		if count < int64(CompoundMinMaterialCount) || count > int64(MaxMaterialCount) {
			return CompoundLadder{}, fmt.Errorf("%w: %s row %d material count %d is outside 2..4", ErrCompoundCatalogInvalid, key, index/3, count)
		}
		if rate > int64(CompoundRateDenominator) {
			return CompoundLadder{}, fmt.Errorf("%w: %s row %d rate %d exceeds %d", ErrCompoundCatalogInvalid, key, index/3, rate, CompoundRateDenominator)
		}
		if index > 0 && rarity <= int64(rows[len(rows)-1].Rarity) {
			return CompoundLadder{}, fmt.Errorf("%w: %s grades are not strictly ascending at row %d", ErrCompoundCatalogInvalid, key, index/3)
		}
		rows = append(rows, CompoundRecipe{
			Rarity:           uint32(rarity),
			MaxMaterialCount: uint32(count),
			RatePerMaterial:  uint32(rate),
		})
	}
	for index, row := range rows {
		terminal := index == len(rows)-1
		if terminal && row.RatePerMaterial != 0 {
			return CompoundLadder{}, fmt.Errorf("%w: %s terminal row must have rate 0", ErrCompoundCatalogInvalid, key)
		}
		if !terminal && row.RatePerMaterial == 0 {
			return CompoundLadder{}, fmt.Errorf("%w: %s non-terminal row %d has rate 0", ErrCompoundCatalogInvalid, key, index)
		}
	}
	return CompoundLadder{rows: rows}, nil
}

func compoundSectionInts(document *dnfpvf.Document, key, closing string) ([]int64, error) {
	var values []int64
	matches := 0
	for index, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), key) {
			continue
		}
		matches++
		if matches > 1 {
			return nil, fmt.Errorf("%w: duplicate section %q", ErrCompoundCatalogInvalid, key)
		}
		if index+1 >= len(document.Sections) ||
			!strings.EqualFold(strings.TrimSpace(document.Sections[index+1].Name), closing) {
			return nil, fmt.Errorf("%w: section %q must close with %q", ErrCompoundCatalogInvalid, key, closing)
		}
		tokens := document.Tokens[section.Start:section.End]
		values = make([]int64, 0, len(tokens))
		for _, token := range tokens {
			if token.Kind != dnfpvf.TokenInt {
				return nil, fmt.Errorf("%w: section %q contains non-integer token %q at %d:%d",
					ErrCompoundCatalogInvalid, key, token.Raw, token.Line, token.Column)
			}
			values = append(values, token.Int)
		}
	}
	if matches == 0 {
		return nil, fmt.Errorf("%w: missing section %q", ErrCompoundCatalogInvalid, key)
	}
	return values, nil
}

// CompoundRateDenominator 是成功率分母，满概率为 10000。
const CompoundRateDenominator uint32 = 10000
