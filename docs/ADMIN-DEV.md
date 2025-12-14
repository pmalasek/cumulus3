# Admin Rozhraní - Technická dokumentace

## Implementace

### Přehled komponent

```
src/internal/api/
├── admin.go           - Handler pro admin UI, autentizace
├── system.go          - System API endpointy, job management
├── handlers.go        - Routing
└── static/
    ├── admin.html     - HTML UI
    └── admin.js       - JavaScript logika
```

### Architektura

#### 1. Job Management System

**Asynchronní zpracování operací:**

```go
type Job struct {
    ID          string
    Type        string      // "compact", "compact-all", "integrity-check"
    Status      JobStatus   // "pending", "running", "completed", "failed"
    Progress    string
    Error       string
    VolumeID    *int64
    StartedAt   time.Time
    CompletedAt *time.Time
}
```

**JobManager:**
- Globální singleton (`globalJobManager`)
- Thread-safe pomocí `sync.RWMutex`
- In-memory úložiště (jobs se neuchovávají po restartu)
- API pro vytváření, aktualizaci a dotazování jobů

#### 2. System API Handlers

**HandleSystemStats** - Vrací agregované statistiky
- BLOB statistiky (count, sizes, compression ratio)
- File statistiky (count, deduplication ratio)
- Storage statistiky (total, deleted, fragmentation)

**HandleSystemVolumes** - Seznam volumes
- Používá `MetadataSQL.GetVolumesToCompact(0)` pro všechny volumes
- Počítá fragmentaci pro každý volume

**HandleSystemCompact** - Spouští kompaktaci
- Podporuje single volume nebo all volumes
- Vytvoří job a spustí goroutine
- Používá `Store.CompactVolume()`

**HandleSystemJobs** - Job tracking
- Vrací seznam všech jobů nebo detail jednoho jobu
- Používá JobManager pro dotazy

**HandleSystemIntegrity** - Kontrola integrity
- Asynchronní kontrola orphaned/missing blobs
- Výsledek ukládán do job.Progress jako JSON

#### 3. Admin UI

**HTML/CSS:**
- Modern gradient design (purple theme)
- Responsive grid layout
- Card-based components
- Progress bars pro vizualizaci fragmentace
- Alert systém pro notifikace

**JavaScript:**
- Fetch API pro komunikaci se serverem
- Automatické obnovování (3s při běžících úlohách, 10s v klidu)
- Formátování bytů pomocí `formatBytes()`
- Job status tracking s barevným zvýrazněním

**Auto-refresh logika:**
```javascript
if (hasRunningJobs && !refreshInterval) {
    startAutoRefresh();  // Každé 3 sekundy
} else if (!hasRunningJobs && refreshInterval) {
    stopAutoRefresh();   // Zpět na 10 sekund
}
```

#### 4. Autentizace

**Basic Auth Middleware:**
```go
func AdminAuthMiddleware(username, password string, next http.Handler) http.Handler
```

- Používá standardní HTTP Basic Auth
- Credentials z ENV nebo default admin/admin
- Ochrana admin UI i JavaScript souboru
- System API endpointy nejsou chráněny (TODO: zvážit autentizaci)

### Použité existující funkce

**Store (storage/store.go):**
- `CompactVolume(volumeID, meta)` - Kompaktace volume

**MetadataSQL (storage/metadata.go):**
- `GetStorageStats()` - Storage statistiky
- `GetVolumesToCompact(threshold)` - Seznam volumes
- `GetDB()` - Přímý přístup k databázi pro custom queries

### Bezpečnostní úvahy

1. **Basic Auth** - Jednoduchá autentizace, vhodná pro interní nástroje
2. **System API není chráněno** - Zvážit autentizaci v produkci
3. **CORS** - Není implementován, admin UI musí být na stejné doméně
4. **Rate limiting** - Není implementován
5. **HTTPS** - Doporučeno v produkci

### Testování

**Unit testy:**
```bash
go test ./src/internal/api/...
```

**Integration testy:**
```bash
# Stats
curl http://localhost:8800/system/stats

# Compact single volume
curl -X POST http://localhost:8800/system/compact \
  -H "Content-Type: application/json" \
  -d '{"volumeId": 1}'

# Jobs
curl http://localhost:8800/system/jobs
```

**Admin UI:**
```bash
# Otevřít v prohlížeči
open http://localhost:8800/admin
# Login: admin/admin
```

### Rozšíření

#### Přidání nového typu úlohy:

1. Vytvořit handler v `system.go`:
```go
func (s *Server) HandleSystemNewOperation(w http.ResponseWriter, r *http.Request) {
    job := globalJobManager.CreateJob("new-operation", nil)
    
    go func() {
        globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Starting...", nil)
        
        // Vaše operace zde
        err := s.doNewOperation()
        
        if err != nil {
            globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
            return
        }
        
        globalJobManager.UpdateJob(job.ID, JobStatusCompleted, "Done", nil)
    }()
    
    w.WriteHeader(http.StatusAccepted)
    json.NewEncoder(w).Encode(map[string]interface{}{
        "jobId": job.ID,
        "message": "Operation started",
    })
}
```

2. Přidat route v `handlers.go`:
```go
mux.HandleFunc("/system/new-operation", s.HandleSystemNewOperation)
```

3. Přidat tlačítko v `admin.html` a funkci v `admin.js`

#### Přidání nových statistik:

1. Rozšířit SQL query v `HandleSystemStats`:
```go
var newStat int64
err = s.FileService.MetaStore.GetDB().QueryRow(`
    SELECT COUNT(*) FROM table WHERE condition
`).Scan(&newStat)
```

2. Přidat do response:
```go
stats := map[string]interface{}{
    "newSection": map[string]interface{}{
        "newStat": newStat,
    },
}
```

3. Aktualizovat UI v `admin.html` a `admin.js`

### Performance

**Considerations:**
- Job manager je in-memory - rychlý, ale nestálý
- SQL queries jsou optimalizované (indexes)
- Kompaktace používá per-volume locking
- Auto-refresh interval je konfigurovatelný

**Limits:**
- Není limit na počet současných jobů
- Jobs nejsou automaticky pročišťovány (možné memory leak při velkém počtu)
- Není podpora pro clustered deployment (jobs jsou local)

### Monitoring

**Metriky:**
- Existující Prometheus metriky se používají
- Zvážit přidání job-specific metrik:
  - Počet běžících jobů
  - Doba trvání operací
  - Chybovost

### TODOs

- [ ] Přidat rate limiting pro API
- [ ] Implementovat autentizaci pro System API
- [ ] Přidat CORS support
- [ ] Job persistence (SQLite nebo file-based)
- [ ] Job cleanup (automatické mazání starých jobů)
- [ ] Webhook notifikace po dokončení jobu
- [ ] Export statistik (CSV, JSON)
- [ ] Scheduling (cron-like) pro automatickou kompaktaci
- [ ] Multi-node support (distributed job management)
