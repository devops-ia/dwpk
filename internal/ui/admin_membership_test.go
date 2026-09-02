package ui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	"github.com/devops-ia/dwpk/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func membershipEqual(a, b UserSpaceMembership) bool {
	return a.Name == b.Name && a.Role == b.Role && a.Disabled == b.Disabled
}

func TestMembershipFromForm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		formName string
		role     string
		disabled string
		want     UserSpaceMembership
		wantErr  bool
	}{
		{
			name:     "a blank role falls back to the CRD default",
			formName: testUsername,
			want: UserSpaceMembership{
				Name: testUsername,
				Role: dwpkv1alpha1.UserSpaceRoleUser,
			},
		},
		{
			name:     "an unchecked checkbox sends nothing and means enabled",
			formName: testUsername,
			role:     dwpkv1alpha1.UserSpaceRoleAdmin,
			want: UserSpaceMembership{
				Name: testUsername,
				Role: dwpkv1alpha1.UserSpaceRoleAdmin,
			},
		},
		{
			name:     "a checked checkbox disables the account",
			formName: testUsername,
			disabled: boolTrue,
			want: UserSpaceMembership{
				Name:     testUsername,
				Role:     dwpkv1alpha1.UserSpaceRoleUser,
				Disabled: true,
			},
		},
		{
			// Administrator is offered by the form; admission decides whether the
			// caller may actually grant it.
			name:     "admin is a valid role here",
			formName: testUsername,
			role:     dwpkv1alpha1.UserSpaceRoleAdmin,
			want: UserSpaceMembership{
				Name: testUsername,
				Role: dwpkv1alpha1.UserSpaceRoleAdmin,
			},
		},
		{
			name:     "an unknown role is refused before it reaches the API server",
			formName: testUsername,
			role:     "superuser",
			wantErr:  true,
		},
		{
			name:    "a missing name is refused",
			role:    dwpkv1alpha1.UserSpaceRoleUser,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := membershipFromForm(test.formName, test.role, test.disabled)
			if test.wantErr {
				if err == nil {
					t.Fatalf("membershipFromForm() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("membershipFromForm() error = %v", err)
			}
			if !membershipEqual(got, test.want) {
				t.Fatalf("membershipFromForm() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestAdminMembershipPatchesRoleAndDisabled(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	var captured UserSpaceMembership
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{membership: &captured}}

	request := authedFormRequest("/admin/userspaces/alice/membership", csrf,
		"role=admin&disabled=true")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := UserSpaceMembership{
		Name:     testUsername,
		Role:     dwpkv1alpha1.UserSpaceRoleAdmin,
		Disabled: true,
	}
	if !membershipEqual(captured, want) {
		t.Fatalf("patched %+v, want %+v", captured, want)
	}
}

// An omitted field must keep its current value. A merge patch built from every
// field would otherwise blank the ones the caller left out.
func TestAPIMembershipPatchKeepsOmittedFields(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.loginFlow = fakeSessionAuthenticator{identity: SessionIdentity{UserSpaceNamespace: testOwnerNS}, token: testToken}
	csrf, err := server.csrfStore.Ensure(testSession)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	var captured UserSpaceMembership
	server.clientFactory = fakeAPIClientFactory{api: fakeAPI{
		membership: &captured,
		userSpaces: []dwpkv1alpha1.UserSpace{{
			ObjectMeta: metav1.ObjectMeta{Name: testUsername},
			Spec: dwpkv1alpha1.UserSpaceSpec{
				Role:     dwpkv1alpha1.UserSpaceRoleAdmin,
				Disabled: false,
			},
		}},
	}}

	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/userspaces/alice", strings.NewReader(`{"disabled":true}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSession})
	request.Header.Set(csrfHeaderName, csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := UserSpaceMembership{
		Name:     testUsername,
		Role:     dwpkv1alpha1.UserSpaceRoleAdmin,
		Disabled: true,
	}
	if !membershipEqual(captured, want) {
		t.Fatalf("patched %+v, want %+v", captured, want)
	}
}

// A disabled account keeps its namespace and data, but must not be able to
// start a session. The gate lives in sessionIdentityFrom so both the OAuth2
// and the local login path go through it.
func TestSessionIdentityRefusesDisabledUserSpace(t *testing.T) {
	t.Parallel()

	userSpace := &dwpkv1alpha1.UserSpace{
		ObjectMeta: metav1.ObjectMeta{Name: testUsername},
		Spec:       dwpkv1alpha1.UserSpaceSpec{Owner: testOwner, Disabled: true},
		Status:     dwpkv1alpha1.UserSpaceStatus{Namespace: testOwnerNS},
	}

	_, err := sessionIdentityFrom(userSpace, auth.Claims{Email: testOwner}, "")
	if err == nil {
		t.Fatal("sessionIdentityFrom() succeeded for a disabled UserSpace")
	}
	if !errors.Is(err, ErrUserSpaceDisabled) {
		t.Fatalf("error = %v, want ErrUserSpaceDisabled", err)
	}

	userSpace.Spec.Disabled = false
	identity, err := sessionIdentityFrom(userSpace, auth.Claims{Email: testOwner}, "")
	if err != nil {
		t.Fatalf("sessionIdentityFrom() error = %v", err)
	}
	if identity.UserSpaceNamespace != testOwnerNS {
		t.Fatalf("namespace = %q", identity.UserSpaceNamespace)
	}
	if identity.Role != dwpkv1alpha1.UserSpaceRoleUser {
		t.Fatalf("role = %q, want the default", identity.Role)
	}
}
