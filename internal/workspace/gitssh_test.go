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

import "testing"

func TestGitSSHConfigRendersOneBlockPerHost(t *testing.T) {
	t.Parallel()

	got := GitSSHConfig([]string{"github.com", "gitlab.example.com"})
	want := "Host github.com\n" +
		"  IdentityFile /etc/dwpk/git-ssh/key-github.com\n" +
		"  StrictHostKeyChecking accept-new\n\n" +
		"Host gitlab.example.com\n" +
		"  IdentityFile /etc/dwpk/git-ssh/key-gitlab.example.com\n" +
		"  StrictHostKeyChecking accept-new\n\n"
	if got != want {
		t.Errorf("GitSSHConfig() = %q, want %q", got, want)
	}
}

func TestGitSSHConfigEmptyWithNoHosts(t *testing.T) {
	t.Parallel()

	if got := GitSSHConfig(nil); got != "" {
		t.Errorf("GitSSHConfig(nil) = %q, want empty", got)
	}
}

func TestGitSSHHostsFromDataRecoversAndSortsHosts(t *testing.T) {
	t.Parallel()

	data := map[string][]byte{
		"key-gitlab.example.com": []byte("b"),
		"key-github.com":         []byte("a"),
		"config":                 []byte("ignored"),
	}
	got := GitSSHHostsFromData(data)
	want := []string{"github.com", "gitlab.example.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("GitSSHHostsFromData() = %v, want %v", got, want)
	}
}
