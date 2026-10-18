package size_test

import (
	"testing"

	"kube-dump/internal/size"
)

func TestKiB(t *testing.T) {
	cases := map[int64]string{-1: "unknown", 512: "512 KiB", 2048: "2 MiB", 3 * 1024 * 1024: "3.0 GiB"}
	for in, want := range cases {
		if got := size.KiB(in); got != want {
			t.Errorf("KiB(%d) = %q, want %q", in, got, want)
		}
	}
}
