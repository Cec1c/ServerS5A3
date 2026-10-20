package skillcmd

import (
	"strings"
	"testing"
)

// TestFormatGrowTypeDetailSeparatesMissingFromUntransferred 锁定拒绝串的判据：
// `grow=%d` 单独一个数字无法区分"合法未转职"与"权威键没水合"，而这两种情况
// 需要完全不同的下一步动作（数据修复 vs 仓储排查）。
func TestFormatGrowTypeDetailSeparatesMissingFromUntransferred(t *testing.T) {
	tests := []struct {
		name    string
		packed  int
		present bool
		want    []string
		absent  []string
	}{
		{
			name: "authoritative key missing", packed: 0, present: false,
			want:   []string{"grow_present=false", "grow_out_of_domain=false"},
			absent: []string{"branch="},
		},
		{
			name: "legal untransferred", packed: 0, present: true,
			want: []string{"grow_present=true", "branch=0", "awakening=0", "grow_out_of_domain=false"},
		},
		{
			name: "second awakening branch 1", packed: 0x21, present: true,
			want: []string{"grow_present=true", "branch=1", "awakening=2", "grow_out_of_domain=false"},
		},
		{
			name: "out of tinyint domain", packed: 256, present: true,
			want: []string{"grow_present=true", "grow_out_of_domain=true", "grow_raw=0"},
		},
	}
	for _, test := range tests {
		got := formatGrowTypeDetail(test.packed, test.present)
		for _, want := range test.want {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: %q must contain %q", test.name, got, want)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(got, absent) {
				t.Fatalf("%s: %q must not contain %q", test.name, got, absent)
			}
		}
	}
}
