# Build stage
FROM golang:1.25.5-alpine AS builder

# Instalace závislostí potřebných pro kompilaci (sqlite3 vyžaduje gcc)
RUN apk add --no-cache git gcc musl-dev sqlite-dev

WORKDIR /app

# Kopírování go.mod a go.sum pro cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Kopírování zdrojového kódu
COPY . .

# Generování swagger dokumentace
RUN go install github.com/swaggo/swag/cmd/swag@latest && \
    swag init -g src/cmd/volume-server/main.go

# Build všech binárních souborů
RUN mkdir -p build && \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o build/migrate_cumulus ./src/cmd/migrate_cumulus && \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o build/recovery-tool ./src/cmd/recovery-tool && \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o build/volume-server ./src/cmd/volume-server

# Runtime stage
FROM alpine:latest

# Instalace runtime dependencies
RUN apk --no-cache add ca-certificates sqlite-libs poppler-utils

WORKDIR /app

# Vytvoření neprivilegovaného uživatele
RUN addgroup -g 1000 cumulus && \
    adduser -D -u 1000 -G cumulus cumulus && \
    mkdir -p /app/data/database /app/data/volumes && \
    chown -R cumulus:cumulus /app

# Kopírování binárních souborů z build stage
COPY --from=builder /app/build/* /app/
COPY --from=builder /app/docs /app/docs

# Přepnutí na neprivilegovaného uživatele
USER cumulus

# Exponování portu
EXPOSE 8800

# Healthcheck
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${SERVER_PORT:-8800}/health || exit 1

# Spuštění volume serveru
CMD ["/app/volume-server"]
