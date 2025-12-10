# Cumulus3

## Přegenerování SWAGGER

```bash
swag init -g src/cmd/volume-server/main.go
```

## Spuštění s hot-refresh

```bash
air
```

## Build projektu

```bash
go build ./...
```

## Instalace

```bash
go mod tidy
go install github.com/air-verse/air@latest
go install github.com/swaggo/swag/cmd/swag@latest
```

Poté je potřeba přidat cestu na binárky Go do PATH

- Přidej řádek na konec souboru ~/.bashrc (nebo svého shellu)

```BASH
export PATH=$PATH:$(go env GOPATH)/bin to .bashrc
```

## kompilace všeho

```bash
mkdir -p build && \
go build -o build/migrate_cumulus ./src/cmd/migrate_cumulus && \
go build -o build/recovery-tool ./src/cmd/recovery-tool && \
go build -o build/volume-server ./src/cmd/volume-server
```
