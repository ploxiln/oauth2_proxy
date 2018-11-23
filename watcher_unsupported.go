// +build plan9 solaris

package main

import (
	"log"
)

func WatchForUpdates(filename string, done <-chan bool, action func()) {
	log.Printf("file watching not implemented on this platform")
	go func() { <-done }()
}
