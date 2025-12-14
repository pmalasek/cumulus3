# Admin Rozhraní a System API

## Přehled

Cumulus3 Storage nyní obsahuje kompletní admin rozhraní a System API pro správu a údržbu úložiště.

## Nové Komponenty

### 1. Admin Webové Rozhraní (`/admin`)

Webová stránka pro kompletní správu Cumulus3 Storage s autentizací pomocí Basic Auth.

**Přihlašovací údaje:**
- Výchozí: `admin` / `admin`
- Konfigurace přes environment proměnné: `ADMIN_USERNAME` a `ADMIN_PASSWORD`

**Funkce:**

#### Statistiky v reálném čase:
- **BLOB statistiky:**
  - Počet BLOB
  - Velikost po kompresi
  - RAW velikost
  - Kompresní poměr

- **Soubory:**
  - Počet souborů
  - Počet deduplikovaných souborů
  - Deduplikační poměr

- **Úložiště:**
  - Celková velikost
  - Použitá velikost
  - Smazaná velikost (volné místo)
  - Fragmentace

#### Správa Volumes:
- Přehled všech volumes s:
  - ID volume
  - Velikosti (celkem, použito, smazáno)
  - Fragmentace v %
  - Vizuální progress bar
- Kompaktace jednotlivých volumes
- Kompaktace všech volumes najednou

#### Kontrola Integrity:
- Kontrola orphaned blobs (bloby bez souborů)
- Kontrola missing blobs (soubory odkazující na neexistující bloby)

#### Job Tracking:
- Přehled všech běžících a dokončených úloh
- Status: pending, running, completed, failed
- Průběh operací v reálném čase
- Historie posledních 10 úloh
- Automatické obnovování při běžících úlohách (každé 3 sekundy)

### 2. System API (`/system/*`)

RESTful API pro programový přístup k údržbě úložiště.

## Endpointy

### `GET /system/stats`
Vrací kompletní statistiky úložiště.

**Response:**
```json
{
  "blobs": {
    "count": 476,
    "totalSize": 602707609,
    "rawSize": 710083850,
    "compressionRatio": 15.12
  },
  "files": {
    "count": 501,
    "deduplicatedCount": 25,
    "deduplicationRatio": 4.99
  },
  "storage": {
    "totalSize": 602755817,
    "deletedSize": 0,
    "usedSize": 602755817,
    "fragmentationRatio": 0
  }
}
```

### `GET /system/volumes`
Vrací seznam všech volumes s jejich statistikami.

**Response:**
```json
[
  {
    "id": 1,
    "totalSize": 73400302,
    "deletedSize": 0,
    "usedSize": 73400302,
    "fragmentation": 0
  }
]
```

### `POST /system/compact`
Spustí kompaktaci volume(s).

**Request - Kompaktace jednoho volume:**
```json
{
  "volumeId": 1
}
```

**Request - Kompaktace všech volumes:**
```json
{
  "all": true,
  "threshold": 20  // Volitelné: pouze volumes s fragmentací >= 20%
}
```

**Response:**
```json
{
  "jobId": "compact-1734169234",
  "message": "Compaction started"
}
```

### `GET /system/jobs`
Vrací seznam všech úloh nebo detail konkrétní úlohy.

**Query parametry:**
- `id` (volitelné): ID konkrétní úlohy

**Response - Seznam úloh:**
```json
[
  {
    "id": "compact-1734169234",
    "type": "compact",
    "status": "running",
    "progress": "Compacting volume 1",
    "volumeId": 1,
    "startedAt": "2025-12-14T09:13:54Z",
    "completedAt": null
  }
]
```

**Response - Detail úlohy:**
```json
{
  "id": "compact-1734169234",
  "type": "compact",
  "status": "completed",
  "progress": "Compaction completed",
  "volumeId": 1,
  "startedAt": "2025-12-14T09:13:54Z",
  "completedAt": "2025-12-14T09:14:12Z"
}
```

### `GET /system/integrity`
Spustí kontrolu integrity úložiště.

**Response:**
```json
{
  "jobId": "integrity-check-1734169234",
  "message": "Integrity check started"
}
```

Po dokončení úlohy lze získat výsledky pomocí `GET /system/jobs?id=integrity-check-1734169234`:
```json
{
  "id": "integrity-check-1734169234",
  "type": "integrity-check",
  "status": "completed",
  "progress": "{\"orphanedBlobs\":0,\"missingBlobs\":0,\"status\":\"ok\"}",
  "startedAt": "2025-12-14T09:15:00Z",
  "completedAt": "2025-12-14T09:15:02Z"
}
```

## Konfigurace

### Environment Proměnné

```bash
# Admin přihlašovací údaje
ADMIN_USERNAME=admin
ADMIN_PASSWORD=SecurePassword123

# Standardní konfigurace (již existující)
DB_PATH=./data/database/cumulus3.db
DATA_DIR=./data/volumes
SERVER_PORT=8800
SERVER_ADDRESS=0.0.0.0
```

## Použití

### 1. Webové Rozhraní

1. Otevřete prohlížeč na adrese: `http://localhost:8800/admin`
2. Přihlaste se pomocí admin/admin (nebo vlastních přihlašovacích údajů)
3. Dashboard se automaticky načte a zobrazí statistiky
4. Pro kompaktaci:
   - Klikněte na "🔧 Kompaktovat" u konkrétního volume
   - Nebo "🔧 Kompaktovat vše" pro kompaktaci všech volumes
5. Sledujte průběh v sekci "Běžící úlohy"

### 2. API příklady

#### Získání statistik:
```bash
curl http://localhost:8800/system/stats
```

#### Kompaktace volume:
```bash
curl -X POST http://localhost:8800/system/compact \
  -H "Content-Type: application/json" \
  -d '{"volumeId": 1}'
```

#### Kompaktace všech volumes:
```bash
curl -X POST http://localhost:8800/system/compact \
  -H "Content-Type: application/json" \
  -d '{"all": true, "threshold": 20}'
```

#### Kontrola integrity:
```bash
curl http://localhost:8800/system/integrity
```

#### Sledování úloh:
```bash
# Všechny úlohy
curl http://localhost:8800/system/jobs

# Konkrétní úloha
curl "http://localhost:8800/system/jobs?id=compact-1734169234"
```

## Asynchronní Operace

Všechny náročné operace (kompaktace, integrity check) běží asynchronně:

1. API okamžitě vrátí Job ID
2. Operace běží na pozadí
3. Stav lze sledovat pomocí `/system/jobs`
4. Operace pokračuje i po zavření admin stránky
5. Admin UI automaticky obnovuje stav při běžících úlohách

## Job Stavy

- **pending**: Úloha čeká na spuštění
- **running**: Úloha právě běží
- **completed**: Úloha úspěšně dokončena
- **failed**: Úloha selhala (error pole obsahuje důvod)

## Poznámky

- Admin rozhraní je chráněno Basic Auth
- System API endpointy **NEJSOU** chráněny (pokud potřebujete, přidejte vlastní autentizaci)
- Kompaktace běží se per-volume locking - server může běžet během kompaktace
- Každý job má unikátní ID ve formátu `{type}-{timestamp}`
- Jobs jsou uloženy v paměti - restartování serveru je vymaže
- Admin UI automaticky aktualizuje data každých 10 sekund
- Při běžících úlohách se UI aktualizuje každé 3 sekundy

## Soubory

- `src/internal/api/system.go` - System API handlers a job management
- `src/internal/api/admin.go` - Admin UI handler a autentizace
- `src/internal/api/static/admin.html` - Admin UI HTML
- `src/internal/api/static/admin.js` - Admin UI JavaScript
- `src/internal/api/handlers.go` - Routes konfigurace

## Bezpečnost

⚠️ **Důležité:**
- Změňte výchozí heslo v produkčním prostředí!
- Použijte HTTPS v produkci
- Zvažte přidání rate limitingu
- System API endpointy nejsou chráněny - zvažte vlastní autentizaci
