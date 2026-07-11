FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mqtt-connector ./cmd/mqtt-connector

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /mqtt-connector /mqtt-connector

# Minimal health surface (#35): GET /health on HEALTH_PORT (default 8080).
EXPOSE 8080

ENTRYPOINT ["/mqtt-connector"]
