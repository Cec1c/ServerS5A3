# 退队后清理副本前城镇队伍记录

两人副本内一名队员中途回城后，留图者的退出者模型已经删除、怪物可以攻击，通关也不包含退出者翻牌，但左上组队 UI 仍可能显示退出者超时。现有服务端只清副本上下文（op9 owner0）的成员记录；进图前当前频道（town context）的队伍记录可能仍在，留给局部 UI 读取。

本次修复保持现有战斗、实例和奖励路径，只补同一成员快照的城镇上下文记录退役：

1. 先清当前频道的 town records，非本人在前、本人最后。
2. 再清原 owner0 dungeon records，仍按非本人在前、本人最后。
3. 两套记录都消费完旧槽后，才发送 NOTI6 删除退出者活动 actor。
4. 发送 op99 空实时信息并保持原有副本实例、固定槽及结算逻辑。

三人以上的常规名册差异路径、队长退出和错误场景边界保持原逻辑。单人生存路径只在当前客户端版本、当前频道可表示为 op9 owner 字节且成员身份有效时发送新增记录。

## V176 客户端保护

原 V176 kind3 helper 只为 route1/owner0 覆盖本地模型保护。若直接补发 current-channel town record，客户端会清掉记录和本地关联，同时销毁仍活着的本地模型。`scripts/client/dungeon-peer-retire/helpers.py` 的候选扩展只在以下条件同时成立时使用非破坏清理：

- 记录 route 为 1；
- 当前 scene 为普通副本场景 3；
- 本地 actor 存在；
- 记录 owner 等于客户端当前选中频道；
- kind3 目标键属于发送 FFFF 的退役记录。

owner0、非 scene3、错频道、错 route、FFFF 和他人仍绑定的记录保持原语义。kind0 wrapper 不变，且不会覆盖已有非空 town association。

客户端输入固定为已发布 V176：

```text
SHA256 2a6cc14f3353e3d9d142b8580e432fe76ff3c1bbdc5be6818092347f9003bc74
SIZE   111919104
BASE   0x6EC5000
```

`repair_v176_context.py` 只重写 kind3 helper 与 PE checksum，并要求其他字节、kind0 helper 和退役 callsite 逐字节一致。

## 便携验证

需要 `Docs/dungeon-peer-retire-requirements-validation.txt` 中的 `unicorn==2.1.4` 和 `capstone==5.0.7`：

```text
python scripts/client/dungeon-peer-retire/repair_v176_context.py \
  --input <published-v176.exe> \
  --output <new-context-candidate.exe> \
  --receipt <new-build-receipt.json>

python scripts/client/dungeon-peer-retire/verify_context.py \
  --input <published-v176.exe> \
  --candidate <new-context-candidate.exe> \
  --receipt <new-verification-receipt.json>
```

`verify_context.py` 先强制输入 V176 摘要，再调用当前树内的 `repair_v176_context.build()` 生成期望候选并要求实际候选逐字节相同。它执行真实 V176 kind3 调用、setter/slot 清理、本地队伍 getter 和退队判定。v177-v3 组合回执已实际执行该 portable 验证器。

离线矩阵覆盖五种初始关联、本人槽 0/1、旧/新 helper 和 town-before-dungeon/self-last 顺序。负向守卫覆盖 town scene、其他 scene、错频道、错 route 和不可表示频道；另验重复 kind0 不清已有 town association。

HUD retirement 复用同一组五种初始关联、槽 0/1 和旧/新 helper，共 20 case。v177-v3 actual run 执行了真实 V176 `2207E90` 的 16 控件隐藏循环，以及真实 `296E5E0` 本地记录 getter和 `224C418` 本地队伍 layout redraw gate。

- v177-v3 native 回执在当前固定 V176/候选上记录：真实 dungeon-only 清理且初始关联为 Town/TownPeer 的 4 个 case 仍发起 layout 绘制。
- 同一回执对新 town+dungeon 序列记录 0 个 layout 绘制，且 20 个 HUD case 的本地模型都保留。

角色查找、scene/channel getter、peer manager reset、virtual hide leaf、layout 绘制末端、GPU rendering 和游戏画面是夹具。验证停在进入 network reset 边；reset 主体、完整 dispatcher、真实 UI/GPU 和游戏内验收不在该脚本范围。

## 当前证据和边界

- 服务端定向回归当前为 20 pass / 0 fail / 0 skip，覆盖单人留图、队长/队员退出、重连方向、记录顺序和写入失败。
- 开发阶段 V176 组合窗口矩阵为 20 case / 5 guard：候选 helper 的 10 个 case 均保留本地模型，旧 helper 的 10 个 case 均不保留。
- v177-v3 portable actual run：20 context + 20 HUD + 5 guard 全部通过；5 个上下文 guard 也使用两个 occupied slots。旧 dungeon-only 路径的 Town/TownPeer 4 case 仍重绘，新 town+dungeon 序列 0 case 重绘；两次均隐藏 16 个控件并保留本地模型。virtual hide、layout 末端、scene 和 GPU 是夹具。
- 实际 native 覆盖的是 setter/cleanup/getter、退队分支以及 `2207E90`/`296E5E0`/`224C418` bounded HUD gates；**network reset 主体没有执行**，完整 network dispatcher、真实 HUD/GPU 和游戏画面仍未覆盖，不能称为 UI 已验收。

正式发布必须同时包含配套客户端和依赖本 helper 的服务端；只更新任一端会分别造成旧 UI 残留或本地模型被误删。发布、部署和用户验收分别记录。

