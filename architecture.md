# Architecture: yaffw (Next Generation)

This document outlines the architectural design for a distributed, cloud-native media server. Unlike the legacy monolithic OTHER STTREAMING APP architecture, this system decouples the Control Plane (API/Frontend) from the Compute Plane (Transcoding) and uses a shared-nothing architecture for data persistence.

## 1. System Overview

The system is designed to run on Kubernetes. It moves away from local filesystem locks and SQLite in favor of PostgreSQL for data, Redis for state coordination, and Ephemeral Microservices for video processing.

### High-Level Topology

```mermaid
graph TD
    subgraph External
        Traffic[External Traffic]
    end

    subgraph "Control Plane (Stateless)"
        LB[Load Balancer]
        API[API Replicas (Go/HTMX)]
    end

    subgraph "Compute Plane (Transcoders)"
        KEDA[KEDA Scaler]
        Worker[Transcode Workers (Go/FFmpeg)]
    end

    subgraph "Data Persistence Layer"
        PG[(PostgreSQL)]
        Redis[(Redis Cluster)]
        S3[(Object Storage - Images/Metadata)]
        PVC[(Read-Only Media PVC)]
    end

    Traffic --> LB
    LB --> API
    API --> PG
    API --> Redis
    API --> S3
    API -- Read --> PVC

    Redis -- Enqueue Job --> KEDA
    KEDA -- Scale --> Worker
    Worker -- Pop Job --> Redis
    Worker -- Read --> PVC
    Worker -- Update Status --> Redis
    API -- Stream Proxy Request --> Worker
    Worker -- Serve Segments --> API
```

## 2. Core Components

### 2.1. The Control Plane (API & Frontend)

The API layer is responsible for user management, library browsing, metadata scanning, and orchestrating playback. It performs no video processing.

*   **Technology**: Go (Golang) / `html/template` + HTMX + Tailwind CSS.
*   **State Strategy**: Stateless. No local config files. Configuration is injected via K8s ConfigMaps/Secrets.
*   **Database Interaction**: Uses `pgx` or standard `database/sql` with PostgreSQL.
*   **Authentication**: JWT Tokens passed via HTTP Headers; invalidation lists stored in Redis.
*   **Real-time Comms**: SSE (Server-Sent Events) with Redis Pub/Sub. Ensures notifications (e.g., "User B started watching X") are broadcast to all API replicas.
*   **Scaling**: Horizontal Pod Autoscaler (HPA) based on CPU/Memory usage.

### 2.2. The Compute Plane (Transcode Workers)

A specialized fleet of pods dedicated solely to running FFmpeg.

*   **Technology**: Go wrapper around ffmpeg command line tools.
*   **Storage Mounts**:
    *   **Media Volume (ReadOnly)**: Direct access to source media files (NFS/CephFS).
    *   **Ephemeral scratch space**: `emptyDir` (RAM Disk) to store HLS segments (`.ts`) and manifests (`.m3u8`).
*   **Network**: Runs a lightweight HTTP server to serve the generated HLS segments internally.
*   **Scaling**: KEDA (Kubernetes Event-driven Autoscaling). Scales from 0 to N based on the length of the Redis `transcode_jobs` queue.
*   **Hardware**: Pods utilize Kubernetes Device Plugins to request GPU resources (nvidia.com/gpu, intel.com/gpu).

### 2.3. Data & State Layer

*   **PostgreSQL**: Stores Users, Library Hierarchy, Watch Status, Plugin Configurations, and Metadata.
*   **Redis**:
    *   **Session Store**: Distributed user sessions.
    *   **Job Queue**: Transcoding tasks and Library Scan tasks.
    *   **Service Discovery**: Maps `SessionID` -> `Worker_Internal_IP` (e.g., `session_123` is handled by `10.42.0.5`).
*   **Object Storage (MinIO/S3)**: Stores images (posters, backdrops, actor photos). Removes the need for a shared configuration volume for images.

## 3. Detailed Workflows

### 3.1. Workflow: Direct Play

In this scenario, the client supports the codec natively. No transcoding is required.

1.  User requests `GET /Videos/Stream/file.mkv`.
2.  Ingress routes to **API Pod A**.
3.  API Pod A queries **Postgres** for file permissions.
4.  API Pod A calculates the byte range.
5.  API Pod A reads directly from the **Read-Only Media PVC** and streams bytes to the user.

### 3.2. Workflow: Transcoding (The Decoupled Pipeline)

The user requires transcoding due to incompatible codecs or bitrate limits.

1.  **Request**: User requests `GET /Videos/Stream.m3u8` (HLS Manifest).
2.  **Decision**: API Pod A determines transcoding is needed.
3.  **Queuing**:
    *   API Pod A generates a `JobPayload` (Source Path, Target Codec, Bitrate, StartTime).
    *   Pushes payload to Redis List `queue:transcode`.
    *   Subscribes to Redis Channel `events:session_123` to await worker readiness.
4.  **Scaling**: KEDA detects 1 item in the queue and scales **Worker Deployment** from 0 -> 1.
5.  **Processing**:
    *   **Worker Pod 1** starts, pops the job.
    *   Starts FFmpeg process.
    *   FFmpeg writes `segment_0.ts` to local RAM disk.
    *   Worker Pod 1 starts internal HTTP server on port 8080.
    *   Worker Pod 1 publishes "Ready" event to Redis and sets key `map:session_123 = 10.42.5.5:8080`.
6.  **Streaming**:
    *   API Pod A receives "Ready" signal.
    *   API Pod A acts as a **Reverse Proxy**.
    *   API fetches `http://10.42.5.5:8080/stream.m3u8` and forwards to User.
7.  **Failover**: If User requests segment 5, and the Load Balancer routes them to **API Pod B**, Pod B simply looks up `map:session_123` in Redis, finds the Worker IP, and proxies the stream. The user session is sticky to the stream, not the API pod.

## 4. Database Schema Design (Migration from SQLite)

The move to Postgres requires a schema refactor to ensure referential integrity and concurrency.

*   **Users**: Standard auth table.
*   **MediaItems**: The core table. Hierarchy is managed via `ParentId` (Adjacency List) or `Path` (Materialized Path).
    *   *Constraint*: Unique Index on `(Path, HeaderHash)` to prevent duplicates.
*   **MediaStreams**: 1:N relationship with `MediaItems`. Stores codec info (codec, channels, bitrate).
*   **ActivityLog**: High-volume write table. Partitioned by date.
*   **UserUserData**: Stores `Played`, `PlaybackPosition`, `IsFavorite`. Keyed by `(UserId, MediaItemId)`.

## 5. Deployment Strategy

### Kubernetes Manifests

*   **StatefulSet (Database)**: CloudNativePG Cluster or Helm Chart.
*   **Deployment (API)**:
    *   Replicas: 3
    *   ReadinessProbe: Checks DB connection.
    *   VolumeMounts: `/media` (ReadOnly).
*   **Deployment (Workers)**:
    *   Replicas: 0 (Managed by KEDA).
    *   Tolerations: `gpu-node=true`.
    *   VolumeMounts: `/media` (ReadOnly).
*   **Service (Internal)**: ClusterIP for API and Redis.
*   **Service (Headless)**: For Worker discovery (optional, if not using direct IP lookup).

## 6. Benefits over Legacy Architecture

| Feature | Legacy OTHER STTREAMING APP | yaffw |
| :--- | :--- | :--- |
| **Database** | SQLite (Locked file) | Postgres (Multi-writer, transactional) |
| **Frontend Scaling** | Impossible (Stateful) | Unlimited (Stateless API) |
| **Transcoding** | Monolithic (Same OS process) | Distributed (Separate pods/nodes) |
| **Updates** | Downtime required | Rolling Updates (Zero Downtime) |
| **Filesystem** | Heavy Read/Write | Read-Only Media, Ephemeral Writes |
| **GPU Usage** | Tied to API node | Dedicated GPU nodes only for workers |

## 7. Potential Challenges & Mitigations

*   **Network Latency**: Proxying streams from Worker -> API -> User adds hops.
    *   *Mitigation*: Keep API and Workers in the same zone/cluster. Use gRPC for stream transport if HTTP/1.1 is too slow.
*   **Orphaned Workers**: If an API pod crashes before killing a transcode job, the worker might run forever.
    *   *Mitigation*: Workers implement a "Lease" system. If they don't receive a "Keep-Alive" ping from the API every 30 seconds via Redis, they self-terminate.
*   **Database Migration**: Moving users from SQLite to Postgres.
    *   *Mitigation*: Develop a robust ETL tool (Extract-Transform-Load) to migrate `library.db` data to the Postgres schema before first launch.
