package worldmap

import "testing"

func TestSeekerQST012MonsterFollowTypeParsesAndClones(t *testing.T) {
	doc := parseTestDocument(t, "map/qst012-follow.map", `
[monster]
3001 10 0 100 100 0 0 0 `+"`[fixed]` `[normal]`"+`
3002 11 0 120 100 0 0 0 `+"`[fixed]` `[normal]`"+`
[monster team]
100 0
[monster follow type]
`+"`none` `follow`"+`
`)
	parsed := ParseMap(57962, "map/qst012-follow.map", doc)
	if len(parsed.MonsterTeam) != 2 || parsed.MonsterTeam[0] != 100 || parsed.MonsterTeam[1] != 0 {
		t.Fatalf("monster teams=%v", parsed.MonsterTeam)
	}
	if len(parsed.MonsterFollowType) != 2 || parsed.MonsterFollowType[0] != "none" || parsed.MonsterFollowType[1] != "follow" {
		t.Fatalf("monster follow types=%v sections=%+v diagnostics=%+v", parsed.MonsterFollowType, parsed.SourceSections, parsed.Diagnostics)
	}

	cloned := cloneMap(parsed)
	cloned.MonsterFollowType[0] = "changed"
	if parsed.MonsterFollowType[0] != "none" {
		t.Fatalf("clone aliased source follow types: source=%v clone=%v", parsed.MonsterFollowType, cloned.MonsterFollowType)
	}
}

func TestSeekerQST012MonsterFollowUnknownSlotKeepsIndex(t *testing.T) {
	doc := parseTestDocument(t, "map/qst012-follow-invalid.map", "[monster follow type]\n7 `follow`\n")
	parsed := ParseMap(1, "map/qst012-follow-invalid.map", doc)
	if len(parsed.MonsterFollowType) != 2 || parsed.MonsterFollowType[0] != "" || parsed.MonsterFollowType[1] != "follow" {
		t.Fatalf("invalid slot shifted a following declaration: %v", parsed.MonsterFollowType)
	}
}
