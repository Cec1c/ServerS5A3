# 困难夏洛夫名称修复

固定输入 SHA256：`8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790`。
输出 SHA256：`9327087d147576314dd95c6c0b0a638b1525bca48734658fe4f9b97dc33f82ea`，111865856字节。

名称函数VA01466150/RVA01066150遗漏困难夏洛夫63847。新增分支返回66071，与普通63843一致。使用函数尾14字节空隙，只改15字节代码及PE checksum共17字节；协议与PVF保持。

在安装Unicorn的Python环境执行，路径均由调用者明确提供：

```text
python patch_name.py --input <固定摘要的原始EXE> --out-dir <新输出目录>
python verify_name.py --input <同一原始EXE> --candidate <输出目录/DNF-r195-empire-name.exe> --receipt <新验证回执>
```

脚本拒绝输入摘要或固定指令不符、覆盖已有输出及超出允许区间的变化。Windows可用物理内存保护3GiB。验证执行16756次有界原生指令调用，覆盖已知映射、边界和随机输入；只有63847的返回值变化。2026-10-22用户已在v154验收名称显示。该版本已发布，源码整理不重新生成或替换玩家EXE。
