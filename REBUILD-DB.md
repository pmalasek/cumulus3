# Database Rebuild Tool

Utilita pro rekonstrukci databáze z fyzických souborů (.dat, .meta, files_metadata.bin).

## Použití

```bash
./build/rebuild-db --data-dir ./data/volumes --db-path ./data/database/cumulus3_rebuilt.db
```

## Parametry

- `--data-dir` - Cesta k adresáři s volume soubory (default: `./data/volumes`)
- `--db-path` - Cesta k výstupní databázi (default: `./data/database/cumulus3_rebuilt.db`)

## Co dělá

1. **Skenuje .meta soubory** - Rychlé načtení blob indexů z každého volume
2. **Fallback na .dat skenování** - Pokud .meta chybí nebo je poškozený
3. **Čte files_metadata.bin** - Obnovuje záznamy souborů
4. **Detekuje MIME types** - Automaticky určí typ každého blobu
5. **Vytváří novou databázi** - Kompletní rebuild všech tabulek:
   - `file_types` - MIME typy a kategorie
   - `blobs` - Všechny bloby s lokacemi
   - `files` - Všechny soubory a jejich metadata
   - `volumes` - Velikosti volumes

## Kdy použít

### Disaster Recovery
```bash
# Záloha dat
cp -r data/volumes /backup/

# Databáze se zničila
rm data/database/cumulus3.db

# Rebuild
./build/rebuild-db
```

### Migrace na jinou databázi
```bash
# Rebuild do nové DB
./build/rebuild-db --db-path ./data/database/new.db

# Přepnout aplikaci na novou DB
mv data/database/cumulus3.db data/database/cumulus3.db.old
mv data/database/new.db data/database/cumulus3.db
```

### Validace konzistence
```bash
# Vytvoř referenční DB
./build/rebuild-db --db-path ./data/database/reference.db

# Porovnej s aktivní DB
sqlite3 data/database/cumulus3.db "SELECT COUNT(*) FROM blobs"
sqlite3 data/database/reference.db "SELECT COUNT(*) FROM blobs"
```

### Oprava po chybách
```bash
# Pokud se DB rozsynchronizovala s .dat soubory
./build/rebuild-db

# Zastav server
pkill volume-server

# Nahraď DB
mv data/database/cumulus3_rebuilt.db data/database/cumulus3.db

# Spusť server
./build/volume-server
```

## Výstup

```
🔨 Cumulus3 Database Rebuild Tool
===================================
Data directory: ./data/volumes
Output database: ./data/database/cumulus3_rebuilt.db

📊 Initializing database schema...

🔍 Scanning volume files...
  → Reading volume_00000001.dat (using .meta)
  → Reading volume_00000002.dat (using .meta)
  → Reading volume_00000003.dat (using .meta)
✅ Found 1234 blobs in 3 volumes

📂 Reading files metadata...
✅ Found 1000 file records

💾 Populating database...
  → Inserting blobs...
  ✅ Inserted 1234 blobs
  → Inserting files...
  ✅ Inserted 1000 files
  → Updating volume sizes...
  ✅ Updated 3 volumes

🎉 Database rebuild complete!
   Database: ./data/database/cumulus3_rebuilt.db
   Blobs: 1234
   Files: 1000
   Volumes: 3
```

## Poznámky

- **Rychlé:** Používá .meta soubory pro rychlý index
- **Spolehlivé:** Fallback na přímé skenování .dat souborů
- **Bezpečné:** Vytváří novou databázi, nemodifikuje existující
- **Automatické:** Detekuje MIME types z dat
- **Kompletní:** Obnovuje všechny tabulky včetně volume sizes

## Omezení

- **Hash placeholders:** Používá `blob_<ID>` jako hash (originální hashe nejsou v .meta)
- **MIME detekce:** Z compressed dat může být nepřesná
- **SizeRaw:** Není dostupný z .meta, nastaví se na 0

Pro produkční disaster recovery doporučujeme **pravidelné zálohy databáze**, nejen volumes!
