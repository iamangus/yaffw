package filesystem

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	libraryPath string
	watcher     *fsnotify.Watcher
	scanner     *Scanner
	stopCh      chan struct{}
	doneCh      chan struct{}
	debounceMu  sync.Mutex
	debounceMap map[string]time.Time
}

func NewWatcher(libraryPath string, scanner *Scanner) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		libraryPath: libraryPath,
		watcher:     fsw,
		scanner:     scanner,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		debounceMap: make(map[string]time.Time),
	}

	if err := w.addWatchRecursive(libraryPath); err != nil {
		fsw.Close()
		return nil, err
	}

	return w, nil
}

func (w *Watcher) addWatchRecursive(path string) error {
	return filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if err := w.watcher.Add(walkPath); err != nil {
				log.Printf("[Watcher] Failed to add watch for %s: %v", walkPath, err)
			}
		}
		return nil
	})
}

func (w *Watcher) Start() {
	go w.run()
}

func (w *Watcher) Stop() {
	close(w.stopCh)
	w.watcher.Close()
	<-w.doneCh
}

func (w *Watcher) run() {
	defer close(w.doneCh)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[Watcher] Error: %v", err)
		case <-ticker.C:
			w.flushDebounced()
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Write) == 0 {
		return
	}

	dir := filepath.Dir(event.Name)
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			_ = w.watcher.Add(event.Name)
		}
	}

	w.debounceMu.Lock()
	w.debounceMap[dir] = time.Now()
	w.debounceMu.Unlock()
}

func (w *Watcher) flushDebounced() {
	w.debounceMu.Lock()
	if len(w.debounceMap) == 0 {
		w.debounceMu.Unlock()
		return
	}

	now := time.Now()
	var toScan []string
	for dir, t := range w.debounceMap {
		if now.Sub(t) >= 5*time.Second {
			toScan = append(toScan, dir)
			delete(w.debounceMap, dir)
		}
	}
	w.debounceMu.Unlock()

	if len(toScan) > 0 {
		log.Printf("[Watcher] Triggering rescan for %d changed directories", len(toScan))
		_ = w.scanner.Scan(context.Background())
	}
}
