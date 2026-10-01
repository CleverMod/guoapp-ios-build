import Flutter
import UIKit
import DuanjuCore
import CFNetwork

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var brightnessScreen: UIScreen?
  private var originalBrightness: CGFloat?
  private var brightnessBackgroundObserver: NSObjectProtocol?

  private func resetPlaybackBrightness() {
    if let screen = brightnessScreen, let brightness = originalBrightness {
      screen.brightness = brightness
    }
    brightnessScreen = nil
    originalBrightness = nil
  }

  override func applicationDidEnterBackground(_ application: UIApplication) {
    resetPlaybackBrightness()
    super.applicationDidEnterBackground(application)
  }

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    ZgjEnsureCoreLinked()
    brightnessBackgroundObserver = NotificationCenter.default.addObserver(
      forName: UIScene.didEnterBackgroundNotification,
      object: nil,
      queue: .main
    ) { [weak self] _ in
      self?.resetPlaybackBrightness()
    }
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    if let registrar = engineBridge.pluginRegistry.registrar(forPlugin: "DeviceSettings") {
      let channel = FlutterMethodChannel(name: "duanju/device", binaryMessenger: registrar.messenger())
      channel.setMethodCallHandler { [weak self] call, result in
        let screen = UIScreen.main
        switch call.method {
        case "getBrightness":
          result(Double(screen.brightness))
          return
        case "setBrightness":
          guard let self,
                let arguments = call.arguments as? [String: Any],
                let brightness = arguments["brightness"] as? NSNumber else {
            result(FlutterError(code: "invalid_brightness", message: "亮度参数无效", details: nil))
            return
          }
          guard UIApplication.shared.applicationState == .active else {
            result(nil)
            return
          }
          if self.originalBrightness == nil {
            self.originalBrightness = screen.brightness
            self.brightnessScreen = screen
          }
          screen.brightness = CGFloat(min(1.0, max(0.01, brightness.doubleValue)))
          result(nil)
          return
        case "resetBrightness":
          self?.resetPlaybackBrightness()
          result(nil)
          return
        default:
          break
        }
        guard call.method == "systemProxy" else {
          result(FlutterMethodNotImplemented)
          return
        }
        let settings = CFNetworkCopySystemProxySettings()?.takeRetainedValue() as? [String: Any] ?? [:]
        func address(_ prefix: String) -> String {
          guard (settings["\(prefix)Enable"] as? NSNumber)?.boolValue == true,
                let host = settings["\(prefix)Proxy"] as? String,
                let port = settings["\(prefix)Port"] as? NSNumber,
                !host.isEmpty, port.intValue > 0 else { return "" }
          let name = host.contains(":") ? "[\(host)]" : host
          return "http://\(name):\(port.intValue)"
        }
        let http = address("HTTP")
        let https = address("HTTPS")
        result(["http": http, "https": https.isEmpty ? http : https,
                "bypass": settings["ExceptionsList"] as? [String] ?? [],
                "pac": (settings["ProxyAutoConfigEnable"] as? NSNumber)?.boolValue == true])
      }
    }
  }
}
