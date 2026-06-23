FROM gradle:8.14-jdk21-alpine AS build

WORKDIR /build

# Cache dependencies separately from source
COPY settings.gradle.kts build.gradle.kts ./
RUN gradle dependencies --no-daemon -q 2>/dev/null || true

COPY src/ src/
RUN gradle installDist --no-daemon

FROM eclipse-temurin:21-jre-alpine

WORKDIR /app

COPY --from=build /build/build/install/nexus-opcua-connector/ .

ENTRYPOINT ["bin/nexus-opcua-connector"]
