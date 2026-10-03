package ru.kinodom.app.ui

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import android.util.TypedValue
import android.view.GestureDetector
import android.view.Gravity
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.window.OnBackInvokedDispatcher
import androidx.annotation.OptIn
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MimeTypes
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.Tracks
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.HttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.AspectRatioFrameLayout
import androidx.media3.ui.PlayerView
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.R
import ru.kinodom.app.core.Foreground
import ru.kinodom.app.core.Hold
import ru.kinodom.app.core.Mem
import ru.kinodom.app.core.MovieInfo
import ru.kinodom.app.core.MovieSub
import ru.kinodom.app.core.MovieTrack
import ru.kinodom.app.core.MovieTracks
import ru.kinodom.app.core.MovieUrls
import ru.kinodom.app.net.Api
import ru.kinodom.app.net.Prefs

// MoviePlayerActivity — плеер фильмов приложения (спека цикла 18, раздел 6; план 18В). Прямо — исходный файл (Media3
// сам решает, умеет ли ТВ звук); выбранную озвучку ТВ не умеет — поток сервера с ключевого кадра, звук в AAC
// (stream.mkv, перемотка — новым потоком). Пульт ТВ: кнопки скрыты — OK пауза, ←/→ перемотка (удержание —
// шаг растёт), «Назад» — закрыть; кнопки показаны — шкала и ряд «Пауза, Озвучка, Субтитры, Следующая серия».
@OptIn(UnstableApi::class)
class MoviePlayerActivity : Activity() {
    private lateinit var base: String
    private lateinit var src: String
    private var fromStart = false
    private lateinit var prefs: Prefs
    private var sounds: Sounds? = null
    private val scope = MainScope()
    private val handler = Handler(Looper.getMainLooper())
    private val sid = MovieUrls.newSid()

    private var info: MovieInfo? = null
    private var player: ExoPlayer? = null
    private var fallback = false // поток сервера вместо исходного файла
    private var k = 0.0 // кадр начала потока запасного пути
    private var audio: MovieTrack? = null
    private var sub: MovieSub? = null
    private var decided = false // дорожки выбраны для текущего источника
    private var retried = false
    private var resumeAt = -1.0 // откуда начать, когда источник откроется
    private var hold: Hold? = null
    private var holdTime = 0L
    private var row = 0 // фокус в кнопках: 0 — шкала, 1 — ряд кнопок
    private var col = 0
    private var nextLeft = 0

    private lateinit var root: FrameLayout
    private lateinit var video: PlayerView
    private lateinit var load: ProgressBar
    private lateinit var controls: LinearLayout
    private lateinit var title: TextView
    private lateinit var bar: ProgressBar
    private lateinit var time: TextView
    private lateinit var buttons: List<TextView>
    private lateinit var menu: TrackMenu
    private lateinit var box: LinearLayout // сообщение или «Следующая»
    private lateinit var boxText: TextView
    private lateinit var boxButtons: LinearLayout

    private val hide = Runnable { if (player?.isPlaying == true && !menu.shown) showControls(false) }
    private val tick = object : Runnable {
        override fun run() {
            drawTime()
            if (player?.isPlaying == true) {
                report()
                retried = false // 10 с просмотра без обрыва — следующий обрыв снова с повтором
            }
            handler.postDelayed(this, REPORT_MS)
        }
    }
    private val clock = object : Runnable {
        override fun run() {
            drawTime()
            handler.postDelayed(this, 500)
        }
    }
    private val countdown = object : Runnable {
        override fun run() {
            nextLeft -= 1
            if (nextLeft <= 0) goNext() else {
                drawNext()
                handler.postDelayed(this, 1000)
            }
        }
    }

    private fun pos(): Double {
        val p = player ?: return resumeAt.coerceAtLeast(0.0)
        if (resumeAt >= 0) return resumeAt
        return (if (fallback) k else 0.0) + p.currentPosition / 1000.0
    }

    private fun dur(): Double = info?.durationSec ?: 0.0

    @SuppressLint("ClickableViewAccessibility")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val b = intent.getStringExtra(EXTRA_BASE)
        val s = intent.getStringExtra(EXTRA_SRC)
        if (b == null || s == null || !MovieUrls.validSrc(s)) {
            finish()
            return
        }
        base = b
        src = s
        fromStart = intent.getBooleanExtra(EXTRA_FROM_START, false)
        prefs = Prefs(this)
        sounds = Sounds(this)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        buildViews()
        setContentView(root)
        fullScreen()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { back() }
        }
    }

    private fun text(sp: Float, bold: Boolean = false) = TextView(this).apply {
        setTextColor(getColor(R.color.text))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, sp)
        if (bold) setTypeface(typeface, android.graphics.Typeface.BOLD)
        setShadowLayer(6f, 0f, 0f, Color.BLACK)
    }

    private fun buildViews() {
        val pad = Screens.dp(this, 24)
        root = FrameLayout(this).apply {
            setBackgroundColor(Color.BLACK)
            isFocusable = true
        }
        video = PlayerView(this).apply {
            useController = false
            resizeMode = AspectRatioFrameLayout.RESIZE_MODE_FIT
            setShutterBackgroundColor(Color.BLACK)
            isFocusable = false
        }
        root.addView(video, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        load = ProgressBar(this)
        root.addView(load, FrameLayout.LayoutParams(Screens.dp(this, 56), Screens.dp(this, 56), Gravity.CENTER))
        title = text(22f, true).apply { setPadding(pad, pad, pad, pad); maxLines = 1 }
        bar = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply { max = 1000 }
        time = text(18f)
        val names = listOf(R.string.movie_pause, R.string.movie_audio, R.string.movie_subs, R.string.movie_next)
        buttons = names.mapIndexed { i, n ->
            text(18f).apply {
                setText(n)
                setPadding(pad / 2, pad / 3, pad / 2, pad / 3)
                setOnClickListener { press(i) }
            }
        }
        val rowView = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(time, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            buttons.forEach { addView(it) }
        }
        controls = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            background = GradientDrawable(GradientDrawable.Orientation.BOTTOM_TOP, intArrayOf(0xE6000000.toInt(), 0x00000000))
            setPadding(pad, pad * 2, pad, pad)
            addView(bar, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, Screens.dp(this@MoviePlayerActivity, 10)))
            addView(rowView, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
        root.addView(title, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.TOP).apply {
            title.background = GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM, intArrayOf(0xCC000000.toInt(), 0x00000000))
        })
        root.addView(controls, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM))
        menu = TrackMenu(this) { i -> pickFromMenu(i) }
        root.addView(menu.view, FrameLayout.LayoutParams(Screens.dp(this, 380), ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.END or Gravity.BOTTOM)
            .apply { setMargins(pad, pad, pad, Screens.dp(this@MoviePlayerActivity, 140)) })
        boxText = text(20f).apply { gravity = Gravity.CENTER }
        boxButtons = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER }
        box = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setPadding(pad, pad, pad, pad)
            background = GradientDrawable().apply { setColor(0xF0161A1E.toInt()); cornerRadius = Screens.dp(this@MoviePlayerActivity, 14).toFloat() }
            addView(boxText)
            addView(boxButtons)
            visibility = View.GONE
        }
        root.addView(box, FrameLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.CENTER))
        val gestures = GestureDetector(this, object : GestureDetector.SimpleOnGestureListener() {
            override fun onDown(e: MotionEvent) = true

            override fun onSingleTapConfirmed(e: MotionEvent): Boolean {
                showControls(controls.visibility != View.VISIBLE)
                return true
            }

            override fun onDoubleTap(e: MotionEvent): Boolean {
                val w = video.width.toFloat()
                when {
                    e.x < w / 3 -> seekTo(pos() - 10)
                    e.x > w * 2 / 3 -> seekTo(pos() + 10)
                }
                return true
            }
        })
        video.setOnTouchListener { _, e -> gestures.onTouchEvent(e) }
        showControls(false)
    }

    private fun fullScreen() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        if (Build.VERSION.SDK_INT >= 28) {
            window.attributes = window.attributes.apply {
                layoutInDisplayCutoutMode = WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            }
        }
        WindowCompat.getInsetsController(window, window.decorView).apply {
            hide(WindowInsetsCompat.Type.systemBars())
            systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        }
    }

    override fun onStart() {
        super.onStart()
        Foreground.app.start() // плеер поверх пульта — не вход в приложение
        if (!::src.isInitialized) return
        player = ExoPlayer.Builder(this).build().also {
            it.addListener(listener)
            video.player = it
        }
        val at = if (info != null) pos() else -1.0
        if (info == null) loadInfo() else open(at)
        handler.post(tick)
        handler.post(clock)
    }

    override fun onStop() {
        report()
        handler.removeCallbacksAndMessages(null)
        resumeAt = pos()
        player?.release()
        player = null
        if (::video.isInitialized) video.player = null
        Foreground.app.stop()
        super.onStop()
    }

    override fun onDestroy() {
        sounds?.release()
        sounds = null
        scope.cancel()
        super.onDestroy()
    }

    private fun loadInfo() {
        load.visibility = View.VISIBLE
        scope.launch {
            val i = Api.get(base, MovieUrls.info(src, fromStart), INFO_TIMEOUT_MS)?.let { MovieInfo.parse(it) }
            if (i == null) {
                fail(getString(R.string.movie_no_info), false)
                return@launch
            }
            info = i
            title.text = i.title
            buttons[3].visibility = if (i.nextSrc != null) View.VISIBLE else View.GONE
            buttons[1].visibility = if (i.audio.size > 1) View.VISIBLE else View.GONE
            buttons[2].visibility = if (i.subs.isNotEmpty()) View.VISIBLE else View.GONE
            audio = MovieTracks.pickAudio(i.audio, prefs.trackMem("audio", i.hash))
            sub = MovieTracks.pickSub(i.subs, prefs.trackMem("subs", i.hash), true)
            fallback = false
            open(i.startSec.toDouble())
        }
    }

    // open — источник с места at: прямо — исходный файл и перемотка в нём; запасной путь — поток с кадра.
    private fun open(at: Double) {
        val i = info ?: return
        val p = player ?: return
        decided = false
        box.visibility = View.GONE
        load.visibility = View.VISIBLE
        if (!fallback) {
            val subs = i.subs.filter { it.external }.map { s ->
                MediaItem.SubtitleConfiguration.Builder(Uri.parse(MovieUrls.subs(base, src, s.id, sid)))
                    .setId(s.id).setMimeType(MimeTypes.TEXT_VTT).setLanguage(s.lang).setLabel(s.title).build()
            }
            resumeAt = -1.0
            p.setMediaItem(MediaItem.Builder().setUri(i.direct).setSubtitleConfigurations(subs).build(), (at * 1000).toLong())
            p.prepare()
            p.play()
            return
        }
        resumeAt = at
        scope.launch {
            val kf = if (at > 0) Api.get(base, MovieUrls.keyframe(src, at), INFO_TIMEOUT_MS)?.let { Regex("\"t\":([0-9.]+)").find(it)?.groupValues?.get(1)?.toDoubleOrNull() } else 0.0
            if (kf == null) {
                fail(getString(R.string.movie_broken), true)
                return@launch
            }
            val pl = player ?: return@launch
            k = kf
            resumeAt = -1.0
            val s = sub?.takeIf { !it.image }?.id
            pl.setMediaItem(MediaItem.fromUri(MovieUrls.stream(base, src, k, audio?.id, s, sid)))
            pl.prepare()
            pl.play()
        }
    }

    // decideTracks — дорожки по выбору (Review Focus 1): озвучка поддерживается — выбрать её; нет — запасной путь.
    private fun decideTracks(tracks: Tracks) {
        val i = info ?: return
        val p = player ?: return
        if (decided) return
        decided = true
        val audioGroups = tracks.groups.filter { it.type == C.TRACK_TYPE_AUDIO }
        val textGroups = tracks.groups.filter { it.type == C.TRACK_TYPE_TEXT }
        var params = p.trackSelectionParameters.buildUpon()
        if (!fallback) {
            val want = audio
            val g = want?.let { audioGroups.getOrNull(i.audio.indexOf(it)) }
            if (want != null && (g == null || !g.isTrackSupported(0))) {
                Log.i(TAG, "озвучка ${want.codec} не поддерживается — поток сервера")
                val at = pos()
                fallback = true
                open(at)
                return
            }
            if (g != null) params = params.setOverrideForType(TrackSelectionOverride(g.mediaTrackGroup, 0))
        }
        val s = sub
        params = if (s == null) {
            params.setTrackTypeDisabled(C.TRACK_TYPE_TEXT, true)
        } else {
            val g = if (fallback) textGroups.firstOrNull() else if (s.external) {
                textGroups.firstOrNull { it.getTrackFormat(0).id == s.id }
            } else {
                textGroups.filter { tg -> i.subs.none { it.external && it.id == tg.getTrackFormat(0).id } }.getOrNull(i.subs.filter { !it.external }.indexOf(s))
            }
            if (g == null) params.setTrackTypeDisabled(C.TRACK_TYPE_TEXT, true)
            else params.setTrackTypeDisabled(C.TRACK_TYPE_TEXT, false).setOverrideForType(TrackSelectionOverride(g.mediaTrackGroup, 0))
        }
        p.trackSelectionParameters = params.build()
        Log.i(TAG, "${if (fallback) "поток сервера" else "прямо"}: озвучка ${audio?.id}, субтитры ${sub?.id}")
    }

    private val listener = object : Player.Listener {
        override fun onTracksChanged(tracks: Tracks) {
            if (!tracks.isEmpty) decideTracks(tracks)
        }

        override fun onPlaybackStateChanged(state: Int) {
            load.visibility = if (state == Player.STATE_BUFFERING) View.VISIBLE else View.GONE
            if (state == Player.STATE_ENDED) ended()
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            buttons[0].setText(if (isPlaying) R.string.movie_pause else R.string.movie_play)
            if (!isPlaying) {
                report()
                showControls(true)
            } else {
                scheduleHide()
            }
        }

        override fun onPlayerError(error: PlaybackException) {
            Log.w(TAG, "ошибка плеера: ${error.errorCodeName}", error)
            var c: Throwable? = error
            while (c != null) {
                if (c is HttpDataSource.InvalidResponseCodeException && c.responseCode == 429) {
                    fail(getString(R.string.movie_busy), true)
                    return
                }
                c = c.cause
            }
            if (!fallback && (error.errorCode == PlaybackException.ERROR_CODE_DECODER_INIT_FAILED ||
                    error.errorCode == PlaybackException.ERROR_CODE_DECODING_FORMAT_UNSUPPORTED)) {
                fail(getString(R.string.movie_cant_show), true)
                return
            }
            if (!retried) {
                retried = true
                open(pos())
                return
            }
            fail(getString(R.string.movie_broken), true)
        }
    }

    private fun ended() {
        val i = info ?: return
        if (fallback && !MovieUrls.nearEnd(pos(), dur())) { // поток кончился посреди файла — обрыв
            listener.onPlayerError(PlaybackException("поток кончился раньше файла", null, PlaybackException.ERROR_CODE_IO_UNSPECIFIED))
            return
        }
        report(final = true)
        if (i.nextSrc == null) {
            finish()
            return
        }
        nextLeft = NEXT_AFTER
        drawNext()
        showBox(listOf(getString(R.string.movie_play) to { goNext() }, getString(R.string.movie_cancel) to { finish() }))
        handler.postDelayed(countdown, 1000)
    }

    private fun drawNext() {
        boxText.text = getString(R.string.movie_next_in, info?.nextTitle.orEmpty(), nextLeft)
    }

    private fun goNext() {
        handler.removeCallbacks(countdown)
        val n = info?.nextSrc ?: return
        report()
        src = n
        fromStart = false
        info = null
        fallback = false
        box.visibility = View.GONE
        player?.stop()
        loadInfo()
    }

    private fun fail(text: String, external: Boolean) {
        player?.stop()
        load.visibility = View.GONE
        boxText.text = text
        val vlc = external && info != null && vlcIntent()?.resolveActivity(packageManager) != null
        showBox(listOfNotNull(
            if (vlc) getString(R.string.movie_vlc) to { openVlc() } else null,
            getString(R.string.movie_back) to { finish() },
        ))
    }

    private fun showBox(actions: List<Pair<String, () -> Unit>>) {
        showControls(false)
        boxButtons.removeAllViews()
        actions.forEachIndexed { n, (label, act) ->
            boxButtons.addView(text(18f).apply {
                text = label
                setPadding(Screens.dp(this@MoviePlayerActivity, 18), Screens.dp(this@MoviePlayerActivity, 10), Screens.dp(this@MoviePlayerActivity, 18), Screens.dp(this@MoviePlayerActivity, 10))
                isFocusable = true
                background = focusBackground()
                setOnClickListener { sounds?.play("select"); act() }
                if (n == 0) post { requestFocus() }
            })
        }
        box.visibility = View.VISIBLE
    }

    private fun focusBackground() = android.graphics.drawable.StateListDrawable().apply {
        addState(intArrayOf(android.R.attr.state_focused), GradientDrawable().apply { setColor(0x33FFFFFF); cornerRadius = 12f })
        addState(intArrayOf(), GradientDrawable().apply { setColor(0x00000000) })
    }

    private fun vlcIntent(): Intent? {
        val i = info ?: return null
        return Intent(Intent.ACTION_VIEW).setDataAndType(Uri.parse(MovieUrls.vlcUri(i.direct)), "video/*").setPackage("org.videolan.vlc")
            .putExtra("title", i.title).putExtra("position", (pos() * 1000).toLong())
    }

    private fun openVlc() {
        vlcIntent()?.let { startActivity(it) }
        finish()
    }

    // report — место серверу (спека 18, раздел 4).
    private fun report(final: Boolean = false) {
        val i = info ?: return
        if (dur() <= 0) return
        val body = MovieUrls.report(if (final) dur() else pos(), dur())
        scope.launch { Api.put(base, MovieUrls.history(i.hash, i.index), body) }
    }

    private fun seekTo(t: Double) {
        val p = player ?: return
        val to = t.coerceIn(0.0, (dur() - 1).coerceAtLeast(0.0))
        report()
        if (fallback) open(to) else p.seekTo((to * 1000).toLong())
        showControls(true)
    }

    private fun togglePause() {
        val p = player ?: return
        if (p.isPlaying) p.pause() else p.play()
    }

    private fun drawTime() {
        val t = hold?.target?.coerceIn(0.0, dur()) ?: pos()
        bar.progress = if (dur() > 0) (t / dur() * 1000).toInt() else 0
        time.text = "${fmt(t)} / ${fmt(dur())}"
    }

    private fun fmt(sec: Double): String {
        val s = sec.toLong().coerceAtLeast(0)
        return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s / 60 % 60, s % 60) else "%d:%02d".format(s / 60, s % 60)
    }

    private fun showControls(on: Boolean) {
        controls.visibility = if (on) View.VISIBLE else View.GONE
        title.visibility = controls.visibility
        if (on) {
            drawFocus()
            scheduleHide()
        } else {
            row = 0
            col = 0
        }
    }

    private fun scheduleHide() {
        handler.removeCallbacks(hide)
        handler.postDelayed(hide, HIDE_MS)
    }

    private fun visibleButtons() = buttons.indices.filter { buttons[it].visibility == View.VISIBLE }

    private fun drawFocus() {
        bar.alpha = if (row == 0) 1f else 0.6f
        val vis = visibleButtons()
        buttons.forEachIndexed { n, b ->
            b.background = if (row == 1 && vis.getOrNull(col) == n) GradientDrawable().apply { setColor(0x40FFFFFF); cornerRadius = 12f } else null
        }
    }

    private fun press(n: Int) {
        sounds?.play("select")
        when (n) {
            0 -> togglePause()
            1 -> openMenu("audio")
            2 -> openMenu("subs")
            3 -> goNext()
        }
    }

    private var menuKind = ""

    private fun openMenu(kind: String) {
        val i = info ?: return
        menuKind = kind
        val items = if (kind == "audio") {
            MovieTracks.labels(i.audio).mapIndexed { n, l -> l to (i.audio[n] == audio) }
        } else {
            val subs = i.subs.filter { !fallback || !it.image }
            listOf(getString(R.string.movie_subs_off) to (sub == null)) + MovieTracks.subLabels(subs).mapIndexed { n, l -> l to (subs[n] == sub) }
        }
        menu.show(getString(if (kind == "audio") R.string.movie_audio else R.string.movie_subs), items)
        handler.removeCallbacks(hide)
    }

    private fun pickFromMenu(n: Int) {
        val i = info ?: return
        menu.hide()
        root.requestFocus()
        if (menuKind == "audio") {
            val t = i.audio.getOrNull(n) ?: return
            if (t == audio) return
            audio = t
            prefs.setTrackMem("audio", i.hash, Mem.of(t.title, t.lang, t.codec))
            val at = pos()
            fallback = false // новая озвучка — снова пробуем исходный файл
            open(at)
        } else {
            val subs = i.subs.filter { !fallback || !it.image }
            sub = if (n == 0) null else subs.getOrNull(n - 1)
            prefs.setTrackMem("subs", i.hash, sub?.let { Mem.of(it.title, it.lang) } ?: Mem.OFF)
            if (fallback) open(pos()) else player?.currentTracks?.let { decided = false; decideTracks(it) }
        }
        scheduleHide()
    }

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (box.visibility == View.VISIBLE || menu.shown) {
            if (event.keyCode == KeyEvent.KEYCODE_BACK && event.action == KeyEvent.ACTION_UP) {
                back()
                return true
            }
            return super.dispatchKeyEvent(event) // стрелки и OK — по кнопкам окна и пунктам меню
        }
        val code = event.keyCode
        val down = event.action == KeyEvent.ACTION_DOWN
        val now = SystemClock.uptimeMillis()
        if (code == KeyEvent.KEYCODE_DPAD_LEFT || code == KeyEvent.KEYCODE_DPAD_RIGHT) {
            val dir = if (code == KeyEvent.KEYCODE_DPAD_RIGHT) 1 else -1
            if (controls.visibility == View.VISIBLE && row == 1) {
                if (down) moveCol(dir)
                return true
            }
            if (down && event.repeatCount == 0) {
                hold = Hold(dir, pos(), now)
                showControls(true)
                drawTime()
            } else if (down) {
                hold?.repeat(now)
                drawTime()
                scheduleHide()
            } else {
                hold?.let { seekTo(it.target) }
                hold = null
            }
            return true
        }
        if (!down) return super.dispatchKeyEvent(event)
        scheduleHide()
        when (code) {
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> {
                if (controls.visibility == View.VISIBLE && row == 1) visibleButtons().getOrNull(col)?.let { press(it) }
                else {
                    togglePause()
                    showControls(true)
                }
                return true
            }
            KeyEvent.KEYCODE_DPAD_DOWN -> {
                if (controls.visibility != View.VISIBLE) showControls(true) else if (row == 0) { row = 1; sounds?.play("move"); drawFocus() }
                return true
            }
            KeyEvent.KEYCODE_DPAD_UP -> {
                if (controls.visibility != View.VISIBLE) showControls(true) else if (row == 1) { row = 0; sounds?.play("move"); drawFocus() }
                return true
            }
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, KeyEvent.KEYCODE_MEDIA_PLAY, KeyEvent.KEYCODE_MEDIA_PAUSE -> {
                togglePause()
                return true
            }
            KeyEvent.KEYCODE_MEDIA_FAST_FORWARD -> { seekTo(pos() + 30); return true }
            KeyEvent.KEYCODE_MEDIA_REWIND -> { seekTo(pos() - 10); return true }
            KeyEvent.KEYCODE_MEDIA_NEXT -> { if (info?.nextSrc != null) goNext(); return true }
            KeyEvent.KEYCODE_BACK -> return true // по отпусканию — back()
        }
        return super.dispatchKeyEvent(event)
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            back()
            return true
        }
        return super.onKeyUp(keyCode, event)
    }

    private fun moveCol(dir: Int) {
        val n = visibleButtons().size
        val c = (col + dir).coerceIn(0, (n - 1).coerceAtLeast(0))
        sounds?.play(if (c == col) "edge" else "move")
        col = c
        drawFocus()
    }

    @Deprecated("Back до Android 13")
    override fun onBackPressed() = back()

    // back — «Назад»: закрыть открытое (меню, окно «Следующая», кнопки), иначе — закрыть плеер (место сохранено).
    private fun back() {
        sounds?.play("back")
        when {
            menu.shown -> {
                menu.hide()
                root.requestFocus()
                scheduleHide()
            }
            box.visibility == View.VISIBLE -> {
                handler.removeCallbacks(countdown)
                finish()
            }
            controls.visibility == View.VISIBLE -> showControls(false)
            else -> finish()
        }
    }

    companion object {
        const val EXTRA_BASE = "base"
        const val EXTRA_SRC = "src"
        const val EXTRA_FROM_START = "fromStart"
        private const val TAG = "KinodomMovie"
        private const val HIDE_MS = 5000L
        private const val REPORT_MS = 10_000L
        private const val NEXT_AFTER = 10
        private const val INFO_TIMEOUT_MS = 40_000 // сервер ждёт сведений до 30 с (ProbeTimeout, план 18А)
    }
}
