package ru.kinodom.app.ui

import android.app.Activity
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.StateListDrawable
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import ru.kinodom.app.R
import ru.kinodom.app.core.Layout

// Screens — простые экраны приложения в стиле пульта: тёмный фон, крупный текст, кнопки, по которым ходят
// стрелки пульта ТВ (фокус — светлая рамка).
object Screens {
    fun column(a: Activity): LinearLayout = LinearLayout(a).apply {
        orientation = LinearLayout.VERTICAL
        gravity = Gravity.CENTER
        setBackgroundColor(a.getColor(R.color.bg))
        val pad = dp(a, Layout.SIDE_DP)
        setPadding(pad, pad, pad, pad)
        fitBars(this, pad)
        layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
    }

    fun title(a: Activity, text: String): TextView = TextView(a).apply {
        this.text = text
        setTextColor(a.getColor(R.color.text))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 26f)
        typeface = Typeface.DEFAULT_BOLD
        gravity = Gravity.CENTER
        setPadding(0, 0, 0, dp(a, 16))
    }

    fun note(a: Activity, text: String, error: Boolean = false): TextView = TextView(a).apply {
        this.text = text
        setTextColor(a.getColor(if (error) R.color.error else R.color.muted))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
        gravity = Gravity.CENTER
        setPadding(0, dp(a, 8), 0, dp(a, 8))
    }

    fun button(a: Activity, text: String, onClick: () -> Unit): Button = Button(a).apply {
        this.text = text
        isAllCaps = false
        setTextColor(a.getColor(R.color.text))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 20f)
        background = buttonBackground(a)
        isFocusable = true
        setOnClickListener { onClick() }
        layoutParams = LinearLayout.LayoutParams(fieldWidth(a), ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(a, 12)
        }
        minHeight = dp(a, 56)
    }

    private fun buttonBackground(a: Activity): StateListDrawable {
        fun box(fill: Int, stroke: Int) = GradientDrawable().apply {
            cornerRadius = dp(a, 8).toFloat()
            setColor(fill)
            setStroke(dp(a, 2), stroke)
        }
        return StateListDrawable().apply {
            addState(intArrayOf(android.R.attr.state_focused), box(a.getColor(R.color.raised), a.getColor(R.color.text)))
            addState(intArrayOf(android.R.attr.state_pressed), box(a.getColor(R.color.raised), a.getColor(R.color.text)))
            addState(intArrayOf(), box(a.getColor(R.color.raised), a.getColor(R.color.line)))
        }
    }

    fun dp(a: Activity, v: Int): Int = (v * a.resources.displayMetrics.density).toInt()

    // fieldWidth — ширина поля и кнопок: 420 dp, на узком телефоне — по экрану с отступами.
    fun fieldWidth(a: Activity): Int = dp(a, Layout.fieldWidthDp(a.resources.configuration.screenWidthDp))

    // fitBars — отступы под строку состояния, панель жестов, вырез и клавиатуру: на Android 15+ окно рисуется от
    // края до края (финальное ревью 13a); на ТВ панелей нет — отступы нулевые. Вложенным окнам отступы не нужны.
    fun fitBars(v: View, pad: Int = 0) {
        ViewCompat.setOnApplyWindowInsetsListener(v) { view, insets ->
            val b = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout() or WindowInsetsCompat.Type.ime(),
            )
            view.setPadding(pad + b.left, pad + b.top, pad + b.right, pad + b.bottom)
            WindowInsetsCompat.CONSUMED
        }
    }

    fun gone(v: View?) {
        v?.visibility = View.GONE
    }
}
