package dnfbridge

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentRandomTitleEnchantPath = "etc/randomtitleenchant.etc"

var (
	errRandomTitleEnchantCatalogUnavailable = errors.New("random title enchant catalog is unavailable")
	errRandomTitleEnchantConfigMissing      = errors.New("random title enchant config is missing")
	errRandomTitleEnchantRowsInvalid        = errors.New("random title enchant rows are invalid")
)

type currentRandomTitleEnchantOption struct {
	Job     string
	SkillID int64
	Level   int64
}

type currentRandomTitleEnchantRow struct {
	ID      uint16
	Weight  int64
	Options []currentRandomTitleEnchantOption
}

type currentRandomTitleEnchantConfig struct {
	ID   uint16
	Rows []currentRandomTitleEnchantRow
}

type currentRandomTitleEnchantCatalog struct {
	Configs map[uint16]currentRandomTitleEnchantConfig
}

func parseCurrentRandomTitleEnchantCatalog(source dnfpvf.Source) (currentRandomTitleEnchantCatalog, error) {
	if source == nil {
		return currentRandomTitleEnchantCatalog{}, errRandomTitleEnchantCatalogUnavailable
	}
	text, err := source.ReadText(currentRandomTitleEnchantPath)
	if err != nil {
		return currentRandomTitleEnchantCatalog{}, fmt.Errorf("read %s: %w", currentRandomTitleEnchantPath, err)
	}
	document, err := dnfpvf.Parse(currentRandomTitleEnchantPath, text)
	if err != nil {
		return currentRandomTitleEnchantCatalog{}, fmt.Errorf("parse %s: %w", currentRandomTitleEnchantPath, err)
	}
	catalog := currentRandomTitleEnchantCatalog{Configs: make(map[uint16]currentRandomTitleEnchantConfig)}
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "index") || section.Start < 0 || section.End > len(document.Tokens) || section.Start >= section.End {
			continue
		}
		config, ok, parseErr := parseCurrentRandomTitleEnchantIndex(document.Tokens[section.Start:section.End])
		if parseErr != nil {
			return currentRandomTitleEnchantCatalog{}, fmt.Errorf("parse random title enchant index: %w", parseErr)
		}
		if ok {
			if _, exists := catalog.Configs[config.ID]; !exists {
				catalog.Configs[config.ID] = config
			}
		}
	}
	return catalog, nil
}

func parseCurrentRandomTitleEnchantIndex(tokens []dnfpvf.Token) (currentRandomTitleEnchantConfig, bool, error) {
	if len(tokens) == 0 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int <= 0 || tokens[0].Int > 0xffff {
		return currentRandomTitleEnchantConfig{}, false, nil
	}
	config := currentRandomTitleEnchantConfig{ID: uint16(tokens[0].Int)}
	pos := 1
	seen := make(map[uint16]struct{})
	for pos < len(tokens) {
		if pos+2 >= len(tokens) || tokens[pos].Kind != dnfpvf.TokenInt || tokens[pos+1].Kind != dnfpvf.TokenInt || tokens[pos+2].Kind != dnfpvf.TokenInt {
			return currentRandomTitleEnchantConfig{}, false, fmt.Errorf("%w: config=%d row header at token=%d", errRandomTitleEnchantRowsInvalid, config.ID, pos)
		}
		rowID, optionCount, weight := tokens[pos].Int, tokens[pos+1].Int, tokens[pos+2].Int
		pos += 3
		if rowID <= 0 || rowID > 0xffff || optionCount < 1 || optionCount > 2 || weight < 0 {
			return currentRandomTitleEnchantConfig{}, false, fmt.Errorf("%w: config=%d row=%d options=%d weight=%d", errRandomTitleEnchantRowsInvalid, config.ID, rowID, optionCount, weight)
		}
		row := currentRandomTitleEnchantRow{ID: uint16(rowID), Weight: weight, Options: make([]currentRandomTitleEnchantOption, 0, optionCount)}
		for index := int64(0); index < optionCount; index++ {
			if pos+2 >= len(tokens) || (tokens[pos].Kind != dnfpvf.TokenString && tokens[pos].Kind != dnfpvf.TokenIdent) || tokens[pos+1].Kind != dnfpvf.TokenInt || tokens[pos+2].Kind != dnfpvf.TokenInt {
				return currentRandomTitleEnchantConfig{}, false, fmt.Errorf("%w: config=%d row=%d option=%d", errRandomTitleEnchantRowsInvalid, config.ID, rowID, index)
			}
			if strings.TrimSpace(tokens[pos].Value) == "" || tokens[pos+1].Int <= 0 || tokens[pos+2].Int <= 0 {
				return currentRandomTitleEnchantConfig{}, false, errRandomTitleEnchantRowsInvalid
			}
			row.Options = append(row.Options, currentRandomTitleEnchantOption{Job: strings.TrimSpace(tokens[pos].Value), SkillID: tokens[pos+1].Int, Level: tokens[pos+2].Int})
			pos += 3
		}
		if _, exists := seen[row.ID]; exists {
			continue
		}
		seen[row.ID] = struct{}{}
		config.Rows = append(config.Rows, row)
	}
	return config, true, nil
}

func (c currentRandomTitleEnchantCatalog) choose(configID uint16) (currentRandomTitleEnchantRow, error) {
	config, ok := c.Configs[configID]
	if !ok {
		return currentRandomTitleEnchantRow{}, errRandomTitleEnchantConfigMissing
	}
	var total int64
	for _, row := range config.Rows {
		if row.Weight > 0 {
			if total > math.MaxInt64-row.Weight {
				return currentRandomTitleEnchantRow{}, errRandomTitleEnchantRowsInvalid
			}
			total += row.Weight
		}
	}
	if total <= 0 {
		return currentRandomTitleEnchantRow{}, errRandomTitleEnchantRowsInvalid
	}
	roll, err := cryptorand.Int(cryptorand.Reader, big.NewInt(total))
	if err != nil {
		return currentRandomTitleEnchantRow{}, fmt.Errorf("random title enchant roll: %w", err)
	}
	return chooseCurrentRandomTitleEnchantRow(config.Rows, roll.Int64()), nil
}

func chooseCurrentRandomTitleEnchantRow(rows []currentRandomTitleEnchantRow, roll int64) currentRandomTitleEnchantRow {
	for _, row := range rows {
		if row.Weight <= 0 {
			continue
		}
		if roll < row.Weight {
			return row
		}
		roll -= row.Weight
	}
	return currentRandomTitleEnchantRow{}
}
