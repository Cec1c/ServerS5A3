---
category: 功能修复
---

# 交易字段不再被当作消耗品的使用行为

## 变更

- `resolveMagicBoxNeedMaterial` 不再回退读取顶层 `[need material]`。该段在 PVF 里是「NPC 兑换 / 商店购买这个礼盒所需的材料」，不是「每次开启再交一次」。原先只对 `10094738` 做了硬编码豁免，其余礼盒会被重复收费：例如 `490700581`「异次元碎片礼盒」用 2 个 `490700573` 邀请函兑换，开启时又被扣 2 个。现在只认显式开启材料 `[lottery use need item]`；开启金币 `[lottery use cost]` 仍由 `currentLotteryOpeningGold` 那条独立路径处理。
- 经验胶囊的段白名单 `capsuleSectionNames` 补上 `"price"`。`[price]` 是卖店价、纯交易数值，与胶囊行为无关；白名单漏掉它会让整颗胶囊被判为「未知行为段」而彻底无法使用（`10005308`「(50百分比)兔兔成长胶囊」即因此失效，`10005307` 不带该段所以正常）。
- 四处把旧语义写进夹具的用例改用 `[lottery use need item]`（保留材料扣减的测试意图），另新增两条回归：背包零材料时带 `[need material]` 的罐子仍能开启且不扣材料；带 `[price]` 的胶囊能正常解析。

## 验证

- 在干净上游树（`a1f700a8`）的独立 worktree 上重建并验证：`go build -tags=timetzdata ./...` 通过、`go vet` 通过、改动文件 `gofmt` 无差异。
- `internal/modules/dnf/experienceitem` 包测试全绿；`internal/services/dnfbridge` 与本改动相关的 lottery / booster / magicbox / capsule 用例全绿，未引入新增失败。
- `python scripts/ci/changelog_fragments.py check` 通过。
