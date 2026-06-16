# 下载校验与杀软误报说明

MultiPortProxy 是一个本机代理端口管理工具，会启动用户本机已有的 `mihomo.exe` / `xray.exe`，并监听 `127.0.0.1` 上的本地代理端口。部分杀毒软件可能因为“代理工具、端口监听、启动子进程、未签名的新文件”等特征产生启发式误报。

## 校验发布文件

每个 GitHub Release 都会上传 `SHA256SUMS.txt`。下载后可以用 PowerShell 校验：

```powershell
Get-FileHash .\MultiPortProxy.exe -Algorithm SHA256
```

将输出的 SHA256 与 Release 页面中的 `SHA256SUMS.txt` 对比，确认文件没有被第三方篡改。

## 如果出现 360 等杀软误报

建议优先向对应安全厂商提交误报申诉，并附上：

- GitHub Release 下载地址
- `SHA256SUMS.txt` 中的哈希
- 杀软提示截图
- 软件说明：本工具只管理本机代理端口，不捆绑代理内核，不读取浏览器密码、照片等个人文件

360 软件误报申诉入口：

- https://fuwu.360.cn/shensu
- https://open.soft.360.cn/report.php

## 长期降低误报的正规方式

- 使用 Authenticode 代码签名证书签名发布文件
- 保持 exe 版本信息与 Release tag 一致
- 发布 SHA256 校验文件
- 不使用压缩壳、混淆、UPX
- 避免在程序启动时自动写入开始菜单、注册表、计划任务等持久化位置

