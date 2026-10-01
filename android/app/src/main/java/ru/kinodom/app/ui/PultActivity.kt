package ru.kinodom.app.ui

import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.window.OnBackInvokedDispatcher
import ru.kinodom.app.BuildConfig
import ru.kinodom.app.R
import ru.kinodom.app.core.Action
import ru.kinodom.app.core.BackDecision
import ru.kinodom.app.core.Links
import ru.kinodom.app.core.Route
import ru.kinodom.app.core.WebViewVersion

// PultActivity — пульт Kinodom во весь экран (спека этапа 13, раздел 3.3): WebView со страницей сервера, «Назад» —
// по истории пульта, на первом экране — свернуть; фильмы и «На трекере» — снаружи (VLC, браузер); сервер не
// отвечает — «Kinodom не отвечает» с «Повторить» и «Другой адрес».
class PultActivity : Activity() {
    private lateinit var base: String
    private lateinit var web: WebView
    private var overlay: View? = null

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        base = intent.getStringExtra(EXTRA_BASE) ?: run {
            startActivity(Intent(this, StartActivity::class.java))
            finish()
            return
        }
        val root = FrameLayout(this)
        if (!WebViewVersion.ok(WebSettings.getDefaultUserAgent(this))) {
            val c = Screens.column(this)
            c.addView(Screens.title(this, getString(R.string.webview_old)))
            setContentView(c)
            return
        }
        web = WebView(this).apply {
            setBackgroundColor(getColor(R.color.bg))
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            addJavascriptInterface(Bridge(), "KinodomApp")
            webViewClient = Client()
            isFocusable = true
            isFocusableInTouchMode = true
        }
        root.addView(web, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        setContentView(root)
        if (savedInstanceState?.let { web.restoreState(it) } == null) web.loadUrl(base)
        web.requestFocus()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { back() }
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        if (::web.isInitialized) web.saveState(outState)
    }

    @Deprecated("До Android 13 «Назад» приходит сюда")
    override fun onBackPressed() = back()

    private fun back() {
        val canGoBack = ::web.isInitialized && web.canGoBack()
        when (BackDecision.onBack(canGoBack, overlay != null)) {
            Action.CloseOverlay -> moveTaskToBack(true) // «Kinodom не отвечает» поверх мёртвого пульта — назад некуда
            Action.GoBack -> web.goBack()
            Action.Minimize -> moveTaskToBack(true)
        }
    }

    override fun onPause() {
        if (::web.isInitialized) web.onPause()
        super.onPause()
    }

    override fun onResume() {
        super.onResume()
        if (::web.isInitialized) web.onResume()
    }

    override fun onDestroy() {
        if (::web.isInitialized) web.destroy()
        super.onDestroy()
    }

    // unreachable — «Kinodom не отвечает» поверх пульта.
    private fun unreachable() {
        if (overlay != null) return
        val c = Screens.column(this)
        c.isClickable = true
        c.addView(Screens.title(this, getString(R.string.no_answer)))
        c.addView(Screens.note(this, base.removePrefix("http://").removeSuffix("/")))
        val retry = Screens.button(this, getString(R.string.retry)) {
            (web.parent as ViewGroup).removeView(overlay)
            overlay = null
            web.loadUrl(base)
            web.requestFocus()
        }
        c.addView(retry)
        c.addView(Screens.button(this, getString(R.string.other_address)) {
            startActivity(Intent(this, StartActivity::class.java).putExtra(StartActivity.EXTRA_ASK, true))
            finish()
        })
        (web.parent as ViewGroup).addView(c)
        overlay = c
        retry.requestFocus()
    }

    // openOutside — плейлист и поток — в плеер (VLC), остальное — в браузер.
    private fun openOutside(url: String) {
        val uri = Uri.parse(url)
        val media = Links.route(url, base) == Route.Outside && uri.scheme == "http" && (uri.path ?: "").let {
            it.startsWith("/m3u/") || it.startsWith("/stream/") || it.endsWith(".m3u8")
        }
        val i = Intent(Intent.ACTION_VIEW).apply { if (media) setDataAndType(uri, "video/*") else data = uri }
        try {
            startActivity(i)
        } catch (e: ActivityNotFoundException) {
            // нет приложения для ссылки — ничего не открыть
        }
    }

    // openIntent — intent:// пульта (VLC с местом); VLC нет — запасной адрес (.m3u8).
    private fun openIntent(url: String) {
        val i = try {
            Intent.parseUri(url, Intent.URI_INTENT_SCHEME).apply { addCategory(Intent.CATEGORY_BROWSABLE); component = null; selector = null }
        } catch (e: Exception) {
            return
        }
        try {
            startActivity(i)
        } catch (e: ActivityNotFoundException) {
            i.getStringExtra("browser_fallback_url")?.let { openOutside(it) }
        }
    }

    private inner class Client : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val url = request.url.toString()
            return when (Links.route(url, base)) {
                Route.Inside -> false
                Route.Outside -> {
                    openOutside(url)
                    true
                }
                Route.Intent -> {
                    openIntent(url)
                    true
                }
            }
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (Links.fatalError(request.isForMainFrame, request.url.toString(), base)) unreachable()
        }
    }

    // Bridge — объект KinodomApp для пульта (спека этапа 13, раздел 5.2): в 13a — только версия.
    private inner class Bridge {
        @JavascriptInterface
        fun version(): String = BuildConfig.VERSION_NAME
    }

    companion object {
        const val EXTRA_BASE = "base"
    }
}
