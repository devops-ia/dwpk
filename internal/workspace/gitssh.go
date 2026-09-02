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

package workspace

import (
	"fmt"
	"slices"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// GitSSHHostsFromData recovers the host list from a Secret's Data keys - the
// config entry is generated from this, never stored as its own source of
// truth, so the two can never drift apart.
//
// Exported and shared between internal/ui (which writes the source Secret
// with the caller's own forwarded token) and internal/controller (which
// decrypts it into the runtime Secret every workspace actually mounts): both
// need the identical config rendering, and duplicating it would be exactly
// the drift risk a shared pure function exists to avoid.
func GitSSHHostsFromData(data map[string][]byte) []string {
	hosts := make([]string, 0, len(data))
	for key := range data {
		if host, ok := strings.CutPrefix(key, dwpkv1alpha1.GitSSHKeyDataPrefix); ok {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

// GitSSHConfig renders one OpenSSH client config block per host, IdentityFile
// pointing at where the runtime Secret mounts it - Host-scoped, so two keys
// for two providers never collide. StrictHostKeyChecking is accept-new
// rather than the default "ask": a headless workspace has nowhere to prompt
// "yes", and UserKnownHostsFile is left unset - it defaults to the user's
// own ~/.ssh/known_hosts on their persistent home volume, so a once-accepted
// host key is pinned there for good, not re-accepted on every pod restart.
func GitSSHConfig(hosts []string) string {
	var b strings.Builder
	for _, host := range hosts {
		fmt.Fprintf(&b, "Host %s\n  IdentityFile %s/%s%s\n  StrictHostKeyChecking accept-new\n\n",
			host, dwpkv1alpha1.GitSSHMountPath, dwpkv1alpha1.GitSSHKeyDataPrefix, host)
	}
	return b.String()
}
