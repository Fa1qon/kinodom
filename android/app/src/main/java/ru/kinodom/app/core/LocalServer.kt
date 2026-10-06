package ru.kinodom.app.core

import android.content.Context
import java.io.File

// LocalServer — сервер Kinodom на этом устройстве (полный порт, план 2026-10-06): тот же код, что
// на Windows, собран для android/arm64 и запущен процессом из nativeLibraryDir (оттуда разрешён
// exec). Интерфейс — пульт приложения на 127.0.0.1; отдельный сервер в сети не обязателен.
// Память: GOMEMLIMIT держит кучу сервера в границах, а загрузка качает только то, что смотрят —
// очередь серии и «окно» кусков вокруг плейхеда, кэш кусков на диске (как у TorrServer).
object LocalServer {

    // apiPort/torrentPort — порты локального сервера: не 8090/42090, чтобы не столкнуться с
    // сервером на ПК, когда оба видны в сети.
    const val API_PORT = 8190
    const val TORRENT_PORT = 42190

    // BINARY — имя сервера в jniLibs: сборка кладёт его как lib*.so, Android сам распаковывает в
    // nativeLibraryDir, откуда разрешён запуск (из папок данных — нельзя с Android 10).
    const val BINARY = "libkinodomserver.so"

    // MEM_LIMIT — предел кучи сервера: серверные структуры и кэш каталога — умеренно, главное
    // окно кусков в памяти движка ограничено самим темпом просмотра.
    const val MEM_LIMIT = "224Mi"

    // home — папка данных сервера внутри приложения: база, загрузки, кэш.
    fun home(ctx: Context) = File(ctx.filesDir, "kinodom").absolutePath

    // binary — исполняемый файл сервера; null — этой сборки нет (апк без полной версии).
    fun binary(ctx: Context): File? {
        val f = File(ctx.applicationInfo.nativeLibraryDir, BINARY)
        return if (f.isFile) f else null
    }

    // command — командная строка запуска: сервер в консоли; папка данных — через окружение.
    // Чистая функция — для теста.
    fun command(binary: String): List<String> = listOf(binary, "run")

    // env — окружение процесса: папка данных и предел кучи. Чистая функция — для теста.
    fun env(home: String): Map<String, String> = mapOf(
        "KINODOM_HOME" to home,
        "GOMEMLIMIT" to MEM_LIMIT,
    )

    // bootstrap — содержимое kinodom.json: порты локального сервера. Чистая функция — для теста.
    fun bootstrap() = """{"apiPort":$API_PORT,"torrentPort":$TORRENT_PORT}"""

    // ensureHome — папка данных и kinodom.json с портами до первого запуска: сервер свои по
    // умолчанию ставит иначе. Возвращает папку данных.
    fun ensureHome(ctx: Context): File {
        val home = File(ctx.filesDir, "kinodom")
        home.mkdirs()
        val boot = File(home, "kinodom.json")
        if (!boot.isFile) boot.writeText(bootstrap())
        return home
    }

    // baseUrl — адрес пульта локального сервера.
    fun baseUrl() = "http://127.0.0.1:$API_PORT"
}
