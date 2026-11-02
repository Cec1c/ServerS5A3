package dnfbridge

import "time"

type selectProjectionStage int

const (
	selectProjectionLucky selectProjectionStage = iota
	selectProjectionFatiguePotion
	selectProjectionEpicWish
	selectProjectionCollectbox
	selectProjectionPermission
	selectProjectionLevelChallenge
	selectProjectionStoryDigest
	selectProjectionAccountNotice
	selectProjectionTutorial
	selectProjectionTownReentry
	selectProjectionWallet
	selectProjectionReconnect
	selectProjectionStageCount
)

// These are helper durations, including their reads and writes. In a channel
// batch they precede the actual ACK flush; they are not socket-only durations
// or proof that the client has rendered. Keep the stage set fixed and emit it
// on the existing single correlated, rate-limited selection sample.
var selectProjectionLogKeys = [...]string{
	"projection_lucky_ms", "projection_fatigue_potion_ms", "projection_epic_wish_ms",
	"projection_collectbox_ms", "projection_permission_ms", "projection_level_challenge_ms",
	"projection_story_digest_ms", "projection_account_notice_ms", "projection_tutorial_ms",
	"projection_town_reentry_ms", "projection_wallet_ms", "projection_reconnect_ms",
}

func (t *currentSelectTiming) measureProjection(stage selectProjectionStage, run func() error) error {
	started := time.Now()
	defer func() { t.projections[stage] += time.Since(started) }()
	return run()
}

func (t *currentSelectTiming) appendProjectionLog(args []any) []any {
	for i, key := range selectProjectionLogKeys {
		args = append(args, key, float64(t.projections[i].Microseconds())/1000.0)
	}
	return args
}
