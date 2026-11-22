# 积分商城、卢克战利品商店与罗什气泡

输入是[公开团本与商店配方](../selected-raid-shop-20261118/README.md)的两份输出。[公开 PVF-CLI](https://github.com/Cec1c/PVF-CLI) 的 `6834395da196f6cc6fa1d37a72c281a391f5094d` 版本支持这里使用的 schema 2。按工具说明构建并将 `pvf` 加入 PATH，或用 `--pvf-cli` 指定生成器使用的工具路径。以下命令在本目录运行，请为输出指定新文件。

```text
python pvf_plan.py --input public-raid.pvf --out selected-plan.json
pvf patch --pvf public-raid.pvf --plan selected-plan.json --out selected.pvf
pvf verify --pvf public-raid.pvf --plan selected-plan.json --out selected.pvf
```

配方只移除积分商品 1000064 及其购买限制，将卢克团队塔罗牌袖珍罐 10159653 的售价从 1000 改为 500 宿命之恒，并从战利品货架移除 10159652。去掉商品出售入口后，已持有袖珍罐的使用定义仍然保留。验证时三项资源发生变化，其他 1,126,602 项的身份均保持。输出 SHA-256 为 `f86090d9f39dc558a397062f5a794496343c1dd7e04a2443b982733c2c6418ee`。

客户端脚本需要 Python 与 pefile。它开启已注册的积分余额通知 427，在原生罗什气泡第四帧缺失时跳过该帧的可选时长设置。要同时包含兵营进度条，请先运行[兵营进度配方](../fiendwar-barracks-board/README.md)，再运行同一条客户端命令。保留输入客户端的来源通知。

```text
python build_client.py --input DNF-base.exe --out DNF-selected.exe
python test_client.py DNF-base.exe DNF-selected.exe
```

| DNF-base.exe 来源 | DNF-selected.exe 的 SHA-256 |
|---|---|
| 公开团本重建输出 `a853463e…` | `fb2db71ee7cb4956e91891eea9731a2999cbb3694e681801e139239833458927` |
| 上述兵营进度输出 `e3e1b2bb…` | `21a9ed0b0c86e8e3a95f7b6c84270025c91219d3df3e9143bdd6629264c9166e` |
