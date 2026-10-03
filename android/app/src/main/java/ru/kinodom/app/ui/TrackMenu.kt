package ru.kinodom.app.ui

import android.app.Activity
import android.graphics.drawable.ColorDrawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.StateListDrawable
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.LinearLayout
import android.widget.ListView
import android.widget.TextView
import ru.kinodom.app.R

// TrackMenu — меню озвучки или субтитров (план 18В): заголовок и пункты; стрелки — по пунктам, OK или касание — выбрать.
class TrackMenu(private val a: Activity, onPick: (Int) -> Unit) {
    private val head = TextView(a).apply {
        setTextColor(a.getColor(R.color.muted))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
        setPadding(Screens.dp(a, 16), Screens.dp(a, 12), Screens.dp(a, 16), Screens.dp(a, 6))
    }
    private val list = ListView(a).apply {
        divider = null
        isFocusable = true
        itemsCanFocus = false
        selector = StateListDrawable().apply {
            addState(intArrayOf(android.R.attr.state_focused), ColorDrawable(a.getColor(R.color.raised)))
            addState(intArrayOf(android.R.attr.state_pressed), ColorDrawable(a.getColor(R.color.raised)))
        }
        setOnItemClickListener { _, _, i, _ -> onPick(i) }
    }
    val view: LinearLayout = LinearLayout(a).apply {
        orientation = LinearLayout.VERTICAL
        background = GradientDrawable().apply { setColor(0xF2161A1E.toInt()); cornerRadius = Screens.dp(a, 14).toFloat() }
        setPadding(0, 0, 0, Screens.dp(a, 8))
        addView(head)
        addView(list, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        visibility = View.GONE
    }
    val shown: Boolean get() = view.visibility == View.VISIBLE

    // show — заголовок и пункты (подпись, выбран ли); фокус — на выбранном.
    fun show(title: String, items: List<Pair<String, Boolean>>) {
        head.text = title
        list.adapter = object : ArrayAdapter<String>(a, android.R.layout.simple_list_item_1, items.map { (t, on) -> (if (on) "✓  " else "    ") + t }) {
            override fun getView(position: Int, convertView: View?, parent: ViewGroup): View =
                (super.getView(position, convertView, parent) as TextView).apply {
                    setTextColor(a.getColor(R.color.text))
                    setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
                }
        }
        view.visibility = View.VISIBLE
        val on = items.indexOfFirst { it.second }.coerceAtLeast(0)
        list.requestFocus()
        list.setSelection(on)
    }

    fun hide() {
        view.visibility = View.GONE
    }
}
