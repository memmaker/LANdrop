//go:build !darwin && !windows

package main

import "fmt"

func sendInput(inputArgs) error {
	return fmt.Errorf("remote input is not supported on this platform")
}
