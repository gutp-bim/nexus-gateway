plugins {
    java
    application
}

group = "io.nexus.gateway"
version = "0.1.0"

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(21)
    }
}

repositories {
    mavenCentral()
}

val miloVersion = "0.6.16"
val natsVersion = "2.25.3"
val jacksonVersion = "2.22.0"

dependencies {
    implementation("org.eclipse.milo:sdk-client:$miloVersion")
    implementation("org.eclipse.milo:stack-client:$miloVersion")
    implementation("io.nats:jnats:$natsVersion")
    implementation("com.fasterxml.jackson.core:jackson-databind:$jacksonVersion")
    implementation("org.slf4j:slf4j-api:2.0.18")
    runtimeOnly("ch.qos.logback:logback-classic:1.5.35")

    testImplementation(platform("org.junit:junit-bom:6.1.1"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

application {
    mainClass = "io.nexus.gateway.opcua.Main"
}

tasks.test {
    useJUnitPlatform()
    testLogging { events("passed", "failed", "skipped") }
}
