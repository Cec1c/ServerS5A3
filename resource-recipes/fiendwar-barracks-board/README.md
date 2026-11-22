# 超时空兵营修复进度

上、中、下路兵营的修复由服务端计时 720 秒。雷达和炮塔都已通关时，旧客户端仍将正在修复的兵营进度停在当前帧。此配方让战况表在绘制该控件时按已过去的时间更新。

输入取自 [公开的罗什客户端重建配方](../selected-raid-shop-20261118/README.md)，SHA-256 为 `a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf`。请保留原客户端随附的来源通知。在本目录运行：

```text
python build.py DNF-public-raid.exe DNF-board.exe
python verify.py DNF-public-raid.exe DNF-board.exe
```

输出 SHA-256 为 `e3e1b2bb2fdf689a82216923d1516505c606cc4ec56cfc81fa1bab527dd52ef8`。新建的输出文件包含两处战况表修改和 PE 校验和更新。玩家可在雷达和炮塔都已通关后观察兵营进度条，180 秒约为四分之一，360 秒为一半，719 秒仍在修复，720 秒重新开放。原生多人画面有待实测。
