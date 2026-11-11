# upgrade-shop（休亚商店 / 强化增幅药水）

公开增量 PVF 配方。原始 PVF 不随本目录分发，也不执行部署。

授权：用户公开清单第 7 项「强化增幅药水、穿戴加成及休亚商店累计」。

## 输入

输入 PVF SHA256：

    936cb6c027d665554993aabdca022a056dae481fa60aa516b17e0abce4adc82a

必须提供与该摘要一致的合法本地输入。

## 计划

plans/upgrade-shop.pvf-plan.json（schema 1，4 处编辑，每处含 before_sha256 / after_sha256 / raw_base64）：

- stackable/private_aiken_exchange/990031102.stk
- stackable/private_aiken_exchange/990031103.stk
- stackable/490008001/chn_490008445.stk
- itemshop/legend_yoshua.shp

## 限制

- 计划文本仅含 PVF 内部路径与 token，无本机绝对路径、凭据或玩家资料。
- 不含完整 PVF/NPK/private-additions 二进制。
- 本配方不执行部署。
