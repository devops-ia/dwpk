/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gateway

import "testing"

// A direct-tcpip request naming anything but the resolved pod's own loopback
// used to fall through to net.Dial from the gateway pod's own network stack -
// a legitimate SSH session became an open proxy onto whatever the gateway pod
// itself could reach (other cluster Services, a cloud metadata endpoint).
// Only loopback may ever be forwarded.
func TestIsLoopbackForwardTarget(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"localhost":              true,
		"127.0.0.1":              true,
		"::1":                    true,
		"169.254.169.254":        false,
		"10.0.0.1":               false,
		"kubernetes.default.svc": false,
		"":                       false,
		"evil.example.com":       false,
	}

	for addr, want := range cases {
		if got := isLoopbackForwardTarget(addr); got != want {
			t.Errorf("isLoopbackForwardTarget(%q) = %v, want %v", addr, got, want)
		}
	}
}

// A window-change request carries cols/rows as uint32 straight from the SSH
// client. uint16(cols) silently wraps a value above 65535 into an unrelated
// small one instead of reporting the terminal's real (clamped) size.
func TestClampToUint16(t *testing.T) {
	t.Parallel()

	cases := map[uint32]uint16{
		0:          0,
		80:         80,
		65535:      65535,
		65536:      65535,
		4294967295: 65535,
	}

	for in, want := range cases {
		if got := clampToUint16(in); got != want {
			t.Errorf("clampToUint16(%d) = %d, want %d", in, got, want)
		}
	}
}
