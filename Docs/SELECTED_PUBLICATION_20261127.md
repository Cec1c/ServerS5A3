# 2026-11-27 选定内容公开范围

本批从公开 `ServerS5A3 master` 的 `e13e58b34b3e8c6917724e023e221679aa35e198` 抽取，公开 A04、A20–A22、C01–C18、D01–D06，以及已采用的私有组队与玩家/队友模型方案。公开树没有引入私有 Git 历史、运行配置或玩家记录。

| 编号 | 可核对的内容 | 代码/配方入口 |
| --- | --- | --- |
| A04 | 黑钻礼包 490709931 允许代币券不足时点券补足，其他仅代币券商品继续限制 | `cera_shop_purchase.go` 与 `cera_shop_black_diamond_payment_test.go` |
| A20–A22 | 天空外盒 14 项选择、既有职业/颜色/八部位映射审计及无效职业块清理；五个新增合成器的独立定义 A15–A19 不在本批 | `resource-recipes/20261127-selected-public/resources/` |
| C01–C02 | 林纳斯原生制作窗、跨职业选材与预览 | `client/weapon-skin/client-source/同窗候选源码/` |
| C03 | 背包实际槽复核、重复登记前置检查、扣料与产出事务 | `inventory/weapon_skin_forge.go`、`weaponskin/forge.go` |
| C04–C06 | 神奇/秘传成品、同账号流转、邮件/交易约束、右键确认与当前角色入库 | `itemtrade/owner.go`、`mail/owner.go`、`weapon_skin_forge.go` |
| C07–C09 | 仓库总览/武器页、应用/解除外观、伤害字体页兼容 | `skin_warehouse_summary.go`、`weapon_skin_demo.go` 及原生源码 |
| C10–C11 | 选角/城镇/副本/换房重绑、队友站街与战斗图层、旧图层释放 | `scene_finish_loading.go`、`dungeon_party_copresence.go`、`client-source/场景绘制/` |
| C12–C13 | 武器外观窄读、缓存失效、限速列表保留与有效读取后重试标记消费 | `character_weaponskin.go`、`mysql_character_weaponskin.go`、原生仓库接线 |
| C14–C16 | 林纳斯第五页、神奇模具 100 万、秘传模具 200 万 | 资源配方与价格/货架核验 |
| C17 | 13,329 条全量外观目录、装备/消耗品索引增量、五个 NPK | `weaponskin/catalog.tsv` 与资源配方 |
| C18 | 原始精确 EXE 差量、18 份 C++/头文件、静态移植构建与逐字节保留检查 | `client/weapon-skin/` |
| D01 | 远端技能页就绪后的职业原生进图初始化，按副本周期去重和补查 | `client/party-render/build_party_v4.py` |
| D02–D04 | 设备完整恢复、VB/IB 失败与重试、渲染空对象保护和纹理回退 | `client/party-render/build_blackscreen.py` |
| D05 | 修改白名单、12 挂点反汇编、恢复计数与原生指令仿真 | `client/party-render/verify_*.py` |
| D06 | c2→D798→D777→FE6 来源范围、稳定区核验和固定输出包装器 | `client/party-render/lineage/`、`build_formal_release.py` |
| 追加组队/模型 | 名单前 actor 准备、中继绑定、原生准入重试、加载快照、跨场景资料预算、初始读条回城守卫、团本成员定向外观与预算过期名单交付 | [服务端方案](PARTY_MODEL_LIFECYCLE_20261127.md) |

表中的服务端文件位于 `go-server/internal/services/dnfbridge/` 或列出的 `go-server/internal/modules/dnf/` 子目录，客户端路径相对 [原生配方](../resource-recipes/20261127-selected-public/client/README.md)。

## 公开交付边界

交付源码、测试、按条目合并资源的配方和授权新增资源。旧私有累计 EXE/PVF 含有其他未选玩法，本批没有将其作为公开成品发布。D06 公布的是可审计的历史构建流程，仍要求操作者持有精确前驱；47e 之前没有完整可公开的重建链，不能由公开 V217 独立生成历史 FE6。

此前公开的组队中继生命周期、模型登记、迟到 mode1 补登记和客户端回执继续保留。当前 `party-room-lifecycle-registration` 构建/验证源码已存在，并与私有版本相同；早期缺件记录不再适用。

本批没有发布未选中的金币/商城政策、剑鬼、活动进度、超艾肯或其累计客户端资源，也没有重新启用已取消实验。公开源码、资源配方、客户端成品、玩家通道和生产服为分别记录的状态。本次公开不切换生产服，不发布登录器公告。

## 验证口径

原生配方的新执行结果为：D777/FE6 输出摘要匹配，13 组队友初始化、19 组渲染恢复仿真通过；后者覆盖 T01–T20，T11/T12 合为一组。18 份移植源码与已验证输入一致；客户端二进制目录与服务端 13,329 条目录的五个身份字段逐行相同。本次未重新编译 C++，未进行新的游戏内验收。

服务端按领域、组队/仓库协议与真实 PVF 选定用例执行 Linux 验证，具体终态记在本次发布验证回执。MySQL 全包扩展检查存在四个顶层旧失败：Buff 所有权夹具、非锁 settings 夹具、账号邮件事务夹具及其锁顺序夹具；同一组失败在公开基线相同源码上复现，不计通过。新增窄读/按名字查询及相关定向用例分别核验。缓存命中、资源缺失跳过、实际执行和构建分别计数。部署其他 EXE/PVF/NPK 组合时，须重新核对原生入口、编号、帧格式与实际游戏行为。

历史角色已拥有的外观/字体若没有取得时间记录，不补造时间，因此可能不出现在“最近获得”页；既有拥有状态和应用入口仍可用。

资源工具已在真实 PVF 上复现：A20 外盒为 14 项时保持输入字节一致；A21 仅改职业映射表，清除 21 个无效职业块、168 个映射，保留 161 个有效组合；C14–C17 核验 40,032 个内容路径，完成货架与两种模具价格共三处修改，其余 1,169,934 个文件保持。A20 从旧列表改为 14 项另由 token 回归验证。五个 NPK 的名称、字节数与 SHA-256 逐一匹配。
