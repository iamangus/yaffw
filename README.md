# yaffw

Distributed media server with decoupled Control Plane (API/frontend) and Compute Plane (transcoding workers).

## Quick Start (Docker Compose)

```bash
docker compose up -d
```

This starts:
- **Postgres** on port 5432
- **Control Plane** on port 8096
- **2 Compute Plane workers**

Both Control Plane and workers share a named Docker volume (`segments`) for HLS segment files.

## Configuration

All configuration is via environment variables (K8s-style). No local config files required.

### Control Plane

| Env Var | Default | Description |
|---------|---------|-------------|
| `DATABASE_URL` | (required) | Postgres connection string |
| `SEGMENT_DIR` | `./temp/transcode` | Shared filesystem path where workers write `.ts` segments and CP serves them |
| `MEDIA_DIR` | `/media` | Read-only mount of source media library |
| `INTERNAL_TOKEN` | (none) | Shared secret for worker→CP queue API auth |

### Compute Plane (Worker)

| Env Var | Default | Description |
|---------|---------|-------------|
| `CONTROL_URL` | `http://localhost:8096` | Control Plane base URL for queue API |
| `SEGMENT_DIR` | `./temp/transcode` | Shared filesystem path — must match the CP's `SEGMENT_DIR` and be backed by the same NFS/CephFS volume |
| `INTERNAL_TOKEN` | (none) | Shared secret matching the Control Plane |

## Shared Segment Filesystem (`SEGMENT_DIR`)

### Why it's needed

Workers run FFmpeg, which writes `.ts` segment files. The Control Plane serves these files to clients via `http.ServeFile`. For this to work, **both the worker and the Control Plane must see the same filesystem path**.

### What to mount

- **Worker pods**: `SEGMENT_DIR` mounted **ReadWrite** (workers write segment files)
- **Control Plane pods**: `SEGMENT_DIR` mounted **ReadOnly** (CP reads and serves segment files)

### Supported backends

| Backend | Access Mode | Notes |
|---------|-------------|-------|
| NFS | `ReadWriteMany` | Most common; works with `nfs-subdir-external-provisioner` |
| CephFS | `ReadWriteMany` | Built-in K8s support |
| HostPath (single node) | `DirectoryOrCreate` | Dev only — doesn't work across nodes |
| Docker named volume | (compose only) | For local dev via `docker-compose.yml` |

### K8s PVC example

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: segment-storage
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: nfs-client
  resources:
    requests:
      storage: 100Gi
```

Mount in both deployments:

```yaml
# Worker deployment
volumeMounts:
  - name: segments
    mountPath: /shared/segments
    # ReadWrite — workers write .ts files here

# Control Plane deployment
volumeMounts:
  - name: segments
    mountPath: /shared/segments
    readOnly: true
```

Set `SEGMENT_DIR=/shared/segments` in both pod specs.

### How segments are served

1. Worker's FFmpeg writes `segment_000.ts` to `SEGMENT_DIR/<jobID>/segment_000.ts`
2. Worker's `watchSegments` goroutine detects the new file via the `.m3u8` manifest and calls `AddSegment` (inserts a `job_segments` DB row)
3. Client requests `/hls/<jobID>/segment_000.ts`
4. Control Plane queries `GetSegment` from Postgres (confirms FFmpeg closed the file)
5. Control Plane calls `http.ServeFile` from `SEGMENT_DIR/<jobID>/segment_000.ts`

### Worker death recovery

When a worker dies mid-stream:
- Its segment files **persist** on the shared volume (unlike the old reverse-proxy model where they were on the dead pod's local disk)
- The recovery monitor detects heartbeat timeout, marks the worker dead (increments `discontinuity_count`)
- JIT recovery resumes from the **latest segment file on disk** (via `highestSegmentOnDisk()`)
- Clients keep playing existing segments through the gap — only missing segments need re-transcoding

### Cleanup

Segment files are cleaned up by the Control Plane's session watchdog when a job is deleted (after idle retention timeout, default 300s). The watchdog calls `os.RemoveAll(SEGMENT_DIR/<jobID>)` before `DeleteJob`.

## Development

### Prerequisites

- Go 1.22+
- FFmpeg + ffprobe installed and on `PATH`
- PostgreSQL 14+

### Build & Test

```bash
go build ./...
go test ./...
go vet ./...
```

### Run locally (without Docker)

```bash
# Terminal 1: Postgres
docker run -p 5432:5432 -e POSTGRES_DB=yaffw -e POSTGRES_USER=yaffw -e POSTGRES_PASSWORD=yaffw_dev postgres:16-alpine

# Terminal 2: Control Plane
DATABASE_URL="postgres://yaffw:yaffw_dev@localhost:5432/yaffw?sslmode=disable" \
SEGMENT_DIR="./temp/transcode" \
go run ./src/ControlPlane

# Terminal 3: Worker
CONTROL_URL="http://localhost:8096" \
SEGMENT_DIR="./temp/transcode" \
go run ./src/ComputePlane
```

Both processes share `./temp/transcode` on the local filesystem.
