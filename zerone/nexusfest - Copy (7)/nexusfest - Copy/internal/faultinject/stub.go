//go:build !faultinject

package faultinject

import (
	"encoding/json"
	"errors"
)

// Hit is a no-op in production builds.
func Hit(name string) Action {
	return Action{Kind: "none"}
}

// Arm is a no-op in production builds and returns an error indicating failpoints are disabled.
func Arm(spec json.RawMessage) error {
	return errors.New("fault injection not enabled in production build")
}

// Disarm is a no-op in production builds.
func Disarm() {}

// Counters returns an empty map in production builds.
func Counters() map[string]uint64 {
	return map[string]uint64{}
}

// SetDataDir is a no-op in production builds.
func SetDataDir(dir string) {}

// MaybePartialWrite directly writes the entire buffer in production builds.
func MaybePartialWrite(name string, buf []byte, writeFn func([]byte) (int, error)) (int, error) {
	return writeFn(buf)
}

// RecordSyncedLength is a no-op in production builds.
func RecordSyncedLength(filename string, length int64) {}

// SelfCrash is a no-op in production builds.
func SelfCrash() {}

