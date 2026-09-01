plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.cloudix.messenger"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.cloudix.messenger"
        minSdk = 21
        targetSdk = 34
        versionCode = 1
        versionName = "0.2.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // Sideloading, not Play: the debug key is what makes the APK
            // installable straight off a build with no keystore to manage.
            // Swap in a real signing config before distributing anything.
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }

    packaging {
        // The .aar ships one .so per ABI; keep them all so a single APK runs on
        // both arm64 phones and x86_64 emulators.
        jniLibs.useLegacyPackaging = false
    }
}

dependencies {
    // Built by ../build.sh via `gomobile bind`.
    implementation(files("../libs/cloudixmobile.aar"))
    implementation("androidx.activity:activity-ktx:1.8.2")
    implementation("androidx.core:core-ktx:1.12.0")
}
