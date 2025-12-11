# Cumulus Migration Tool

Nástroj pro migraci souborů ze staré verze Cumulus do nové verze pomocí REST API.

## Vlastnosti

- **Paralelní zpracování**: Podporuje více současně běžících workerů pro maximální rychlost
- **Odeslání přes API**: Odesílá soubory přes `/v2/files/upload` endpoint místo přímého zápisu do databáze
- **BZ2 dekomprese**: Automaticky dekomprimuje BZ2 soubory
- **Zachování metadat**: Zachovává původní ID, tagy a další metadata
- **Pooling spojení**: HTTP client s connection poolingem pro lepší výkon

## Použití

```bash
./build/migrate_cumulus \
  -db-host <source-db-host> \
  -db-port <source-db-port> \
  -db-user <source-db-user> \
  -db-pass <source-db-password> \
  -db-name <source-db-name> \
  -files-path <path-to-source-files> \
  -api-host <cumulus-api-host> \
  -api-port <cumulus-api-port> \
  -workers <number-of-workers> \
  -limit <max-files-to-migrate>
```

## Parametry

### Povinné:
- `-db-host`: IP adresa zdrojové MySQL databáze
- `-db-user`: Uživatelské jméno pro zdrojovou databázi
- `-db-name`: Název zdrojové databáze
- `-files-path`: Cesta ke zdrojovým souborům

### Volitelné:
- `-db-port`: Port zdrojové databáze (výchozí: 3306)
- `-db-pass`: Heslo pro zdrojovou databázi
- `-api-host`: IP/hostname Cumulus API serveru (výchozí: localhost)
- `-api-port`: Port Cumulus API serveru (výchozí: 8080)
- `-workers`: Počet paralelních workerů (výchozí: 10)
- `-limit`: Maximum souborů k migraci (výchozí: 10000)

## Příklad

```bash
./build/migrate_cumulus \
  -db-host 192.168.1.100 \
  -db-port 3306 \
  -db-user cumulus \
  -db-pass secretpass \
  -db-name cumulus_old \
  -files-path /mnt/old-cumulus/files \
  -api-host localhost \
  -api-port 8080 \
  -workers 20 \
  -limit 50000
```

## Jak to funguje

1. **Načtení ze zdrojové DB**: Načte seznam souborů z MySQL databáze (tabulka `filenames` a `raw_files`)
2. **Paralelní zpracování**: Vytvoří pool workerů pro paralelní zpracování
3. **Pro každý soubor**:
   - Otevře zdrojový BZ2 soubor
   - Dekomprimuje obsah
   - Vytvoří multipart/form-data request
   - Odešle na `/v2/files/upload` endpoint
   - Předá `old_cumulus_id`, `tags` a další metadata
4. **Reporting**: Loguje úspěšné i neúspěšné migrace s celkovou statistikou

## Optimalizace výkonu

- **Počet workerů**: Nastavte podle počtu CPU jader a rychlosti síťového připojení (doporučeno 10-50)
- **HTTP pooling**: HTTP client používá connection pooling pro znovupoužití spojení
- **Paralelní načítání**: Každý worker načítá a odesílá nezávisle
- **Timeout**: 5 minut timeout pro každý request (lze upravit v kódu)

## Logování

Nástroj loguje:
- Počet načtených souborů
- Progress každého workera
- Úspěšné i neúspěšné migrace
- Celkovou statistiku s dobou běhu

Příklad výstupu:
```
2025/12/11 10:00:00 Loaded 1000 files to migrate. Starting migration with 10 workers...
2025/12/11 10:00:01 [Worker 3] SUCCESS: document.pdf (ID: 12345)
2025/12/11 10:00:01 [Worker 5] ERROR: image.jpg (ID: 12346) - source file not found
2025/12/11 10:05:30 Migration completed in 5m30s. Success: 995, Errors: 5, Total: 1000
```

## Bezpečnost

- Podporuje heslo pro databázi
- Komunikuje s API přes HTTP (pro HTTPS změňte v kódu `http://` na `https://`)
- Neprovádí žádné změny ve zdrojové databázi (pouze čtení)

## Troubleshooting

### "Source file not found"
- Zkontrolujte cestu v `-files-path`
- Ověřte, že soubory mají správnou strukturu: `<rounded_id>/<raw_id>.bz2`

### "API returned status 500"
- Zkontrolujte, že Cumulus server běží
- Ověřte správnost `-api-host` a `-api-port`
- Zkontrolujte logy Cumulus serveru

### Pomalá migrace
- Zvyšte počet workerů (`-workers`)
- Zkontrolujte síťové připojení
- Ověřte výkon Cumulus serveru
