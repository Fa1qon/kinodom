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
import android.os.SystemClock
import android.speech.RecognizerIntent
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.TextView
import android.widget.Toast
import android.window.OnBackInvokedDispatcher
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import java.io.File
import kotlinx.coroutines.Job
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.BuildConfig
import ru.kinodom.app.R
import ru.kinodom.app.core.Action
import ru.kinodom.app.core.BackDecision
import ru.kinodom.app.core.Foreground
import ru.kinodom.app.core.Lineup
import ru.kinodom.app.core.LocalServer
import ru.kinodom.app.core.Links
import ru.kinodom.app.core.MovieInfo
import ru.kinodom.app.core.MovieUrls
import ru.kinodom.app.core.PlayerMode
import ru.kinodom.app.core.Recovery
import ru.kinodom.app.core.Route
import ru.kinodom.app.core.ServerApp
import ru.kinodom.app.core.UpdateDecision
import ru.kinodom.app.core.Voice
import ru.kinodom.app.core.WebViewVersion
import ru.kinodom.app.net.Api
import ru.kinodom.app.net.Prefs
import ru.kinodom.app.net.ServerFinder
import ru.kinodom.app.net.Status
import ru.kinodom.app.net.Updater

// PultActivity — пульт Kinodom во весь экран (спека этапа 13, раздел 3.3): WebView со страницей сервера, «Назад» —
// по истории пульта, на первом экране — свернуть; фильмы и «На трекере» — снаружи (VLC, браузер); сервер не
// отвечает — «Kinodom не отвечает» с «Повторить» и «Другой адрес»; новая версия на сервере — «Обновить» (раздел 6).
class PultActivity : Activity() {
    private lateinit var base: String
    private lateinit var web: WebView
    private lateinit var root: FrameLayout
    private lateinit var updater: Updater
    private var sounds: Sounds? = null
    private val scope = MainScope()
    private val handler = Handler(Looper.getMainLooper())

    // tick — пока пульт на экране: сразу и раз в минуту — отвечает ли Kinodom (приложение на ТВ месяцами живёт
    // свёрнутым, а ПК за ночь мог перезагрузиться и сменить адрес — финальное ревью 13a).
    private val tick = object : Runnable {
        override fun run() {
            watch()
            handler.postDelayed(this, WATCH_MS)
        }
    }
    private var checking: Job? = null
    private var updateCheckedAt: Long? = null

    private var overlay: View? = null
    private var overlayClosable = false // окно обновления — «Назад» закрывает; «Kinodom не отвечает» — нет
    private var lost: TextView? = null // строка под «Kinodom не отвечает»: адрес или «Ищу Kinodom…»
    private var forget = false // пульт открыт с нового адреса — история прежнего «Назад» не нужна
    private var pendingApk: File? = null // скачанное обновление ждёт разрешения «устанавливать из Kinodom»
    private var pendingApp: ServerApp? = null
    private var filePick: ValueCallback<Array<Uri>>? = null // <input type=file> ждёт выбранный файл
    private var custom: View? = null // видео во весь экран (кнопка «полный экран» у <video>)
    private var customDone: WebChromeClient.CustomViewCallback? = null
    private var searchDown = false // «Поиск» пульта нажата в этом окне
    private var picking = false // открыт выбор файла: возврат из него — не вход в приложение (ревью 15Г)

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        sounds = Sounds(this)
        base = savedInstanceState?.getString(EXTRA_BASE) ?: intent.getStringExtra(EXTRA_BASE) ?: run {
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
        Screens.fitBars(root)
        web = WebView(this).apply {
            setBackgroundColor(getColor(R.color.bg))
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            addJavascriptInterface(Bridge(), "KinodomApp")
            webViewClient = Client()
            webChromeClient = Chrome()
            // Кнопка «.m3u8» — ссылка с download: WebView делает из неё загрузку, а не переход — в плеер, как ссылку.
            setDownloadListener { url, _, _, _, _ -> openOutside(url) }
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
        if (::web.isInitialized) {
            web.saveState(outState)
            outState.putString(EXTRA_BASE, base)
        }
    }

    @Deprecated("До Android 13 «Назад» приходит сюда")
    override fun onBackPressed() = back()

    // back — открыто окно пульта («Скачать «…»?», выбор папки) — закрыть его: WebView не передаёт «Назад»
    // странице, а Escape пульт понимает (финальное ревью 13a); иначе — по BackDecision.
    // «Назад» пульта ТВ странице не приходит — звук «Назад» (план 16В) играет здесь, когда шаг назад случился.
    private fun back() {
        if (custom != null) {
            sounds?.play("back")
            leaveFullscreen()
            return
        }
        if (!::web.isInitialized || overlay != null) {
            backDecision()
            return
        }
        web.evaluateJavascript(CLOSE_DIALOG) { closed -> if (closed == "true") sounds?.play("back") else backDecision() }
    }

    private fun backDecision() {
        val canGoBack = ::web.isInitialized && web.canGoBack()
        when (BackDecision.onBack(canGoBack, overlay != null)) {
            Action.CloseOverlay -> if (overlayClosable) {
                sounds?.play("back")
                postponed = true
                closeOverlay()
            } else {
                moveTaskToBack(true) // «Kinodom не отвечает» поверх мёртвого пульта — назад некуда
            }
            Action.GoBack -> {
                sounds?.play("back")
                web.goBack()
            }
            Action.Minimize -> moveTaskToBack(true)
        }
    }

    // onStart — вход в приложение (запуск или возврат из фона, не возврат из плеера): спросить сервер про новую
    // версию сразу, не дожидаясь суток; «Позже» — до следующего входа (заказчик 2026-10-01).
    override fun onStart() {
        super.onStart()
        if (Foreground.app.start() && !picking) {
            postponed = false
            updateCheckedAt = null
        }
    }

    override fun onStop() {
        Foreground.app.stop()
        super.onStop()
    }

    override fun onPause() {
        handler.removeCallbacks(tick)
        if (::web.isInitialized) web.onPause()
        super.onPause()
    }

    override fun onResume() {
        super.onResume()
        if (!::web.isInitialized) return
        web.onResume()
        // Вернулись из настроек «устанавливать из Kinodom»: разрешили — поставить скачанное; нет — снова окно
        // «Есть новая версия», «Обновить» — сразу к разрешению (файл уже скачан).
        val apk = pendingApk
        val app = pendingApp
        if (apk != null && app != null) {
            pendingApk = null
            pendingApp = null
            if (updater.canInstall()) install(apk) else offerUpdate(app, apk)
        }
        handler.removeCallbacks(tick)
        handler.post(tick)
    }

    override fun onDestroy() {
        handler.removeCallbacks(tick)
        sounds?.release()
        sounds = null
        scope.cancel()
        if (::web.isInitialized) web.destroy()
        super.onDestroy()
    }

    // Chrome — выбор файла (<input type=file>: логотип своего канала) и видео во весь экран; без него WebView молча
    // ничего не делает (ревью 14Д, п. 12).
    private inner class Chrome : WebChromeClient() {
        override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
            filePick?.onReceiveValue(null)
            filePick = callback
            // Тип — один MIME из accept, иначе любой: «.m3u,…» createIntent передал бы как есть (ревью 15Г).
            val mime = params.acceptTypes.singleOrNull()?.takeIf { '/' in it } ?: "*/*"
            val pick = Intent(Intent.ACTION_GET_CONTENT).addCategory(Intent.CATEGORY_OPENABLE).setType(mime)
            return try {
                picking = true
                @Suppress("DEPRECATION")
                startActivityForResult(pick, PICK_FILE)
                true
            } catch (e: ActivityNotFoundException) {
                picking = false
                filePick = null
                false
            }
        }

        override fun onShowCustomView(view: View, callback: CustomViewCallback) {
            if (custom != null) {
                callback.onCustomViewHidden()
                return
            }
            custom = view
            customDone = callback
            root.addView(view, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
            web.visibility = View.GONE
            // Во весь экран — без системных панелей, как плеер каналов (ревью 15Г).
            WindowCompat.getInsetsController(window, window.decorView).apply {
                hide(WindowInsetsCompat.Type.systemBars())
                systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            }
        }

        override fun onHideCustomView() = leaveFullscreen()
    }

    // leaveFullscreen — из полного экрана обратно в пульт («Назад» или кнопка видео).
    private fun leaveFullscreen() {
        val v = custom ?: return
        custom = null
        root.removeView(v)
        web.visibility = View.VISIBLE
        WindowCompat.getInsetsController(window, window.decorView).show(WindowInsetsCompat.Type.systemBars())
        val done = customDone
        customDone = null
        done?.onCustomViewHidden()
        web.requestFocus()
    }

    // voiceIntent — системное распознавание речи (план 17В): русский, свободная речь. Разрешения на микрофон
    // приложению не нужно — слушает системная служба.
    private fun voiceIntent() = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE, "ru-RU")
        .putExtra(RecognizerIntent.EXTRA_PROMPT, getString(R.string.voice_prompt))

    private fun canVoice() = voiceIntent().resolveActivity(packageManager) != null

    // startVoice — окно распознавания; возврат из него — не вход в приложение (picking, как у выбора файла).
    private fun startVoice() {
        // Поверх пульта своё окно («Kinodom не отвечает», обновление) — сказанное некуда показать; окно распознавания
        // уже открыто — второе не открываем (ревью 17В).
        if (picking || overlay != null || !::web.isInitialized || web.url?.startsWith(base) != true) return
        try {
            picking = true
            @Suppress("DEPRECATION")
            startActivityForResult(voiceIntent(), VOICE)
        } catch (e: ActivityNotFoundException) {
            picking = false
        }
    }

    // onSearchRequested — кнопка «Поиск» пульта (у части пультов): голосовой поиск, если он есть. Ассистента на пульте
    // Google TV забирает система.
    override fun onSearchRequested(): Boolean {
        if (!canVoice()) return super.onSearchRequested()
        startVoice()
        return true
    }

    // dispatchKeyEvent — «Поиск» пульта доходил до WebView и терялся там (эмулятор ТВ): перехват до страницы.
    // Окно — по отпусканию той же нажатой здесь кнопки (не отменённому системой).
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (event.keyCode == KeyEvent.KEYCODE_SEARCH && canVoice()) {
            when (event.action) {
                KeyEvent.ACTION_DOWN -> if (event.repeatCount == 0) searchDown = true
                KeyEvent.ACTION_UP -> {
                    if (searchDown && !event.isCanceled) startVoice()
                    searchDown = false
                }
            }
            return true
        }
        return super.dispatchKeyEvent(event)
    }

    @Deprecated("startActivityForResult — у android.app.Activity другого пути нет")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode == VOICE) {
            picking = false
            val q = if (resultCode == RESULT_OK) Voice.query(data?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)) else null
            // Окна пульта (подтверждения, выбор папки) при смене адреса не закрываются — сначала они (ревью 17В).
            if (q != null && ::web.isInitialized && web.url?.startsWith(base) == true) web.evaluateJavascript(CLOSE_DIALOGS + Voice.hashJs(q), null)
            return
        }
        if (requestCode == PICK_FILE) {
            picking = false
            filePick?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(resultCode, data))
            filePick = null
            return
        }
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
    }

    private fun showOverlay(v: View, closable: Boolean, focus: View?) {
        overlay?.let { root.removeView(it) }
        v.isClickable = true
        root.addView(v)
        overlay = v
        overlayClosable = closable
        lost = null
        focus?.requestFocus()
    }

    private fun closeOverlay() {
        overlay?.let { root.removeView(it) }
        overlay = null
        lost = null
        web.requestFocus()
    }

    // unreachable — «Kinodom не отвечает» поверх пульта.
    private fun unreachable() {
        if (overlay != null && !overlayClosable) return
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.no_answer)))
        val note = Screens.note(this, address())
        c.addView(note)
        val retry = Screens.button(this, getString(R.string.retry)) { watch() }
        c.addView(retry)
        c.addView(Screens.button(this, getString(R.string.other_address)) {
            startActivity(Intent(this, StartActivity::class.java).putExtra(StartActivity.EXTRA_SEARCH, true))
            finish()
        })
        showOverlay(c, closable = false, focus = retry)
        lost = note
    }

    private fun address() = base.removePrefix("http://").removeSuffix("/")

    // watch — отвечает ли Kinodom. Да — убрать «Kinodom не отвечает» (если было) и раз в сутки спросить про новую
    // версию; нет — «Kinodom не отвечает» и поиск в сети (спека 3.2: не ответил — поиск снова): нашёлся тот же
    // или ровно один другой и отвечает — пульт с него, иначе окно остаётся («Повторить» — ещё раз).
    private fun watch() {
        if (checking != null) return
        checking = scope.launch {
            try {
                if (Status.ok(base)) {
                    if (lost != null) reopen(base) else if (updateDue()) checkUpdate()
                    return@launch
                }
                unreachable()
                lost?.text = getString(R.string.searching)
                val target = when (val r = Recovery.after(ServerFinder(this@PultActivity).find(), base)) {
                    Recovery.Reload -> base
                    is Recovery.SwitchTo -> r.base
                    Recovery.Stay -> null
                }
                if (target != null && Status.ok(target)) reopen(target) else lost?.text = address()
            } finally {
                checking = null
            }
        }
    }

    // reopen — пульт заново с target: сервер вернулся или ПК сменил адрес (новый — запомнить).
    private fun reopen(target: String) {
        if (target != base) {
            base = target
            Prefs(this).base = target
            updater = Updater(this, target)
            forget = true
        }
        closeOverlay()
        web.loadUrl(target)
    }

    private fun updateDue(): Boolean {
        val last = updateCheckedAt
        return last == null || SystemClock.elapsedRealtime() - last >= DAY_MS
    }

    // checkUpdate — на сервере новее и «Позже» не нажимали — окно «Есть новая версия Kinodom».
    private fun checkUpdate() {
        if (BuildConfig.FLAVOR != "full") return // «Клиент» обновляется не с сервера: там full-APK другого пакета
        updateCheckedAt = SystemClock.elapsedRealtime()
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

    // updateNow — «Обновить» из пульта: на сервере новее — загрузка и установщик; загрузка уже идёт — не вторую.
    private fun updateNow() {
        if (overlay != null && !overlayClosable) return
        scope.launch {
            val app = updater.check() ?: return@launch
            if (app.versionCode > BuildConfig.VERSION_CODE) startUpdate(app)
        }
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

    // installOrAsk — установщик Android; Android 8+ без разрешения «устанавливать из Kinodom» — сначала настройки,
    // а нет в прошивке таких настроек — сразу установщик (он сам попросит разрешить источник).
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
            install(f)
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

        override fun onPageFinished(view: WebView, url: String) {
            if (forget && url.startsWith(base)) {
                forget = false
                view.clearHistory()
            }
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (Links.fatalError(request.isForMainFrame, request.url.toString(), base)) {
                unreachable()
                watch()
            }
        }
    }

    // Bridge — объект KinodomApp для пульта (спека этапа 13, раздел 5.2): версия, плеер каналов и его настройка.
    // Методы зовутся не с главного потока.
    private inner class Bridge {
        @JavascriptInterface
        fun version(): String = BuildConfig.VERSION_NAME

        // playChannels — «Смотреть» у канала: список, из которого открыли, и стартовый; не разобрался — false
        // (пульт откроет канал по-старому). Плеер открывается, только если в WebView — страница своего сервера.
        @JavascriptInterface
        fun playChannels(json: String): Boolean {
            if (Lineup.parse(json) == null) return false
            handler.post {
                if (::web.isInitialized && web.url?.startsWith(base) == true) {
                    startActivity(Intent(this@PultActivity, PlayerActivity::class.java)
                        .putExtra(PlayerActivity.EXTRA_BASE, base).putExtra(PlayerActivity.EXTRA_LINEUP, json))
                }
            }
            return true
        }

        @JavascriptInterface
        fun player(): String = Prefs(this@PultActivity).channelPlaybackMode.id

        @JavascriptInterface
        fun setPlayer(mode: String) {
            PlayerMode.of(mode)?.let { Prefs(this@PultActivity).channelPlaybackMode = it }
        }

        @JavascriptInterface
        fun playbackMode(): String = Prefs(this@PultActivity).playbackMode.id

        @JavascriptInterface
        fun setPlaybackMode(mode: String) {
            PlayerMode.of(mode)?.let { Prefs(this@PultActivity).playbackMode = it }
        }

        @JavascriptInterface
        fun moviePlaybackMode(): String = Prefs(this@PultActivity).moviePlaybackMode.id

        @JavascriptInterface
        fun setMoviePlaybackMode(mode: String) {
            PlayerMode.of(mode)?.let { Prefs(this@PultActivity).moviePlaybackMode = it }
        }

        @JavascriptInterface
        fun channelPlaybackMode(): String = Prefs(this@PultActivity).channelPlaybackMode.id

        @JavascriptInterface
        fun setChannelPlaybackMode(mode: String) {
            PlayerMode.of(mode)?.let { Prefs(this@PultActivity).channelPlaybackMode = it }
        }

        // localServer — сервер на этом устройстве (полный порт, план 2026-10-06): есть ли он в сборке
        // и включён ли. Включение переставляет пульт на 127.0.0.1 после перезапуска входа.
        @JavascriptInterface
        fun localServer(): String {
            val p = Prefs(this@PultActivity)
            return if (LocalServer.binary(this@PultActivity) == null) "none" else if (p.localServer) "on" else "off"
        }

        @JavascriptInterface
        fun setLocalServer(on: Boolean) {
            Prefs(this@PultActivity).localServer = on
        }
        // Compatibility aliases for older web clients.
        // moviePlayer, setMoviePlayer — compatibility aliases for the movie mode.
        @JavascriptInterface
        fun moviePlayer(): String = Prefs(this@PultActivity).moviePlaybackMode.id

        @JavascriptInterface
        fun setMoviePlayer(mode: String) {
            PlayerMode.of(mode)?.let { Prefs(this@PultActivity).moviePlaybackMode = it }
        }

        // playMovie — «Смотреть» фильм или серию src («torrent/<hash>/<номер>», «library/<номер>») в своём плеере; не тот
        // src — false (пульт откроет по-старому).
        @JavascriptInterface
        fun playMovie(src: String, fromStart: Boolean): Boolean {
            if (!MovieUrls.validSrc(src)) return false
            handler.post {
                if (!(::web.isInitialized && web.url?.startsWith(base) == true)) return@post
                if (Prefs(this@PultActivity).moviePlaybackMode == PlayerMode.System) {
                    launchSystemPlayer(src, fromStart) // «Системный» — установленный плеер, не встроенный
                } else {
                    startActivity(Intent(this@PultActivity, MoviePlayerActivity::class.java).putExtra(MoviePlayerActivity.EXTRA_BASE, base)
                        .putExtra(MoviePlayerActivity.EXTRA_SRC, src).putExtra(MoviePlayerActivity.EXTRA_FROM_START, fromStart))
                }
            }
            return true
        }

        // launchSystemPlayer — поток сервера (Matroska, звук как есть) установленному плееру через
        // выбор приложения Android; с места из истории, если оно есть.
        private fun launchSystemPlayer(src: String, fromStart: Boolean) {
            scope.launch {
                var at = 0.0
                if (!fromStart) {
                    try {
                        val r = Api.fetch(base, MovieUrls.info(src, false), 15_000)
                        at = r.body?.let { MovieInfo.parse(it)?.startSec?.toDouble() } ?: 0.0
                    } catch (e: Exception) { /* без истории — с начала */ }
                }
                var k = 0.0
                if (at > 0) {
                    try {
                        val r = Api.fetch(base, MovieUrls.keyframe(src, at), 15_000)
                        k = r.body?.let { Regex("\"t\":([0-9.]+)").find(it)?.groupValues?.get(1)?.toDoubleOrNull() } ?: 0.0
                    } catch (e: Exception) { /* не нашли кадр — с начала */ }
                }
                val url = MovieUrls.stream(base, src, k, null, null, MovieUrls.newSid())
                val i = Intent(Intent.ACTION_VIEW).setDataAndType(Uri.parse(url), "video/x-matroska")
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                if (i.resolveActivity(packageManager) != null) {
                    startActivity(i)
                } else {
                    Toast.makeText(this@PultActivity, R.string.no_external_player, Toast.LENGTH_LONG).show()
                }
            }
        }

        // versionCode — номер сборки: пульт сравнивает его с APK на сервере («Обновить» или «Последняя версия»).
        @JavascriptInterface
        fun versionCode(): Int = BuildConfig.VERSION_CODE

        // canPickFiles — есть ли на устройстве чем выбрать файл типа type («image/*», «*/*»; на ТВ часто нечем):
        // иначе пульт не показывает поле.
        @JavascriptInterface
        fun canPickFiles(type: String): Boolean = Intent(Intent.ACTION_GET_CONTENT).addCategory(Intent.CATEGORY_OPENABLE)
            .setType(type.ifBlank { "*/*" }).resolveActivity(packageManager) != null

        // sound — звук меню (план 16В): шаг фокуса, OK, «Назад», упор; играет, если звуки включены.
        @JavascriptInterface
        fun sound(name: String) {
            sounds?.play(name)
        }

        @JavascriptInterface
        fun soundsOn(): Boolean = Prefs(this@PultActivity).sounds

        @JavascriptInterface
        fun setSoundsOn(on: Boolean) {
            Prefs(this@PultActivity).sounds = on
        }

        // voiceAvailable, voice — голосовой поиск (план 17В): есть ли распознавание; открыть его.
        @JavascriptInterface
        fun voiceAvailable(): Boolean = canVoice()

        @JavascriptInterface
        fun voice() {
            handler.post { startVoice() }
        }

        // update — «Обновить» в пульте: скачать и поставить новую версию с сервера сразу, без окна и «Позже».
        @JavascriptInterface
        fun update() {
            handler.post { if (::web.isInitialized && web.url?.startsWith(base) == true) updateNow() }
        }
    }

    companion object {
        const val EXTRA_BASE = "base"
        private const val DAY_MS = 24L * 60 * 60 * 1000
        private const val WATCH_MS = 60L * 1000
        private const val PICK_FILE = 41
        private const val VOICE = 42

        // CLOSE_DIALOGS — закрыть все окна пульта (Escape каждому), перед переходом по голосовому поиску.
        private const val CLOSE_DIALOGS = "document.querySelectorAll('.dlg-back').forEach(function(b){" +
            "b.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}))});"

        // CLOSE_DIALOG — закрыть верхнее окно пульта (openModal в ui.js закрывает его по Escape); true — было окно.
        private const val CLOSE_DIALOG = "(function(){var a=document.querySelectorAll('.dlg-back');if(!a.length)return false;" +
            "a[a.length-1].dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));return true})()"

        // postponed — «Позже»: до следующего входа в приложение (запуск или возврат из фона — onStart; заказчик 2026-10-01).
        private var postponed = false
    }
}
