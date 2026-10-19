package progression

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestMainQuestExperiencePercentUsesCumulativeThresholdDifference(t *testing.T) {
	tables := mainQuestPercentTestTables(t, 90)
	for _, test := range []struct {
		level int
		want  uint32
	}{
		{level: 1, want: 150},
		{level: 3, want: 192},
		{level: 15, want: 14556},
		{level: 35, want: 235974},
		{level: 65, want: 2440828},
		{level: 88, want: 32029581},
		{level: 89, want: 42444667},
	} {
		t.Run(fmt.Sprintf("level-%d", test.level), func(t *testing.T) {
			got, err := tables.MainQuestExperiencePercent(test.level, 15)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("MainQuestExperiencePercent(%d, 15)=%d want=%d", test.level, got, test.want)
			}
		})
	}

	for percent, want := range map[int]uint32{
		0:   0,
		10:  157316,
		20:  314632,
		100: 1573160,
	} {
		got, err := tables.MainQuestExperiencePercent(35, percent)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("MainQuestExperiencePercent(35, %d)=%d want=%d", percent, got, want)
		}
	}
}

func TestMainQuestExperiencePercentRejectsInvalidInput(t *testing.T) {
	tables := mainQuestPercentTestTables(t, 4)
	for _, percent := range []int{-1, 101, math.MaxInt} {
		if _, err := tables.MainQuestExperiencePercent(1, percent); !errors.Is(err, ErrQuestExperiencePercent) {
			t.Fatalf("percent=%d error=%v", percent, err)
		}
	}
	for _, level := range []int{-1, 0, 5} {
		if _, err := tables.MainQuestExperiencePercent(level, 15); !errors.Is(err, ErrLevelOutOfRange) {
			t.Fatalf("level=%d error=%v", level, err)
		}
	}

	source := progressionTestSource()
	source[ExperienceTablePath] = "100 100 300 600\n"
	equal, err := Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := equal.MainQuestExperiencePercent(2, 15); !errors.Is(err, ErrTableMalformed) {
		t.Fatalf("equal adjacent threshold error=%v", err)
	}
}

func TestMainQuestExperiencePercentWideArithmeticAndRounding(t *testing.T) {
	for _, tc := range []struct {
		thresholds     []uint32
		level, percent int
		want           uint32
	}{
		{[]uint32{math.MaxUint32}, 1, 100, math.MaxUint32},
		{[]uint32{math.MaxUint32}, 1, 15, 644245094},
		{[]uint32{math.MaxUint32 - 7, math.MaxUint32}, 2, 15, 1},
		{[]uint32{5}, 1, 15, 0},
	} {
		tables := &Tables{experienceThresholds: tc.thresholds}
		got, err := tables.MainQuestExperiencePercent(tc.level, tc.percent)
		if err != nil || got != tc.want {
			t.Fatalf("thresholds=%v level=%d percent=%d got=%d want=%d err=%v", tc.thresholds, tc.level, tc.percent, got, tc.want, err)
		}
	}
	var absent *Tables
	if _, err := absent.MainQuestExperiencePercent(1, 15); !errors.Is(err, ErrLevelOutOfRange) {
		t.Fatalf("missing table: %v", err)
	}
}

func mainQuestPercentTestTables(t *testing.T, levels int) *Tables {
	t.Helper()
	// Explicit interval fixtures are independent of the expected percentage outputs.
	spans := map[int]uint32{1: 1000, 3: 1280, 15: 97040, 35: 1573160, 65: 16272192, 88: 213530540, 89: 282964448}
	thresholds := make([]uint64, 0, levels)
	var cumulative uint64
	for level := 1; level <= levels; level++ {
		increment := uint64(1000)
		if span, exists := spans[level]; exists {
			increment = uint64(span)
		}
		cumulative += increment
		thresholds = append(thresholds, cumulative)
	}

	var experience strings.Builder
	for _, threshold := range thresholds {
		fmt.Fprintf(&experience, "%d ", threshold)
	}
	var sp strings.Builder
	sp.WriteString("[sp table]\n")
	for level := 1; level <= levels; level++ {
		fmt.Fprintf(&sp, "%d 0\n", level)
	}
	sp.WriteString("[/sp table]\n[tp table]\n")
	for level := 1; level <= levels; level++ {
		fmt.Fprintf(&sp, "%d 0\n", level)
	}
	sp.WriteString("[/tp table]\n")
	tables, err := Load(context.Background(), progressionMemorySource{
		ExperienceTablePath: experience.String(),
		SkillPointTablePath: sp.String(),
		QuestParameterPath:  "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10 -1\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tables
}
