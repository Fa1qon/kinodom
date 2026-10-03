//go:build !windows

package playback

import (
	"os"
	"os/exec"
)

func hide(*exec.Cmd)    {}
func adopt(*os.Process) {}
