package ru.kinodom.app.ui

import android.annotation.SuppressLint
import android.app.Activity
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.GestureDetector
import android.view.Gravity
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.TextView
import android.window.OnBackInvokedDispatcher
import androidx.annotation.OptIn
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.Tracks
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DefaultHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.ui.AspectRatioFrameLayout
import androidx.media3.ui.PlayerView
import kotlin.math.abs
import kotlinx.coroutines.Job
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.R
import ru.kinodom.app.core.Epg
import ru.kinodom.app.core.Failover
import ru.kinodom.app.core.Guide
import ru.kinodom.app.core.Lineup
import ru.kinodom.app.core.NowTitles
import ru.kinodom.app.core.Source
import ru.kinodom.app.core.Sources
import ru.kinodom.app.core.Tuner
import ru.kinodom.app.net.Api
import ru.kinodom.app.net.Logos

// PlayerActivity — плеер каналов (спека этапа 13, раздел 4): канал во весь экран; источники — по порядку из
// /play, нет картинки за 8 с, ошибка или поток кончился — следующий, кончились — «Канал сейчас не показывает».
// Пульт ТВ: вверх/вниз и «Канал ±» — соседний канал списка, цифры — номер, OK — список поверх видео,
// влево/вправо и «Инфо» — плашка с передачей; телефон: свайп вверх/вниз, касание — плашка. «Назад» — закрыть
// открытое, иначе в пульт на то же место.
@OptIn(UnstableApi::class)
class PlayerActivity : Activity() {
    private lateinit var base: String
    private lateinit var lineup: Lineup
    private lateinit var tuner: Tuner
    private lateinit var root: FrameLayout
    private lateinit var video: PlayerView
    private lateinit var message: TextView
    private lateinit var corner: TextView
    private lateinit var plate: InfoPlate
    private lateinit var panel: ChannelPanel
    private lateinit var logos: Logos
    private var player: ExoPlayer? = null
    private val scope = MainScope()
    private val handler = Handler(Looper.getMainLooper())

    private var playing = -1
    private var loading: Job? = null
    private var sources: List<Source> = emptyList()
    private var failover = Failover(0)
    private var firstFrame = false
    private var hasVideo = false
    private var hasAudio = false
    private val silence = Runnable { if (!Failover.accepts(hasVideo, hasAudio, firstFrame)) nextSource() }

    private var epg: Epg? = null
    private var epgJob: Job? = null
    private var titlesAt = 0L
    private val hidePlate = Runnable { plate.hide() }
    private val hideCorner = Runnable { corner.visibility = View.GONE }
    private val zap = Runnable { switchTo(tuner.commit()) }
    private val number = Runnable { pickNumber() }
    private val minute = object : Runnable {
        override fun run() {
            loadEpg()
            handler.postDelayed(this, MINUTE_MS)
        }
    }

    @SuppressLint("ClickableViewAccessibility")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val b = intent.getStringExtra(EXTRA_BASE)
        val l = Lineup.parse(intent.getStringExtra(EXTRA_LINEUP).orEmpty())
        if (b == null || l == null) {
            finish()
            return
        }
        base = b
        lineup = l
        tuner = Tuner(l.items, savedInstanceState?.getInt(STATE_CURRENT, l.start)?.takeIf { it in l.items.indices } ?: l.start)
        logos = Logos(b, scope)
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
        // message — «Канал сейчас не показывает» на чёрном: поверх застывшего последнего кадра надпись не читалась
        // и казалось, что канал идёт (вживую, исправление после ревью 13b).
        message = TextView(this).apply {
            setTextColor(getColor(R.color.text))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            gravity = Gravity.CENTER
            setBackgroundColor(Color.BLACK)
            visibility = View.GONE
        }
        root.addView(message, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        // corner — набранный номер и «Нет канала N» справа сверху.
        corner = TextView(this).apply {
            setTextColor(getColor(R.color.text))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 40f)
            typeface = Typeface.DEFAULT_BOLD
            setShadowLayer(8f, 0f, 0f, Color.BLACK)
            visibility = View.GONE
        }
        val m = Screens.dp(this, 32)
        root.addView(corner, FrameLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.TOP or Gravity.END)
            .apply { setMargins(m, m, m, m) })
        // Кнопка «Список» — телефону; эмулятор ТВ тоже сообщает о сенсорном экране, поэтому ТВ — по leanback.
        val touch = packageManager.hasSystemFeature(PackageManager.FEATURE_TOUCHSCREEN) &&
            !packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)
        plate = InfoPlate(this, logos, touch) { openPanel() }
        root.addView(plate.view)
        panel = ChannelPanel(this, l.items, logos) { i ->
            panel.hide()
            root.requestFocus()
            tuner.jump(i)
            switchTo(i)
        }
        root.addView(panel.view)
        val gestures = GestureDetector(this, object : GestureDetector.SimpleOnGestureListener() {
            override fun onDown(e: MotionEvent) = true

            override fun onSingleTapUp(e: MotionEvent): Boolean {
                when {
                    panel.shown -> closePanel()
                    plate.shown -> plate.hide()
                    else -> showPlate(tuner.current)
                }
                return true
            }

            // Свайп вверх — следующий канал, вниз — предыдущий (спека 4.5).
            override fun onFling(e1: MotionEvent?, e2: MotionEvent, vx: Float, vy: Float): Boolean {
                if (panel.shown || abs(vy) < abs(vx)) return false
                step(if (vy < 0) 1 else -1)
                return true
            }
        })
        video.setOnTouchListener { _, e -> gestures.onTouchEvent(e) }
        setContentView(root)
        root.requestFocus()
        fullScreen()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { back() }
        }
    }

    // fullScreen — видео во весь экран: на телефоне без строки состояния и панели навигации (появляются по
    // свайпу от края), под вырезом камеры тоже; на ТВ панелей нет.
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
        if (!::tuner.isInitialized) return
        player = ExoPlayer.Builder(this).build().also {
            it.addListener(listener)
            video.player = it
        }
        playing = -1
        switchTo(tuner.current)
        handler.postDelayed(minute, MINUTE_MS)
    }

    override fun onStop() {
        handler.removeCallbacksAndMessages(null)
        loading?.cancel()
        epgJob?.cancel()
        player?.release()
        player = null
        if (::video.isInitialized) video.player = null
        super.onStop()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        if (::tuner.isInitialized) outState.putInt(STATE_CURRENT, tuner.current)
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (!::tuner.isInitialized) return super.onKeyDown(keyCode, event)
        if (panel.shown) {
            // Список поверх видео: стрелки вверх/вниз — его (на краю списка — никуда), влево/вправо — закрыть.
            return when (keyCode) {
                KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN -> true
                KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_DPAD_RIGHT -> {
                    closePanel()
                    true
                }
                else -> digitOrNot(keyCode) || super.onKeyDown(keyCode, event)
            }
        }
        return when (keyCode) {
            KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_CHANNEL_UP -> {
                step(1)
                true
            }
            KeyEvent.KEYCODE_DPAD_DOWN, KeyEvent.KEYCODE_CHANNEL_DOWN -> {
                step(-1)
                true
            }
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> {
                openPanel()
                true
            }
            KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_DPAD_RIGHT, KeyEvent.KEYCODE_INFO -> {
                showPlate(tuner.current)
                true
            }
            else -> digitOrNot(keyCode) || super.onKeyDown(keyCode, event)
        }
    }

    // digitOrNot — цифры пульта и цифрового блока: набор номера.
    private fun digitOrNot(keyCode: Int): Boolean {
        val n = when (keyCode) {
            in KeyEvent.KEYCODE_0..KeyEvent.KEYCODE_9 -> keyCode - KeyEvent.KEYCODE_0
            in KeyEvent.KEYCODE_NUMPAD_0..KeyEvent.KEYCODE_NUMPAD_9 -> keyCode - KeyEvent.KEYCODE_NUMPAD_0
            else -> return false
        }
        handler.removeCallbacks(zap)
        handler.removeCallbacks(hideCorner)
        corner.text = tuner.digit(n)
        corner.visibility = View.VISIBLE
        handler.removeCallbacks(number)
        handler.postDelayed(number, Tuner.NUMBER_MS)
        return true
    }

    private fun pickNumber() {
        val typed = corner.text.toString()
        val i = tuner.commitNumber()
        if (i == null) {
            corner.text = getString(R.string.no_channel, typed)
            handler.postDelayed(hideCorner, NOTICE_MS)
            return
        }
        corner.visibility = View.GONE
        switchTo(i)
    }

    // step — соседний канал: на плашке — имя набранного, включается тот, на котором остановились.
    private fun step(d: Int) {
        handler.removeCallbacks(number)
        corner.visibility = View.GONE
        val i = tuner.step(d)
        showPlate(i, withGuide = false)
        handler.removeCallbacks(zap)
        handler.postDelayed(zap, Tuner.ZAP_MS)
    }

    @Deprecated("До Android 13 «Назад» приходит сюда")
    override fun onBackPressed() = back()

    private fun back() {
        if (!::tuner.isInitialized) {
            finish()
            return
        }
        when {
            panel.shown -> closePanel()
            plate.shown -> plate.hide()
            else -> finish()
        }
    }

    private fun openPanel() {
        plate.hide()
        panel.show(tuner.current)
        loadTitles()
    }

    private fun closePanel() {
        panel.hide()
        root.requestFocus()
    }

    // showPlate — плашка на 5 с; withGuide — с программой текущего канала (у набранного стрелками её ещё нет).
    private fun showPlate(i: Int, withGuide: Boolean = true) {
        plate.show(lineup.items[i], if (withGuide && i == playing) epg else null, System.currentTimeMillis())
        handler.removeCallbacks(hidePlate)
        handler.postDelayed(hidePlate, PLATE_MS)
    }

    // switchTo — включить канал i (если не он уже играет; тот же после «не показывает» — заново) и показать
    // плашку с его передачей.
    private fun switchTo(i: Int) {
        if (Tuner.restart(i, playing, noSignal = message.visibility == View.VISIBLE)) {
            playing = i
            epg = null
            play(i)
            loadEpg()
        }
        showPlate(i)
    }

    // loadEpg — программа играющего канала: сегодня, а у последней передачи дня — и завтра (спека 4.4).
    private fun loadEpg() {
        val i = playing
        if (i < 0) return
        val v = Uri.encode(lineup.items[i].version)
        epgJob?.cancel()
        epgJob = scope.launch {
            val now = System.currentTimeMillis()
            var e = Api.get(base, "channels/$v/epg")?.let { Epg.parse(it) } ?: return@launch
            if (Guide.needTomorrow(e, now)) {
                Api.get(base, "channels/$v/epg?date=" + Guide.date(1, e.utcOffset, now))?.let { Epg.parse(it) }?.let { e += it }
            }
            if (i != playing) return@launch
            epg = e
            if (plate.shown) plate.show(lineup.items[i], e, System.currentTimeMillis())
        }
    }

    // loadTitles — «что идёт сейчас» для списка поверх видео, не чаще раза в минуту.
    private fun loadTitles() {
        val now = System.currentTimeMillis()
        if (now - titlesAt < MINUTE_MS) return
        titlesAt = now
        scope.launch {
            Api.get(base, "channels")?.let { panel.setTitles(NowTitles.parse(it)) } ?: run { titlesAt = 0 }
        }
    }

    // play — канал i списка: источники с сервера, первый из них.
    private fun play(i: Int) {
        loading?.cancel()
        handler.removeCallbacks(silence)
        player?.stop()
        message.visibility = View.GONE
        val ch = lineup.items[i]
        loading = scope.launch {
            sources = Api.get(base, "channels/" + Uri.encode(ch.version) + "/play")?.let { Sources.parse(it) }.orEmpty()
            failover = Failover(sources.size)
            if (sources.isEmpty()) noSignal() else startSource()
        }
    }

    // startSource — текущий источник: свои User-Agent и Referer, тип — по проверке сервера; 8 с без картинки —
    // следующий.
    private fun startSource() {
        val p = player ?: return
        val s = sources[failover.index]
        firstFrame = false
        hasVideo = false
        hasAudio = false
        val http = DefaultHttpDataSource.Factory().setUserAgent(Sources.userAgent(s)).setAllowCrossProtocolRedirects(true)
        if (s.referrer.isNotEmpty()) http.setDefaultRequestProperties(mapOf("Referer" to s.referrer))
        val item = MediaItem.Builder().setUri(s.url).apply { Sources.mime(s.kind)?.let { setMimeType(it) } }.build()
        p.setMediaSource(DefaultMediaSourceFactory(http).createMediaSource(item))
        p.prepare()
        p.playWhenReady = true
        handler.removeCallbacks(silence)
        handler.postDelayed(silence, Failover.SILENCE_MS)
    }

    private fun nextSource() {
        handler.removeCallbacks(silence)
        if (failover.onFailed() == null) noSignal() else startSource()
    }

    private fun noSignal() {
        handler.removeCallbacks(silence)
        player?.stop()
        message.text = getString(R.string.no_signal)
        message.visibility = View.VISIBLE
    }

    private val listener = object : Player.Listener {
        override fun onRenderedFirstFrame() {
            firstFrame = true
            message.visibility = View.GONE
        }

        override fun onTracksChanged(tracks: Tracks) {
            hasVideo = tracks.containsType(C.TRACK_TYPE_VIDEO)
            hasAudio = tracks.containsType(C.TRACK_TYPE_AUDIO)
        }

        override fun onPlayerError(error: PlaybackException) {
            // Живой поток ушёл вперёд (долгая пауза буфера) — на живой край того же источника, а не следующий.
            if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                player?.seekToDefaultPosition()
                player?.prepare()
                return
            }
            nextSource()
        }

        override fun onPlaybackStateChanged(state: Int) {
            if (state == Player.STATE_ENDED) nextSource()
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            video.keepScreenOn = isPlaying // экран не гаснет, пока играет канал
        }
    }

    companion object {
        const val EXTRA_BASE = "base"
        const val EXTRA_LINEUP = "lineup" // JSON моста playChannels как есть
        private const val STATE_CURRENT = "current"
        private const val PLATE_MS = 5000L
        private const val NOTICE_MS = 2000L
        private const val MINUTE_MS = 60_000L
    }
}
