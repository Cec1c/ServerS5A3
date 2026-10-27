package guild

import (
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

// PracticeMonsterCatalog is the native agitwar passive-object -> monster
// table. Unmapped facilities remain passive actors and have no combat key.
type PracticeMonsterCatalog map[uint32]uint32

func LoadPracticeMonsterCatalog(source pvf.Source) (PracticeMonsterCatalog, error) {
	if source == nil {
		return nil, ErrPracticeCatalog
	}
	text, err := source.ReadText("etc/agitwarpassivemonsters.etc")
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse("etc/agitwarpassivemonsters.etc", text)
	if err != nil {
		return nil, err
	}
	var result PracticeMonsterCatalog
	for _, section := range doc.Sections {
		if strings.ToLower(strings.TrimSpace(section.Name)) != "agitwar monster build" {
			continue
		}
		if result != nil {
			return nil, ErrPracticeCatalog
		}
		tokens := doc.Tokens[section.Start:section.End]
		if len(tokens) == 0 || len(tokens)%2 != 0 {
			return nil, ErrPracticeCatalog
		}
		result = make(PracticeMonsterCatalog, len(tokens)/2)
		for i := 0; i < len(tokens); i += 2 {
			for _, token := range tokens[i : i+2] {
				if token.Kind != pvf.TokenInt || token.Int <= 0 || token.Int > math.MaxInt32 {
					return nil, ErrPracticeCatalog
				}
			}
			key, value := uint32(tokens[i].Int), uint32(tokens[i+1].Int)
			if _, exists := result[key]; exists {
				return nil, ErrPracticeCatalog
			}
			result[key] = value
		}
	}
	if result == nil {
		return nil, ErrPracticeCatalog
	}
	return result, nil
}
