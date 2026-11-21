package live

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The lock-directory lock is what a platform without flock gets (the bench's
// platform is unix, so this is a fallback). It lives in a file without build
// tags so its takeover rule is tested on every platform.

const defaultLockTakeoverAge = 6 * time.Hour

// LockTakeoverAge is how old a lock directory must be before another run
// takes it over: a crash leaves the directory behind, and no flock frees it.
// FOREVER_TRIALS_LOCK_TAKEOVER (a Go duration such as 12h) overrides the six
// hours default; a bad value keeps the default.
func LockTakeoverAge() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("FOREVER_TRIALS_LOCK_TAKEOVER")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultLockTakeoverAge
}

// LockLog, when set, is told every lock takeover (a held lock taken from a
// holder that looked dead is never silent).
var LockLog func(string)

// lockDirFile takes the lock directory beside path: the directory is the
// lock, removed by the owning process. A holder older than age is taken over
// and the takeover is logged.
func lockDirFile(path string, age time.Duration, log func(string)) (*os.File, bool) {
	dir := path + ".l"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		st, serr := os.Stat(dir)
		if serr != nil || time.Since(st.ModTime()) <= age {
			return nil, false
		}
		if log != nil {
			log(fmt.Sprintf("arena lock %s: took over a holder idle for %s (takeover age %s)",
				path, time.Since(st.ModTime()).Round(time.Second), age))
		}
		_ = os.RemoveAll(dir)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, false
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false
	}
	return f, true
}
