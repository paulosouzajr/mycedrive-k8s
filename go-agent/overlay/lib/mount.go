//go:build ignore
// +build ignore

// mount.go is part of the Docker reference overlay2 driver kept for reference only.
// It is NOT compiled into the go-agent binary.

package lib

import (
	"os"
	"runtime"
	"syscall"
)

func mountFrom(dir, device, target, mType, label string) error {
	runtime.LockOSThread()

	// We want to store the original directory so we can re-enter after a
	// successful mount. This solves the problem of a process living
	// in the mounted directory when we want to unmount it. We do this
	// without invoking the reexec chain.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	if err := syscall.Mount(device, target, mType, uintptr(0), label); err != nil {
		return err
	}
	if err := os.Chdir(cwd); err != nil {
		return err
	}

	runtime.UnlockOSThread()

	return nil
}
