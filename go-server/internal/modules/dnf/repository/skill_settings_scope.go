package repository

// 角色结算只准访问明确的技能域，不能扩大为任意设置写权限。
func CharacterSkillSettingsScope(characterID, scope string) bool {
	return characterID != "" && (scope == "character:"+characterID+":dark_knight_combo" ||
		scope == "character:"+characterID+":pvp_dojo_skills_v1" ||
		scope == "character:"+characterID+":pvp_dark_knight_combo_v1")
}
