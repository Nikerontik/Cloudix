import UIKit
import Cloudixmobile

@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.rootViewController = WebAppViewController()
        window.makeKeyAndVisible()
        self.window = window
        return true
    }

    /// iOS suspends the process a short while after this and every socket dies
    /// with it. Nothing short of push can prevent that, so the core is left
    /// running — a brief trip to the home screen and back should not tear down
    /// a session — and reconnection is handled on the way back in.
    func applicationDidEnterBackground(_ application: UIApplication) {}

    func applicationWillEnterForeground(_ application: UIApplication) {
        (window?.rootViewController as? WebAppViewController)?.resumeAfterSuspension()
    }

    func applicationWillTerminate(_ application: UIApplication) {
        MobileStop()
        MobileStopAssets()
    }
}
