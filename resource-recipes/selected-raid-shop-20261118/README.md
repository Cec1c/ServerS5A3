# 攻坚、帝国竞技场和商城资源

| 编号 | 玩家可见的变化 |
| --- | --- |
| 02 | 连续过房不再反复显示城镇未读邮件。新邮件和返城提醒继续生效。 |
| 03 | 同次攻坚过渡复用角色读取结果，编辑名单可以清空分组。 |
| 04 | Ins 整队返城遇到队员队列暂满时重试，同一次通知合并。 |
| 05 | 加载期间合法离队后，其余原队员从原槽位继续加载。 |
| 06 | 已接受的跟随过图按队员会话处理。一人返城后其余人继续；首包零字节失败可在原房间和时限内重试。 |
| 13 | 安徒恩可分配至第 1–10 队，全团最多 20 人，每队最多 4 人。 |
| 14 | 荣耀之光礼包 2683268 可使用，通过原商城礼包流程发放内容。 |
| 18 | 帝国等候区按角色隔离，冷加载后重新给入场确认 15 秒。 |
| 19 | 罗什客户端回调的三处空对象读取转到原清理分支。正常对象继续执行原逻辑。 |
| 20 | 已通关炮台保持通关，60 秒计时器不再复活它。 |
| 21 | 超时空已结算遭遇战同步角色生命状态，按角色、频道和实例核实归属。 |
| 22 | 罗什属性小游戏有独立生命值，共享本体只在有效破防窗口扣血。 |
| 23 | 安徒恩、卢克、超时空的稀有翻牌按已结算结果播放金牌动画。 |
| 25 | 超时空自动入场读取副本声明难度；当前资源为 2，对应已有 HP 表系数 3。 |
| F01 | 日常礼包货架的商品 900129 售价 3000 代币券，购买交付一件黑钻礼包 490709931。代币券不足时交易失败。 |

## 重建客户端与 PVF

在本目录运行以下命令。输入分别取自 [V216 客户端](https://github.com/Cec1c/ServerS5A3/releases/tag/client-v216-selected-feedback-20261117) 和 [公开 PVF](https://github.com/Cec1c/ServerS5A3/releases/tag/pvf-current-server-20261115-66482976)。输入、输出使用不同目录。客户端继续配套 V216 的两个 DLL 和界面 NPK。

```text
python build_rosch.py --input old/DNF.exe --out new/DNF.exe
pvf patch --pvf old/Script.pvf --plan selected.pvf-plan.json --out new/Script.pvf
pvf verify --pvf old/Script.pvf --plan selected.pvf-plan.json --out new/Script.pvf
```

| 输出 | SHA-256 |
| --- | --- |
| DNF.exe | `a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf` |
| Script.pvf | `6aaca082329fa4134d25cb2375df36b6bfcda15e6f668fb7578155a1a0eadd71` |

公开 PVF 原包已有 F01 货架。若从 11 月 15 日 [玩法配方](../selected-gameplay-20261115/README.md) 的输出重建历史 F01，可用以下独立命令加入货架。它只接收摘要以 `2b5453f0` 开头的那一版，输出为 `55b8161478039be9aab026b7d8863a965b94987b1101e529ebcd77797c449829`。

```text
python build_black_diamond_shelf.py --pvf-cli pvf --input gameplay/Script.pvf --out shelf/Script.pvf
```

罗什双队完整通关、客户端金牌动画和各怪物的实机绝对 HP 仍待实机验收。
