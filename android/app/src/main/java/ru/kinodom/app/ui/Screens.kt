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
import ru.kinodom.app.R

// Screens — простые экраны приложения в стиле пульта: тёмный фон, крупный текст, кнопки, по которым ходят
// стрелки пульта ТВ (фокус — светлая рамка).
object Screens {
    fun column(a: Activity): LinearLayout = LinearLayout(a).apply {
        orientation = LinearLayout.VERTICAL
        gravity = Gravity.CENTER
        setBackgroundColor(a.getColor(R.color.bg))
        val pad = dp(a, 32)
        setPadding(pad, pad, pad, pad)
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
        layoutParams = LinearLayout.LayoutParams(dp(a, 420), ViewGroup.LayoutParams.WRAP_CONTENT).apply {
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

    fun gone(v: View?) {
        v?.visibility = View.GONE
    }
}
