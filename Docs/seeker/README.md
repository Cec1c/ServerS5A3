# Seeker 方案索引

工作方式见 [Seeker 工作流](../SEEKER_WORKFLOW.md)，新增方案使用 [方案模板](SOLUTION_TEMPLATE.md)。本表汇总2026年10月18日本轮技术交付；各方案保留根因、实现边界、测试与用户实测目标。

源码按独立修复分支留档，累计部署保留先前修复。自动验证、测试服上线与游戏内验收分别记录；完成券绕过和角色已经继续推进不作为正常流程验收证据。原文涂色的他人成果不计入本轮。

| 编号 | 问题 | 公共提交/来源 | 最新交付状态 |
| --- | --- | --- | --- |
| [EXP-CAP](QUEST-MAX-LEVEL-EXPERIENCE.md) | 满级任务领奖突破90级与挑战者经验 | 5f8b842 / MR !122 | 上游已合并，完整回归及新旧真实PVF通过；待部署实测 |
| [主线经验倍率](MAIN-QUEST-EXPERIENCE-RATE.md) | 正常主线经验调整为 3 倍 | 服务端配置及结算 | 已部署，倍率及真实PVF结算已核验；正常流程待实测 |
| [QST-001](QST-001-terminal-child-rewards.md) | 末子任务材料与奖励结算 | 8879511 | 已部署；正常流程待实测 |
| [QST-002](QST-002-strange-weapon.md) | 奇怪的兵器中途清房与父任务 | d983579 | 已部署；正常流程待实测 |
| [QST-003](QST-003-seek-meet-trigger.md) | 危险研究条件初值与旧活动进度 | ed23e68 | 已部署；材料结算由006补齐 |
| [QST-004](QST-004-northgate-randomized-objects.md) | 北门随机载具入口预检 | 211f53c | 入口/首次生成已部署；骑乘与交付待实测 |
| [QST-005](QST-005-image-communication.md) | 远程通信收费、应答与NPC资格 | 1560f72 | 客户端及服务端已部署；正常流程待实测 |
| [QST-006](QST-006-queen-linked-objectives.md) | 梅娅书信及危险研究多材料交付 | e199af2 | 原材料消费修复已部署；[无奖励完成后的书信恢复](QST-006-queen-letter-source.md)实现及专项通过，待部署 |
| [QST-007](QST-007-cinematic-apc-death.md) | 单次剧情APC销毁确认 | f9d35b1 | 兼容修复已部署；原反馈过门待实测 |
| [QST-008](QST-008-at5t-entry.md) | 回收AT-5T步行者入口 | 复用004 | 入口已部署；[按Z不上机](QST-008-at5t-completion.md)继续研究 |
| [QST-009](QST-009-story-digest-gate.md) | 等级领先主线导致提前播放结局 | 7df2705 | V128客户端已发布并完成实际下载核验；待实测 |
| [QST-011](QST-011-randolph-story-permission.md) | 伦道夫剧情与难度保持 | 已有修复复核 | 真实场景/退出/重连与回包通过；UI待实测 |
| [QST-012](QST-012-friendly-apc-deaths.md) | 友军跨房击杀与通知重放 | c474f6a | 已部署；正常友军击杀与过门待实测 |
| [QST-013](QST-013-quest-menu.md) | 居民任务接取与列表 | 调查验证 | 接取至清图/交付/重建投影通过；15项有限原生通过，菜单画面未复现 |
| [QST-014](QST-014-awakening-passage.md) | 觉醒疑惑之村电梯通路 | 已有修复复核 | 两种电梯图与八房到Boss通过；交付待实测 |
| [QST-015/016](QST-015-016-tournament-entry.md) | 五类武斗大会入口及黑龙APC | 37d1b14 | 黑龙修复与五图vendor已部署；四轮画面与奖励待实测 |
| [QST-010](QST-010-antber-vehicle-apc.md) | 南门乘车、根特防御与尼贝尔 | 04e726a验证分支 | 真实地图/三类死亡/南门过门通过；原生表现继续调查 |
| [QST-017](QST-017-hunt-monster-dispatch.md) | 年轻时的吉赛尔击杀进度与零件奖励 | hunt-monster死亡接线候选 | 正式入口、失败重试、真实PVF及全量/双平台通过；待上游合入与部署 |

原始反馈、玩家资料、运行环境、部署回执和用户实测结果在本机私有记录中维护。公开方案不包含这些资料。

北门后续研究已留档：[316/296协议及生命周期方案](QST-004-ridable-lifecycle.md)（6813511）与[296客户端候选](QST-004-ridable-client-gate.md)（aa8367c）。编解码、完整操作号对照及35项有限原生验证通过；候选未发布。当前证据不支持新增即时再生规则，也未证明它是入口修复的必要依赖；保留正常骑乘与交付实测，不计新增玩法修复。

山之泪APC任务物品：[权威APC类别2死亡掉落](QST-022-ai-character-quest-drops.md)。正式入口、失败重试、真实PVF、race及完整检查/双平台通过，待同步。

山之泪再次挑战跳图：[未交付任务保留当前副本](QST-022-incomplete-quest-retry.md)。正式按钮红绿、真实PVF资格链及全量/双平台通过，待同步。
