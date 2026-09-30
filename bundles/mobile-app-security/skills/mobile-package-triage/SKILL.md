---
name: mobile-package-triage
description: >-
  移动端安装包分诊：定位包体、确认版本与构建、枚举导出组件/权限/本地存储/硬编码端点，并判定动态阶段的优先攻击面。
  Use when triaging an Android APK or iOS IPA before dynamic testing, enumerating exported components, or hunting hardcoded secrets.
metadata:
  tags: [移动端, mobile, 静态分析]
---

## 移动端包分诊（Mobile package triage）

**目的**：在动态测试之前，用一次静态分诊把攻击面排好优先级，避免在设备上盲试。

**顺序**

1. 定位包体并确认基本信息：包名/bundle id、版本、构建号、签名者。版本号决定后续哪些历史问题该被验证。
2. 解包并枚举清单文件：导出组件（Activity/Service/Receiver/Provider）及其 `intent-filter`、权限申请、
   `network_security_config`（Android）或 `NSAppTransportSecurity`（iOS）的例外域。
3. 硬编码面：密钥、token、内网端点、调试开关。按"是否在生产路径上被读取"过滤，只报可执行的那批。
4. 本地存储：SharedPreferences / SQLite / 文件三处的内容是否含敏感数据、是否可被其他应用读到。
5. 证书校验：是否实现固定、是否有绕过分支（`onReceivedSslError` 直接 proceed、自签信任锚）。
6. 输出动态阶段优先级清单，每项写明证据（文件路径 + 原始片段）。

**注意**：只读分析。不修改签名、不重打包投递、不在真实用户设备上写入生产数据。
