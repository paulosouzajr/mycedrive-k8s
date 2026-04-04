//go:build ignore
// +build ignore

// overlay.go contains Docker's reference overlay2 graphdriver implementation.
// It is kept for reference only and is NOT compiled into the go-agent binary.
// The agent uses the simpler overlay/layers.go (utils.Run mount wrappers) instead.

// Copyright 2016 Dennis Chen <barracks510@gmail.com>
// Copyright 2013-2016 Docker, Inc.
//
// Licensed under the Apache License, Version 2.0

package lib
