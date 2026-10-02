package ru.kinodom.app.ui

import android.app.Activity
import android.graphics.Typeface
import android.graphics.drawable.ColorDrawable
import android.graphics.drawable.StateListDrawable
import android.os.SystemClock
import android.text.TextUtils
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.AbsListView
import android.widget.AdapterView
import android.widget.BaseAdapter
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ListView
import android.widget.TextView
import ru.kinodom.app.R
import ru.kinodom.app.core.Ch
import ru.kinodom.app.core.NowTitles
import ru.kinodom.app.net.Logos

// ChannelPanel — список поверх видео слева (спека этапа 13, 4.3): номер, логотип, имя, что идёт сейчас; стрелки —
// по списку, OK или касание — включить.
// sound — звук меню (план 16В): шаг по списку — move, выбор — select.
class ChannelPanel(
    private val a: Activity,
    private val items: List<Ch>,
    private val logos: Logos,
    private val sound: (String) -> Unit = {},
    onPick: (Int) -> Unit,
) {
    private var titles: Map<String, String> = emptyMap()
    private var playing = -1
    private var shownAt = 0L // выбор текущего канала при показе — не шаг: без звука
    private var quietAt = -1 // строка, которую выбирает сам показ (ревью 16В: тишина по месту, а не только по времени)

    private val adapter = object : BaseAdapter() {
        override fun getCount() = items.size
        override fun getItem(i: Int) = items[i]
        override fun getItemId(i: Int) = i.toLong()

        override fun getView(i: Int, convert: View?, parent: ViewGroup): View {
            val row = convert as? Row ?: Row()
            row.bind(items[i], NowTitles.titleFor(titles, items[i]), i == playing)
            return row
        }
    }

    val view: ListView = ListView(a).apply {
        adapter = this@ChannelPanel.adapter
        divider = null
        isFocusable = true
        itemsCanFocus = false
        selector = StateListDrawable().apply {
            addState(intArrayOf(android.R.attr.state_focused), ColorDrawable(a.getColor(R.color.raised)))
            addState(intArrayOf(android.R.attr.state_pressed), ColorDrawable(a.getColor(R.color.raised)))
            addState(intArrayOf(android.R.attr.state_selected), ColorDrawable(a.getColor(R.color.raised)))
        }
        setBackgroundColor(0xE6111315.toInt())
        isSoundEffectsEnabled = false // свой звук выбора — системный щелчок списка не двоит его
        setOnItemClickListener { _, _, i, _ ->
            sound("select")
            onPick(i)
        }
        onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
            override fun onItemSelected(parent: AdapterView<*>?, v: View?, i: Int, id: Long) {
                // Выбор показом — та строка и вскоре после показа (на слабом ТВ разметка дольше 150 мс): без звука.
                if (i == quietAt && SystemClock.uptimeMillis() - shownAt < QUIET_MS) {
                    quietAt = -1
                    return
                }
                quietAt = -1
                sound("move")
            }

            override fun onNothingSelected(parent: AdapterView<*>?) {}
        }
        visibility = View.GONE
        layoutParams = FrameLayout.LayoutParams(Screens.dp(a, 440), FrameLayout.LayoutParams.MATCH_PARENT, Gravity.START)
    }

    val shown get() = view.visibility == View.VISIBLE

    fun show(current: Int) {
        shownAt = SystemClock.uptimeMillis()
        quietAt = current
        playing = current
        adapter.notifyDataSetChanged()
        view.visibility = View.VISIBLE
        view.requestFocus()
        view.setSelection(current)
    }

    fun setTitles(t: Map<String, String>) {
        titles = t
        adapter.notifyDataSetChanged()
    }

    fun hide() {
        view.visibility = View.GONE
    }

    private inner class Row : LinearLayout(a) {
        private val number = TextView(a).apply {
            setTextColor(a.getColor(R.color.muted))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
            gravity = Gravity.END or Gravity.CENTER_VERTICAL
        }
        private val logo = ImageView(a).apply { scaleType = ImageView.ScaleType.FIT_CENTER }
        private val name = TextView(a).apply {
            setTextColor(a.getColor(R.color.text))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 20f)
            isSingleLine = true
            ellipsize = TextUtils.TruncateAt.END
        }
        private val now = TextView(a).apply {
            setTextColor(a.getColor(R.color.muted))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            isSingleLine = true
            ellipsize = TextUtils.TruncateAt.END
        }

        init {
            orientation = HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val p = Screens.dp(a, 12)
            setPadding(p, p, p, p)
            layoutParams = AbsListView.LayoutParams(AbsListView.LayoutParams.MATCH_PARENT, AbsListView.LayoutParams.WRAP_CONTENT)
            addView(number, LayoutParams(Screens.dp(a, 40), LayoutParams.WRAP_CONTENT).apply { marginEnd = Screens.dp(a, 12) })
            addView(logo, LayoutParams(Screens.dp(a, 56), Screens.dp(a, 40)).apply { marginEnd = Screens.dp(a, 12) })
            addView(LinearLayout(a).apply {
                orientation = VERTICAL
                addView(name)
                addView(now)
            }, LayoutParams(0, LayoutParams.WRAP_CONTENT, 1f))
        }

        fun bind(ch: Ch, title: String, playing: Boolean) {
            number.text = if (ch.number > 0) ch.number.toString() else ""
            logos.into(logo, ch.logo)
            name.text = ch.name
            name.typeface = if (playing) Typeface.DEFAULT_BOLD else Typeface.DEFAULT
            now.text = title
            now.visibility = if (title.isEmpty()) View.GONE else View.VISIBLE
        }
    }

    companion object {
        private const val QUIET_MS = 1000L
    }
}
