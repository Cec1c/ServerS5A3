package guild

import (
	"context"
	"sort"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// SampleRankingMembers reads each account roster once. It uses the same PVF
// adventure table and durable PVP/raid records as their gameplay producers.
// Online and newcomer observations are supplied by their authoritative owners.
// Missing roster members must be proven soft-deleted by an owned durable record;
// a truncated roster, missing character or read failure is not a zero score.
func SampleRankingMembers(ctx context.Context, characters r.CharacterRepository, reg Registry, table *adventuregroup.Tables, rosterLimit int, online, newcomers map[r.GuildOwner]bool) (map[string]RankingMemberFacts, error) {
	if characters == nil || table == nil || rosterLimit < 1 {
		return nil, ErrRepositoryUnavailable
	}
	owners := make(map[string]r.GuildOwner)
	accounts := make(map[string]bool)
	for _, g := range reg.Guilds {
		if g == nil {
			return nil, ErrInvalidRegistry
		}
		for id, m := range g.Members {
			if id == "" || id != m.CharacterID || m.AccountID == "" {
				return nil, ErrInvalidRegistry
			}
			if _, duplicate := owners[id]; duplicate {
				return nil, ErrInvalidRegistry
			}
			owners[id] = r.GuildOwner{AccountID: m.AccountID, CharacterID: id}
			accounts[m.AccountID] = true
		}
	}
	accountIDs := make([]string, 0, len(accounts))
	for id := range accounts {
		accountIDs = append(accountIDs, id)
	}
	sort.Strings(accountIDs)
	facts := make(map[string]RankingMemberFacts, len(owners))
	for _, accountID := range accountIDs {
		roster, err := characters.ListByAccount(ctx, accountID, rosterLimit)
		if err != nil {
			return nil, err
		}
		rows := make([]adventuregroup.Character, 0, len(roster))
		seen := make(map[string]bool, len(roster))
		for _, c := range roster {
			if c.AccountID != accountID || c.CharacterID == "" || seen[c.CharacterID] {
				return nil, ErrCharacterNotFound
			}
			seen[c.CharacterID] = true
			rows = append(rows, adventuregroup.Character{Level: c.Level, Deleted: c.Stats["delete_flag"] != 0})
		}
		summary, err := table.Calculate(rows)
		if err != nil {
			return nil, err
		}
		for _, c := range roster {
			owner, member := owners[c.CharacterID]
			if !member {
				continue
			}
			// Read the durable character schema without depending on PVP's
			// gameplay package (its rewards already depend on guild inventory).
			grade := c.Stats["pvp_grade"]
			if owner.AccountID != accountID {
				return nil, ErrCharacterNotFound
			}
			if c.Stats["delete_flag"] != 0 {
				// ListByAccount may race a deletion between its ID query and
				// record reads. Confirm it through the same fallback below.
				continue
			}
			if grade < 0 || grade > 255 {
				return nil, ErrCharacterNotFound
			}
			facts[c.CharacterID] = RankingMemberFacts{Owner: owner, Character: r.CloneCharacter(c), AdventureLevel: summary.ManageLevel, PVPGrade: int(grade), Online: online[owner], Newbie: newcomers[owner]}
		}
	}
	missing := make([]string, 0, len(owners)-len(facts))
	for id := range owners {
		if _, ok := facts[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		owner := owners[id]
		c, found, err := characters.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found || c.CharacterID != id || c.AccountID != owner.AccountID || c.Stats["delete_flag"] == 0 {
			return nil, ErrCharacterNotFound
		}
		facts[id] = RankingMemberFacts{Owner: owner, Character: r.CloneCharacter(c)}
	}
	return facts, nil
}
