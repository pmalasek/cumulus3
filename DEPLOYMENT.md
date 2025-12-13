# Cumulus3 - Produkční Nasazení

## 📋 Předpoklady

- Docker Engine 20.10+ a Docker Compose v2+
- Minimálně 2GB volného RAM
- Minimálně 10GB diskového prostoru pro data
- Linux server (doporučeno Ubuntu 20.04+)
- Root nebo sudo přístup

## 🚀 Kompletní Postup Nasazení

### Krok 1: Příprava serveru

```bash
# Aktualizace systému
sudo apt update && sudo apt upgrade -y

# Instalace Dockeru (pokud ještě není nainstalován)
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh

# Přidání uživatele do docker skupiny
sudo usermod -aG docker $USER
newgrp docker

# Ověření instalace
docker --version
docker compose version
```

### Krok 2: Stažení projektu

```bash
# Clone repozitáře
git clone https://github.com/pmalasek/cumulus3.git
cd cumulus3

# Nebo aktualizace existující instalace
git pull origin main
```

### Krok 3: Konfigurace environment

```bash
# Vytvoření .env souboru ze šablony
cp .env.production .env

# Editace konfigurace
nano .env
```

**Důležité nastavení v `.env`:**

```bash
# Síťová konfigurace
SERVER_ADDRESS=0.0.0.0
SERVER_PORT=8800

# Cesty k datům (ve Docker kontejneru)
DB_PATH=/app/data/database/cumulus3.db
DATA_DIR=/app/data/volumes

# Úložiště - upravte dle potřeby!
DATA_FILE_SIZE=10GB              # Velikost jednoho volume souboru
MAX_UPLOAD_FILE_SIZE=500MB       # Max velikost jednoho uploadu

# Komprese
USE_COMPRESS=Auto                # Auto/Force/Never
MINIMAL_COMPRESSION=10           # Minimální úspora v %

# Logování - DŮLEŽITÉ pro produkci!
LOG_LEVEL=INFO                   # DEBUG | INFO | WARN | ERROR
LOG_FORMAT=json                  # text | json (json doporučeno pro centralizované logy)
LOG_COLOR=false                  # false pro produkci (true pouze pro dev)

# Grafana přístup - ZMĚŇTE V PRODUKCI!
GF_ADMIN_USER=admin
GF_ADMIN_PASSWORD=changeme       # ← Změňte toto heslo!
```

**Poznámky k logování:**
- `LOG_LEVEL=INFO` je doporučený pro produkci (sbalancované množství informací)
- `LOG_FORMAT=json` umožňuje snadné parsování v Grafana Loki, ELK, Splunk atd.
- `LOG_COLOR=false` vypne ANSI barvy v Docker logs
- Pro debugging změňte na `LOG_LEVEL=DEBUG` a `LOG_FORMAT=text`
- Podrobnou dokumentaci k logování viz [docs/LOGGING.md](docs/LOGGING.md)

### Krok 4: SSL certifikáty (volitelné)

**Pouze pokud chcete aktivovat lokální Nginx** (pro většinu případů použijte váš centrální Nginx):

Pro HTTPS v produkci připravte SSL certifikáty:

```bash
# Vytvoření adresáře pro certifikáty
mkdir -p ssl

# Možnost A: Let's Encrypt (doporučeno)
# Použijte certbot nebo jiný ACME klient
sudo certbot certonly --standalone -d vase-domena.cz

# Zkopírujte certifikáty
sudo cp /etc/letsencrypt/live/vase-domena.cz/fullchain.pem ssl/cert.pem
sudo cp /etc/letsencrypt/live/vase-domena.cz/privkey.pem ssl/key.pem
sudo chown $USER:$USER ssl/*.pem

# Možnost B: Self-signed certifikát (pouze pro testování)
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout ssl/key.pem -out ssl/cert.pem
```

Poté odkomentujte Nginx sekci v `docker-compose.yml`.

> **💡 Tip**: Pro produkční nasazení je obvykle lepší použít centrální Nginx/reverse proxy server než lokální Nginx v Docker Compose. Cumulus3 běží přímo na portu 8800 a můžete ho snadno zpřístupnit přes váš existující proxy.

### Krok 5: Build a spuštění

```bash
# Build Docker images
docker compose build

# Spuštění všech služeb na pozadí
docker compose up -d

# Sledování logů (Ctrl+C pro ukončení sledování)
docker compose logs -f cumulus3
```

### Krok 6: Ověření funkčnosti

```bash
# Kontrola běžících kontejnerů
docker compose ps

# Měli byste vidět:
# - cumulus3 (running)
# - cumulus3-nginx (running)
# - cumulus3-prometheus (running)
# - cumulus3-grafana (running)

# Test health endpointu
curl http://localhost:8800/health

# Mělo by vrátit: {"status":"ok"}
```

### Krok 7: Konfigurace firewallu

```bash
# Povolení Cumulus3 portu pro centrální Nginx/Prometheus
sudo ufw allow 8800/tcp      # Cumulus3 API

# Pokud používáte lokální monitoring (zakomentovaný v docker-compose.yml):
# sudo ufw allow from 10.0.0.0/8 to any port 3000  # Grafana
# sudo ufw allow from 10.0.0.0/8 to any port 9090  # Prometheus

# Pokud používáte lokální Nginx (zakomentovaný v docker-compose.yml):
# sudo ufw allow 80/tcp       # HTTP
# sudo ufw allow 443/tcp      # HTTPS

# Aktivace firewallu
sudo ufw enable

# Kontrola stavu
sudo ufw status
```

### Krok 8: Integrace s centrálním Nginx (doporučeno)

Pokud máte centrální Nginx server, přidejte tuto konfiguraci:

```nginx
# /etc/nginx/sites-available/cumulus3
upstream cumulus3_backend {
    server 10.0.0.X:8800;  # IP serveru s Cumulus3
    # Pro load balancing přidejte další servery:
    # server 10.0.0.Y:8800;
}

server {
    listen 80;
    server_name cumulus.vase-domena.cz;
    
    # Redirect na HTTPS
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name cumulus.vase-domena.cz;
    
    ssl_certificate /etc/letsencrypt/live/cumulus.vase-domena.cz/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/cumulus.vase-domena.cz/privkey.pem;
    
    client_max_body_size 500M;  # Stejné jako MAX_UPLOAD_FILE_SIZE
    
    location / {
        proxy_pass http://cumulus3_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # Timeouts pro velké soubory
        proxy_connect_timeout 300;
        proxy_send_timeout 300;
        proxy_read_timeout 300;
    }
    
    location /health {
        proxy_pass http://cumulus3_backend/health;
        access_log off;
    }
}
```

Aktivace:
```bash
sudo ln -s /etc/nginx/sites-available/cumulus3 /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx
```

### Krok 9: Integrace s centrálním Prometheus

Přidejte Cumulus3 do konfigurace vašeho centrálního Prometheus serveru:

```yaml
# /etc/prometheus/prometheus.yml
scrape_configs:
  - job_name: 'cumulus3'
    static_configs:
      - targets: ['10.0.0.X:8800']  # IP serveru s Cumulus3
        labels:
          instance: 'cumulus3-prod'
          environment: 'production'
    metrics_path: '/metrics'
    scrape_interval: 15s
    
    # Pro více instancí (load balancing):
    # static_configs:
    #   - targets: ['10.0.0.1:8800', '10.0.0.2:8800', '10.0.0.3:8800']
```

Reload Prometheus:
```bash
sudo systemctl reload prometheus
# Nebo přes API
curl -X POST http://prometheus-server:9090/-/reload
```

Ověření metrik:
```bash
# Test endpointu z Prometheus serveru
curl http://10.0.0.X:8800/metrics
```

**Dostupné metriky:**
- `cumulus_storage_total_bytes` - celková velikost uložených dat
- `cumulus_storage_deleted_bytes` - velikost smazaných dat
- `cumulus_http_requests_total` - počet HTTP requestů
- `cumulus_http_request_duration_seconds` - doba trvání requestů

### Krok 10: První přístup

Otevřete v prohlížeči:

- **Cumulus3 API (přímý)**: `http://server-ip:8800/`
- **Swagger dokumentace**: `http://server-ip:8800/swagger/index.html`
- **Přes centrální Nginx**: `https://cumulus.vase-domena.cz/`
- **Metriky**: `http://server-ip:8800/metrics`
- **Centrální Prometheus**: Ověřte, že se Cumulus3 objevil v targets
- **Centrální Grafana**: Vytvořte dashboard s Cumulus3 metrikami

## 📊 Přístup k rozhraním

## Konfigurace

### Environment proměnné

| Proměnná | Výchozí | Popis |
|----------|---------|-------|
| `SERVER_ADDRESS` | `0.0.0.0` | IP adresa serveru |
| `SERVER_PORT` | `8800` | Port serveru |
| `SWAGGER_HOST` | - | Host pro Swagger UI (prázdné = použije se aktuální URL) |
| `DB_PATH` | `/app/data/database/cumulus3.db` | Cesta k databázi |
| `DATA_DIR` | `/app/data/volumes` | Adresář pro volume soubory |
| `DATA_FILE_SIZE` | `100MB` | Max. velikost jednoho volume |
| `MAX_UPLOAD_FILE_SIZE` | `50MB` | Max. velikost uploadu |
| `USE_COMPRESS` | `Auto` | Režim komprese (Auto/Force/Never) |
| `MINIMAL_COMPRESSION` | `10` | Min. úspora pro kompresi (%) |

### Volumes

- `cumulus3-data` - Persistentní úložiště pro databázi a data

> **💡 Poznámka**: Prometheus a Grafana volumes jsou zakomentovány, protože se očekává použití centrálních služeb.

## Struktura dat

```bash
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

Přístup k jednotlivým službám:

| Služba | URL | Poznámka |
|--------|-----|----------|
| **Cumulus3 API** | `http://localhost:8800` | Přímý přístup k API |
| **Přes centrální Nginx** | `https://cumulus.vase-domena.cz` | Doporučeno pro produkci |
| **Swagger UI** | `http://localhost:8800/swagger/index.html` | API dokumentace |
| **Metriky** | `http://localhost:8800/metrics` | Prometheus metriky |
| **Centrální Prometheus** | Váš Prometheus server | Přidejte Cumulus3 do scrape_configs |
| **Centrální Grafana** | Váš Grafana server | Vytvořte dashboard |

> ⚠️ **Bezpečnost**
>
> - Port 8800 je dostupný pouze z interní sítě (firewall)
> - Pro veřejný přístup používejte centrální Nginx s HTTPS
> - Metriky jsou dostupné bez autentizace - omezit firewallem

## Backup & Recovery

### Backup databáze a dat

```bash
# Vytvoření backup adresáře
mkdir -p backups/$(date +%Y%m%d)

# Backup databáze
docker exec cumulus3 sqlite3 /app/data/database/cumulus3.db ".backup '/tmp/backup.db'"
docker cp cumulus3:/tmp/backup.db backups/$(date +%Y%m%d)/cumulus3.db

# Backup volume dat
docker cp cumulus3:/app/data/volumes backups/$(date +%Y%m%d)/

# Kompletní backup (alternativa)
docker run --rm \
  --volumes-from cumulus3 \
  -v $(pwd)/backups:/backup \
  alpine tar czf /backup/cumulus3-$(date +%Y%m%d).tar.gz /app/data
```

### Automatický backup (cron)

```bash
# Vytvoření backup skriptu
cat > /usr/local/bin/cumulus3-backup.sh << 'EOF'
#!/bin/bash
BACKUP_DIR="/var/backups/cumulus3"
DATE=$(date +%Y%m%d-%H%M)
mkdir -p $BACKUP_DIR

docker run --rm \
  --volumes-from cumulus3 \
  -v $BACKUP_DIR:/backup \
  alpine tar czf /backup/cumulus3-$DATE.tar.gz /app/data

# Smazání starších než 7 dní
find $BACKUP_DIR -name "cumulus3-*.tar.gz" -mtime +7 -delete
EOF

chmod +x /usr/local/bin/cumulus3-backup.sh

# Přidání do cronu (denně ve 2:00)
echo "0 2 * * * /usr/local/bin/cumulus3-backup.sh" | sudo crontab -
```

### Restore z backupu

```bash
# Zastavení služby
docker compose stop cumulus3

# Restore z tar.gz
docker run --rm \
  --volumes-from cumulus3 \
  -v $(pwd)/backups:/backup \
  alpine sh -c "cd / && tar xzf /backup/cumulus3-YYYYMMDD.tar.gz"

# Nebo restore jednotlivých souborů
docker cp backups/20241211/cumulus3.db cumulus3:/app/data/database/
docker cp backups/20241211/volumes/. cumulus3:/app/data/volumes/

# Restart služby
docker compose start cumulus3

# Ověření
docker compose logs cumulus3
```

## Bezpečnost

### Doporučené praktiky

1. **Změňte výchozí hesla**:

   ```bash
   export GF_ADMIN_PASSWORD="silne-heslo"
   ```

3. **Používejte HTTPS v production**:
   - Nakonfigurujte Nginx s SSL certifikáty
   - Umístěte certifikáty do `./ssl/` adresáře

4. **Nastavte firewall**:

   ```bash
   sudo ufw allow 80/tcp
   sudo ufw allow 443/tcp
   sudo ufw enable
   ```

5. **Omezení zdrojů**:
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
docker compose logs cumulus3

# Podrobné informace o kontejneru
docker inspect cumulus3 | grep -A 10 Health

# Restart služby
docker compose restart cumulus3
```

### Databáze je zamčená

```bash
# Zkontrolujte WAL mode
docker exec cumulus3 sqlite3 /app/data/database/cumulus3.db "PRAGMA journal_mode;"
# Mělo by vrátit: wal

# Force checkpoint a uzavření WAL
docker exec cumulus3 sqlite3 /app/data/database/cumulus3.db "PRAGMA wal_checkpoint(TRUNCATE);"
```

### Port 8800 není dostupný

```bash
# Kontrola, zda kontejner běží
docker compose ps

# Kontrola port bindingu
docker port cumulus3

# Test z serveru
curl http://localhost:8800/health

# Kontrola firewallu
sudo ufw status
```

### Nginx vrací 502 Bad Gateway

```bash
# Zkontrolujte, zda cumulus3 běží
docker compose ps cumulus3

# Zkontrolujte logy Nginx
docker compose logs nginx

# Zkontrolujte konektivitu
docker exec nginx ping cumulus3
```

### Nedostatek místa

```bash
# Zkontrolujte velikost volumes
docker system df -v

# Vyčistěte nepoužívané zdroje
docker system prune -a --volumes
```

## 🔄 Aktualizace

### Postup aktualizace

```bash
# 1. Zálohujte data
/usr/local/bin/cumulus3-backup.sh

# 2. Stáhněte novou verzi
git pull origin main

# 3. Zastavte služby
docker compose down

# 4. Rebuild images
docker compose build --no-cache

# 5. Spusťte novou verzi
docker compose up -d

# 6. Zkontrolujte logy
docker compose logs -f cumulus3

# 7. Ověřte funkčnost
curl http://localhost:8800/health
```

### Rollback na předchozí verzi

```bash
# Najděte předchozí commit
git log --oneline -n 5

# Vraťte se na předchozí verzi
git checkout <commit-hash>

# Rebuild a restart
docker compose down
docker compose build --no-cache
docker compose up -d
```

## Údržba

### Pravidelné úkoly

1. **Backup** - denně
2. **Kontrola logů** - týdně
3. **Aktualizace** - měsíčně
4. **Čištění starých dat** - dle potřeby
5. **Kompaktace volumes** - při fragmentaci >30%

### Compact Tool - Moderní nástroj pro údržbu

Cumulus3 obsahuje vestavěný nástroj `compact-tool` pro údržbu databáze i volume souborů.

#### Přehled volumes a fragmentace

```bash
# Zobrazení všech volumes a jejich fragmentace
docker exec cumulus3-volume-server-1 /app/compact-tool volumes list
```

Výstup:
```
Volume Status:
─────────────────────────────────────────────────────────
ID       Total Size      Deleted Size    Used Size       Fragmentation Status  
─────────────────────────────────────────────────────────
1        68.5 MB         5.6 MB          62.8 MB         8.2%         OK      
2        69.1 MB         24.8 MB         44.3 MB         35.9%        OK      
3        69.9 MB         43.3 MB         26.6 MB         61.9%        OK      
```

#### Kompaktace konkrétního volume

```bash
# Kompaktace volume 3 (s vysokou fragmentací)
docker exec cumulus3-volume-server-1 /app/compact-tool volumes compact 3
```

Výstup:
```
Starting compaction of volume 3...
Before: Total=69.9 MB, Deleted=43.3 MB, Fragmentation=61.9%
After:  Total=26.6 MB, Deleted=0 B, Fragmentation=0.0%
✓ Space saved: 43.3 MB
✓ Compaction completed successfully
```

**Výhody:**
- ⚡ **Běží za provozu** - ostatní volumes jsou přístupné
- 🔒 **Per-volume locking** - bezpečné pro produkci
- 📊 **Detailní reporty** - před/po statistiky

#### Automatická kompaktace všech fragmentovaných volumes

```bash
# Kompaktace všech volumes s fragmentací >= 30%
docker exec cumulus3-volume-server-1 /app/compact-tool volumes compact-all --threshold 30
```

Výstup:
```
Found 3 volume(s) with fragmentation >= 30.0%

[1/3] Compacting volume 2 (fragmentation: 35.9%)...
  ✓ Saved: 24.8 MB

[2/3] Compacting volume 3 (fragmentation: 61.9%)...
  ✓ Saved: 43.3 MB

[3/3] Compacting volume 7 (fragmentation: 39.4%)...
  ✓ Saved: 27.4 MB

─────────────────────────────────────────────────────────
Summary: 3 succeeded, 0 failed
Total space saved: 95.5 MB
─────────────────────────────────────────────────────────
```

**Doporučení:**
- Spouštějte pravidelně (např. týdně) přes cron
- Threshold 30% je vhodný kompromis
- Kompaktace se provede během provozu bez downtime

#### Kompaktace SQLite databáze (VACUUM)

```bash
# ZASTAVENÍ serveru je povinné!
docker compose stop cumulus3

# Spuštění VACUUM
docker compose run --rm cumulus3 /app/compact-tool db vacuum

# Zpětné spuštění serveru
docker compose start cumulus3
```

Výstup:
```
⚠️  WARNING: Database VACUUM requires exclusive access!
⚠️  Please ensure the Cumulus3 server is stopped before proceeding.

Continue? (yes/no): yes

Opening database...
Database size before VACUUM: 245.3 MB
Starting VACUUM (this may take several minutes)...

✓ VACUUM completed successfully
Database size after VACUUM: 198.7 MB
Space saved: 46.6 MB (19.0%)
```

**Důležité:**
- 🛑 **Vyžaduje zastavení serveru** (downtime)
- 💾 **Potřebuje 2x tolik místa** jako velikost DB
- ⏰ **Může trvat několik minut** u velkých databází
- 📅 **Doporučeno 1x měsíčně** mimo špičku

#### Automatizace údržby přes cron

```bash
# Editace crontabu
crontab -e

# Přidání pravidelné kompaktace (každou neděli ve 2:00)
0 2 * * 0 docker exec cumulus3-volume-server-1 /app/compact-tool volumes compact-all --threshold 30 >> /var/log/cumulus3-compact.log 2>&1
```

### Legacy metoda - Přímá kompaktace databáze (zastaralé)

```bash
# Kompaktace (uvolní nevyužité místo) - zastaralá metoda
docker exec cumulus3 sqlite3 /app/data/database/cumulus3.db "VACUUM;"

# Analýza a optimalizace
docker exec cumulus3 sqlite3 /app/data/database/cumulus3.db "ANALYZE;"
```

**Poznámka:** Doporučujeme používat nový `compact-tool` namísto přímého volání sqlite3.

### Monitoring diskového prostoru

```bash
# Zobrazení využití volumes
docker system df -v

# Velikost dat Cumulus3
docker exec cumulus3 du -sh /app/data/*

# Čištění Docker cache (uvolní místo)
docker system prune -a --volumes
```

## 🔧 Běžné úkony

### Restart služeb

```bash
# Restart pouze Cumulus3
docker compose restart cumulus3

# Restart všech služeb
docker compose restart

# Graceful restart (rebuild + zero downtime)
docker compose up -d --force-recreate --no-deps cumulus3
```

### Zobrazení logů

```bash
# Real-time logy
docker compose logs -f cumulus3

# Posledních 100 řádků
docker compose logs --tail=100 cumulus3

# Všechny služby
docker compose logs -f
```

### Přístup do kontejneru

```bash
# Shell v běžícím kontejneru
docker exec -it cumulus3 sh

# Spuštění příkazů
docker exec cumulus3 ls -la /app/data
docker exec cumulus3 /app/migrate_cumulus --help
```

## 📈 Optimalizace výkonu

### Doporučené nastavení pro produkci

1. **Zvětšete volume size** pro menší fragmentaci:
   ```bash
   # V .env
   DATA_FILE_SIZE=50GB
   ```

2. **Nastavte vhodné resource limity** v `docker-compose.yml`:
   ```yaml
   deploy:
     resources:
       limits:
         cpus: '8'
         memory: 8G
   ```

3. **Použijte SSD disky** pro `/app/data` volume

4. **Monitorujte metriky** v Grafana pro optimalizaci

## ❓ FAQ

**Q: Potřebuji Nginx v docker-compose.yml?**  
A: Ne, Nginx je zakomentovaný. Pro produkci je lepší použít centrální Nginx/reverse proxy server. Cumulus3 běží přímo na portu 8800.

**Q: Potřebuji lokální Prometheus a Grafana?**  
A: Ne, jsou zakomentované. Doporučuje se použít centrální Prometheus/Grafana. Pokud chcete lokální monitoring, odkomentujte služby v `docker-compose.yml`.

**Q: Mohu změnit port 8800 na jiný?**  
A: Ano, upravte `SERVER_PORT` v `.env` a port mapping v `docker-compose.yml`.

**Q: Je možné použít externí databázi?**  
A: Aktuálně Cumulus3 používá pouze SQLite. MySQL/PostgreSQL podpora není implementována.

**Q: Jak migruji data z Cumulus verze 2?**  
A: Použijte nástroj `/app/migrate_cumulus` v kontejneru.

## 📞 Podpora

- **Issues**: [GitHub Issues](https://github.com/pmalasek/cumulus3/issues)
- **Dokumentace**: [README.md](README.md)
- **Email**: support@vase-domena.cz
