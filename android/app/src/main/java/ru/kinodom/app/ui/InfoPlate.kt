package ru.kinodom.app.ui

import android.app.Activity
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import ru.kinodom.app.R
import ru.kinodom.app.core.Ch
import ru.kinodom.app.core.Epg
import ru.kinodom.app.core.Guide
import ru.kinodom.app.core.Plate
import ru.kinodom.app.net.Logos

// InfoPlate — плашка снизу поверх видео (спека этапа 13, 4.4): логотип, «1 Первый канал · МСК+4», «Сейчас:
// 21:00–22:30 …» с полоской прошедшего, «Дальше: …»; программы нет — только имя. На телефоне — кнопка «Список».
class InfoPlate(private val a: Activity, private val logos: Logos, touch: Boolean, onList: () -> Unit) {
    private val logo = ImageView(a).apply { scaleType = ImageView.ScaleType.FIT_CENTER }
    private val title = text(26f, R.color.text, bold = true)
    private val now = text(20f, R.color.text)
    private val bar = ProgressBar(a, null, android.R.attr.progressBarStyleHorizontal).apply {
        max = 100
        progressTintList = ColorStateList.valueOf(a.getColor(R.color.text))
        progressBackgroundTintList = ColorStateList.valueOf(a.getColor(R.color.line))
    }
    private val next = text(18f, R.color.muted)

    val view: LinearLayout = LinearLayout(a).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        val pad = Screens.dp(a, 24)
        setPadding(pad, pad, pad, pad)
        background = GradientDrawable().apply { setColor(0xD9111315.toInt()) }
        addView(logo, LinearLayout.LayoutParams(Screens.dp(a, 96), Screens.dp(a, 64)).apply { marginEnd = Screens.dp(a, 24) })
        addView(LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            addView(title)
            addView(now)
            addView(bar, LinearLayout.LayoutParams(Screens.dp(a, 360), Screens.dp(a, 6)).apply { topMargin = Screens.dp(a, 6); bottomMargin = Screens.dp(a, 6) })
            addView(next)
        }, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        if (touch) addView(Screens.button(a, a.getString(R.string.channel_list)) { onList() }.apply {
            layoutParams = LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT)
            val p = Screens.dp(a, 20)
            setPadding(p, 0, p, 0)
        })
        visibility = View.GONE
        layoutParams = FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM)
    }

    val shown get() = view.visibility == View.VISIBLE

    fun show(ch: Ch, epg: Epg?, at: Long) {
        title.text = Plate.title(ch)
        logos.into(logo, ch.logo)
        val p = epg?.now(at)
        val nowLine = epg?.let { Plate.now(p, it.utcOffset) }
        val nextLine = epg?.let { Plate.next(it.next(at), it.utcOffset) }
        now.text = nowLine.orEmpty()
        now.visibility = if (nowLine == null) View.GONE else View.VISIBLE
        bar.progress = p?.let { Guide.progress(it, at) } ?: 0
        bar.visibility = now.visibility
        next.text = nextLine.orEmpty()
        next.visibility = if (nextLine == null) View.GONE else View.VISIBLE
        view.visibility = View.VISIBLE
    }

    fun hide() {
        view.visibility = View.GONE
    }

    private fun text(sp: Float, color: Int, bold: Boolean = false) = TextView(a).apply {
        setTextColor(a.getColor(color))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, sp)
        if (bold) typeface = Typeface.DEFAULT_BOLD
        isSingleLine = true
        ellipsize = android.text.TextUtils.TruncateAt.END
    }
}
