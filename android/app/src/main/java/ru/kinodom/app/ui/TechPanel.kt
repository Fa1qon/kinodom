package ru.kinodom.app.ui

import android.app.Activity
import android.graphics.drawable.GradientDrawable
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import ru.kinodom.app.R

// TechPanel — окно технических сведений поверх видео (план 2026-10-06, Task B): заголовок и строки
// TechInfo (кодек, размер кадра, FPS, буфер, пропущенные кадры). Содержимое подставляет плеер (show);
// в обоих плеерах — фильма и каналов.
class TechPanel(a: Activity) {
    private val head = TextView(a).apply {
        setTextColor(a.getColor(R.color.muted))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
        setPadding(Screens.dp(a, 16), Screens.dp(a, 12), Screens.dp(a, 16), Screens.dp(a, 6))
        text = a.getString(R.string.tech_title)
    }
    private val body = TextView(a).apply {
        setTextColor(a.getColor(R.color.text))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
        setPadding(Screens.dp(a, 16), 0, Screens.dp(a, 16), Screens.dp(a, 14))
        setLineSpacing(Screens.dp(a, 4).toFloat(), 1f)
    }

    val view: LinearLayout = LinearLayout(a).apply {
        orientation = LinearLayout.VERTICAL
        background = GradientDrawable().apply { setColor(0xF2161A1E.toInt()); cornerRadius = Screens.dp(a, 14).toFloat() }
        addView(head)
        addView(body)
        visibility = View.GONE
    }

    val shown: Boolean get() = view.visibility == View.VISIBLE

    fun show(lines: List<String>) {
        body.text = lines.joinToString("\n")
        view.visibility = View.VISIBLE
    }

    fun hide() {
        view.visibility = View.GONE
    }
}
