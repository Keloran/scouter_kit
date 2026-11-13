import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Release signing: only from keystore.properties next to settings.gradle.kts (git-ignored; storeFile,
// storePassword, keyAlias, keyPassword; storeFile absolute or relative to that file). Without it the release
// APK is unsigned.
val releaseSigning: Properties? = rootProject.file("keystore.properties").takeIf { it.isFile }?.let { f ->
    Properties().apply { f.inputStream().use { load(it) } }
}

android {
    namespace = "scouterkit.chewedfeed"
    compileSdk = 34
    ndkVersion = "27.3.13750724"

    defaultConfig {
        // Not SSH Scouter's ID, so both install side by side. The code stays in scouterkit.chewedfeed (JNI names).
        applicationId = "com.chewedfeed.glasses"
        minSdk = 31
        targetSdk = 34
        versionCode = 1
        versionName = "0.1"
        ndk { abiFilters += "arm64-v8a" }
        externalNativeBuild { cmake { arguments += "-DANDROID_STL=c++_static" } }
    }

    // glasses: the arms over Android's Bluetooth (SCOUTER_G1_NO_BLUEZ); emulated: two emulated arms in the app
    // (SCOUTER_G1_EMULATOR).
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
            // No R8: JNI looks up Native and its methods by name.
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

    lint {
        checkReleaseBuilds = false // release lint's artifacts are not among the verified dependencies
        warningsAsErrors = true
        disable += "ChromeOsAbiSupport" // arm64 only, like SSH Scouter
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

android.buildFeatures.buildConfig = true

// NOTICE, LICENSE and the third-party licences, copied at build time (the tree has no symlinks).
val scouterKit = rootProject.file("../..")
val copiedAssets = layout.buildDirectory.dir("generated/copied/assets")
val copyAssets = tasks.register<Sync>("copyAssets") {
    from(rootProject.file("NOTICE"))
    from(rootProject.file("LICENSE"))
    for (name in listOf("departure_mono", "concurrentqueue", "tinycrypt")) {
        from(scouterKit.resolve("third_party/$name")) {
            include("LICENSE*", "NOTICE")
            into("licenses/$name")
        }
    }
    into(copiedAssets)
}
android.sourceSets.getByName("main") { assets.srcDir(copiedAssets) }
// The merge tasks do not take task dependencies from srcDir; they all run after preBuild.
tasks.named("preBuild") { dependsOn(copyAssets) }

dependencies {
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303") // the JVM tests read the shared handover fixtures
}

// Devices with 16 KB memory pages (Android 15+) refuse native libraries whose LOAD segments are aligned to less:
// every packaged .so is checked after it is stripped (native/CMakeLists.txt links with max-page-size=16384).
abstract class CheckPageAlignment : DefaultTask() {
    @get:InputFiles abstract val libraries: ConfigurableFileCollection

    @TaskAction
    fun check() {
        for (f in libraries.asFileTree.matching { include("**/*.so") }) {
            val b = ByteBuffer.wrap(f.readBytes()).order(ByteOrder.LITTLE_ENDIAN)
            require(b.getInt(0) == 0x464c457f && b.get(4).toInt() == 2) { "$f: not a 64-bit ELF file" }
            val phoff = b.getLong(0x20).toInt()
            val size = b.getShort(0x36).toInt()
            for (i in 0 until b.getShort(0x38).toInt()) {
                val ph = phoff + i * size
                val align = b.getLong(ph + 0x30)
                if (b.getInt(ph) == 1 && align < 16384) throw GradleException("$f: LOAD segment aligned to $align bytes, not 16 KB")
            }
        }
    }
}
android.applicationVariants.all {
    val variant = name.replaceFirstChar { it.uppercase() }
    val check = tasks.register<CheckPageAlignment>("check${variant}PageAlignment") {
        libraries.from(tasks.named("strip${variant}DebugSymbols"))
    }
    tasks.named("assemble$variant") { dependsOn(check) }
}

// HandoverInteropTest runs against the Go peer (go/examples/internal/handover/peertest) when it is given:
// ./gradlew test -Ppeertest=/path/to/peertest. Without it that test is skipped.
tasks.withType<Test>().configureEach {
    providers.gradleProperty("peertest").orNull?.let { systemProperty("handover.peertest", it) }
}
