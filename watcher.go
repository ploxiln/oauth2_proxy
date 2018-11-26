// +build !plan9,!solaris

package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

func WaitForReplacement(filename string, watcher *fsnotify.Watcher) {
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)

		if _, err := os.Stat(filename); err == nil {
			if err := watcher.Add(filename); err == nil {
				log.Printf("watching resumed for %s", filename)
				return
			}
		}
	}
	log.Printf("failed to resume watching for %s", filename)
}

func watchLoop(filename string, watcher *fsnotify.Watcher, done <-chan bool, action func()) {
	for {
		select {
		case _ = <-done:
			log.Printf("Shutting down watcher for: %s", filename)
			watcher.Close()
			return
		case event := <-watcher.Events:
			// On Arch Linux, it appears Chmod events precede Remove events,
			// which causes a race between action() and the coming Remove event.
			if event.Op == fsnotify.Chmod {
				continue
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				log.Printf("watching interrupted on event: %s", event)
				watcher.Remove(filename)
				WaitForReplacement(filename, watcher)
			}
			log.Printf("reloading after event: %s", event)
			action()
		case err := <-watcher.Errors:
			log.Printf("error watching %s: %s", filename, err)
		}
	}
}

func WatchForUpdates(filename string, done <-chan bool, action func()) {
	filename = filepath.Clean(filename)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("failed to create watcher for ", filename, ": ", err)
	}
	if err = watcher.Add(filename); err != nil {
		log.Fatal("failed to add ", filename, " to watcher: ", err)
	}
	go watchLoop(filename, watcher, done, action)
	log.Printf("watching %s for updates", filename)
}
