// Copyright 2016 Dennis Chen <barracks510@gmail.com>
// Copyright 2013-2016 Docker, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lib

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// generateID creates a new random string identifier with the given length
func generateID(l int) string {
	const (
		// ensures we backoff for less than 450ms total. Use the following to
		// select new value, in units of 10ms:
		// 	n*(n+1)/2 = d -> n^2 + n - 2d -> n = (sqrt(8d + 1) - 1)/2
		maxretries = 9
		backoff    = time.Millisecond * 10
	)

	var (
		totalBackoff time.Duration
		count        int
		retries      int
		size         = (l*5 + 7) / 8
		u            = make([]byte, size)
	)
	// TODO: Include time component, counter component, random component

	for {
		// This should never block but the read may fail. Because of this,
		// we just try to read the random number generator until we get
		// something. This is a very rare condition but may happen.
		b := time.Duration(retries) * backoff
		time.Sleep(b)
		totalBackoff += b

		n, err := io.ReadFull(rand.Reader, u[count:])
		if err != nil {
			if retryOnError(err) && retries < maxretries {
				count += n
				retries++
				continue
			}

			// Any other errors represent a system problem. What did someone
			// do to /dev/urandom?
			panic(fmt.Errorf("error reading random number generator, retried for %v: %v", totalBackoff.String(), err))
		}

		break
	}

	s := base32.StdEncoding.EncodeToString(u)

	return s[:l]
}

// retryOnError tries to detect whether or not retrying would be fruitful.
func retryOnError(err error) bool {
	switch err := err.(type) {
	case *os.PathError:
		return retryOnError(err.Err) // unpack the target error
	case syscall.Errno:
		if err == syscall.EPERM {
			// EPERM represents an entropy pool exhaustion, a condition under
			// which we backoff and retry.
			return true
		}
	}

	return false
}
