# Cumulus

**Cumulus3** is a high-performance, distributed object storage server written in **Go**.

It is a modern implementation inspired by the **SeaweedFS** architecture (Facebook Haystack paper). The goal is to provide extremely fast storage and retrieval for millions of small files, overcoming the inode limitations of traditional file systems.

## 🚀 Key Features

* **Efficient Storage (Haystack):** Small files are merged into large "Volume" files, minimizing disk seek latency and file descriptor usage.
* **Smart Deduplication:** Automatic duplicate detection using **BLAKE2b-256**. Content is hashed during the upload stream; if a file already exists, only a new metadata reference is created without writing physical data.
* **Adaptive Compression:** Uses the state-of-the-art **Zstd** algorithm for documents and text, while intelligently skipping re-compression for already compressed media (JPG, PNG, MP4).
* **Robust Metadata:** Metadata is stored in an embedded **SQLite** database running in **WAL mode** (Write-Ahead Logging), optimized for high concurrency and thread safety.
* **Temporary Storage:** Native support for file expiration (`validity`) – perfect for temporary data sharing services.
* **Observability:** Built-in **Prometheus** metrics (`/metrics`) for monitoring upload throughput, deduplication hits, and storage usage.
* **Legacy Support:** Includes fields for migration from older systems (mapping legacy IDs).

## 🛠 Tech Stack

* **Language:** Go 1.25+
* **Database:** SQLite (`mattn/go-sqlite3` with WAL & busy timeout)
* **Hashing:** BLAKE2b (`golang.org/x/crypto`)
* **Compression:** Zstandard (`klauspost/compress`)
* **Documentation:** Swagger/OpenAPI (`swaggo`)
* **Dev Tools:** Air (Live Reload)

## 🏗 Architecture

Cumulus3 separates the concept of a **Logical File** (user view) from a **Physical Blob** (disk data).

1. **Streaming Pipeline:** Data is streamed via `io.MultiWriter` simultaneously to the hasher and the compressor.
2. **Deduplication Logic:**
    * **Hit:** If the hash exists in the `blobs` table, a new entry is added to `files` pointing to the existing blob.
    * **Miss:** Compressed data is appended to the active Volume file (thread-safe), and new entries are created in `blobs` and `files`.
3. **Concurrency:** The server handles multiple concurrent uploads using Goroutines, while SQLite serializes metadata writes efficiently using WAL mode.

## 📦 Getting Started

### Prerequisites

* Go 1.25 or higher
* GCC (required for SQLite CGO driver)

## 🚀 Produkční nasazení

```bash
# Spuštění produkčního stacku (Cumulus3 + Nginx + Prometheus + Grafana)
docker-compose up -d

# Sledování logů
docker-compose logs -f cumulus3
```

### Test Logging

Run the logging demo script:

```bash
./test-logging.sh
```

This script demonstrates different log levels (DEBUG, INFO, ERROR) and formats (text, JSON).

Detailed documentation: 
- [DEPLOYMENT.md](DEPLOYMENT.md) - Production deployment guide
- [docs/LOGGING.md](docs/LOGGING.md) - Comprehensive logging documentation

## 🛠️ VývoDevelopmentj

### Regenerate Swagger

```bash
swag init -g src/cmd/volume-server/main.go
```

## Run with Hot-Reload

```bash
air
```

## Build Project

```bash
go build ./...
```

## Installation

```bash
go mod tidy
go install github.com/air-verse/air@latest
go install github.com/swaggo/swag/cmd/swag@latest
```

Then you need to add the path to Go binaries to PATH

* Add the line to the end of the ~/.bashrc file (or your shell configuration)

```BASH
export PATH=$PATH:$(go env GOPATH)/bin to .bashrc
```

## Compile all

```bash
mkdir -p build && \
go build -o build/migrate_cumulus ./src/cmd/migrate_cumulus && \
go build -o build/recovery-tool ./src/cmd/recovery-tool && \
go build -o build/volume-server ./src/cmd/volume-server
```
