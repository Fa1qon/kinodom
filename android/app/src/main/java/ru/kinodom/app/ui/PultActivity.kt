package ru.kinodom.app.ui

import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
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
import java.io.File
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.BuildConfig
import ru.kinodom.app.R
import ru.kinodom.app.core.Action
import ru.kinodom.app.core.BackDecision
import ru.kinodom.app.core.Links
import ru.kinodom.app.core.Route
import ru.kinodom.app.core.ServerApp
import ru.kinodom.app.core.UpdateDecision
import ru.kinodom.app.core.WebViewVersion
import ru.kinodom.app.net.Updater

// PultActivity — пульт Kinodom во весь экран (спека этапа 13, раздел 3.3): WebView со страницей сервера, «Назад» —
// по истории пульта, на первом экране — свернуть; фильмы и «На трекере» — снаружи (VLC, браузер); сервер не
// отвечает — «Kinodom не отвечает» с «Повторить» и «Другой адрес»; новая версия на сервере — «Обновить» (раздел 6).
class PultActivity : Activity() {
    private lateinit var base: String
    private lateinit var web: WebView
    private lateinit var root: FrameLayout
    private lateinit var updater: Updater
    private val scope = MainScope()
    private val handler = Handler(Looper.getMainLooper())
    private val daily = object : Runnable {
        override fun run() {
            checkUpdate()
            handler.postDelayed(this, DAY_MS)
        }
    }

    private var overlay: View? = null
    private var overlayClosable = false // окно обновления — «Назад» закрывает; «Kinodom не отвечает» — нет
    private var pendingApk: File? = null // скачанное обновление ждёт разрешения «устанавливать из Kinodom»
    private var pendingApp: ServerApp? = null

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        base = intent.getStringExtra(EXTRA_BASE) ?: run {
            startActivity(Intent(this, StartActivity::class.java))
            finish()
            return
        }
        if (!WebViewVersion.ok(WebSettings.getDefaultUserAgent(this))) {
            val c = Screens.column(this)
            c.addView(Screens.title(this, getString(R.string.webview_old)))
            setContentView(c)
            return
        }
        updater = Updater(this, base)
        root = FrameLayout(this)
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
        checkUpdate()
        handler.postDelayed(daily, DAY_MS)
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
            Action.CloseOverlay -> if (overlayClosable) {
                postponed = true
                closeOverlay()
            } else {
                moveTaskToBack(true) // «Kinodom не отвечает» поверх мёртвого пульта — назад некуда
            }
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
        // Вернулись из настроек «устанавливать из Kinodom»: разрешили — поставить скачанное; нет — снова окно
        // «Есть новая версия», «Обновить» — сразу к разрешению (файл уже скачан).
        val apk = pendingApk
        val app = pendingApp
        if (apk != null && app != null && ::updater.isInitialized) {
            pendingApk = null
            pendingApp = null
            if (updater.canInstall()) install(apk) else offerUpdate(app, apk)
        }
    }

    override fun onDestroy() {
        handler.removeCallbacks(daily)
        scope.cancel()
        if (::web.isInitialized) web.destroy()
        super.onDestroy()
    }

    private fun showOverlay(v: View, closable: Boolean, focus: View?) {
        overlay?.let { root.removeView(it) }
        v.isClickable = true
        root.addView(v)
        overlay = v
        overlayClosable = closable
        focus?.requestFocus()
    }

    private fun closeOverlay() {
        overlay?.let { root.removeView(it) }
        overlay = null
        web.requestFocus()
    }

    // unreachable — «Kinodom не отвечает» поверх пульта.
    private fun unreachable() {
        if (overlay != null && !overlayClosable) return
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.no_answer)))
        c.addView(Screens.note(this, base.removePrefix("http://").removeSuffix("/")))
        val retry = Screens.button(this, getString(R.string.retry)) {
            closeOverlay()
            web.loadUrl(base)
        }
        c.addView(retry)
        c.addView(Screens.button(this, getString(R.string.other_address)) {
            startActivity(Intent(this, StartActivity::class.java).putExtra(StartActivity.EXTRA_ASK, true))
            finish()
        })
        showOverlay(c, closable = false, focus = retry)
    }

    // checkUpdate — на сервере новее и «Позже» не нажимали — окно «Есть новая версия Kinodom».
    private fun checkUpdate() {
        scope.launch {
            if (overlay != null || postponed) return@launch
            val app = updater.check()
            if (overlay == null && UpdateDecision.offer(BuildConfig.VERSION_CODE, app, postponed)) offerUpdate(app!!)
        }
    }

    // offerUpdate — «Есть новая версия Kinodom»; ready — файл уже скачан (не дали разрешение на установку).
    private fun offerUpdate(app: ServerApp, ready: File? = null) {
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.update_title)))
        c.addView(Screens.note(this, app.version))
        val now = Screens.button(this, getString(R.string.update_now)) {
            if (ready != null) {
                closeOverlay()
                installOrAsk(app, ready)
            } else {
                startUpdate(app)
            }
        }
        c.addView(now)
        c.addView(Screens.button(this, getString(R.string.update_later)) {
            postponed = true
            closeOverlay()
        })
        showOverlay(c, closable = true, focus = now)
    }

    private fun startUpdate(app: ServerApp) {
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.update_title)))
        val note = Screens.note(this, getString(R.string.update_loading, 0))
        c.addView(note)
        showOverlay(c, closable = false, focus = null)
        scope.launch {
            val f = updater.download(app) { pct -> note.text = getString(R.string.update_loading, pct) }
            if (f == null) {
                val e = Screens.column(this@PultActivity)
                e.addView(Screens.title(this@PultActivity, getString(R.string.update_failed)))
                val close = Screens.button(this@PultActivity, getString(R.string.close)) {
                    postponed = true
                    closeOverlay()
                }
                e.addView(close)
                showOverlay(e, closable = true, focus = close)
                return@launch
            }
            closeOverlay()
            installOrAsk(app, f)
        }
    }

    // installOrAsk — установщик Android; Android 8+ без разрешения «устанавливать из Kinodom» — сначала настройки.
    private fun installOrAsk(app: ServerApp, f: File) {
        if (updater.canInstall()) {
            install(f)
            return
        }
        pendingApk = f
        pendingApp = app
        try {
            startActivity(updater.askInstallPermission())
        } catch (e: ActivityNotFoundException) {
            pendingApk = null
            pendingApp = null
        }
    }

    private fun install(f: File) {
        try {
            startActivity(updater.installIntent(f))
        } catch (e: ActivityNotFoundException) {
            // установщика нет (не бывает на Android с Play и без) — оставить как есть
        }
    }

    // openOutside — плейлист и поток — в плеер (VLC), остальное — в браузер.
    private fun openOutside(url: String) {
        val uri = Uri.parse(url)
        val media = uri.scheme == "http" && (uri.path ?: "").let {
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
            Intent.parseUri(url, Intent.URI_INTENT_SCHEME).apply {
                addCategory(Intent.CATEGORY_BROWSABLE)
                component = null
                selector = null
            }
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
        private const val DAY_MS = 24L * 60 * 60 * 1000

        // postponed — «Позже»: до следующего запуска процесса приложения (спека этапа 13, раздел 6).
        private var postponed = false
    }
}
