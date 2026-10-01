# 红果鉴 / 真果鉴

> ⚠️ **免责声明**：本项目源码来自网上大名鼎鼎的**鱼佬**（原作者）。我只是把它拿来打包、测试着玩，方便自己用，**不保证任何可用性，随时可能删库**。

Flutter 多端独立短剧 / 影视应用，原名「短剧库 APP」。站源请求、解析、下载和播放均在设备上完成，不依赖自建服务。当前源码版本：**0.2.72+79（开发快照，未验收）**。

公开源码仓库：[youniube/guoapp-ios-build](https://github.com/youniube/guoapp-ios-build)，默认分支为 `ios-build`。未签名 iOS 构建从 Actions 的 `Build unsigned iOS IPA` 手动触发；普通构建保持依赖锁文件校验，只有维护锁文件时才勾选 `refresh_lockfile`。

已删除重复的 **野果专线** 入口及其专用接口实现，保留 **野果**。两者属于同一野果站源，目录、搜索、详情及播放使用相同的 API 路径；原专线固定使用旧域名、JSON POST 和 AES-CBC 参数，保留的野果通过页面动态获取接口入口与加密配置，并使用表单请求及线路切换。全站源版现有 11 个站源入口。站源密码锁及隐藏站源过滤已移除，旧版保存的站源锁设置在读取时自动忽略，覆盖升级无需解锁。

旧用户权限中保存的 `yeguo-worker` 在读取时忽略，避免因入口删除而锁定整个用户配置；不会自动授予野果或其他站源权限。原野果专线的收藏、观看记录仍保留在本地数据及备份中，但不再显示，也未自动迁移到野果。

黄果旧版出现 `4007: login too frequently` 表示源站拒绝过于频繁的访客登录。访客登录现在只发送一次，发送前保存访客标识及重试时间；失败或重启后继续使用同一标识。收到 4007 后应用暂停登录 5 分钟，站源管理显示重试倒计时；其他未完成的登录至少间隔 1 分钟，已保存的有效会话继续复用。此间隔是应用的重试策略，源站限制的实际解除时间由服务器决定。

移动端播放器接入方向锁定：画面左右滑动调整进度，进度条拖动与画面横滑共用主画面预览；拖动期间暂停并合并预览跳转请求，松开后保留原播放 / 暂停状态。左侧 35% 区域只有上下滑动才调亮度，其余区域上滑增大播放音量、下滑减小播放音量，范围 0–100%，显示音量百分比提示；已移除上下滑动切集，切集使用选集及上一集 / 下一集按钮。长按 350 毫秒临时 3 倍速、松开恢复原倍速的行为保留。画面双击左、中、右三等分区域分别快退、播放 / 暂停、快进；播放器底部「播放设置」可保存 5 / 10 / 15 / 30 / 60 秒的双击步长，默认 10 秒。iOS 补齐原生亮度通道，退出播放器或切到后台时恢复原亮度。预览使用当前视频解码画面，刷新受关键帧、媒体格式和网络缓冲影响。

> 说明：按项目约定暂停测试与回归；本次播放器手势修改完成 Dart 格式整理、源码引用检查和收尾同步，尚未运行自动化测试、静态分析或设备回归。此前访客登录修复补充的回归用例也尚未执行。当前 `0.2.72+79` 全站源 iOS 未签名 IPA 已通过 macOS Actions 的依赖锁文件校验、修改文件格式检查、编译及包结构 / 版本 / arm64 / SHA-256 检查；Android / Windows 未重新构建。本次手势、音量调节、画面预览与亮度恢复尚未做真机验收，保持开发快照状态。构建及包检查通过不代表真机行为已验收。

| 编译方式 | 应用名称 | 可用站源 |
| --- | --- | --- |
| 默认（不加参数） | 红果鉴 | 仅红果 |
| 加 `--all-sources` | 真果鉴 | 红果、韩小圈、鬼片、青空、黄豆、剧果、野果、帝果、黄果视频、黄果 AI、黄果旧版 |

全站源版共内置 11 个站源入口，管理员默认全部显示；黄果视频、黄果 AI、黄果旧版在首页站源菜单中与其他站源同级显示，各自独立加载目录、分类及榜单，站源管理也直接列出三个入口。站源密码设置、解锁、重新锁定、关闭密码功能及连续点击「最近观看」的隐藏入口已删除。旧版站源锁配置（包括损坏的锁字段）自动忽略，不影响观看记录与收藏；普通用户仍遵守用户管理中分配的站源权限，用户登录密码仍保留。

这是**编译选项**，应用内不能切换版本。标题、Android 桌面名称与电视横幅、Windows 窗口与分发文件名、iOS 显示名随编译选项变化。界面、站源调用、原生下载调度同时限制可用站源；红果版不会访问或继续执行其他站源的旧任务。

两版保留原 Android / iOS 应用标识及数据目录；Android 使用同一签名可相互覆盖升级，不能作为两个独立正式应用并排安装。切换版本保留追剧、观看记录、用户权限和下载记录；备份格式保持兼容。

## 使用

| 功能 | 操作 |
| --- | --- |
| 浏览 | 标题栏依次提供排序与筛选、榜单、多选下载和展开搜索。分类保持独立一行，内容区左右滑动切类；支持默认、名称、自然季号、上线日期、热度、播放量排序及连载状态筛选。列表下滑接近底部自动加载下一页，底部「加载更多」为兜底 |
| 搜索 | 红果合并官网与名称索引，输入停顿 300 毫秒显示最多 10 条联想；韩小圈、剧果、野果、鬼片调用各自的在线搜索并按分页继续加载。部分失败保留有效结果；最近 20 次搜索按用户保存 |
| 推荐 | 仅红果分类栏在「全部」后显示「推荐」，可切换真人剧、漫剧、AI 剧并分别记住位置 |
| 详情 | 普通点剧直接进入播放；下载和兜底入口保留详情页。详情页浏览封面、资料、追剧状态与可展开简介；底部固定「立即播放 / 继续播放」和下载入口。选集默认折叠，展开后每组 50 集，可切换范围、定位当前或输入集数跳转；宽屏与电视采用资料、选集分栏 |
| 追剧与历史 | 按想看 / 在看 / 已看 / 有更新筛选；红果已追剧发现新季后进入「有更新」。追剧和历史均可搜索。每个用户独立保存，每 5 秒及退出播放时记录真实进度 |
| 卡片与批量下载 | 卡片「更多」提供收藏、状态和下载选集；电脑右键，电视菜单键。发现页长按卡片或点标题栏「多选下载」，一次最多 50 部 |
| VIP | 全站源版浏览黄豆或全部站源时显示 VIP 图标，仅过滤黄豆；默认隐藏已确认的 VIP。未知、免费、VIP 分别保存，未知不会覆盖已知状态 |
| 手机播放 | 左右滑动或拖进度条调整进度并预览主画面，松开恢复原播放状态；左侧上下滑调亮度，其余区域上滑增大音量、下滑减小音量；上下滑动不再切集。双击左侧快退、中间播放 / 暂停、右侧快进，步长在底部「播放设置」保存。轻点显示 / 隐藏控制；长按 350 毫秒临时 3 倍速。视频下方为「选集 / 简介 / 下载」Tab |
| 画中画 | Android 手机 / 平板播放器接入画中画按钮；进入前隐藏自绘控制层与弹幕，小窗只保留画面。Android TV、Windows、iOS 暂未接入 |
| 画质增强 | 仅 Windows 桌面端显示并运行增强链路（关闭 / 自动 / 省电增强 / 清晰优先）；移动端与电视端隐藏 |
| 弹幕 | 红果在线播放默认开启；播放器侧边圆形「弹」字按钮可关闭、查看状态及失败重试。本地播放不加载弹幕 |
| 下载 | 播放页「下载」Tab 和详情页下载入口均可选择分集与画质；支持批量暂停、继续、重试、删除及清理任务但保留视频。「更新本剧」补新增或缺失分集 |
| 更多 | 站源管理、用户管理、设置与备份、界面模式、关于 |

| 站源 | 浏览与播放 | 搜索 |
| --- | --- | --- |
| 红果 | 真人剧、漫剧、AI 剧及分集 | 联网搜索与官网搜索联想 |
| 韩小圈 | MacCMS 模板：最新韩剧 / 韩国电影 / 韩国综艺 / 韩国动漫，多线路分集 | 站源在线搜索 |
| 鬼片 | MacCMS 站点：鬼片 / 电视剧 / 动漫，多线路分集 | RSS 最新条目标题匹配（站点搜索已停用） |
| 青空 | 番剧、剧场动画、特摄及分集 | 站源在线搜索 |
| 黄豆 | 列表、VIP 标记及分集 | 筛选已加载短剧 |
| 剧果 | 热门、最新、接口分类、详情及可播放分集；签名 Cookie 接入在线播放、预加载和下载 | 站源在线搜索，支持继续加载 |
| 野果 | 接口实际分类、目录、详情及真实分集；按分集重新取流，保留 H.264 / H.265 地址 | 站源在线分页搜索 |
| 帝果 | 网页分类、目录、详情及分集；vplayer 签名解析 | 站源在线分页搜索 |
| 黄果视频 / 黄果 AI / 黄果旧版 | 列表、分类、详情及分集 | 筛选已加载短剧 |

站源可用性、清晰度和区域限制取决于源站及网络；应用不解除源站 VIP 或其他授权限制。

### 网络与资源设置

管理员从「设置与备份 → 网络与资源」选择自动、直连或手动代理，手动地址支持 HTTP、HTTPS、SOCKS5 / SOCKS5h。代理地址默认遮蔽、不进入备份；设备内播放服务始终绕过代理。自动模式读取系统静态代理与排除列表，恢复前台及每 30 秒更新；代理不会关闭 TLS 校验。

目录请求并发可设 1–6、间隔 0–5000 毫秒（默认 3 / 250ms）；下载并发默认 2、可设 1–6。仍保留前台优先、后台让行、取消、退避及请求数量边界。

### 局域网互联：追剧同步与推送播放（已接入，待验证）

两台设备在同一局域网直连，自动同步追剧记录或接续播放，不依赖账号服务器。服务类型 `_zgj-link._tcp`（DNS-SD / mDNS），按设备 ID 合并多网卡地址，首次配对记录证书指纹，后续连接固定校验。

入口：「追剧 → 标题栏同步」或「设置与备份 → 设备互联」。手动同步默认「双向合并」，可选覆盖对方 / 覆盖本机，先「查看预览」再执行；自动同步仅发送改动条目与删除标记，播放期间约每 10 秒发送进度。冲突记录保留候选，用户从同步页逐项处理。

## 安装包与平台状态

| 平台 | 包与状态 |
| --- | --- |
| Android 8.0+ | 三架构（arm64-v8a / armeabi-v7a / x86_64）APK；同一签名可覆盖升级 |
| Windows 10/11 x64 | 完整 ZIP 解压后运行 `hongguojian.exe` / `zhenguojian.exe`，保留所有 DLL 与 `data`；局域网原生发现依赖 Windows 10 1903+ |
| Android TV | 与手机共用源码，自动识别电视模式并保持横屏；待电视 / 盒子实机验收 |
| iOS 15.1+ | `0.2.72+79` 全站源 arm64 未签名 IPA 已构建并通过包检查；接入进度预览、分区双击、竖滑亮度 / 音量，移除竖滑切集并保留长按倍速，包含黄果旧版访客登录修复。需要用户自行签名，待真机手势、音量、亮度恢复、覆盖升级、播放与下载验收 |

`INSTALL_FAILED_NO_MATCHING_ABIS` 表示 APK 与设备架构不匹配，请更换对应架构安装包。

### GitHub Actions

推送 `main` / `master`、`v*` 标签、提交 PR，或手动运行 **Build app packages**，会先检查再构建两版（默认与 `--all-sources`）：

| 产物 | 内容 |
| --- | --- |
| `*-android` | 三种架构 APK 和 SHA256 |
| `*-windows` | 完整 ZIP 和 SHA256 |
| `*-ios-unsigned` | 未签名 `.app` ZIP 和 SHA256，不能直接当已签名 IPA 安装 |

推送 `main` 且 android / ios / windows 全部构建成功时，自动创建 / 更新 GitHub Release（tag `app-v{version}`）。发布新版本前需先在 `pubspec.yaml` 提升 `version`，否则会覆盖同名 tag 的 Release。

Android 正式发布使用同一签名并递增构建号，在仓库 Secrets 配置：`ANDROID_KEYSTORE_BASE64`、`ANDROID_KEYSTORE_PASSWORD`、`ANDROID_KEY_ALIAS`、`ANDROID_KEY_PASSWORD`。未配置时生成 debug 签名预览包。

本地不入库的 `android/key.properties`：

~~~properties
storeFile=/absolute/path/zhenguojian-release.jks
storePassword=你的密码
keyAlias=zhenguojian
keyPassword=你的密码
~~~

## 开发与构建

Flutter `3.47.4`、Dart `3.12+`、Go `1.24.1+`、Python `3.10+`。Android 需要 JDK 17、SDK 36、NDK `28.2.13676358`；Windows 需要 Visual Studio C++ 桌面组件及 MinGW-w64 x64；iOS 需要 macOS、完整 Xcode 和 CocoaPods。

构建脚本对子进程默认设置 `GOPROXY=https://goproxy.cn,direct`、`GOSUMDB=off`，同名环境变量可覆盖。

~~~sh
python3 scripts/build_android.py                 # 红果鉴
python3 scripts/build_android.py --all-sources   # 真果鉴
python3 scripts/build_android.py --abi arm64-v8a
python3 scripts/build_android.py --cn-mirrors    # 国内镜像
~~~

~~~powershell
.\scripts\build_windows.ps1
.\scripts\build_windows.ps1 -AllSources
.\scripts\build_windows.ps1 -ChinaMirrors
~~~

~~~sh
python3 scripts/build_ios.py
python3 scripts/build_ios.py --all-sources
python3 scripts/build_ios.py --core-only [--simulator]
python3 scripts/build_ios.py --export-options /path/to/ExportOptions.plist
~~~

用户自行签名安装时，在 macOS 上运行 `python3 scripts/build_ios.py --all-sources`，输出全站源版 `dist/ios/zhenguojian-版本-ios-unsigned.ipa`，无需向构建机提供签名证书。打包时保留 `Payload/Runner.app` 层级，并检查 IPA 中的应用可执行文件与 `Info.plist`。也可在本项目 GitHub 仓库的 Actions 中手动运行 `Build unsigned iOS IPA`（`.github/workflows/build-ios.yml`），完成后下载 `zhenguojian-ios-unsigned` 产物。该任务仅构建未签名 IPA 并上传构建产物，不发布 Release。

2026-10-01：[全站源 iOS 构建成功](https://github.com/youniube/guoapp-ios-build/actions/runs/36868405458)，产物 `zhenguojian-0.2.66+73-ios-unsigned.ipa`，30,407,302 字节，应用名「真果鉴」，Bundle ID `com.duanju.duanjuApp`，最低 iOS 15.1。IPA ZIP 完整性、`Payload/Runner.app` 层级、arm64 架构、版本与校验和检查通过；SHA-256 为 `a1c3b4f49fab398d89486499f606bcff86a2144761a95144b18f4cd7320255e6`。构建时补齐缺失的 7 个 Flutter 依赖并保存锁文件，未升级其他已锁定依赖；未做真机验收。

2026-10-01：[移除站源密码锁后的最新版 iOS 构建成功](https://github.com/youniube/guoapp-ios-build/actions/runs/36874193742)，构建源码 `7d5c908`，产物 `zhenguojian-0.2.69+76-ios-unsigned.ipa`，30,385,680 字节，应用名「真果鉴」，Bundle ID `com.duanju.duanjuApp`，最低 iOS 15.1。站源密码设置、解锁、重新锁定、关闭密码及连续点击入口均已删除，旧锁配置自动忽略；合并用户已要求的野果专线去重和黄果三入口拆分，共 11 个独立站源。依赖锁文件校验、修改文件的 Dart 格式检查、iOS 编译、FFI 入口、IPA ZIP 完整性、包层级、版本及 arm64 架构检查通过；SHA-256 为 `3286f9ce06375a76cd20fae70b1ab9315167449ef62df2d1aa6d38d0da5b0a3b`。未运行自动化测试或真机验收。

2026-10-01：[播放器手势与音量调整后的 iOS 构建成功](https://github.com/youniube/guoapp-ios-build/actions/runs/36882465549)，构建源码 `90e575f`，产物 `zhenguojian-0.2.72+79-ios-unsigned.ipa`，30,396,264 字节，应用名「真果鉴」，Bundle ID `com.duanju.duanjuApp`，最低 iOS 15.1。横滑及进度条拖动接入主画面预览；画面双击左 / 中 / 右分别快退 / 播放暂停 / 快进，跳转步长可保存；左侧竖滑调亮度，其余区域竖滑调播放音量，已移除竖滑切集，保留长按 350 毫秒临时 3 倍速。包含黄果旧版访客登录单次请求及冷却持久化修复。依赖锁文件校验、修改文件的 Dart 格式检查、iOS 编译、FFI 入口、IPA ZIP 完整性、包层级、版本、arm64 架构及校验和检查通过；SHA-256 为 `c36d449ffa9432189475d6f3df5161285ed57387cac7ce5a96d28f6ea318579a`。未运行自动化测试、静态分析或真机验收。

产物在 `dist/android`、`dist/windows`、`dist/ios`，红果版以 `hongguojian-` 开头，全站源版以 `zhenguojian-` 开头。

首次 Android 调试先编译对应架构核心，再运行：

~~~sh
python3 scripts/build_native.py --platform android --abi arm64-v8a
flutter pub get --enforce-lockfile
flutter run
~~~

调试全站源版：先给 `build_native.py` 加 `--all-sources`，再 `flutter run --dart-define=ALL_SOURCES=true`；iOS 对应 `build_ios.py --core-only --all-sources`。脚本会同步设置 Dart 常量和 Go 编译参数，应用启动时校验二者一致，避免混装原生库。

播放器使用 [media_kit](https://github.com/media-kit/media-kit) / libmpv，合并和导出使用 [FFmpegKit min-gpl](https://github.com/sk3llo/ffmpeg_kit_flutter)（含 GPL 媒体组件）。FFmpegKit 不参与正常播放或下载的转码。

### 集中检查与真机回归

~~~sh
python3 -m unittest discover -s scripts -p 'test_*.py'
dart format --output=none --set-exit-if-changed lib test integration_test test_driver
dart analyze --fatal-infos lib test integration_test test_driver
flutter test --dart-define=DISABLE_REMOTE_IMAGES=true
flutter test --dart-define=DISABLE_REMOTE_IMAGES=true --dart-define=ALL_SOURCES=true
cd native
go test -race ./...
go test -race -ldflags="-X duanjuapp/native/core.buildAllSources=true" ./...
~~~

Android 设备回归（连接并授权 USB 调试、保持解锁）：

~~~sh
python3 scripts/create_test_media.py
python3 scripts/serve_test_media.py
adb reverse tcp:38473 tcp:38473
flutter drive --driver=test_driver/playback.dart --target=integration_test/playback_test.dart \
  --dart-define=DISABLE_REMOTE_IMAGES=true --dart-define=FIXTURE_BASE_URL=http://127.0.0.1:38473
~~~

结果在 `build/device-test/results/`；结束后停服务并 `adb reverse --remove tcp:38473`。

### 源码同步

~~~sh
python3 scripts/finish_task.py --message "本次实际完成的变更"
python3 scripts/sync_source.py --check
~~~

脚本只同步纯源码到同级 `../guoapp`，并生成 `真果·鉴-YYYYMMDDHHMM.zip` 源码压缩包；不执行 Git 提交、分支或推送。

## 目录结构

| 目录 | 内容 |
| --- | --- |
| `lib` | 页面、播放器、本地用户、FFI、下载和媒体处理 |
| `native/core`、`native/bridge` | 独立站源核心、缓存、下载、目录迁移及 C ABI |
| `android`、`windows`、`ios` | 平台工程与必要资源 |
| `assets/video_enhancement`、`packages/media_kit_libs_windows_video` | 增强 Shader 与许可、固定 Windows 媒体依赖插件 |
| `scripts`、`.github/workflows` | 构建、签名、验证、同步和版本快照 |
| `test`、`integration_test` | 自动化与设备回归 |

## 站源开发约定

站源是 Go 原生 provider（`native/core/provider_*.go`），不是运行期加载的 Python 源。新增站源需接入：`provider_huangguo.go`（常量 / 白名单 / `canonicalProviderSource` / `GetHuangguoChapters`）、`provider_media.go`（baseURL / host 反查 / 播放分派）、`app_categories.go`、`app_cover_metadata.go`、`app_runtime.go`（Config 字段 + 目录 / 搜索 / 详情分派 + 空页放行），并在 `lib/models.dart` 登记 `SourceSite`。

注意：Go RE2 正则**不支持 lookahead**。解析多线路播放列表时不能用 `(?=...)` 做分段，否则非捕获组会消耗下一段开头；应改用显式字符串截断（从容器标记之后查找下一段标记）。
