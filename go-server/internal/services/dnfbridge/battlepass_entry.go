package dnfbridge

// 原版95的活动2520关联HUD入口1195；90仅增补该PVF定义及点击适配。
const currentBattlePassEventID uint16 = 2520

// 现有战令已开放，新增统一管理项默认延续当前状态。
func (s *Service) currentBattlePassEnabled() bool {
	return s != nil && (s.activityControls == nil || s.activityControls.Enabled(currentBattlePassEventID))
}

func (s *Service) currentBattlePassEntryAvailable() bool {
	if !s.currentBattlePassEnabled() {
		return false
	}
	p, err := currentBattlePassPolicy()
	if err != nil || !p.Active(s.gameplayNow()) {
		return false
	}
	_, _, err = s.currentBattlePassCatalog()
	return err == nil
}
