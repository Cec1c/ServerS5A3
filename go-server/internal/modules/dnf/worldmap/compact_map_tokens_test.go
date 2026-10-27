package worldmap

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"sync"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func compactTokenFixture() Map {
	tokens := make([]dnfpvf.Token, 256)
	for i := range tokens {
		n := int64(i - 80)
		if i == 0 {
			n = math.MinInt64
		}
		if i == 1 {
			n = math.MaxInt64
		}
		raw := strconv.FormatInt(n, 10)
		tokens[i] = dnfpvf.Token{Kind: dnfpvf.TokenInt, Raw: raw, Value: raw, Int: n, Line: i + 2, Column: i + 1}
	}
	return Map{ID: 123, Path: "map/test.map", SourceSections: []RawSection{{Name: "tile map", Occurrence: 2, Scope: []string{"nested"}, Block: true, Tokens: tokens}}, Monsters: []MonsterSpawn{{MonsterID: 77, RandomDropCount: 2}}, UnknownSections: []RawSection{{Name: "unknown", Tokens: []dnfpvf.Token{{Kind: dnfpvf.TokenIdent, Raw: "opaque", Value: "opaque"}}}}}
}

func TestCompactMapTokensPreservePublicValuesAndIndependentCopies(t *testing.T) {
	original := compactTokenFixture()
	stored := original
	compactMapSourceTokens(&stored)
	if len(stored.compactSourceTokens) != 1 || stored.SourceSections[0].Tokens != nil {
		t.Fatal("长整数段没有实际紧凑保存")
	}
	if len(original.SourceSections[0].Tokens) != 256 {
		t.Fatal("改变了调用者的分段切片")
	}
	got := cloneMap(stored)
	if !reflect.DeepEqual(got, original) {
		t.Fatal("公开地图字段改变")
	}
	a, _ := json.Marshal(original)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatal("公开JSON改变")
	}
	got.SourceSections[0].Tokens[0].Int = 999
	got.SourceSections[0].Scope[0] = "changed"
	if !reflect.DeepEqual(cloneMap(stored), original) {
		t.Fatal("查询结果改动污染了目录")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if !reflect.DeepEqual(cloneMap(stored), original) {
					t.Error("并发查询改变了数据")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCompactMapTokensPreserveAllKindsAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*dnfpvf.Token)
	}{
		{"前导零", func(v *dnfpvf.Token) { v.Raw = "001"; v.Value = "001"; v.Int = 1 }},
		{"原始值不同", func(v *dnfpvf.Token) { v.Value = "other" }},
		{"浮点字段", func(v *dnfpvf.Token) { v.Float = 1 }},
		{"负零浮点", func(v *dnfpvf.Token) { v.Float = math.Copysign(0, -1) }},
		{"符号", func(v *dnfpvf.Token) { v.Kind = dnfpvf.TokenSymbol }},
		{"字符串", func(v *dnfpvf.Token) { v.Kind = dnfpvf.TokenString; v.Raw = "`str`"; v.Value = "str" }},
		{"未知类型", func(v *dnfpvf.Token) { v.Kind = "custom" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := compactTokenFixture()
			tc.change(&m.SourceSections[0].Tokens[50])
			want := cloneMap(m)
			compactMapSourceTokens(&m)
			got := cloneMap(m)
			if len(m.compactSourceTokens) != 1 || !reflect.DeepEqual(got, want) || math.Float64bits(got.SourceSections[0].Tokens[50].Float) != math.Float64bits(want.SourceSections[0].Tokens[50].Float) {
				t.Fatal("原始元数据未完整恢复")
			}
		})
	}
	m := compactTokenFixture()
	m.SourceSections[0].Tokens = m.SourceSections[0].Tokens[:4]
	want := cloneMap(m)
	compactMapSourceTokens(&m)
	if !reflect.DeepEqual(m, want) {
		t.Fatal("短段发生变化")
	}
	compactMapSourceTokens(nil)
}
func TestCompactMapTokensKeepUnrepresentablePositions(t *testing.T) {
	for _, pos := range []int{-1, math.MaxUint16 + 1} {
		m := compactTokenFixture()
		m.SourceSections[0].Tokens[0].Column = pos
		want := cloneMap(m)
		compactMapSourceTokens(&m)
		if m.compactSourceTokens != nil || !reflect.DeepEqual(m, want) {
			t.Fatal("超出紧凑范围的源码位置被改动")
		}
	}
}
