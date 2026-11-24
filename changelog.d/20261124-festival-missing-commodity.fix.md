---
category: 功能修复
---

# 商城礼包被 PVF 删除后不再导致整个商城无法购买

## 变更

- PVF 从 `etc/cerashop.etc` 删掉礼包，而多买多送商品表仍列着这些礼包时，`parsePrivateFestivalCatalog` 只把它们移出白名单并计数，不再返回错误拖垮整个商城目录。
- `pvfCeraShopCatalog` 增加 `festivalSkipped`，加载日志 `dnf-cera-shop-pvf-catalog-loaded` 记录被跳过的多买多送商品数。

## 验证

- `go build -tags=timetzdata ./...` 通过；dnfbridge 全量测试在改动前后失败集合一致。
- 此前整个目录加载失败会让所有购买提示“物品栏空间不足”；现在其余商品照常购买。
