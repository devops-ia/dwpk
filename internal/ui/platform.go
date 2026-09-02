package ui

import (
	"context"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// platformConfigTTL is how long the settings are reused before being re-read.
//
// Every page render needs the platform's name, and the login page needs it
// before there is a session to read it with. A short cache keeps that from
// being an API call per request; five seconds is short enough that a rename
// appears immediately in human terms.
const platformConfigTTL = 5 * time.Second

// PlatformReader reads the platform settings with the UI's own credential.
//
// This is the one thing the UI reads as itself rather than as the person using
// it, and it is deliberately narrow: get on a single cluster-scoped object
// holding a name, a logo and a default theme. The login page has to render the
// platform's name and favicon before anyone has signed in, so there is no user
// token to borrow.
//
// It does not weaken SPEC §8.1. That rule exists so the UI cannot act on a
// user's behalf beyond what their own RBAC allows; reading the platform's own
// name grants nothing over anybody's workspaces, and the settings are written
// with the administrator's forwarded token like every other change.
type PlatformReader interface {
	Get(ctx context.Context, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error
}

type platformCache struct {
	mu      sync.RWMutex
	config  *dwpkv1alpha1.PlatformConfig
	fetched time.Time
}

// platformConfig returns the settings, or nil when there are none.
//
// It returns no error on purpose. A cluster that has never had settings written
// is the ordinary case, not a failure, and there is nothing a caller could do
// about a failed read except render the defaults - which is what nil already
// means. Name(), Theme() and HasLogo() are all nil-safe for exactly that reason.
func (s *Server) platformConfig(r *http.Request) *dwpkv1alpha1.PlatformConfig {
	if s.platformReader == nil {
		return nil
	}

	s.platform.mu.RLock()
	cached, fetched := s.platform.config, s.platform.fetched
	s.platform.mu.RUnlock()
	if !fetched.IsZero() && s.now().Sub(fetched) < platformConfigTTL {
		return cached
	}

	config := &dwpkv1alpha1.PlatformConfig{}
	err := s.platformReader.Get(r.Context(),
		ctrlclient.ObjectKey{Name: dwpkv1alpha1.PlatformConfigName}, config)
	if err != nil {
		// Cache the absence too. A cluster that has never had settings written
		// would otherwise make a failing API call on every single render.
		config = nil
	}

	s.platform.mu.Lock()
	s.platform.config, s.platform.fetched = config, s.now()
	s.platform.mu.Unlock()
	return config
}

// invalidatePlatformConfig drops the cache after a write, so the person who
// just renamed the platform sees the new name rather than waiting out the TTL
// and concluding it did not save.
func (s *Server) invalidatePlatformConfig() {
	s.platform.mu.Lock()
	s.platform.fetched = time.Time{}
	s.platform.mu.Unlock()
}

// gpuResource is the extended resource a GPU is asked for as, from the platform
// settings. It reads the same cached object every screen already reads, so the
// quota display and the create form agree without a second lookup.
func (s *Server) gpuResource(r *http.Request) corev1.ResourceName {
	return corev1.ResourceName(s.platformConfig(r).GPUResource())
}
