package quest

import "testing"

func TestTargetCharacterWildcardKeepsOtherTupleGuards(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rule      TargetCharacterRule
		character CharacterEligibility
		want      bool
	}{
		{"any_awakening", TargetCharacterRule{"[swordman]", 0, -1}, CharacterEligibility{Job: 0, GrowType: 0x20}, true},
		{"any_branch", TargetCharacterRule{"[swordman]", -1, 1}, CharacterEligibility{Job: 0, GrowType: 0x13}, true},
		{"both_wildcards", TargetCharacterRule{"[swordman]", -1, -1}, CharacterEligibility{Job: 0, GrowType: 0x23}, true},
		{"exact_awakening_rejects", TargetCharacterRule{"[swordman]", -1, 1}, CharacterEligibility{Job: 0, GrowType: 0x23}, false},
		{"exact_branch_rejects", TargetCharacterRule{"[swordman]", 0, -1}, CharacterEligibility{Job: 0, GrowType: 0x21}, false},
		{"wrong_job_rejects", TargetCharacterRule{"[swordman]", -1, -1}, CharacterEligibility{Job: 2}, false},
		{"other_negative_branch_rejects", TargetCharacterRule{"[swordman]", -2, -1}, CharacterEligibility{Job: 0}, false},
		{"other_negative_awakening_rejects", TargetCharacterRule{"[swordman]", -1, -2}, CharacterEligibility{Job: 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := Definition{JobChangeQuest: 1, TargetCharacterRules: []TargetCharacterRule{tc.rule}}
			if got := definitionMatchesCharacter(definition, tc.character); got != tc.want {
				t.Fatalf("matches=%v want=%v rule=%+v character=%+v", got, tc.want, tc.rule, tc.character)
			}
		})
	}
}
