#!/bin/bash

# Log files
CONTROL_LOG="control_test.log"
COMPUTE_LOG="compute_test.log"
WEB_LOG="web_test.log"

rm -rf ./temp/transcode/*
rm -rf ./*.log

# Function to start control
start_control() {
    echo "Starting Control Plane..."
    RUN_MODE=test PORT=8000 go run ./src/ControlPlane > "$CONTROL_LOG" 2>&1 &
    CONTROL_PID=$!
    echo "Control Plane started with PID $CONTROL_PID. Logs: $CONTROL_LOG"
}

# Function to start compute
start_compute() {
    echo "Starting Compute Plane..."
    WORKER_PORT=8001 CONTROL_URL=http://localhost:8000 go run ./src/ComputePlane > "$COMPUTE_LOG" 2>&1 &
    COMPUTE_PID=$!
    echo "Compute Plane started with PID $COMPUTE_PID. Logs: $COMPUTE_LOG"
}

# Function to start web frontend
start_web() {
    echo "Starting Web Frontend..."
    PORT=3000 CONTROL_PLANE_URL=http://localhost:8000 go run ./src/WebFrontend > "$WEB_LOG" 2>&1 &
    WEB_PID=$!
    echo "Web Frontend started with PID $WEB_PID. Logs: $WEB_LOG"
}

# Function to stop all processes
stop_all() {
    echo "Stopping all processes..."
    pkill ComputePlane
    pkill ControlPlane
    pkill WebFrontend
    # Since we run with 'go run', the binary name might be different or it might be a child process.
    # pkill -f "go run ./src/WebFrontend" might be safer but pkill usually works if binary name matches.
    # However, 'go run' builds a temporary binary.
    # Let's rely on pkill -P $$ if we tracked PIDs, but we didn't.
    # We'll try to kill by port or just pkill main (risky).
    # Actually, 'go run' creates a subprocess.
    # Let's just kill the PIDs we captured if possible, but for now let's add pkill for the likely binary names.
    # The binary name for 'go run ./src/WebFrontend' is usually 'WebFrontend' (folder name) in recent Go versions?
    # Or it's in /tmp.
    # Let's just kill the PIDs we started.
    if [ ! -z "$CONTROL_PID" ]; then kill $CONTROL_PID; fi
    if [ ! -z "$COMPUTE_PID" ]; then kill $COMPUTE_PID; fi
    if [ ! -z "$WEB_PID" ]; then kill $WEB_PID; fi
    
    pkill ffmpeg
    echo "All processes stopped."
}

# Handle script exit
cleanup() {
    stop_all
    exit 0
}

# Trap interrupts
trap cleanup SIGINT SIGTERM

# Main execution
echo "Initializing test environment..."

# 1.a Start Control
start_control

# Give Control a moment to initialize
sleep 1

# 1.b Start Compute
start_compute

# 1.c Start Web Frontend
start_web

while true; do
    echo ""
    echo "------------------------------------------------"
    echo "Test Menu:"
    echo "1. End test (Stop control, stop compute, pkill ffmpeg)"
    echo "2. Simulate compute failure (Stop compute, pkill ffmpeg, wait to restart)"
    echo "------------------------------------------------"
    read -p "Enter choice [1-2]: " choice

    case $choice in
        1)
            cleanup
            ;;
        2)
            echo "Simulating compute failure..."
            pkill ComputePlane
            echo "Compute Plane stopped."
            pkill ffmpeg
            echo "ffmpeg processes killed."
            
            echo "Waiting for confirmation to restart..."
            read -p "Press Enter to start Compute Plane again..."
            start_compute
            ;;
        *)
            echo "Invalid option. Please choose 1 or 2."
            ;;
    esac
done