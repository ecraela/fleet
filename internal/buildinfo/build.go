// Package buildinfo describes the executable, independently of the HAProxy version.
package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"sync"
)

// Release builds set these with -ldflags. Development builds are never advertised
// as a signed release. Protocol 1 remains compatible with pre-versioned agents.
var Version = "dev"
var Revision = "unknown"

const Protocol = 1

type Info struct {
	Version          string `json:"version"`
	Revision         string `json:"revision"`
	Platform         string `json:"platform"`
	Protocol         uint32 `json:"protocol"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

var once sync.Once
var executableSHA256 string

func Current() Info {
	once.Do(func() {
		path, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			executableSHA256 = hex.EncodeToString(h.Sum(nil))
		}
	})
	return Info{Version: Version, Revision: Revision, Platform: runtime.GOOS + "/" + runtime.GOARCH, Protocol: Protocol, ExecutableSHA256: executableSHA256}
}
