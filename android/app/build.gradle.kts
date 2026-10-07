import java.util.Properties

plugins {
    id("com.android.application")
}

// Ключ подписи — вне репозитория (спека этапа 13, раздел 6): %USERPROFILE%\.kinodom\kinodom-release.properties
// (storeFile, storePassword, keyAlias, keyPassword); создаёт android\make-key.ps1. Нет файла — выпуск без подписи.
val keyProps = File(System.getProperty("user.home"), ".kinodom/kinodom-release.properties")
    .takeIf { it.isFile }
    ?.let { f -> Properties().apply { f.inputStream().use { load(it) } } }

android {
    namespace = "ru.kinodom.app"
    compileSdk = 36

    defaultConfig {
        // Имя зарегистрировано в Android Developer Console (2026-10-02, аккаунт ограниченного распространения) — без
        // предупреждения Play Защиты. Прежнее ru.kinodom.app Google уже видел на устройствах, такой аккаунт его не берёт.
        // Пакеты кода (namespace) остались ru.kinodom.app.
        applicationId = "ru.kinodom.home"
        minSdk = 24
        targetSdk = 36
        // Версия — та же, что у сервера; номер сборки — число коммитов (build.ps1 передаёт оба).
        versionName = (findProperty("versionName") as String?) ?: "0.0.0-dev"
        versionCode = (findProperty("versionCode") as String?)?.toInt() ?: 1
    }

    signingConfigs {
        if (keyProps != null) {
            create("release") {
                storeFile = File(keyProps.getProperty("storeFile"))
                storePassword = keyProps.getProperty("storePassword")
                keyAlias = keyProps.getProperty("keyAlias")
                keyPassword = keyProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
        }
    }

    // Два распространения (просьба 2026-10-07): full — «Kinodom», сервер на устройстве внутри
    // (jniLibs кладёт build.ps1 в src/full/jniLibs); client — «Kinodom Client», без сервера,
    // отдельный пакет: оба живут на одном устройстве.
    flavorDimensions += "distribution"
    productFlavors {
        create("full") { dimension = "distribution" }
        create("client") {
            dimension = "distribution"
            applicationIdSuffix = ".client"
        }
    }

    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.2")
    implementation("androidx.core:core:1.13.1") // FileProvider: установщику Android — APK обновления
    // Плеер каналов (спека этапа 13, раздел 4): HLS, DASH, поток MPEG-TS по HTTP; PlayerView — без своих кнопок.
    val media3 = "1.11.1"
    implementation("androidx.media3:media3-exoplayer:$media3")
    implementation("androidx.media3:media3-exoplayer-hls:$media3")
    implementation("androidx.media3:media3-exoplayer-dash:$media3")
    implementation("androidx.media3:media3-ui:$media3")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20260814") // в тестах на JVM org.json из Android — заглушки
}
