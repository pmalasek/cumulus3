# Cumulus3 - Produkční Nasazení

## Rychlý start

```bash
# 1. Příprava environment souboru
cp .env.production .env
# Upravte hodnoty v .env dle potřeby (zejména hesla!)

# 2. Build a spuštění všech služeb
docker-compose up -d

# 3. Sledování logů
docker-compose logs -f cumulus3

# 4. Kontrola stavu
docker-compose ps

# 5. Zastavení
docker-compose down
```

## Konfigurace

### Environment proměnné

| Proměnná | Výchozí | Popis |
|----------|---------|-------|
| `SERVER_ADDRESS` | `0.0.0.0` | IP adresa serveru |
| `SERVER_PORT` | `8080` | Port serveru |
| `DB_PATH` | `/app/data/database/cumulus3.db` | Cesta k databázi |
| `DATA_DIR` | `/app/data/volumes` | Adresář pro volume soubory |
| `DATA_FILE_SIZE` | `100MB` | Max. velikost jednoho volume |
| `MAX_UPLOAD_FILE_SIZE` | `50MB` | Max. velikost uploadu |
| `USE_COMPRESS` | `Auto` | Režim komprese (Auto/Force/Never) |
| `MINIMAL_COMPRESSION` | `10` | Min. úspora pro kompresi (%) |

### Volumes

- `cumulus3-data` - Persistentní úložiště pro databázi a data
- `prometheus-data` - Metriky (pokud aktivní)
- `grafana-data` - Grafana konfigurace (pokud aktivní)

## Struktura dat

```
/app/data/
├── database/
│   └── cumulus3.db          # SQLite databáze
├── volumes/
│   ├── volume_001.dat       # Data soubory
│   └── volume_002.dat
└── metadata/
    └── operations.log        # Recovery log
```

## Monitoring

### Přístup k rozhraním

- **Cumulus3 API**: http://localhost:8080
- **Swagger dokumentace**: http://localhost:8080/swagger/index.html
- **Prometheus**: http://localhost:9090
- **Grafana**: http://localhost:3000

### Grafana přihlášení

- Uživatel: `admin`
- Heslo: `changeme` (změňte v production!)

## Backup & Recovery

### Backup databáze

```bash
# Online backup
docker exec cumulus3-volume-server /app/migrate_cumulus backup

# Manuální kopie
docker cp cumulus3-volume-server:/app/data/database/cumulus3.db ./backup/
docker cp cumulus3-volume-server:/app/data/volumes ./backup/
```

### Restore

```bash
# Recovery z metadata logu
docker exec cumulus3-volume-server /app/recovery-tool

# Restore z backupu
docker cp ./backup/cumulus3.db cumulus3-volume-server:/app/data/database/
docker cp ./backup/volumes/. cumulus3-volume-server:/app/data/volumes/
docker-compose restart cumulus3
```

## Bezpečnost

### Doporučené praktiky

1. **Změňte výchozí hesla**:
   ```bash
   export GF_ADMIN_PASSWORD="silne-heslo"
   ```

2. **Používejte HTTPS v production**:
   - Nakonfigurujte Nginx s SSL certifikáty
   - Umístěte certifikáty do `./ssl/` adresáře

3. **Nastavte firewall**:
   ```bash
   sudo ufw allow 80/tcp
   sudo ufw allow 443/tcp
   sudo ufw enable
   ```

4. **Omezení zdrojů**:
   - V production compose jsou nastaveny CPU a memory limity
   - Upravte dle HW možností serveru

## Škálování

### Horizontální škálování

Pro vícenásobné instance použijte load balancer před více Cumulus3 kontejnery.

### Vertikální škálování

Upravte limity v `docker-compose.yml`:

```yaml
deploy:
  resources:
    limits:
      cpus: '8'
      memory: 8G
```

## Troubleshooting

### Kontejner se neustále restartuje

```bash
# Zkontrolujte logy
docker-compose logs cumulus3

# Zkontrolujte health
docker inspect cumulus3-volume-server | grep -A 10 Health
```

### Databáze je zamčená

```bash
# Zkontrolujte WAL mode
docker exec cumulus3-volume-server sqlite3 /app/data/database/cumulus3.db "PRAGMA journal_mode;"

# Mělo by vrátit: wal
```

### Nedostatek místa

```bash
# Zkontrolujte velikost volumes
docker system df -v

# Vyčistěte nepoužívané zdroje
docker system prune -a --volumes
```

## Aktualizace

```bash
# Pull nový kód
git pull

# Rebuild a restart
docker-compose build --no-cache
docker-compose up -d
```

## Údržba

### Pravidelné úkoly

1. **Backup** - denně
2. **Kontrola logů** - týdně
3. **Aktualizace** - měsíčně
4. **Čištění starých dat** - dle potřeby

### Kompaktace databáze

```bash
docker exec cumulus3-volume-server sqlite3 /app/data/database/cumulus3.db "VACUUM;"
```

## Podpora

Pro problémy otevřete issue na GitHub nebo kontaktujte správce systému.
