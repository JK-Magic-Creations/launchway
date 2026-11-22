//go:build unix

package metrics

import "syscall"

// WatchDisk reports the free and total bytes of the volume holding dir at every scrape.
func WatchDisk(dir string) {
	Default.OnScrape(func() {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err != nil {
			return
		}
		bs := uint64(st.Bsize) //nolint:gosec // a block size is never negative
		DataFreeBytes.Set(float64(uint64(st.Bavail) * bs))
		DataTotalBytes.Set(float64(uint64(st.Blocks) * bs))
	})
}
