package stories

import (
	"errors"
	"os"
	"syscall"
)

const lockPath = "/tmp/gok-stories.lock"

// TryLock takes the lock that keeps the digen job and the stories command from
// deciding topics at the same time. It returns ok=false when another process
// holds it.
func TryLock() (release func(), ok bool, err error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, true, nil
}
