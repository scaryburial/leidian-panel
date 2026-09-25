# 雷电验证器 — Aegis 定制分支（Android 2FA）

基于开源两步验证（TOTP/HOTP）应用 **[Aegis Authenticator](https://github.com/beemdevelopment/Aegis)**（GPL-3.0，上游 v3.4.3）的定制分支。

- 应用名：**雷电验证器**（版本 **3.4.3**，versionCode 82）
- applicationId：`com.leidian.aegis`（**内部 namespace 保持 `com.beemdevelopment.aegis`**，二者解耦）
- 本地生成验证码，**不联网**

## 定制内容
- 改名「雷电验证器」并更换 applicationId。
- 去除 About 页的 6 个外链（GitHub / 作者站点 / 邮箱 / Play 评分等）。
- 新增渐变背景与启动图标（火狐头剪影 + 渐变），适配 Android 12+ 启动画面。
- 中文（zh-rCN）与英文文案对齐。

## 📥 下载
到本仓库 **Releases** 下载 `leidian-aegis.apk`（见 `aegis-v*` 标签）。

> ⚠️ 该 APK 使用自定义签名密钥，与官方 Aegis **不兼容**（无法互相覆盖安装/升级）。请自行评估后再使用。

## 🔨 从源码构建（需 Android SDK，JDK 17）

```bash
cd apps/leidian-aegis
ANDROID_HOME=/path/to/android-sdk ./gradlew :app:assembleRelease
```

release 默认无 signingConfig，需自行 `zipalign` + `apksigner sign` 后再安装。

## ⚖️ 许可

本分支遵循上游 **GPL-3.0**（见 `LICENSE`），保留原作者版权。
