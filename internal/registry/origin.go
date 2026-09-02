package registry

import (
	"regexp"
	"strings"
)

var ecrHost = regexp.MustCompile(`^\d+\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com$`)

const (
	originDockerHub = "Docker Hub"
	originGoogle    = "Google"
)

// OriginOf names the cloud or registry an image reference belongs to, derived
// from its host rather than stored - it works for a hand-created catalog
// entry too, and it cannot drift from spec.image the way a separately stored
// field could.
func OriginOf(image string) string {
	host := hostOf(image)
	switch {
	case host == "":
		return originDockerHub
	case ecrHost.MatchString(host):
		return "AWS"
	case host == "gcr.io" || strings.HasSuffix(host, ".gcr.io") || strings.HasSuffix(host, "-docker.pkg.dev"):
		return originGoogle
	case host == "ghcr.io":
		return "GitHub"
	case host == "quay.io":
		return "Quay"
	case host == "docker.io" || host == "index.docker.io":
		return originDockerHub
	default:
		return host
	}
}

// hostOf extracts the registry host from an image reference, the same rule
// Docker itself uses: the part before the first "/" only counts as a host if
// it looks like one (contains a "." or ":", or is literally "localhost") -
// otherwise the whole reference is a Docker Hub repository name.
func hostOf(image string) string {
	first, _, found := strings.Cut(image, "/")
	if !found {
		return ""
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return ""
}
