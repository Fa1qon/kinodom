package ru.kinodom.app.ui

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.util.TypedValue
import android.view.KeyEvent
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import ru.kinodom.app.R
import ru.kinodom.app.core.Found
import ru.kinodom.app.core.Parsed
import ru.kinodom.app.core.ServerAddress
import ru.kinodom.app.core.StartFlow
import ru.kinodom.app.core.Step
import ru.kinodom.app.net.Prefs
import ru.kinodom.app.net.ServerFinder
import ru.kinodom.app.net.Status

// StartActivity — первый экран (спека этапа 13, раздел 3.2): запомненный адрес → поиск в сети → выбор или ввод
// адреса → пульт. EXTRA_ASK — сразу экран адреса («Другой адрес» из пульта).
class StartActivity : Activity() {
    private val scope = MainScope()
    private lateinit var prefs: Prefs

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        go(if (intent.getBooleanExtra(EXTRA_ASK, false)) Step.AskAddress else StartFlow.first(prefs.base))
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun go(step: Step) {
        when (step) {
            is Step.CheckSaved -> {
                waiting(getString(R.string.connecting))
                scope.launch { go(StartFlow.afterSavedCheck(step.base, Status.ok(step.base))) }
            }
            Step.Search -> {
                waiting(getString(R.string.searching))
                scope.launch { go(StartFlow.afterSearch(ServerFinder(this@StartActivity).find())) }
            }
            is Step.Choose -> choose(step.list)
            Step.AskAddress -> ask(null, null)
            is Step.Open -> open(step.base)
        }
    }

    private fun waiting(text: String) {
        val c = Screens.column(this)
        c.addView(ProgressBar(this))
        c.addView(Screens.note(this, text))
        setContentView(c)
    }

    private fun choose(list: List<Found>) {
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.choose_server)))
        list.forEach { f -> c.addView(Screens.button(this, f.name + "\n" + f.base.removePrefix("http://").removeSuffix("/")) { open(f.base) }) }
        c.addView(Screens.button(this, getString(R.string.other_address)) { ask(null, null) })
        setContentView(c)
        c.getChildAt(1)?.requestFocus()
    }

    // ask — экран «Адрес Kinodom»: поле, «Подключиться», «Искать снова»; error — строка под полем, typed — что
    // ввели (после ошибки остаётся в поле), иначе запомненный адрес.
    private fun ask(error: String?, typed: String?) {
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.address_title)))
        val field = EditText(this).apply {
            hint = getString(R.string.address_hint)
            setHintTextColor(getColor(R.color.muted))
            setTextColor(getColor(R.color.text))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            imeOptions = EditorInfo.IME_ACTION_GO
            isSingleLine = true
            setText(typed ?: prefs.base?.removePrefix("http://")?.removeSuffix("/").orEmpty())
            layoutParams = LinearLayout.LayoutParams(Screens.dp(this@StartActivity, 420), LinearLayout.LayoutParams.WRAP_CONTENT)
        }
        c.addView(field)
        if (error != null) c.addView(Screens.note(this, error, error = true))
        val connect = { connect(field.text.toString()) }
        field.setOnEditorActionListener { _, action, event ->
            if (action == EditorInfo.IME_ACTION_GO || event?.keyCode == KeyEvent.KEYCODE_ENTER) {
                connect()
                true
            } else {
                false
            }
        }
        c.addView(Screens.button(this, getString(R.string.connect)) { connect() })
        c.addView(Screens.button(this, getString(R.string.search_again)) { go(Step.Search) })
        setContentView(c)
        field.requestFocus()
    }

    private fun connect(input: String) {
        when (val p = ServerAddress.parse(input)) {
            Parsed.Bad -> ask(getString(R.string.bad_address), input)
            is Parsed.Ok -> {
                waiting(getString(R.string.connecting))
                scope.launch {
                    if (Status.ok(p.base)) open(p.base) else ask(getString(R.string.no_answer_at, input.trim()), input)
                }
            }
        }
    }

    private fun open(base: String) {
        prefs.base = base
        startActivity(Intent(this, PultActivity::class.java).putExtra(PultActivity.EXTRA_BASE, base))
        finish()
    }

    companion object {
        const val EXTRA_ASK = "ask"
    }
}
