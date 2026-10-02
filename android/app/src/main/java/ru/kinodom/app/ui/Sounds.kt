package ru.kinodom.app.ui

import android.content.Context
import android.media.AudioAttributes
import android.media.SoundPool
import android.os.SystemClock
import ru.kinodom.app.R
import ru.kinodom.app.core.SoundGate
import ru.kinodom.app.net.Prefs

// Sounds — звуки меню (план 16В): свои, синтезированные (android/tools/make-sounds.py). SoundPool — без задержки, как у
// системных звуков; громкость — от громкости устройства (поток «звуки интерфейса»). Выключаются в «Настройки →
// Приложение».
class Sounds(context: Context) {
    private val prefs = Prefs(context)
    private val pool = SoundPool.Builder()
        .setMaxStreams(4)
        .setAudioAttributes(
            AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_ASSISTANCE_SONIFICATION)
                .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                .build(),
        )
        .build()
    private val ids = mapOf(
        "move" to pool.load(context, R.raw.nav_move, 1),
        "select" to pool.load(context, R.raw.nav_select, 1),
        "back" to pool.load(context, R.raw.nav_back, 1),
        "edge" to pool.load(context, R.raw.nav_edge, 1),
    )
    private val gate = SoundGate(ids.keys)

    // play — звук name, если звуки включены и он не звучал только что. Можно звать с любого потока.
    fun play(name: String) {
        if (!gate.allow(name, prefs.sounds, SystemClock.uptimeMillis())) return
        val id = ids[name] ?: return
        pool.play(id, 1f, 1f, 1, 0, 1f)
    }

    fun release() = pool.release()
}
