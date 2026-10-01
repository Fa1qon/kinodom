package ru.kinodom.app.ui

import android.app.Activity
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.Gravity
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
import kotlinx.coroutines.Job
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.R
import ru.kinodom.app.core.Failover
import ru.kinodom.app.core.Lineup
import ru.kinodom.app.core.Source
import ru.kinodom.app.core.Sources
import ru.kinodom.app.core.Tuner
import ru.kinodom.app.net.Api

// PlayerActivity — плеер каналов (спека этапа 13, раздел 4): канал во весь экран; источники — по порядку из
// /play, нет картинки за 8 с, ошибка или поток кончился — следующий, кончились — «Канал сейчас не показывает»;
// «Назад» — в пульт на то же место.
@OptIn(UnstableApi::class)
class PlayerActivity : Activity() {
    private lateinit var base: String
    private lateinit var lineup: Lineup
    private lateinit var tuner: Tuner
    private lateinit var root: FrameLayout
    private lateinit var video: PlayerView
    private lateinit var message: TextView
    private var player: ExoPlayer? = null
    private val scope = MainScope()
    private val handler = Handler(Looper.getMainLooper())

    private var loading: Job? = null
    private var sources: List<Source> = emptyList()
    private var failover = Failover(0)
    private var firstFrame = false
    private var hasVideo = false
    private var hasAudio = false
    private val silence = Runnable { if (!Failover.accepts(hasVideo, hasAudio, firstFrame)) nextSource() }

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
        root = FrameLayout(this).apply { setBackgroundColor(Color.BLACK) }
        video = PlayerView(this).apply {
            useController = false
            resizeMode = AspectRatioFrameLayout.RESIZE_MODE_FIT
            setShutterBackgroundColor(Color.BLACK)
            isFocusable = false
        }
        root.addView(video, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        message = TextView(this).apply {
            setTextColor(getColor(R.color.text))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            gravity = Gravity.CENTER
            visibility = View.GONE
        }
        root.addView(message, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        setContentView(root)
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
        play(tuner.current)
    }

    override fun onStop() {
        handler.removeCallbacks(silence)
        loading?.cancel()
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

    @Deprecated("До Android 13 «Назад» приходит сюда")
    override fun onBackPressed() = back()

    private fun back() = finish()

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
    }
}
