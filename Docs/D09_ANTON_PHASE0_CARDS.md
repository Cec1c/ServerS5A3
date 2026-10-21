# D09 安徒恩阻截 phase0 双牌时序

## 结论与范围

正常安徒恩 phase0 有两段展示：party 牌与 squad/team 牌。自动选牌在同一个推进循环中先提交 party，随后立即提交并切换到 squad，导致第一段已提交的 party 结果没有保留一个显示窗口。候选仅在 `Mode==1`、非单人、`Phase==0` 时记录 party 全部提交时刻，并等待真实 PVF `MovieEndSeconds` 后再推进 squad。手动在时限前完成全部 party 选择时使用实际完成时刻，不再追加第二个窗口。

不修改奖励表、资产事务、最低参与人数、owner、组成员、周账本、659/660 请求格式或 587/600 包布局。未证明用户所说“手动翻不开”的全部原因；不发 GUI 或部署验收结论。

## 真实入口证据

- 实际 PVF SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`；v151 SHA256 `2def3cc35c1ee00a9c697fe81d1f31259860468d042d6d477cd3b4be08f0e6eb`。
- v151 静态：659注册`0x15707D3->0x15705F0`，660注册`0x15707EB->0x156D500`；自然659发送路径含`0x18FBF29`与零值字段`0x18FBF3A`，660写入器`0x00F19600`。NOTI600解析kind、owner、cardtype、itemcount及每项8字节描述。
- 原生`0x1929A60`执行映射：kind0/1→类别0，kind2→1，kind3→2；kind0被消费者正常接受。因此没有 gold pool不能单独推导出“删除kind0合法”，相关修改已撤回。
- 完整真实PVF清关fixture下验证单/双人、4人同组、4+1多组；自然659保持item与gold两套mask，party owners只取同组且最多4人；自动party先揭示一个电影窗口，squad后以全体recipients容量显示。自然660只选一张、奖励30、无额外squad生成；重新连接恢复已提交party且不重发。

## 验证边界

- 精确HEAD overlay RED：自动选牌点直接出现`party=2/squad=2`，以及4/5人表立即出现squad；候选GREEN保留party一个窗口后进入squad。
- 单人、phase1最终电影、R158迟到659、下一阶段等待区及卢克手动翻牌回归通过。
- 未执行完整GUI/动画，未重放用户现场人数与请求序列。`TestRealPVFLukeOwnedTwoPhasesRewardsAndWeekly`存在既存基线失败，当前与baseline一致，不能归因于本候选。
