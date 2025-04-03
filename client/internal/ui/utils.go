package ui

import "fmt"

func formatSize(bytes int) string {
	if bytes == 0 {
		return "0 KB"
	}

	kb := float64(bytes) / 1024
	if kb < 1024 {
		if kb < 1 {
			return "1 KB"
		}
		return fmt.Sprintf("%d KB", int(kb))
	}

	mb := kb / 1024
	if mb < 1024 {
		return fmt.Sprintf("%.1f MB", mb)
	}

	gb := mb / 1024
	return fmt.Sprintf("%.2f GB", gb)
}

// formatSpeed converts a speed value in bytes/sec to a human-readable string
// with appropriate units (B/s, KB/s, MB/s, GB/s)
func formatSpeed(bytesPerSec int64) string {
	if bytesPerSec == 0 {
		return "0 B/s"
	}

	// 1 GiB/s = 1073741824 bytes/sec
	if bytesPerSec >= 1073741824 {
		return fmt.Sprintf("%.2f GB/s", float64(bytesPerSec)/1073741824)
	}

	// 1 MiB/s = 1048576 bytes/sec
	if bytesPerSec >= 1048576 {
		return fmt.Sprintf("%.1f MB/s", float64(bytesPerSec)/1048576)
	}

	// 1 KiB/s = 1024 bytes/sec
	if bytesPerSec >= 1024 {
		return fmt.Sprintf("%.1f KB/s", float64(bytesPerSec)/1024)
	}

	return fmt.Sprintf("%d B/s", bytesPerSec)
}
