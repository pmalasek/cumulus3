# 🛠️ VývoDevelopmentj

## Regenerate Swagger

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
go build -o build/compact-tool ./src/cmd/compact-tool && \
go build -o build/volume-server ./src/cmd/volume-server
```
