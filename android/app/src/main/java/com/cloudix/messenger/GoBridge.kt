package com.cloudix.messenger

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.util.Log
import android.webkit.JavascriptInterface
import android.webkit.WebView
import mobile.Callback
import org.json.JSONObject
import java.io.File
import java.util.concurrent.Executors

/**
 * GoBridge is the whole contract between the Go core and the WebView.
 *
 * Go calls in through mobile.Callback (events, logs, saving a file); the page
 * calls out through the CloudixNative JavascriptInterface. Both directions
 * carry JSON, so the existing frontend runs unchanged — see
 * frontend/public/mobile-bridge.js, which installs the window.go and
 * window.runtime stand-ins this talks to.
 */
class GoBridge(private val activity: Activity, private val webView: WebView) : Callback {

    // Go can block here — a send waits on the network — so calls never run on
    // the thread that is also driving the UI.
    private val workers = Executors.newCachedThreadPool()

    companion object {
        private const val TAG = "cloudix"

        /**
         * What this build can actually do.
         *
         * Android keeps almost everything the desktop has: a foreground service
         * holds the socket open, so messages and calls arrive while the app is
         * in the background, and MediaProjection can share the screen.
         * Hosting is off because a phone behind carrier NAT can never accept an
         * inbound connection — the UI hides it rather than offering a control
         * that always fails.
         */
        fun featuresJson(): String = JSONObject(
            mapOf(
                "calls" to true,
                "screenShareSend" to true,
                "screenShareReceive" to true,
                "lanDiscovery" to true,
                "manualPeers" to true,
                "networkHosting" to false,
                "backgroundDelivery" to true,
                "openDataFolder" to false,
                "notifications" to true,
            )
        ).toString()
    }

    // MARK: page -> Go

    @JavascriptInterface
    fun post(raw: String) {
        val msg = try {
            JSONObject(raw)
        } catch (e: Exception) {
            Log.w(TAG, "bad bridge message: $raw", e)
            return
        }

        when (msg.optString("type")) {
            "call" -> {
                val id = msg.optInt("id", -1)
                val method = msg.optString("method")
                val args = msg.optString("args", "[]")
                if (id < 0 || method.isEmpty()) return
                workers.execute {
                    try {
                        resolve(id, true, mobile.Mobile.call(method, args) ?: "")
                    } catch (e: Exception) {
                        resolve(id, false, e.message ?: "call failed")
                    }
                }
            }

            "openURL" -> {
                val url = msg.optString("url")
                if (url.isEmpty()) return
                activity.runOnUiThread {
                    try {
                        activity.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                    } catch (e: Exception) {
                        Log.w(TAG, "cannot open $url", e)
                    }
                }
            }
        }
    }

    private fun resolve(id: Int, ok: Boolean, payload: String) {
        evaluate("window.__cloudix.resolve($id, $ok, ${quote(payload)})")
    }

    private fun evaluate(js: String) {
        webView.post { webView.evaluateJavascript(js, null) }
    }

    /**
     * JSON-encodes a string so it can be pasted into a JS expression. Message
     * bodies carry quotes, newlines and base64 payloads; naive escaping breaks
     * on all three.
     */
    private fun quote(s: String): String = JSONObject.quote(s)

    // MARK: Go -> page

    override fun onEvent(name: String?, payloadJSON: String?) {
        if (name == null) return
        evaluate("window.__cloudix.event(${quote(name)}, ${quote(payloadJSON ?: "")})")
    }

    override fun onLog(level: String?, message: String?) {
        when (level) {
            "error" -> Log.e(TAG, message ?: "")
            "warn" -> Log.w(TAG, message ?: "")
            else -> Log.i(TAG, message ?: "")
        }
    }

    /**
     * Writes into the app's own Downloads directory, which is visible in the
     * Files app and needs no storage permission on any supported API level.
     */
    override fun saveFile(suggestedName: String?, data: ByteArray?): String {
        if (data == null) return ""
        val name = if (suggestedName.isNullOrEmpty()) "cloudix-file" else suggestedName
        val dir = activity.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)
            ?: activity.filesDir
        dir.mkdirs()
        val file = File(dir, name)
        file.writeBytes(data)
        return file.absolutePath
    }

    fun shutdown() {
        workers.shutdownNow()
    }
}
