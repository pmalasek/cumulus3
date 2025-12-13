# Inteligentní využití volného místa (Space Reuse)

## Popis

Systém inteligentně využívá místo v existujících volumes místo vytváření nových. Po kompakci se volumes zmenší (truncate) a nové soubory je postupně doplňují až do maximální velikosti (DATA_FILE_SIZE).

## Jak to funguje

### 1. Kompakce a Truncate

Při kompaktaci volume:

1. **Kopírování aktivních dat** - pouze živé bloby se zkopírují do nového souboru
2. **Aktualizace metadat** - offsety v databázi se přepočítají
3. **Truncate** - soubor se "setřepe" na skutečnou velikost dat
4. **Výsledek** - volume má volné místo do DATA_FILE_SIZE

Příklad:

```bash
Před kompakci:  volume_1.dat = 70 MB (50 MB aktivní + 20 MB smazané)
Po kompakci:    volume_1.dat = 50 MB (truncate)
Volné místo:    20 MB (70 MB - 50 MB)
```

Viz [compact.go](src/internal/storage/compact.go).

### 2. Inteligentní výběr volume při zápisu

Při ukládání nového souboru funkce `WriteBlob`:

1. **Hledá první volume s dostatečným místem** (od volume 1 až po current)
2. **Používá databázové hodnoty** `size_total` z tabulky `volumes` (zdroj pravdy)
3. **Zapisuje do prvního vhodného** volume kde `size_total + nová_data <= DATA_FILE_SIZE`
4. **Pokud jsou všechny plné** - vytvoří nový volume

Příklad:

```bash
volume_1: 50 MB (z 70 MB max) ✓ má místo → zapíše sem
volume_2: 69 MB (z 70 MB max) ✓ má místo, ale volume_1 je první
volume_3: 70 MB (plný)
```

Výhody:

- Volumes se plní postupně od nejstaršího
- Žádné "díry" v číslování
- Méně souborů celkově

Viz [store.go](src/internal/storage/store.go) funkce `findVolumeWithSpaceNoLock`.

### 3. Automatická recalkulace

Po kompakci se automaticky přepočítá "current volume":

- Najde první volume s volným místem
- Přepne na něj pro další zápisy
- Starší volumes se doplňují před vytvořením nových

Viz [store.go](src/internal/storage/store.go) funkce `RecalculateCurrentVolume`.

## API změny

### Store

```go
// Najde první volume s dostatkem místa (od 1 do current)
func (s *Store) findVolumeWithSpaceNoLock(requiredSize int64) int64

// Přepočítá current volume na první s volným místem
func (s *Store) RecalculateCurrentVolume()

// Interní verze bez locku (pro volání v CompactVolume)
func (s *Store) recalculateCurrentVolumeNoLock()

// WriteBlob nyní automaticky hledá volume s místem
func (s *Store) WriteBlob(blobID int64, data []byte, compressionAlg uint8) (volumeID, offset int64, err error)
```

### Compact

Po kompakci:

1. Soubor se truncate na skutečnou velikost
2. Automaticky se přepočítá current volume
3. Nové zápisy jdou do zkompaktovaných volumes

## Výhody

1. **Jednoduchost**: Žádné složité trackování, žádná extra tabulka
2. **Efektivní**: Volumes se plní postupně od nejstaršího
3. **Méně souborů**: Doplňuje existující místo vytváření nových
4. **Okamžité uvolnění**: Truncate po kompakci uvolní disk space
5. **Automatické**: Žádná manuální konfigurace

## Limitace

1. **Append-only**: Data se stále pouze připojují na konec
2. **Fragmentace**: Při častém mazání vzniká → řeší kompakce
3. **Kompakce nutná**: Pro optimální využití pravidelná kompakce

## Konfigurace

Maximální velikost volume v `.env`:

```env
DATA_FILE_SIZE=70MB
```

Po kompakci se volume truncate na skutečnou velikost a postupně se doplňuje až do DATA_FILE_SIZE.

## Monitoring

Sledování využití volumes:

```bash
# Seznam volumes s fragmentací
./build/compact-tool volumes list

# Kompakce volumes s vysokou fragmentací
./build/compact-tool volumes compact-all --threshold 20
```

SQL dotazy:

```sql
-- Informace o volumes
SELECT id, size_total, size_deleted, 
       ROUND(CAST(size_deleted AS FLOAT) / size_total * 100, 1) as fragmentation_pct
FROM volumes 
WHERE size_total > 0
ORDER BY id;

-- Celková statistika
SELECT 
    SUM(size_total) as total_size,
    SUM(size_deleted) as deleted_size,
    SUM(size_total - size_deleted) as used_size
FROM volumes;
```

## Workflow

Typický životní cyklus:

1. **Upload** → zapisuje do prvního volume s místem
2. **Mazání** → zvyšuje `size_deleted` v databázi
3. **Kompakce** → přepíše volume jen s aktivními daty + truncate
4. **Upload** → pokračuje v plnění zkompaktovaného volume
