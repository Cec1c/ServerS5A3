# 低级深渊碎片与装备等级范围

全局85/90门槛曾令低级图仍从高级碎片集合抽取，同时清空这些图的史诗装备候选。默认档现在只在副本minimum required level和basis level均已知、均低于85时，使用现有hellPartyRewardGradeRangeForLevels区间。碎片读取其真实成品.equ的grade和minimum level；不从等级推算grade，不改变掉落次数、权重、稀有度、波次、所有权或拾取账本。

这是当前服务端既定装备grade规则向碎片的有限一致性修复，不是新发现的原服经济规则。高级图保持用户既定85/90策略；不完整副本等级不启用低级适配。新低级分支拒绝未知使用等级，碎片另要求有效grade和rarity=4。既有高级兼容与legacy语义保持。

自动适配仅适用于两个环境值均未配置/空白的默认档。显式DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR / CEILING整数、非法值回落及legacy保留原行为；若部署显式设置85/90，低级图仍受该显式策略约束。

真实PVF SHA256：25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348。

| 副本 | 最低/基准 | 既有grade区间 | 默认碎片成品等级与数量 | 史诗装备候选 |
|---|---|---|---|---|
| 86 海上列车 | 71/74 | 71–81 | 65:67、70:200、75:195 | 462 |
| 87 海上列车 | 72/75 | 72–82 | 70:200、75:195 | 395 |
| 190 | 85/88 | 85–95 | 85:138、90:110（高级原策略） | 138 |
| 310 | 87/90 | 87–97 | 85:138、90:110（高级原策略） | 248 |

因此玩家描述的“lv70深渊”不能直接翻译成“只发70级碎片”。这里以资源实际等级和现有grade选择契约为准。190碎片保持90级是有意保留既定高级策略，其装备候选仍由grade区间限定。

验证：TestAbyssLowLevelRealEquipment与TestAbyssLowLevelRealFragments读取显式DNFBRIDGE_REAL_PVF_SMOKE；无设置Skip，设置错误Fatal。真实资源验证两条装备选择消费者、稀有度计数和完整碎片集合。TestAbyssLowLevelDeathPickup使用合法90级角色、已有合成双波次房间，通过实际死亡/掉落/拾取handler检查低级与高级、重复死亡和拾取。该用例不是海上列车真实全图入场或客户端实测。TestAbyssLowLevelPolicyBoundaries覆盖缺等级、未知grade/等级、错误rarity、高grade以及显式配置/legacy。

尚未部署、尚未现场验收。原始资源探索不进入默认CI；完整红绿回执与冻结manifest由本次独立交付保存。
