plugins {
    id("com.android.application") version "8.7.3" apply false
    id("org.jetbrains.kotlin.android") version "2.1.21" apply false
}

// -PbuildRoot=DIR (or buildRoot in ~/.gradle/gradle.properties) keeps the build outputs out of the tree.
providers.gradleProperty("buildRoot").orNull?.let { root ->
    allprojects { layout.buildDirectory.set(file("$root/${project.name}")) }
}
