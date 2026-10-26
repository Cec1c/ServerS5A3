package dungeon

import "longheng.io/server/internal/modules/dnf/guild"

type GuildContributionClear struct {
	Contract   *guild.ContributionContract
	Activity   guild.ContributionActivity
	Revalidate func() bool
}
