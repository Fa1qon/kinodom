package ru.kinodom.app.ui

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.graphics.Typeface
import android.text.InputType
import android.text.method.ScrollingMovementMethod
import android.util.TypedValue
import android.view.KeyEvent
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.ScrollView
import android.widget.TextView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import ru.kinodom.app.R
import ru.kinodom.app.core.Found
import ru.kinodom.app.core.LocalServer
import ru.kinodom.app.core.Parsed
import ru.kinodom.app.core.ServerAddress
import ru.kinodom.app.core.StartFlow
import ru.kinodom.app.core.Step
import ru.kinodom.app.net.LocalServerProcess
import ru.kinodom.app.net.Prefs
import ru.kinodom.app.net.ServerFinder
import ru.kinodom.app.net.Status

// StartActivity — первый экран (спека этапа 13, раздел 3.2): запомненный адрес → поиск в сети → выбор или ввод
// адреса → пульт. EXTRA_SEARCH — сразу поиск («Другой адрес» из «Kinodom не отвечает»: запомненный не отвечает).
class StartActivity : Activity() {
    private val scope = MainScope()
    private lateinit var prefs: Prefs

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        // Автономное приложение (полный порт, план 2026-10-06): сервер встроен — никакого ввода
        // адреса и поиска по сети, своё запускается всегда; мастер первых шагов покажет сам пульт.
        // Сборка без сервера (Kinodom Client) — прежний путь: запомненный адрес, поиск.
        if (LocalServer.binary(this) != null) {
            waiting(getString(R.string.local_starting))
            scope.launch {
                val ok = withContext(Dispatchers.IO) { LocalServerProcess.start(applicationContext) }
                if (ok) open(LocalServer.baseUrl()) else localFail()
            }
            return
        }
        go(if (intent.getBooleanExtra(EXTRA_SEARCH, false)) Step.Search else StartFlow.first(prefs.base))
    }

    // localFail — свой сервер не поднялся: не отправлять в поиск по сети, дать повторить.
    // На экране — хвост журнала сервера: по нему сразу видно причину (план 2026-10-07).
    private fun localFail() {
        val c = Screens.column(this)
        c.addView(Screens.title(this, getString(R.string.local_fail)))
        val logText = try {
            val f = java.io.File(java.io.File(filesDir, "kinodom"), "server.log").takeIf { it.isFile }
            f?.readText()?.takeLast(1200) ?: getString(R.string.local_fail_note)
        } catch (e: Exception) {
            e.message ?: getString(R.string.local_fail_note)
        }
        val log = TextView(this).apply {
            setTextColor(getColor(R.color.muted))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
            typeface = Typeface.MONOSPACE
            movementMethod = ScrollingMovementMethod()
            setTextIsSelectable(true)
            text = logText
        }
        c.addView(ScrollView(this).apply {
            layoutParams = LinearLayout.LayoutParams(Screens.fieldWidth(this@StartActivity), Screens.dp(this@StartActivity, 320))
            addView(log)
        })
        c.addView(Screens.button(this, getString(R.string.retry)) {
            waiting(getString(R.string.local_starting))
            scope.launch {
                val ok = withContext(Dispatchers.IO) { LocalServerProcess.start(applicationContext) }
                if (ok) open(LocalServer.baseUrl()) else localFail()
            }
        })
        setContentView(c)
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
            layoutParams = LinearLayout.LayoutParams(Screens.fieldWidth(this@StartActivity), LinearLayout.LayoutParams.WRAP_CONTENT)
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
        const val EXTRA_SEARCH = "search"
    }
}
