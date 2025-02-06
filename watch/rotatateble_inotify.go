package watch

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/tomb.v1"
)

type RotatingFileWatcher struct {
	symlink string
	target  string
	watcher *fsnotify.Watcher
	changes *FileChanges
	mutex   sync.Mutex
}

func NewRotatingFileWatcher(filename string) FileWatcher {
	return &RotatingFileWatcher{
		symlink: filepath.Clean(filename),
		changes: NewFileChanges(),
	}
}

func (fw *RotatingFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	if _, err := os.Stat(fw.symlink); !os.IsNotExist(err) {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(filepath.Dir(fw.symlink)); err != nil {
		return err
	}

	for {
		select {
		case event := <-watcher.Events:
			if event.Name == fw.symlink {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
}

func (fw *RotatingFileWatcher) ChangeEvents(t *tomb.Tomb, pos int64) (*FileChanges, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch symlink's directory
	if err := watcher.Add(filepath.Dir(fw.symlink)); err != nil {
		watcher.Close()
		return nil, err
	}

	// Get and watch current target
	target, err := os.Readlink(fw.symlink)
	if err != nil {
		watcher.Close()
		return nil, err
	}

	if err := watcher.Add(target); err != nil {
		watcher.Close()
		return nil, err
	}

	fw.mutex.Lock()
	fw.watcher = watcher
	fw.target = target
	fw.mutex.Unlock()

	go fw.watchEvents(t, pos)
	return fw.changes, nil
}

func (fw *RotatingFileWatcher) watchEvents(t *tomb.Tomb, pos int64) {
	defer fw.watcher.Close()

	for {
		select {
		case event := <-fw.watcher.Events:
			fw.handleEvent(event, pos)
		case <-t.Dying():
			return
		}
	}
}

func (fw *RotatingFileWatcher) handleEvent(event fsnotify.Event, pos int64) {
	fw.mutex.Lock()
	defer fw.mutex.Unlock()

	switch {
	case event.Name == fw.symlink && (event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Rename == fsnotify.Rename):
		// Symlink changed - update target
		if newTarget, err := os.Readlink(fw.symlink); err == nil {
			fw.watcher.Remove(fw.target)
			fw.target = newTarget
			fw.watcher.Add(newTarget)
			fw.changes.NotifyModified()
		} else {
			fw.changes.NotifyDeleted()
		}

	case event.Name == fw.target:
		if event.Op&fsnotify.Write == fsnotify.Write {
			fi, err := os.Stat(fw.target)
			if err != nil {
				fw.changes.NotifyDeleted()
				return
			}

			if fi.Size() < pos {
				fw.changes.NotifyTruncated()
			} else {
				fw.changes.NotifyModified()
			}
		} else if event.Op&fsnotify.Remove == fsnotify.Remove {
			fw.changes.NotifyDeleted()
		}
	}
}
