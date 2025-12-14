# Admin Interface and System API

## Overview

Cumulus3 Storage now includes a complete admin interface and System API for storage management and maintenance.

## New Components

### 1. Admin Web Interface (`/admin`)

Web page for complete Cumulus3 Storage management with Basic Auth authentication.

**Login Credentials:**
- Default: `admin` / `admin`
- Configuration via environment variables: `ADMIN_USERNAME` and `ADMIN_PASSWORD`

**Features:**

#### Real-time Statistics:
- **BLOB Statistics:**
  - BLOB count
  - Compressed size
  - RAW size
  - Compression ratio

- **Files:**
  - File count
  - Deduplicated files count
  - Deduplication ratio

- **Storage:**
  - Total size
  - Used size
  - Deleted size (free space)
  - Fragmentation

#### Volume Management:
- Overview of all volumes with:
  - Volume ID
  - Sizes (total, used, deleted)
  - Fragmentation in %
  - Visual progress bar
- Compact individual volumes
- Compact all volumes at once

#### Integrity Check:
- **Quick Check** - Fast metadata check (~1s):
  - Orphaned blobs (blobs without files)
  - Missing blobs (files referencing non-existent blobs)
- **Deep Check** - Deep check including physical data (slower):
  - Everything from Quick Check
  - Existence of volume files on disk
  - Blob readability at their offsets
  - Physical data validity

#### Job Tracking:
- Overview of all running and completed jobs
- Status: pending, running, completed, failed
- Real-time operation progress
- History of last 10 jobs
- Automatic refresh during running jobs (every 3 seconds)

### 2. System API (`/system/*`)

RESTful API for programmatic access to storage maintenance.

## Endpoints

### `GET /system/stats`
Returns complete storage statistics.

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
Returns list of all volumes with their statistics.

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
Starts volume(s) compaction.

**Request - Compact one volume:**
```json
{
  "volumeId": 1
}
```

**Request - Compact all volumes:**
```json
{
  "all": true,
  "threshold": 20  // Optional: only volumes with fragmentation >= 20%
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
Returns list of all jobs or detail of specific job.

**Query Parameters:**
- `id` (optional): Specific job ID

**Response - Job List:**
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

**Response - Job Detail:**
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
Starts storage integrity check.

**Query Parameters:**
- `deep=true` - Performs deep check including physical files (default: false)

**Response:**
```json
{
  "jobId": "integrity-check-1734169234",
  "message": "Integrity check started"
}
```

#### Quick Check

Default mode checks only database metadata:
- Orphaned blobs (blobs without references from files)
- Missing blobs (files referencing non-existent blobs)

Suitable for regular checks, takes ~1s even on large databases.

```bash
curl http://localhost:8800/system/integrity
```

**Result:**
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

#### Deep Check

Extended mode also checks physical integrity:
- Everything from quick check
- Existence of volume files on disk
- Blob readability at physical offsets
- Data validity in files

Suitable for thorough diagnostics, takes longer (depends on data amount).

```bash
curl "http://localhost:8800/system/integrity?deep=true"
```

**Result:**
```json
{
  "id": "integrity-check-deep-1734169234",
  "type": "integrity-check-deep",
  "status": "completed",
  "progress": "{\"orphanedBlobs\":0,\"missingBlobs\":0,\"missingVolumes\":[],\"unreadableBlobs\":0,\"totalBlobsChecked\":1000,\"status\":\"ok\"}",
  "startedAt": "2025-12-14T09:15:00Z",
  "completedAt": "2025-12-14T09:15:45Z"
}
```

**Possible States:**
- `ok` - Everything is fine
- `warning` - Minor issues (e.g., orphaned blobs)
- `error` - Serious problems (missing blobs, volume files, or unreadable data)

## Configuration

### Environment Variables

```bash
# Admin credentials
ADMIN_USERNAME=admin
ADMIN_PASSWORD=SecurePassword123

# Standard configuration (already existing)
DB_PATH=./data/database/cumulus3.db
DATA_DIR=./data/volumes
SERVER_PORT=8800
SERVER_ADDRESS=0.0.0.0
```

## Usage

### 1. Web Interface

1. Open browser at: `http://localhost:8800/admin`
2. Login using admin/admin (or your custom credentials)
3. Dashboard will automatically load and display statistics
4. For compaction:
   - Click "🔧 Compact" on specific volume
   - Or "🔧 Compact All" to compact all volumes
5. Monitor progress in "Running Jobs" section

### 2. API Examples

#### Get Statistics:
```bash
curl http://localhost:8800/system/stats
```

#### Compact Volume:
```bash
curl -X POST http://localhost:8800/system/compact \
  -H "Content-Type: application/json" \
  -d '{"volumeId": 1}'
```

#### Compact All Volumes:
```bash
curl -X POST http://localhost:8800/system/compact \
  -H "Content-Type: application/json" \
  -d '{"all": true, "threshold": 20}'
```

#### Quick Integrity Check:
```bash
curl http://localhost:8800/system/integrity
```

#### Deep Integrity Check:
```bash
curl "http://localhost:8800/system/integrity?deep=true"
```

#### Monitor Jobs:
```bash
# All jobs
curl http://localhost:8800/system/jobs

# Specific job
curl "http://localhost:8800/system/jobs?id=compact-1734169234"
```

## Asynchronous Operations

All heavy operations (compaction, integrity check) run asynchronously:

1. API immediately returns Job ID
2. Operation runs in background
3. State can be monitored using `/system/jobs`
4. Operation continues even after closing admin page
5. Admin UI automatically refreshes state during running jobs

## Job States

- **pending**: Job waiting to start
- **running**: Job currently running
- **completed**: Job successfully completed
- **failed**: Job failed (error field contains reason)

## Notes

- Admin interface is protected by Basic Auth
- System API endpoints are **NOT** protected (add your own authentication if needed)
- Compaction runs with per-volume locking - server can run during compaction
- Each job has unique ID in format `{type}-{timestamp}`
- Jobs are stored in memory - restarting server will clear them
- Admin UI automatically updates data every 10 seconds
- During running jobs, UI updates every 3 seconds

## Files

- `src/internal/api/system.go` - System API handlers and job management
- `src/internal/api/admin.go` - Admin UI handler and authentication
- `src/internal/api/static/admin.html` - Admin UI HTML
- `src/internal/api/static/admin.js` - Admin UI JavaScript
- `src/internal/api/handlers.go` - Routes configuration

## Security

⚠️ **Important:**
- Change default password in production environment!
- Use HTTPS in production
- Consider adding rate limiting
- System API endpoints are not protected - consider your own authentication
