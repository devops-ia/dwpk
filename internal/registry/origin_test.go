package registry

import "testing"

func TestOriginOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		image string
		want  string
	}{
		{"python:3.12", "Docker Hub"},
		{"library/python:3.12", "Docker Hub"},
		{"docker.io/library/python:3.12", "Docker Hub"},
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com/dwpk/python:3.12", "AWS"},
		{"gcr.io/my-project/python:3.12", "Google"},
		{"eu-docker.pkg.dev/my-project/repo/python:3.12", "Google"},
		{"ghcr.io/devops-ia/dwpk/python:3.12", "GitHub"},
		{"quay.io/devops-ia/python:3.12", "Quay"},
		{"registry.internal.example.com/team/python:3.12", "registry.internal.example.com"},
		{"localhost:5000/python:3.12", "localhost:5000"},
	}
	for _, tt := range tests {
		if got := OriginOf(tt.image); got != tt.want {
			t.Errorf("OriginOf(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}
