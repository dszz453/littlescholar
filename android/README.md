# 小书童 · 安卓客户端

WebView 套壳工程。把已经做好的孩子端 PWA 包成 APK，额外解决浏览器做不到的三件事：

| 能力 | 浏览器 | 本套壳 |
|---|---|---|
| 添加到桌面 | 需手动操作 | 原生安装 |
| 息屏播放 | 部分机型会断 | 前台服务 + 唤醒锁 |
| 后台播放 | 依赖浏览器策略 | 不调 `WebView.onPause()` |
| 系统返回键 | 无 | 接管为页面后退 |
| 全屏视频 | 有 | 有（`onShowCustomView`） |
| 服务器地址 | 只能是书签里那一个 | **应用内填写**，换地址不用重装（长按屏幕即可改） |

---

## 为什么是套壳而不是原生重写

孩子端的功能（目录导航、搜索、AI 对话、播放器、绘本阅读）已经在
`frontend-kid` 里实现完毕，且经过真实构建与渲染验证。原生重写意味着：

- 维护两套 UI，改一处要改两处
- 网盘直链鉴权、AI 接口、权限过滤等逻辑全部要重写一遍
- 失去 PWA 的自动更新能力

套壳只付出一个 WebView 的代价，换来零重复开发。

---

## 首次使用：填服务器地址

装好打开 App，**第一次进入的是设置页**，让家长填服务器地址。
当前线上入口是 **`https://study.19860819.xyz:8616`**（打包时已作为默认预填值），
内网设备也可以改建 `http://192.168.31.220:8093`（更快、不绕公网）。
不用带 `http://`，程序会自动补；地址保存在本地，之后每次直接进孩子端。

设置页有三个入口：

| 入口 | 场景 |
|---|---|
| 首次打开（没存过地址） | 装完第一次用 |
| **长按屏幕任意处** | 家长要换地址（孩子不会误触长按） |
| **页面加载失败时自动弹出** | 地址填错 / 服务器没起 / 不在同一网络 |
| **扫二维码** | 设置页里的按钮；扫后台「资源接入 → 孩子端入口」那张码，不用手输 |

> 扫码只**回填**到输入框，不自动连接 —— 扫到的可能是公网也可能是内网地址，
> 让家长看一眼再点「连接」，扫错了还能改。
> 相机权限被拒时会明确提示去哪儿打开，不做静默失败。

第三条是关键：没有它，填错地址就是一片白屏，家长也不知道该改什么。
错误提示里会带上"当前尝试的地址"，方便对着改。

> 为什么不做成打包时写死：内网 IP 会随路由器分配变，公网端口也可能换。
> 写死意味着每换一次地址就要重新打包、重新分发、重新安装。
> 现在 `-PsiteUrl=` 注入的 `BuildConfig.SITE_URL` **只作为首次的预填建议**。

---

## 目录结构

```
android/
├── app/
│   ├── build.gradle                    # 模块构建脚本（含 SITE_URL 注入）
│   ├── proguard-rules.pro
│   └── src/main/
│       ├── AndroidManifest.xml          # 权限 + 前台服务声明
│       ├── java/xyz/littlescholar/kid/
│       │   ├── MainActivity.kt          # WebView 宿主
│       │   ├── ServerConfig.kt          # 服务器地址的本地存储与归一化
│       │   ├── ServerSetupView.kt       # 「填写服务器地址」设置页
│       │   └── MediaPlaybackService.kt  # 息屏播放保障
│       └── res/
│           ├── drawable/                # 通知图标 + 自适应图标前景
│           ├── mipmap-*/                # 各密度启动图标
│           ├── values/                  # 字符串 / 颜色 / 主题
│           └── xml/                     # 网络安全配置
├── build.gradle                         # 顶层构建脚本
├── settings.gradle
└── gradle.properties
```

---

## 构建

### 前置

- JDK 17
- Android SDK（Platform 34 + Build-Tools 34.0.0）
- 设置 `ANDROID_HOME` 环境变量
- **Gradle 8.7 ~ 8.9**（wrapper 已提交，正常情况不需要自己装 Gradle）

> ⚠️ **版本约束（容易踩）**：本项目用 AGP `8.5.2`，官方要求 **Gradle ≥ 8.7**。
> 写 8.5 会直接构建失败，报
> `Minimum supported Gradle version is 8.7. Current version is 8.5`。
> AGP 8.5.x 的支持区间是 8.7 – 8.9，wrapper 里用的是 **8.9**。

### gradle wrapper（已提交，直接可用）

`gradlew`、`gradlew.bat`、`gradle/wrapper/gradle-wrapper.jar`、
`gradle/wrapper/gradle-wrapper.properties` **都已提交进仓库**，
clone 下来直接跑 `./gradlew` 即可，本机不需要预装 Gradle。

> 生成方式（仅供日后升级参考，用官方 Gradle 8.9 执行）：
> ```bash
> cd android
> # ⚠️ 不能用 `gradle wrapper --gradle-version 8.9`
> #    那个形式会去连 services.gradle.org 验证 URL，国内超时后报
> #    "Test of distribution url ... failed"
> # 必须直接指定镜像地址：
> gradle wrapper \
>   --gradle-distribution-url https://mirrors.aliyun.com/macports/distfiles/gradle/gradle-8.9-bin.zip \
>   --distribution-type bin
> ```
> wrapper 里的 `gradle-wrapper.jar` 是官方产物，约 43 KB。
> 生成后确认 `gradlew` 是 **LF** 行尾、`gradlew.bat` 是 **CRLF** 行尾 ——
> 两者错了在对应平台上会直接执行失败。

> **验证配置是否正常**（不需要 Android SDK）：
> ```bash
> cd android && ./gradlew tasks      # 期望 BUILD SUCCESSFUL
> ```
> 这一步会加载 AGP、解析 `settings.gradle`、检查 Kotlin 插件配置，
> 能覆盖绝大多数配置类错误。但**不会编译 Kotlin 代码** ——
> `assembleDebug` 还需要 Android SDK（Platform 34 + Build-Tools）。

### 打包

```bash
cd android

# debug 包（地址在 App 内填，这里不传也能直接打）
./gradlew assembleDebug

# 想换"首次预填的地址"（不是必须的）：
./gradlew assembleDebug -PsiteUrl=http://192.168.31.220:8093
./gradlew assembleRelease -PsiteUrl=https://kid.example.com
```

Windows 下若 `./gradlew` 不可执行，用 `sh gradlew assembleDebug`。

产物位置：`app/build/outputs/apk/debug/app-debug.apk`

> **首次构建**会下载 Gradle 与依赖，耗时较长。已配置阿里云镜像加速。

### release 签名

**已经配好了**：`app/build.gradle` 会自动读 `android/keystore.properties`，
存在就签名、不存在就产出未签名包（两者都不影响 debug 构建）。

```
android/
├── keystore.jks            # 密钥库（已 gitignore）
└── keystore.properties     # 口令（已 gitignore）
```

```properties
storeFile=keystore.jks
storePassword=***
keyAlias=littlescholar
keyPassword=***
```

```bash
sh gradlew assembleRelease     # 产出 app/build/outputs/apk/release/app-release.apk
```

> ⚠️ **这两个文件务必备份。** 密钥丢了就再也无法给已安装的 App 升级
> —— Android 只允许「同一签名」的包覆盖安装，签名不一致会被系统直接拒绝，
> 只能卸载重装（用户数据、登录态全丢）。
>
> 换机器/换人构建时，把这两个文件拷过去即可；不要把口令提交进仓库。

---

## 息屏 / 后台播放说明

这是本套壳最核心的部分，也是最容易踩坑的地方。

### 实现要点

1. **不调用 `WebView.onPause()`**
   `MainActivity.onPause()` 里刻意留空。一旦调用，WebView 内部的
   `<audio>`/`<video>` 会被强制暂停，"锁屏听故事"直接失效。

2. **`mediaPlaybackRequiresUserGesture = false`**
   否则音频必须等用户点击才能播，自动连播下一集会被阻断。

3. **前台服务 + `PARTIAL_WAKE_LOCK`**
   `MediaPlaybackService` 把进程优先级提到最高，并持唤醒锁防止
   CPU 休眠导致解码中断。通知渠道用 `IMPORTANCE_LOW`，不响铃不震动。

4. **网页侧配合 `MediaSession`**
   `frontend-kid` 的 `Play.vue` 已设置 `navigator.mediaSession.metadata`
   与各 action handler，通知栏的播放/暂停/上一首/下一首由系统媒体面板呈现。

### 已知限制

- **国产 ROM 仍可能杀后台**：MIUI、EMUI、ColorOS 有独立的电池管理。
  需引导家长在系统设置里把「小书童」的电池策略设为**无限制**，
  或在最近任务里**锁定**该应用。
- **`pagehide` 上报可能丢失**：进程被强杀时最后一次进度上报会丢。
  已用 15 秒定时上报兜底，最坏损失 15 秒进度。
- **WebView 内核版本**：不同厂商的 WebView 版本差异较大，
  `MediaSession` 的部分 action（如 `seekto`）在旧内核上可能不支持。
  代码里已用 `try/catch` 包裹，不支持时静默降级。

---

## 与 PWA 的关系

两者共用同一份服务器端资源，可以并存：

- **PWA**：家长用手机浏览器打开链接 → 添加到主屏幕。零安装成本。
- **APK**：需要更稳定的息屏播放体验时安装。功能完全一致。

如果只是"让孩子在平板上听故事"，PWA 通常够用；
如果发现息屏后音频频繁中断，再装 APK。

---

## 构建环境（本机已跑通的那一套）

| 组件 | 版本 | 位置 |
|---|---|---|
| JDK | Temurin **17.0.20.1** | `~/.workbuddy/toolchains/jdk17/jdk-17.0.20.1+1` |
| Android SDK | Platform **34** + Build-Tools **34.0.0** + platform-tools | `~/.workbuddy/toolchains/sdk` |
| Gradle | **8.9**（wrapper 自带，从阿里云镜像拉） | 无需预装 |

`android/local.properties` 里的 `sdk.dir` 指向上面的 SDK（该文件已被 `.gitignore` 忽略）。

---

## 还没验证的部分

`assembleDebug` 已在本机跑通并产出 APK，**静态构建层面没有未知数**。
但以下只能在真机上确认：

- 前台服务的 `foregroundServiceType="mediaPlayback"` 在 Android 14 上的行为
- `onShowCustomView` 的全屏切换在国产 ROM 上的表现
- 各厂商 WebView 对 `MediaSession` 的支持完整度（旧内核可能不支持 `seekto`，代码里已 try/catch 降级）
- 国产 ROM 的电池策略仍需家长手动设为「无限制」，否则息屏播一会儿仍可能被系统杀掉
