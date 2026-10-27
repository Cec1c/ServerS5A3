package worldmap

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
)

// 目录内只保存字符串池下标和原数值；公开查询恢复完整 Token 副本。
// 原始文本、浮点位、顺序、源码位置及玩法字段均保留，不重新读盘或解析 PVF。
type compactMapToken struct {
	integer          int64
	floating         float64
	kind, raw, value uint32
	line, column     uint16
}

const compactMapTokenMinCount = 8

func compactMapSourceTokens(m *Map) {
	if m == nil || len(m.compactSourceTokens) != 0 {
		return
	}
	var total uint64
	for _, section := range m.SourceSections {
		total += uint64(len(section.Tokens))
		if total > math.MaxUint32/3 {
			return
		}
	}
	var indices map[string]uint32
	intern := func(text string) uint32 {
		if i, ok := indices[text]; ok {
			return i
		}
		i := uint32(len(m.compactSourceTexts))
		m.compactSourceTexts = append(m.compactSourceTexts, text)
		indices[text] = i
		return i
	}
	for index, section := range m.SourceSections {
		if len(section.Tokens) < compactMapTokenMinCount {
			continue
		}
		fits := true
		for _, token := range section.Tokens {
			if token.Line < 0 || token.Line > math.MaxUint16 || token.Column < 0 || token.Column > math.MaxUint16 {
				fits = false
				break
			}
		}
		if !fits {
			continue
		}
		if m.compactSourceTokens == nil {
			m.compactSourceTokens = make(map[int][]compactMapToken)
			m.SourceSections = append([]RawSection(nil), m.SourceSections...)
			indices = make(map[string]uint32)
		}
		packed := make([]compactMapToken, len(section.Tokens))
		for i, token := range section.Tokens {
			packed[i] = compactMapToken{integer: token.Int, floating: token.Float, kind: intern(string(token.Kind)), raw: intern(token.Raw), value: intern(token.Value), line: uint16(token.Line), column: uint16(token.Column)}
		}
		m.compactSourceTokens[index] = packed
		m.SourceSections[index].Tokens = nil
	}
}
func expandMapTokens(packed []compactMapToken, texts []string) []dnfpvf.Token {
	tokens := make([]dnfpvf.Token, len(packed))
	for i, token := range packed {
		tokens[i] = dnfpvf.Token{Kind: dnfpvf.TokenKind(texts[token.kind]), Raw: texts[token.raw], Value: texts[token.value], Int: token.integer, Float: token.floating, Line: int(token.line), Column: int(token.column)}
	}
	return tokens
}
