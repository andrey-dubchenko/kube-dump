// Package size formats byte sizes for humans.
package size

import "fmt"

// KiB formats a size given in KiB; a negative size means unknown.
func KiB(kb int64) string {
	switch {
	case kb < 0:
		return "unknown"
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%.0f MiB", float64(kb)/1024)
	}
	return fmt.Sprintf("%d KiB", kb)
}
