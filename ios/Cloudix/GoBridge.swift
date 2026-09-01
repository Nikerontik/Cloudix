import Foundation
import Cloudixmobile
import WebKit

/// GoBridge is the whole contract between the Go core and the WebView.
///
/// Go calls in through MobileCallback (events, logs, saving a file); the page
/// calls out through a WKScriptMessageHandler. Both directions carry JSON, so
/// the existing frontend runs unchanged — see frontend/public/mobile-bridge.js,
/// which installs the window.go / window.runtime stand-ins this talks to.
final class GoBridge: NSObject {
    private weak var webView: WKWebView?
    /// The share sheet needs a view controller to present from.
    private weak var presenter: UIViewController?

    init(webView: WKWebView, presenter: UIViewController) {
        self.webView = webView
        self.presenter = presenter
    }

    /// Where the SQLite database lives. os.UserConfigDir() means nothing inside
    /// the sandbox, so the Go side is told explicitly.
    static var dataDirectory: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let dir = base.appendingPathComponent("Cloudix", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    /// What this build can actually do.
    ///
    /// Everything switched off here is off because iOS or the signing account
    /// will not allow it, not because it is unfinished:
    ///   • screenShareSend    — ReplayKit needs a broadcast extension and an App
    ///                          Group, and App Groups need a paid account.
    ///   • lanDiscovery       — multicast needs an Apple-granted entitlement.
    ///   • networkHosting     — iOS kills a listening socket on suspend.
    ///   • backgroundDelivery — no push without a paid account and a sender.
    ///   • notifications      — same reason.
    /// The UI hides each of these rather than showing a control that fails.
    static let features: [String: Bool] = [
        "calls": true,
        "screenShareSend": false,
        "screenShareReceive": true,
        "lanDiscovery": false,
        "manualPeers": true,
        "networkHosting": false,
        "backgroundDelivery": false,
        "openDataFolder": false,
        "notifications": false,
    ]

    static var featuresJSON: String {
        guard let data = try? JSONSerialization.data(withJSONObject: features),
              let json = String(data: data, encoding: .utf8) else { return "{}" }
        return json
    }

    // MARK: page -> Go

    /// Handles one {type:"call"|"openURL"} message from the page.
    func handle(_ body: [String: Any]) {
        switch body["type"] as? String {
        case "call":
            guard let id = body["id"] as? Int,
                  let method = body["method"] as? String else { return }
            let args = body["args"] as? String ?? "[]"
            // Go can block here — a send waits on the network — so never run it
            // on the thread that is also driving the UI.
            DispatchQueue.global(qos: .userInitiated).async { [weak self] in
                var err: NSError?
                let result = MobileCall(method, args, &err)
                if let err = err {
                    self?.resolve(id: id, ok: false, payload: err.localizedDescription)
                } else {
                    self?.resolve(id: id, ok: true, payload: result)
                }
            }

        case "openURL":
            guard let raw = body["url"] as? String, let url = URL(string: raw) else { return }
            DispatchQueue.main.async { UIApplication.shared.open(url) }

        default:
            break
        }
    }

    private func resolve(id: Int, ok: Bool, payload: String) {
        let js = "window.__cloudix.resolve(\(id), \(ok), \(Self.quote(payload)))"
        evaluate(js)
    }

    private func evaluate(_ js: String) {
        DispatchQueue.main.async { [weak self] in
            self?.webView?.evaluateJavaScript(js, completionHandler: nil)
        }
    }

    /// JSON-encodes a string so it can be pasted into a JS expression. Message
    /// bodies contain quotes, newlines and base64 payloads; naive escaping
    /// breaks on all three.
    static func quote(_ s: String) -> String {
        guard let data = try? JSONEncoder().encode(s),
              let out = String(data: data, encoding: .utf8) else { return "\"\"" }
        return out
    }
}

// MARK: - Go -> page

extension GoBridge: MobileCallbackProtocol {
    func onEvent(_ name: String?, payloadJSON: String?) {
        guard let name = name else { return }
        evaluate("window.__cloudix.event(\(Self.quote(name)), \(Self.quote(payloadJSON ?? "")))")
    }

    func onLog(_ level: String?, message: String?) {
        NSLog("[cloudix/%@] %@", level ?? "info", message ?? "")
    }

    /// Writes the bytes into Documents, which UIFileSharingEnabled exposes in
    /// the Files app, and then offers a share sheet. Returning the path keeps
    /// the Go side's contract; the sheet is a convenience on top.
    func saveFile(_ suggestedName: String?, data: Data?, error: NSErrorPointer) -> String {
        guard let data = data else { return "" }
        let name = (suggestedName?.isEmpty == false ? suggestedName! : "cloudix-file")
        let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let url = docs.appendingPathComponent(name)
        do {
            try data.write(to: url, options: .atomic)
        } catch let failure as NSError {
            error?.pointee = failure
            return ""
        }

        DispatchQueue.main.async { [weak self] in
            guard let presenter = self?.presenter else { return }
            let sheet = UIActivityViewController(activityItems: [url], applicationActivities: nil)
            sheet.popoverPresentationController?.sourceView = presenter.view
            presenter.present(sheet, animated: true)
        }
        return url.path
    }
}
