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
        applicationId = "ru.kinodom.app"
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

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.2")
    testImplementation("junit:junit:4.13.2")
}
