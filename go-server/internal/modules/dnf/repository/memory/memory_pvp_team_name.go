package memory

import (
	"context"
	"encoding/json"
	"strings"
)

func (s *memoryAccountStore) FindAccountIDByPVPTotalTeamName(ctx context.Context, name string) (string, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return "", false, err
	}
	for _, record := range s.Snapshot(nil) {
		var team struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(record.Metadata["pvp_total_match_team_v1"]), &team) == nil && strings.EqualFold(team.Name, name) {
			return record.AccountID, true, nil
		}
	}
	return "", false, nil
}
