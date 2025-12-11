# Cumulus3 - Dokumentace logování

## Přehled

Cumulus3 používá centralizovaný logovací systém s podporou různých úrovní logování (log levels) a formátů výstupu.

## Konfigurace

Logování se konfiguruje pomocí proměnných prostředí v `.env` souboru:

```bash
# Úroveň logování
LOG_LEVEL=INFO              # DEBUG | INFO | WARN | ERROR

# Formát logů
LOG_FORMAT=text             # text | json

# Barevné logy (pouze pro text format)
LOG_COLOR=true              # true | false
```

### LOG_LEVEL - Úrovně logování

| Úroveň | Popis | Použití |
|--------|-------|---------|
| `DEBUG` | Nejpodrobnější logy, obsahují všechny detaily | Pouze pro vývoj a debugging |
| `INFO` | Standardní informativní zprávy | **Výchozí pro produkci** |
| `WARN` | Varování (neblokující problémy) | Produkce - sledování potenciálních problémů |
| `ERROR` | Pouze chybové zprávy | Produkce - minimální logování |

### LOG_FORMAT - Formát výstupu

#### Text (výchozí)
Čitelný formát pro lidské oko:
```
2025-12-11 14:30:45 [INFO] [UPLOAD] SUCCESS: filename=test.pdf, file_id=abc-123, dedup=false, remote=192.168.1.10
```

#### JSON (doporučeno pro produkci)
Strukturovaný formát pro centralizované nástroje (ELK, Grafana Loki):
```json
{"time":"2025-12-11 14:30:45","level":"INFO","category":"UPLOAD","message":"SUCCESS: filename=test.pdf, file_id=abc-123, dedup=false, remote=192.168.1.10"}
```

### LOG_COLOR - Barevné logy

- `true` - Barevné logy (pouze text format, dobré pro vývoj v terminálu)
- `false` - Bez barev (pro log soubory, Docker logs)
- Automaticky vypnuto pokud je nastaven `NO_COLOR` nebo `LOG_FORMAT=json`

## Kategorie logů

Cumulus3 používá následující kategorie pro snadné filtrování:

| Kategorie | Popis | Příklady |
|-----------|-------|----------|
| `STARTUP` | Inicializace aplikace | Start serveru, konfigurace |
| `UPLOAD` | Nahrávání souborů | Příjem souboru, validace, ukládání |
| `DOWNLOAD` | Stahování souborů | Čtení metadat, dekomprese, odeslání |
| `DELETE` | Mazání souborů | Smazání souboru z metadat i storage |
| `FILE_INFO` | Informace o souboru | Dotaz na metadata |
| `DOWNLOAD_OLD_ID` | Stahování dle starého ID | Kompatibilita se starou verzí |
| `SERVICE` | Servisní operace | Deduplikace, komprese, blob operace |
| `STORAGE` | Úložištní operace | Čtení/zápis blobů, validace CRC |

## Filtrování logů

### Docker Compose
```bash
# Sledování pouze chyb
docker-compose logs cumulus3 | grep ERROR

# Sledování pouze uploadů
docker-compose logs cumulus3 | grep UPLOAD

# Sledování konkrétního souboru
docker-compose logs cumulus3 | grep "file_id=abc-123"
```

### JSON logy s jq
```bash
# Pouze ERROR logy
docker-compose logs cumulus3 | jq 'select(.level=="ERROR")'

# Pouze UPLOAD kategorie
docker-compose logs cumulus3 | jq 'select(.category=="UPLOAD")'

# Chyby za poslední hodinu
docker-compose logs --since 1h cumulus3 | jq 'select(.level=="ERROR")'
```

## Příklady logovacích zpráv

### DEBUG level
```
[DEBUG] [SERVICE] Detected file type: application/pdf for file_id=abc-123
[DEBUG] [SERVICE] Compression decision: algorithm=gzip, threshold=10%, ratio=45.2%
[DEBUG] [STORAGE] Reading blob: volume_id=1, offset=12345, size=1024
```

### INFO level (výchozí)
```
[INFO] [UPLOAD] SUCCESS: filename=dokument.pdf, file_id=abc-123, dedup=false, remote=192.168.1.10
[INFO] [DOWNLOAD] SUCCESS: file_id=abc-123, filename=dokument.pdf, size=512000, mime=application/pdf, remote=192.168.1.10
[INFO] [SERVICE] Deduplication HIT: hash=blake2b:abc..., original_file_id=def-456
```

### WARN level
```
[WARN] [UPLOAD] Missing file ID from 192.168.1.10
[WARN] [FILE_INFO] File not found: file_id=invalid-id, remote=192.168.1.10
```

### ERROR level
```
[ERROR] [UPLOAD] ERROR: filename=test.pdf, remote=192.168.1.10, error=database connection failed
[ERROR] [STORAGE] Failed to read blob: volume_id=1, offset=12345, expected_size=1024, actual_size=512, error=EOF
[ERROR] [DOWNLOAD] Decompression failed: algorithm=gzip, file_id=abc-123, error=gzip: invalid header
```

## Doporučení pro produkční nasazení

### 1. Základní konfigurace
```bash
LOG_LEVEL=INFO
LOG_FORMAT=json
LOG_COLOR=false
```

### 2. Centralizované logování s Grafana Loki

#### docker-compose.yml
```yaml
cumulus3:
  # ... existující konfigurace
  logging:
    driver: "json-file"
    options:
      max-size: "10m"
      max-file: "3"
      labels: "service,environment"
```

#### Promtail konfigurace
```yaml
clients:
  - url: http://loki:3100/loki/api/v1/push

scrape_configs:
  - job_name: cumulus3
    docker_sd_configs:
      - host: unix:///var/run/docker.sock
        refresh_interval: 5s
    relabel_configs:
      - source_labels: ['__meta_docker_container_name']
        regex: '/(.*)'
        target_label: 'container'
      - source_labels: ['__meta_docker_container_label_com_docker_compose_service']
        target_label: 'service'
```

#### Grafana Loki dotazy
```logql
# Všechny ERROR logy
{service="cumulus3"} | json | level="ERROR"

# Upload statistiky
{service="cumulus3"} | json | category="UPLOAD" | level="INFO"

# Chybovost za čas
sum(count_over_time({service="cumulus3"} | json | level="ERROR" [5m]))
```

### 3. Rotace logů bez centrálního systému

#### Docker daemon.json
```json
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "5"
  }
}
```

### 4. Monitorování chyb

#### Prometheus alert
```yaml
groups:
  - name: cumulus3
    rules:
      - alert: HighErrorRate
        expr: rate(cumulus3_http_errors_total[5m]) > 0.1
        annotations:
          summary: "Vysoká chybovost v Cumulus3"
```

## Ladění problémů

### 1. Zvýšit úroveň logování
```bash
# V .env
LOG_LEVEL=DEBUG

# Restart
docker-compose restart cumulus3

# Sledování logů
docker-compose logs -f cumulus3
```

### 2. Analyzovat konkrétní soubor
```bash
# Najít všechny operace s konkrétním souborem
docker-compose logs cumulus3 | grep "file_id=abc-123"
```

### 3. Sledovat specifickou kategorii
```bash
# Pouze storage operace
docker-compose logs cumulus3 | grep "\[STORAGE\]"
```

### 4. Export logů pro analýzu
```bash
# Poslední hodina do souboru
docker-compose logs --since 1h cumulus3 > cumulus3-debug.log

# S časovými razítky
docker-compose logs -t --since 1h cumulus3 > cumulus3-debug.log
```

## Výkon a úložiště

### Dopad log levelu na výkon
- `DEBUG`: ~5-10% overhead, velké množství dat
- `INFO`: ~1-2% overhead, rozumné množství dat
- `WARN`: < 1% overhead, minimální data
- `ERROR`: zanedbatelný overhead, pouze chyby

### Odhad velikosti logů
- DEBUG: ~100-200 MB/den při 1000 req/hod
- INFO: ~10-20 MB/den při 1000 req/hod  
- WARN: ~1-2 MB/den při 1000 req/hod
- ERROR: < 1 MB/den (v případě běžného provozu)

## Bezpečnostní doporučení

1. **Nikdy nelogovat hesla nebo tokeny**
2. **JSON formát pro produkci** - snadnější parsování a filtrování citlivých dat
3. **Rotace logů** - zabránit zaplnění disku
4. **Omezený přístup k logům** - obsahují IP adresy a názvy souborů
5. **DEBUG pouze pro development** - obsahuje citlivé informace o struktuře

## Troubleshooting

### Logy se nezobrazují
```bash
# Zkontrolovat log level
docker-compose exec cumulus3 env | grep LOG_LEVEL

# Zkontrolovat formát
docker-compose exec cumulus3 env | grep LOG_FORMAT
```

### Barevné logy v Dockeru
```bash
# Vypnout barvy pro Docker
LOG_COLOR=false
```

### JSON formát neparsovatelný
```bash
# Zkontrolovat že není nastavena barva
LOG_COLOR=false
LOG_FORMAT=json
```

