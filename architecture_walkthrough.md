# YAFFW Architecture & Recovery Walkthrough

This document provides a detailed technical walkthrough of the YAFFW (Yet Another FFmpeg Wrapper) application, focusing on its distributed architecture and robust failure recovery mechanisms.

## 1. System Architecture Overview

The system is split into two main components: the **Control Plane** and the **Compute Plane**.

### Control Plane (`src/ControlPlane/main.go`)
The Control Plane is the brain of the operation. It handles:
- **API Requests**: Serves the frontend and client API calls.
- **Job Management**: Maintains the `JobQueue` (backed by PostgreSQL) as the single source of truth.
- **HLS Orchestration**: Generates dynamic HLS playlists (`.m3u8`) and proxies segment requests (`.ts`) to the appropriate workers.
- **Failure Detection**: Runs a `PassiveRecoveryMonitor` to monitor worker health and trigger JIT recovery.

### Compute Plane (`src/ComputePlane/main.go`)
The Compute Plane consists of stateless (or semi-stateful) workers that:
- **Poll for Jobs**: Connect to the Control Plane via HTTP to request work.
- **Transcode**: Execute `ffmpeg` to convert media into HLS segments.
- **Serve Content**: Host a local HTTP server to serve the generated segments.
- **Report Status**: Send heartbeats and segment updates back to the Control Plane.

---

## 2. The Transcoding Lifecycle

### Step 1: Job Creation
When a user requests playback (`handlePlaybackRequest` in `ControlPlane/main.go`), the system:
1. Probes the media file.
2. Determines if transcoding is needed.
3. If needed, creates a `TranscodeJob` and enqueues it into the `JobQueue`.

### Step 2: Job Pickup
A worker in the Compute Plane (`src/internal/services/worker.go`):
1. Polls the queue via `queue.Dequeue`.
2. Receives a job and calls `processJob`.
3. Immediately sends a heartbeat to mark the job as "Processing" and registers its `WorkerAddress`.

### Step 3: Transcoding & Reporting
The worker starts `ffmpeg` to generate HLS segments.
- **Segment Watcher**: A goroutine (`watchSegments`) monitors the output directory.
- **Reporting**: When a new segment appears, it probes it (using `ffprobe`) and reports it to the Control Plane via `queue.AddSegment`.
- **Heartbeats**: A background loop sends a heartbeat every 1 second to keep the job alive.

### Step 4: Playback
The client receives a master playlist URL.
- **Playlist Request**: The Control Plane serves the playlist (`serveDynamicPlaylist`). It constructs the playlist dynamically based on the segments reported by workers.
- **Segment Request**: The client requests a segment (e.g., `segment_001.ts`).
    - The Control Plane looks up which worker owns that segment.
    - It proxies the request to that worker's internal HTTP server.

---

## 3. Failure Recovery Mechanisms

The system is designed to handle worker failures gracefully through three distinct layers of recovery.

### Layer 1: Passive Recovery Monitor (JIT-Aware)
**File:** `src/ControlPlane/main.go` -> `startPassiveRecoveryMonitor`

This background routine runs in the Control Plane, replacing the old "Reaper" service.
- **Mechanism**: It checks active jobs every 1 second.
- **Trigger**: If a job's `LastHeartbeat` is older than 3 seconds (3 missed heartbeats).
- **Action**:
    1. Logs a warning: `⚠️ DETECTED DEAD WORKER`.
    2. **State Analysis**: Inspects the `JobQueue` to find the last segment successfully reported by the dead worker.
    3. **Calculation**: Calculates the `StartSegment` and `StartTime` to resume exactly where the worker left off.
    4. **Trigger**: Calls `triggerJITRecovery` to re-enqueue the job with these explicit start parameters.
- **Result**: A new worker picks up the job and starts transcoding from the calculated resume point. This enables recovery even in ephemeral environments (Pods) where local disk is lost.

### Layer 2: JIT Recovery (Active Detection)
**File:** `src/ControlPlane/main.go` -> `triggerJITRecovery`

This occurs when a client tries to fetch a segment, but the worker is unreachable.
- **Trigger**: The `httputil.ReverseProxy` fails to connect to the worker.
- **Action**:
    1. The `ErrorHandler` in the proxy captures the error.
    2. Calls `jobQueue.MarkWorkerAsDead` to remove that worker's segments from the state.
    3. Calls `triggerJITRecovery`:
        - Calculates the `StartTime` for the missing segment.
        - Resets job to `Pending`.
        - Sets `StartSegment` and `StartTime` explicitly.
    4. Returns `503 Service Unavailable` with a `Retry-After` header.
- **Result**: A new worker picks up the job and starts transcoding *exactly* from the missing segment. The client retries after 2 seconds and gets the new segment.

### Layer 3: Worker Local Recovery (Resume)
**File:** `src/internal/services/worker.go` -> `analyzeExistingSegments`

When a worker starts a job, it checks if it can resume work locally (useful if the worker process crashed but the disk is intact).

- **Logic**:
    1. **Explicit Instruction**: If the job has `StartSegment > 0` (from JIT Recovery), it prioritizes that.
    2. **Local Scan**: If not, it scans its temp directory for `segment_*.ts` files.
    3. **Verification**: It probes the last few segments to ensure they are valid and not truncated.
    4. **Calculation**: It calculates the `nextSegment` index and the `totalDuration` (timestamp) to resume from.
- **FFmpeg Resume**:
    - The worker constructs the `ffmpeg` command with `-ss [time]` and `-start_number [num]`.
    - This ensures the new segments align perfectly with the previous ones, maintaining a seamless stream for the client.

## 4. Code Reference Map

| Feature | Component | Function/Method | Description |
|---------|-----------|-----------------|-------------|
| **Job Queue** | Shared | `Enqueue`, `Dequeue` | PostgreSQL queue operations (SKIP LOCKED). |
| **Passive Monitor** | Control | `startPassiveRecoveryMonitor` | Monitors heartbeats and triggers JIT recovery. |
| **Smart Proxy** | Control | `main` (HLS Handler) | Proxies segments; triggers recovery on failure. |
| **JIT Trigger** | Control | `triggerJITRecovery` | Configures a job to restart at a specific segment. |
| **Worker Loop** | Compute | `processJob` | Main worker logic: setup, heartbeat, ffmpeg. |
| **Local Resume** | Compute | `analyzeExistingSegments` | Scans disk to resume transcoding after crash. |
| **FFmpeg Args** | Compute | `buildFFmpegArgs` | Constructs command with seek/start_number for resume. |