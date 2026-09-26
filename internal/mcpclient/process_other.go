//go:build !unix

package mcpclient

import (
	"fmt"
	"os/exec"
)

func isolateProcess(*exec.Cmd) error {
	return fmt.Errorf("MCP subprocess supervision currently requires Unix")
}
func cleanupProcessGroup(*exec.Cmd) error { return nil }
