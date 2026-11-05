import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// native/third_party/build.sh's output directory.
val thirdParty: String = providers.gradleProperty("thirdPartyDir")
    .getOrElse(rootProject.file("native/third_party/out").path)

// Release signing: only from keystore.properties next to settings.gradle.kts (git-ignored; storeFile,
// storePassword, keyAlias, keyPassword; storeFile absolute or relative to that file). Without it the release
// APK is unsigned.
val releaseSigning: Properties? = rootProject.file("keystore.properties").takeIf { it.isFile }?.let { f ->
    Properties().apply { f.inputStream().use { load(it) } }
}

android {
    namespace = "scouterkit.ssh"
    compileSdk = 34
    ndkVersion = "27.3.13750724"

    defaultConfig {
        applicationId = "eu.mrogalski.scouter" // the code stays in package scouterkit.ssh (JNI names)
        minSdk = 31
        targetSdk = 34
        versionCode = 1
        versionName = "0.1"
        ndk { abiFilters += "arm64-v8a" }
        externalNativeBuild {
            cmake { arguments += listOf("-DTHIRD_PARTY_DIR=$thirdParty", "-DANDROID_STL=c++_static") }
        }
    }

    // glasses: the arms over Android's Bluetooth; emulated: two emulated arms in the app (R12).
    flavorDimensions += "arms"
    productFlavors {
        create("glasses") {
            dimension = "arms"
            externalNativeBuild { cmake { arguments += "-DARMS=glasses" } }
        }
        create("emulated") {
            dimension = "arms"
            applicationIdSuffix = ".emulated"
            externalNativeBuild { cmake { arguments += "-DARMS=emulated" } }
        }
    }

    signingConfigs {
        if (releaseSigning != null) create("release") {
            storeFile = rootProject.file(releaseSigning.getProperty("storeFile"))
            storePassword = releaseSigning.getProperty("storePassword")
            keyAlias = releaseSigning.getProperty("keyAlias")
            keyPassword = releaseSigning.getProperty("keyPassword")
        }
    }

    buildTypes {
        getByName("release") {
            // No R8: JNI looks up Native, GlassesLink and their methods by name, and JSch loads its algorithms
            // by class name.
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
        }
    }

    externalNativeBuild {
        cmake {
            path = file("../native/CMakeLists.txt")
            version = "3.31.6"
            providers.gradleProperty("buildRoot").orNull?.let { buildStagingDirectory = file("$it/cxx") }
        }
    }

    // Release builds without lint: its artifacts are not among the verified dependencies (the build runs offline).
    lint { checkReleaseBuilds = false }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

android.buildFeatures.buildConfig = true
android.packaging.resources.excludes += "META-INF/versions/*/OSGI-INF/MANIFEST.MF"

// Assets and the launcher icon from elsewhere in the tree, copied at build time (the tree has no symlinks):
// NOTICE, licenses/ and icon.png next to settings.gradle.kts, the fonts from scouter_kit.
val scouterKit = rootProject.file("../..")
val copiedAssets = layout.buildDirectory.dir("generated/copied/assets")
val copiedRes = layout.buildDirectory.dir("generated/copied/res")
val copyAssets = tasks.register<Sync>("copyAssets") {
    from(rootProject.file("NOTICE"))
    from(rootProject.file("licenses")) { into("licenses") }
    from(scouterKit.resolve("third_party/departure_mono_nerd/DepartureMonoNerdFontMono-Regular.otf")) {
        rename { "departure_mono_nerd.otf" }
    }
    from(scouterKit.resolve("go/examples/term/fonts/departure_mono_nerd.skf"))
    into(copiedAssets)
}
val copyRes = tasks.register<Sync>("copyRes") {
    from(rootProject.file("icon.png")) { into("drawable-nodpi") }
    into(copiedRes)
}
android.sourceSets.getByName("main") {
    assets.srcDir(copiedAssets)
    res.srcDir(copiedRes)
}
// The merge tasks do not take task dependencies from srcDir; they all run after preBuild.
tasks.named("preBuild") { dependsOn(copyAssets, copyRes) }

dependencies {
    implementation("com.github.mwiede:jsch:2.28.8")
    implementation("org.bouncycastle:bcprov-jdk18on:1.86") // ed25519 and curve25519 for JSch on Android
}
