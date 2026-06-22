package pipe

import (
	"os"
	"path/filepath"
	"syscall"
)

// Setup creates the inbox and outbox FIFOs in dir.
// inbox: goblin-horn → scout (goblin-horn writes, scout reads)
// outbox: scout → goblin-horn (scout writes, goblin-horn reads)
func Setup(dir string) error {
	for _, name := range []string{"inbox", "outbox"} {
		p := filepath.Join(dir, name)
		if err := syscall.Mkfifo(p, 0o600); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}
