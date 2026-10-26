package repository

// SkillTree 返回隔离的加点视图，不改变扩展资格或当前生效页。
func SkillTree(r SkillRecord, tree int) (SkillProfile, bool) {
	if tree < 0 || tree > 1 {
		return SkillProfile{}, false
	}
	if tree == ActiveSkillTree(r) {
		return SkillProfile{Skills: cloneSkillMap(r.Skills), Points: r.Points}, true
	}
	return SkillProfile{Skills: cloneSkillMap(r.Trees.Other.Skills), Points: r.Trees.Other.Points}, true
}
