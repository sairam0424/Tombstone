plugins {
    id("java")
    id("org.jetbrains.kotlin.jvm") version "1.9.25"
    id("org.jetbrains.intellij.platform") version "2.1.0"
}

group = "io.tombstone"
version = "0.1.0"

repositories {
    mavenCentral()
    intellijPlatform { defaultRepositories() }
}

dependencies {
    intellijPlatform {
        intellijIdeaCommunity("2024.3")
        bundledPlugin("com.intellij.java")
    }
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("com.google.code.gson:gson:2.10.1")
}

intellijPlatform {
    pluginConfiguration {
        name = "Tombstone Feature Flags"
        version = "0.1.0"
        description = "Inline flag state, kill switch, and stale flag detection for Tombstone"
        ideaVersion { sinceBuild = "243"; untilBuild = "251.*" }
    }
    signing { /* keystore from env TOMBSTONE_SIGNING_KEY */ }
    publishing { token = System.getenv("JETBRAINS_PUBLISH_TOKEN") ?: "" }
}

kotlin {
    jvmToolchain(17)
}

tasks {
    buildSearchableOptions {
        enabled = false
    }
}
