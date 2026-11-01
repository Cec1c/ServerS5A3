# 2026-11-01 授权功能与资源贡献

本次从当前公开主干独立提取76项已授权内容。私有提交历史未作为本分支祖先；对应数据文件保持审核后的原始字节。源码贡献、资源下载、玩家通道和正式部署各自独立。

## 功能范围

|编号|内容|交付位置|
|---|---|---|
|01-01|账号级GM编辑准入|go-server/internal/modules/dnf/repository；internal/services/dnfbridge账号与编辑准入|
|01-02|角色级GM编辑准入|go-server/internal/modules/dnf/repository；internal/services/dnfbridge账号与编辑准入|
|01-03|完整在线会话快照|go-server/internal/modules/dnf/repository；internal/services/dnfbridge账号与编辑准入|
|01-04|首次补档并发强化|go-server/internal/modules/dnf/repository；internal/services/dnfbridge账号与编辑准入|
|01-05|私有账号管理API接线|go-server/internal/modules/dnf/repository；internal/services/dnfbridge账号与编辑准入|
|02-01|统计账本与原子记账|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-02|疲劳统计|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-03|深渊完成统计|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-04|装备拾取统计|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-05|史诗来源分项|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-06|金币明细与角色贡献|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-07|无色实际消耗统计|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-08|个人统计游戏连接接口|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-09|本机统计HTTP适配|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-10|五类排行榜与原生接口|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-11|周榜冻结与待发资格|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-12|统计字段截断修复|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|02-13|组队拾取重复加锁修复|repository/activity；activityrank；dnfbridge/account_activity、player_activity、activity_rank|
|03-01|战令赛季与状态核心|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-02|战令购买支付机制|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-03|战令领奖事务|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-04|战令副本任务接入|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-05|战令装备养成任务|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-06|战令无色技能任务|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-07|战令冒险团/公会/商城任务|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|03-08|战令通信与入口提醒|modules/dnf/battlepass；dnfbridge/battlepass及各资产事务接线|
|04-01|每日活跃度服务端|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-02|活跃度历史配置迁移|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-03|升级大挑战288原生实现|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-04|288全职业新角色自动参加|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-05|幸运1054资格与经验|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-06|幸运掉落权重与疲劳燃烧|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-07|赛丽亚回归资格与原生界面|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-08|赛丽亚新冒险家资格|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-09|回归NPC赠礼与引用地图修复|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-10|Lv预约222|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-11|旧升级预约116|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-12|劳动节2348登录奖励|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-13|劳动节2348在线奖励|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|04-14|新增活动管理与目录接入|modules/dnf/activeness、onlineevent；dnfbridge活动接线|
|05-03|歌兰蒂斯固定碎片机制|equipmentservice、npcshop、packageitem及桥接回调|
|05-04|NPC永久账号累计限购|equipmentservice、npcshop、packageitem及桥接回调|
|05-08|开礼盒可信事件回调|equipmentservice、npcshop、packageitem及桥接回调|
|06-01|普通深渊原生PVF掉落链|dnfbridge/private_pvf_drop_rules及副本掉落接线|
|06-02|特殊副本逐图概率配置|dnfbridge/private_pvf_drop_rules及副本掉落接线|
|06-03|魔界裂缝掉落件数权重|dnfbridge/private_pvf_drop_rules及副本掉落接线|
|06-04|怪物模板概率配置|dnfbridge/private_pvf_drop_rules及副本掉落接线|
|07-01|战令任务目录与经验参数|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-02|战令价格及初级/高级奖励表|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-06|每日活跃度8任务与3档奖励|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-09|练级少女NPC和五项商店|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-15|六图36建筑时空石掉落|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-16|六类+13券与远古罐图标|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-17|普通深渊具体产出表|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-28|莎兰兑换及赛丽亚下架|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-32|商城价格/排序/下架清单|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-34|节日花篮NPC与兑换表|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-37|赛丽亚1金币欢迎礼盒|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-38|永久灵跃37武器与动画目录|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-39|装扮与称号移动速度|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-40|完整累计Script.pvf|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-41|六份尚未公开NPK|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-43|六项GM索引成品|resources/selected-20261101/manifest.json及同名资源发行附件|
|07-44|120频道表资源|resources/selected-20261101/manifest.json及同名资源发行附件|
|08-01|活跃度客户端窗口源码|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|08-04|混合客户端构建和映像校验|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|08-06|历史七对接收许可工具|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|08-07|历史五对分类接收工具|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|08-08|安徒恩readiness旧候选与研究|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|08-09|客户端构建说明增量|scripts/client/activeness-ui、r195-receivers、retest-receiver-gate、raid-team-readiness及Docs|
|11-01|SQL事务计时去重/按需计时|相应Go回归测试、Docs技术说明及本贡献说明|
|11-02|账号/GM并发回归增量|相应Go回归测试、Docs技术说明及本贡献说明|
|11-03|副本/组队/团本兼容回归增量|相应Go回归测试、Docs技术说明及本贡献说明|
|11-04|物品/经济/活动累计回归增量|相应Go回归测试、Docs技术说明及本贡献说明|
|11-05|历史技术说明增量|相应Go回归测试、Docs技术说明及本贡献说明|
|11-06|私有累计来源索引和变更说明模板|相应Go回归测试、Docs技术说明及本贡献说明|

## 资源与兼容性

完整PVF SHA256为`95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c`。完整资源选择包括其中累计物品、任务、价格、奖励和概率数据；单项列举不构成过滤后的另一份PVF。六个NPK、六个GM索引与120频道表身份详见资源manifest。索引复用已有同输入产物，未重新生成。

数据快照并不意味着所有历史服务端机制都随源码公开。本次未纳入寄售三钱包、强化/增幅七分之一金币、艾肯称号投影/专用装扮券映射、绝对目标胶囊和最终翻牌三倍金币机制，也未纳入双Hub或私有部署CI。采用累计客户端或PVF时必须核对各自服务器规则；资源发行不是直接覆盖生产环境的安装包。

活跃度组件从混合源码单独提取，保留匹配输入的原生入口契约和已公开礼包窗口集成。构建产出静态payload组件；不提供覆盖完整私有payload的安装脚本。08-05内嵌历史位图未纳入，使用已授权NPK和帧映射。08-04仅包含所选组件需要的构建/映像检查；历史完整008部署工具没有进入本次公开范围。

历史七对/五对接收工具及readiness研究标明固定输入身份和原始局限。部分v2脚本已在公开raid-team-ready-v4中存在，沿用已公开版本。历史验证不写成本次游戏实测。

## 已知资源限制

222级别预约的真实PVF奖励检查失败：预约硬币490003528仍带2017-01-16 22:00:00 UTC截止时间，早于配置活动结束时间。该检查保留，未修改资源或把失败改为通过。活动机制以历史现状公开；启用该活动前需独立修订并验收奖励配置。

账号统计周榜冻结只产生资格，默认没有实际奖励档位或领奖发奖实现。并非本次所有活动都已在正式服启用，原游戏验收只承接用户实际确认范围。

## 验证记录

本机 go mod verify、list、领域/PVF、完整桥接、control、vet、Windows构建及GM边界检查通过；隔离MySQL字段迁移、三个真实副本结算、14项真实PVF专项及两项拆分边界真实PVF测试通过。上方222预约资源失败单独保留。部署预检实际执行后因缺少外部 deploy/assets/channel_info.etc 停止，未记为通过。

资源逐件验SHA256和大小，GM索引验证格式及绑定关系；12个公开分卷匿名完整下载及两份重组归档摘要通过。客户端组件通过x86 MSVC编译及XUI一致性检查。源码公开不修改玩家通道或执行服务部署。

来源与验证记录格式：

```json
{"item":"04-01","publicCommit":"<本次公开提交>","paths":["go-server/internal/modules/dnf/activeness"],"verification":{"command":"<实际检查命令>","result":"passed/failed/skipped","limitation":"<具体边界>"},"deployed":false}
```

来源只记录可公开路径、资源摘要及公开提交；本机路径、私有完整历史、玩家数据与原始运维回执不属于该格式的公开内容。
