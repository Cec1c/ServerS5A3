package progression

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

const questGoldFixtureSection = "[gold reward table]\n10\n"

func questGoldCandidateSource(t *testing.T, rows ...string) progressionMemorySource {
	t.Helper()
	source := progressionTestSource()
	questParameter := source[QuestParameterPath]
	if strings.Count(questParameter, questGoldFixtureSection) != 1 {
		t.Fatalf("expected one gold fixture section, source=%q", questParameter)
	}
	replacement := "[gold reward table]\n" + strings.Join(rows, "\n") + "\n"
	source[QuestParameterPath] = strings.Replace(questParameter, questGoldFixtureSection, replacement, 1)
	return source
}

func TestQuestGoldTreatsRewardTableValuesAsFlatRows(t *testing.T) {
	tables, err := Load(context.Background(), questGoldCandidateSource(t, "7", "11", "13"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tables.quest.goldByLevel); got != 3 {
		t.Fatalf("gold rows = %d, want 3", got)
	}

	for index, want := range []uint32{7, 11, 13} {
		level := index + 1
		got, err := tables.QuestGold(level, level, 100, false)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if got != want {
			t.Fatalf("level %d gold = %d, want %d", level, got, want)
		}
	}

	if _, err := tables.QuestGold(4, 4, 100, false); !errors.Is(err, ErrQuestExperienceLookup) {
		t.Fatalf("level 4 error = %v, want ErrQuestExperienceLookup", err)
	}
}

func TestQuestGoldRejectsEveryInvalidRewardValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "negative", value: "-1"},
		{name: "above uint32", value: strconv.FormatUint(uint64(math.MaxUint32)+1, 10)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Put the invalid value at raw index 1 so skipping alternating entries
			// cannot make this case pass silently.
			source := questGoldCandidateSource(t, "10", test.value, "20")
			if _, err := Load(context.Background(), source); !errors.Is(err, ErrTableMalformed) {
				t.Fatalf("Load error = %v, want ErrTableMalformed for %q", err, test.value)
			}
		})
	}
}
