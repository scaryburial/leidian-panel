# ADeep — DeepSeek 余额悬浮窗（Android）

一个安卓原生小工具：**常驻悬浮窗实时显示 DeepSeek（AI）API 余额与今日消费**。

- 悬浮窗实时余额（默认 30 秒刷新，可配 5/10/30/60/300 秒，**点按立即刷新**）
- 「今日消费」估算
- 开机自启、电池优化豁免、无障碍保活（长时间驻留不易被系统清理）
- 暗色科技风界面 +《狐妖小红娘》人物素材
- 隐私：**零第三方依赖**（不用 AndroidX / OkHttp / 协程），网络用系统 `HttpURLConnection`

> 说明：本应用只用于查询你自己 DeepSeek 账户的余额，需自行填写 API Key（仅保存在本机）。

---

## 📥 下载安装

到本仓库的 **Releases** 下载最新 `ADeep.apk`（见 `adeep-v*` 标签），在安卓手机上安装。

首次使用请依次授权：
1. **悬浮窗权限**（显示在其他应用上层）
2. **后台运行 / 电池优化豁免**（防止被系统杀）
3. **通知权限**
4. （可选）无障碍保活

然后在应用内填入 DeepSeek **API Key**，点「测试连接」，再「启动悬浮窗」。

## 🧱 技术栈

- Kotlin，Gradle 8.14.4 + AGP 8.13.2
- compileSdk 36 / minSdk 26 / targetSdk 34
- 零第三方依赖，UI 全用系统控件与原生主题

## 🔨 从源码构建

需要 Android SDK（JDK 17）。仓库内的 `build.sh` 会按 CPU 架构自适应搭环境并编译：

```bash
cd apps/adeep
./build.sh
```

产物在 `app/build/outputs/apk/` 下。

## 📄 目录

```
apps/adeep/
├── build.sh / settings.gradle / build.gradle / gradle.properties / gradlew
├── 开发日志.md                # 架构、二次开发指南、版本变更
└── app/
    ├── build.gradle           # 版本号、签名在此
    └── src/main/
        ├── AndroidManifest.xml
        └── java/com/aicode/balanceoverlay/
            ├── MainActivity.kt              # 配置界面
            ├── OverlayService.kt            # 前台服务 + 悬浮窗 + 定时轮询
            ├── BalanceApi.kt                # 余额接口请求与解析
            ├── DailyUsage.kt / Prefs.kt / BootReceiver.kt
            ├── BalanceAccessibilityService.kt
            └── App.kt
```

版本：**1.6**（versionCode 7）

## ⚖️ 许可

仅供个人自用。DeepSeek 名称与相关素材归其各自所有者。
