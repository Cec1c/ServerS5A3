package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 现用14673D0在普通第4关结束后打开DB确认框；14629C0(0)设置
// 场景170的下一关标记，并逐队员调用HP/MP setter及276E690。
// 死亡保护由setter中的390虚调用保留，不能另发复活或重建角色通知。
// 1467ED0经1A6DA50发送普通op45，X=5/Y=0，MoveKind=0。
// 服务端的恢复标记因此用于验证这次转换，不是额外的恢复包或重置道具CD。
func validateCurrentTagTournamentNativeRecovery(mode tagtournament.Mode, before tagtournament.RunSnapshot, next int, encounter tagtournament.DailyEncounter, nativeRecovery bool) error {
	wantSpecial := mode == tagtournament.Normal && next == 4
	if encounter.Optional != wantSpecial || nativeRecovery != wantSpecial {
		return fmt.Errorf("帝国竞技场原生恢复动作与回合计划不一致")
	}
	if wantSpecial && (before.Phase != tagtournament.SpecialChoice || before.Round != 3 || before.Cleared != 4) {
		return fmt.Errorf("帝国竞技场王房缺少普通四关的完整确认")
	}
	if !wantSpecial && before.Phase == tagtournament.SpecialChoice {
		return fmt.Errorf("帝国竞技场特殊关选择不能进入普通回合")
	}
	return nil
}
