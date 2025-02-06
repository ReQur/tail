// Copyright (c) 2019 FOSS contributors of https://github.com/nxadm/tail
// Copyright (c) 2015 HPE Software Inc. All rights reserved.
// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/tomb.v1"
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
}

func NewInotifyFileWatcher(filename string) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filepath.Clean(filename), 0}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	err := WatchCreate(fw.Filename)
	if err != nil {
		return err
	}
	defer RemoveWatchCreate(fw.Filename)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		return err
	}

	events := Events(fw.Filename)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return fmt.Errorf("inotify watcher has been closed")
			}
			evtName, err := filepath.Abs(evt.Name)
			if err != nil {
				return err
			}
			fwFilename, err := filepath.Abs(fw.Filename)
			if err != nil {
				return err
			}
			if evtName == fwFilename {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
	panic("unreachable")
}

func (fw *InotifyFileWatcher) ChangeEvents(t *tomb.Tomb, pos int64) (*FileChanges, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %v", err)
	}

	dir := filepath.Dir(fw.Filename)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to add watch: %v", err)
	}

	changes := NewFileChanges()
	fw.Size = pos

	go func() {
		defer watcher.Close()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				if event.Name != fw.Filename {
					continue
				}

				if event.Op&fsnotify.Remove == fsnotify.Remove {
					time.Sleep(100 * time.Millisecond)

					if _, err := os.Lstat(fw.Filename); err != nil {
						if os.IsNotExist(err) {
							changes.NotifyDeleted()
							return
						}
						log.Printf("Failed to stat file %v: %v", fw.Filename, err)
						continue
					}

					log.Printf("File %s has been replaced, following new file", fw.Filename)
					changes.NotifyModified()
					fw.Size = 0
					continue
				}

				fi, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						changes.NotifyDeleted()
						return
					}
					log.Printf("Failed to stat file %v: %v", fw.Filename, err)
					continue
				}

				newSize := fi.Size()
				if fw.Size > 0 && fw.Size > newSize {
					changes.NotifyTruncated()
				} else if newSize > fw.Size {
					changes.NotifyModified()
				}
				fw.Size = newSize

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watcher error: %v", err)

			case <-t.Dying():
				return
			}
		}
	}()

	return changes, nil
}
