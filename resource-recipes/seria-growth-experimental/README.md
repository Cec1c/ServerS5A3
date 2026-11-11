# seria-growth-experimental（实验性）

> **实验性（Experimental）**：本目录为实验性公开配方，非稳定发布。配方只描述如何从已批准输入累计生成候选；不包含完整 PVF/NPK、工具二进制、私有路径、凭据或玩家资料，也不执行部署。

授权：用户公开清单第 8 项「赛丽亚 50 换 50 礼盒与统一成长型装备券源码及配方（实验性）」。

## 最终形态

- 商店只售 990033101 普通成长型装备券（购买时为无固有异次元属性的白券）。
- +0 用真实纯净增幅书（1286）赋属性；已强化只能用真实纯净黄金增幅书（8238）转增幅，等级按对应增幅书规则。
- 增幅态右键赋予目标装备本券异次元属性和等级，目标无需原有属性。
- 旧 990033102~105 仅兼容已有实例，不新卖、不发属性。历史五券创建步骤保留为演进记录。

## 输入

基准输入 PVF SHA256：

    a8b2aae3e0058ca08b83c39d0713fb8208a86349ebabf94ba062702b7f75ca0a

必须提供与该摘要一致的合法本地输入，并从本目录 plans/ 依次应用。

## 步骤与每步输出

| 步骤 | 计划文件 | 输入 SHA256 | 输出 SHA256 |
| --- | --- | --- | --- |
| 01 | plans/01-seria-growth-tickets.pvf-plan.json | a8b2aae3...ca0a | f19ab47a...86b6 |
| 02 | plans/02-seria-growth-native.pvf-plan.json | f19ab47a...86b6 | 00a6ebd3...1c765 |
| 03 | plans/03-seria-growth-original-ui.pvf-plan.json | 00a6ebd3...1c765 | d9243e2a...20c3c |
| 04 | plans/04-seria-growth-eligibility.pvf-plan.json | d9243e2a...20c3c | a0362256...27069 |
| 05 | plans/05-seria-growth-unified-ticket.pvf-plan.json | a0362256...27069 | d0dca8fb...dc57f |

## 最终候选

- SHA256：d0dca8fb7d1a512165f2214431e07ab47a62368affb96b94ba61f2b16d6dc57f
- 范围：68 changed / 0 added，其余文件保留。

## 工具

- pvfCLI SHA256：32954604a61342de3304b6412c8df8e038cd1998a4c94829ca5152c7ccef5178
- private-additions 源码：../seria-lucky-boxes/source/private-additions
- 构建：go build -buildvcs=false -trimpath -o private-additions.exe ./cmd/private-additions
- 本次实际运行二进制 SHA256：e667300fd8a26cb327cfc08505fc1f52518d6ab0d93bdaebc7ec41ab8362268f

## 限制

- 实验性；未部署、未发布到稳定 Latest。
- 计划文本仅含 PVF 内部路径与 token，无本机绝对路径、凭据或玩家资料。
- 不含完整 PVF/NPK/private-additions 二进制。
- 每步必须按输入摘要顺序执行，输出摘要必须等于下一步输入；不支持跨版本通用补丁。
- 本配方不执行部署，不覆盖现有玩家资源。
