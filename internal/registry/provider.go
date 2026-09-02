// Package registry lists images from external container registries, kept
// deliberately ignorant of WorkspaceImage and the Kubernetes API: a Provider
// answers "what is in this registry", nothing more, so the sync controller
// (internal/controller) is the only place that turns an answer into a
// catalog entry.
package registry

import (
	"context"
	"time"
)

// RemoteImage is one pullable image, registry-neutral.
type RemoteImage struct {
	// Repository is the image name without a tag, e.g. "team/python".
	Repository string
	// Tag is the tag this entry represents. One RemoteImage per tag: a
	// repository with three matching tags is three RemoteImages.
	Tag string
	// Reference is the full pullable reference - the registry host, the
	// repository and the tag - exactly what WorkspaceImage.Spec.Image needs.
	Reference string
	// PushedAt orders tags newest-first for TagSelector.
	PushedAt time.Time
}

// Provider lists what is currently in one external registry. Auth, region and
// account are all provider-specific configuration baked in at construction;
// List takes only a context, because everything else about "which registry"
// is already decided by the time a Provider exists.
type Provider interface {
	List(ctx context.Context) ([]RemoteImage, error)
}
