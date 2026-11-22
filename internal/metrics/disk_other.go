//go:build !unix

package metrics

// WatchDisk does nothing where the system has no statfs.
func WatchDisk(string) {}
