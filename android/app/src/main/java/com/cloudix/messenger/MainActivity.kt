package com.cloudix.messenger

import android.Manifest
import android.annotation.SuppressLint
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.webkit.PermissionRequest
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.webkit.WebSettings
import androidx.activity.ComponentActivity
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import java.io.File

class MainActivity : ComponentActivity() {

    private lateinit var webView: WebView
    private lateinit var bridge: GoBridge

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            settings.cacheMode = WebSettings.LOAD_NO_CACHE
        }
        setContentView(webView)
        WebView.setWebContentsDebuggingEnabled(true)

        bridge = GoBridge(this, webView)
        webView.addJavascriptInterface(bridge, "CloudixNative")

        // The page reads this before the app bundle runs, so the first render
        // already knows the platform and what to hide.
        webView.webChromeClient = object : WebChromeClient() {
            override fun onPermissionRequest(request: PermissionRequest) {
                // The OS-level prompt has already been answered by then; this
                // only grants the WebView itself, which otherwise denies
                // getUserMedia and leaves a call with no media.
                runOnUiThread { request.grant(request.resources) }
            }
        }

        requestMediaPermissions()
        startCore()
    }

    private fun requestMediaPermissions() {
        val wanted = mutableListOf(Manifest.permission.RECORD_AUDIO, Manifest.permission.CAMERA)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            wanted += Manifest.permission.POST_NOTIFICATIONS
        }
        val missing = wanted.filter {
            ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED
        }
        if (missing.isNotEmpty()) {
            ActivityCompat.requestPermissions(this, missing.toTypedArray(), 1)
        }
    }

    private fun startCore() {
        try {
            if (!mobile.Mobile.started()) {
                val dataDir = File(filesDir, "cloudix").apply { mkdirs() }
                mobile.Mobile.start(dataDir.absolutePath, GoBridge.featuresJson(), bridge)
            }
            // Served over loopback rather than from file:// or a content://
            // asset URL: only a trustworthy origin gets getUserMedia, and
            // without it there are no calls.
            val www = Assets.extract(this)
            val url = mobile.Mobile.startAssets(www.absolutePath)
            webView.loadUrl(url)
            // Only once the core is actually up: a foreground notification for
            // a service that failed to start would be a lie.
            ConnectionService.start(this)
        } catch (e: Exception) {
            webView.loadData(
                "<body style='background:#111;color:#eee;font:16px -apple-system,sans-serif;padding:24px'>" +
                    "<h3>Cloudix не запустился</h3><pre>${e.message}</pre></body>",
                "text/html; charset=utf-8",
                null
            )
        }
    }

    override fun onResume() {
        super.onResume()
        // Whatever a suspension or a network change broke is rebuilt the same
        // way the desktop app recovers.
        if (mobile.Mobile.started()) {
            Thread { runCatching { mobile.Mobile.call("RestartNetworking", "[]") } }.start()
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        if (isFinishing) {
            ConnectionService.stop(this)
            bridge.shutdown()
            mobile.Mobile.stopAssets()
            mobile.Mobile.stop()
        }
    }
}
