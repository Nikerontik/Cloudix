package com.cloudix.messenger

import android.content.Context
import java.io.File

/**
 * The UI ships inside the APK, but the Go asset server serves a real directory,
 * so the bundle is unpacked once per version into the app's private storage.
 * It is a few hundred kilobytes; re-extracting on every launch would be waste,
 * so a stamp file records which version is already on disk.
 */
object Assets {
    fun extract(context: Context): File {
        val root = File(context.filesDir, "www")
        val stamp = File(root, ".version")
        val version = try {
            context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "dev"
        } catch (e: Exception) {
            "dev"
        }

        if (stamp.isFile && stamp.readText() == version) return root

        root.deleteRecursively()
        root.mkdirs()
        copyDir(context, "www", root)
        stamp.writeText(version)
        return root
    }

    private fun copyDir(context: Context, assetPath: String, target: File) {
        val entries = context.assets.list(assetPath) ?: return
        if (entries.isEmpty()) {
            // A leaf: list() returns empty for files as well as empty folders.
            context.assets.open(assetPath).use { input ->
                target.parentFile?.mkdirs()
                target.outputStream().use { input.copyTo(it) }
            }
            return
        }
        target.mkdirs()
        for (entry in entries) {
            copyDir(context, "$assetPath/$entry", File(target, entry))
        }
    }
}
