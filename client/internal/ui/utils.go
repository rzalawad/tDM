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
