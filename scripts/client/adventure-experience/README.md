# 冒险团等级条经验口径

当前累计 EXE 的 E6545B..E6549A 将 raw+0 写为等级，raw+8 写为当前等级经验，并按 level+1 查询下一等级所需经验。BDF424..BDF46F 直接计算 current*100/required，没有扣除已完成等级的总经验。服务端旧实现发送角色等级积分，与这套2018经验表不一致。

核验工具只读固定 SHA 的客户端，在 Unicorn 执行该原生百分比计算及64位乘除叶函数；UI对象和最终进度条调用是显式夹具。六项覆盖0、半条、临界值及64位高位。没有执行渲染器或PVF表查找，不是GUI验收。

`python verify_client.py <archived-client.exe> <new-receipt.json>`

依赖 pefile、Unicorn。客户端输入完整SHA由脚本绑定；不修改EXE。当前服务端使用已有账号 HonorExp（满级经验事务累计）和PVF的 adventure exp rate / adventure exp table，推导等级和等级内余数。不补发经验，不写转换存档；没有2018表的旧文档仍用原角色等级算法，部分/损坏的2018表拒绝加载。
