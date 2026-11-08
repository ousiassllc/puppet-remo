//go:build !windows

package main

import (
	"fmt"
	"os"
)

func notWindows() int {
	fmt.Fprintln(os.Stderr, "a2probe は Windows 専用です（GOOS=windows でビルドする）")
	return 2
}

func runProbe([]string) int   { return notWindows() }
func runFixture([]string) int { return notWindows() }
