import UIKit
import WebKit
import Cloudixmobile

/// Hosts the Cloudix UI in a WKWebView and owns the Go core's lifetime.
final class WebAppViewController: UIViewController {
    private var webView: WKWebView!
    private var bridge: GoBridge!

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .black
        buildWebView()
        startCore()
    }

    override var preferredStatusBarStyle: UIStatusBarStyle { .lightContent }

    private func buildWebView() {
        let controller = WKUserContentController()

        // The page reads this before the app bundle runs, so the very first
        // render already knows the platform and what to hide.
        let boot = """
        window.__cloudixBoot = { platform: "ios", features: \(GoBridge.featuresJSON) };
        """
        controller.addUserScript(
            WKUserScript(source: boot, injectionTime: .atDocumentStart, forMainFrameOnly: true)
        )

        let config = WKWebViewConfiguration()
        config.userContentController = controller
        // A call must not require a tap to start playing, and video must stay
        // inside the page rather than taking over the screen.
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []

        webView = WKWebView(frame: .zero, configuration: config)
        webView.translatesAutoresizingMaskIntoConstraints = false
        webView.scrollView.bounces = false
        webView.scrollView.contentInsetAdjustmentBehavior = .never
        webView.isOpaque = false
        webView.backgroundColor = .black
        if #available(iOS 16.4, *) { webView.isInspectable = true }

        view.addSubview(webView)
        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: view.topAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            webView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
        ])

        bridge = GoBridge(webView: webView, presenter: self)
        controller.add(ScriptHandler(bridge: bridge), name: "cloudix")
        webView.uiDelegate = self
    }

    private func startCore() {
        do {
            if !MobileStarted() {
                var startErr: NSError?
                MobileStart(
                    GoBridge.dataDirectory.path,
                    GoBridge.featuresJSON,
                    bridge,
                    &startErr
                )
                if let startErr = startErr { throw startErr }
            }
            // The UI is served over loopback rather than loaded from file://:
            // a file: page is not a secure context, and getUserMedia — so every
            // call — is refused there.
            guard let www = Bundle.main.resourceURL?.appendingPathComponent("www").path else {
                throw NSError(domain: "Cloudix", code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "bundled UI is missing"])
            }
            var assetErr: NSError?
            let url = MobileStartAssets(www, &assetErr)
            if let assetErr = assetErr { throw assetErr }
            guard let target = URL(string: url) else {
                throw NSError(domain: "Cloudix", code: 2,
                              userInfo: [NSLocalizedDescriptionKey: "bad asset URL \(url)"])
            }
            webView.load(URLRequest(url: target))
        } catch {
            showFailure(error)
        }
    }

    /// Called when the app comes back from the background. Whatever the
    /// suspension broke — TCP to the relay, the overlay session — is rebuilt
    /// the same way the desktop app recovers from a network change.
    func resumeAfterSuspension() {
        guard MobileStarted() else {
            // The process was killed and relaunched into a fresh core.
            startCore()
            return
        }
        DispatchQueue.global(qos: .utility).async {
            var err: NSError?
            _ = MobileCall("RestartNetworking", "[]", &err)
        }
    }

    private func showFailure(_ error: Error) {
        let label = UILabel()
        label.text = "Cloudix не запустился:\n\(error.localizedDescription)"
        label.numberOfLines = 0
        label.textAlignment = .center
        label.textColor = .white
        label.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(label)
        NSLayoutConstraint.activate([
            label.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            label.centerYAnchor.constraint(equalTo: view.centerYAnchor),
            label.widthAnchor.constraint(equalTo: view.widthAnchor, multiplier: 0.8),
        ])
    }
}

// MARK: - camera and microphone

extension WebAppViewController: WKUIDelegate {
    /// Without this the WebView denies getUserMedia outright and a call gets no
    /// media. iOS still shows its own permission prompt the first time.
    @available(iOS 15.0, *)
    func webView(_ webView: WKWebView,
                 requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                 initiatedByFrame frame: WKFrameInfo,
                 type: WKMediaCaptureType,
                 decisionHandler: @escaping (WKPermissionDecision) -> Void) {
        decisionHandler(.grant)
    }
}

/// Keeps the strong reference WKUserContentController takes off the bridge, so
/// the bridge's own lifetime stays tied to the view controller.
private final class ScriptHandler: NSObject, WKScriptMessageHandler {
    private let bridge: GoBridge
    init(bridge: GoBridge) { self.bridge = bridge }

    func userContentController(_ controller: WKUserContentController,
                               didReceive message: WKScriptMessage) {
        guard let body = message.body as? [String: Any] else { return }
        bridge.handle(body)
    }
}
