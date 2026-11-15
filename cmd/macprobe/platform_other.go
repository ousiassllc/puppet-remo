//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func notDarwin() int {
	fmt.Fprintln(os.Stderr, "macprobe は macOS 専用です（GOOS=darwin でビルドする）")
	return 2
}

func runProbe([]string) int   { return notDarwin() }
func runFixture([]string) int { return notDarwin() }
