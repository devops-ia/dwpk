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

package webhook

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateAuthorizedKeys checks every entry parses as an OpenSSH
// authorized_keys line.
//
// ssh.ParseAuthorizedKey is the same function the gateway matches with
// (internal/gateway/pod_resolver.go), so a key accepted here is one the gateway
// can use, and every type OpenSSH understands is accepted without this code
// keeping a list: ssh-rsa, the three ecdsa-sha2-nistp curves, ssh-ed25519 and
// both sk-* security-key forms.
//
// Validating at admission rather than in a form matters because kubectl writes
// these objects too, and a bad key otherwise surfaces as "I cannot log in to my
// workspace" long after the mistake.
func validateAuthorizedKeys(path *field.Path, keys []string) field.ErrorList {
	var errs field.ErrorList
	for i, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			errs = append(errs, field.Invalid(path.Index(i), key, "must not be blank"))
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed)); err != nil {
			errs = append(errs, field.Invalid(path.Index(i), summarizeKey(trimmed),
				fmt.Sprintf("not a valid OpenSSH public key: %v", err)))
		}
	}
	return errs
}

// summarizeKey is what goes in an error message. The key material itself is not
// secret, but it is 400 characters of base64 that helps nobody read the error -
// the type and the comment are what identify which key was wrong.
func summarizeKey(key string) string {
	fields := strings.Fields(key)
	switch len(fields) {
	case 0:
		return ""
	case 1, 2:
		return fields[0]
	default:
		return fields[0] + " ... " + strings.Join(fields[2:], " ")
	}
}
