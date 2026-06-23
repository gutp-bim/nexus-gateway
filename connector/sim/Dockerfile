FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sim-connector ./cmd/sim-connector

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /sim-connector /sim-connector

ENTRYPOINT ["/sim-connector"]
