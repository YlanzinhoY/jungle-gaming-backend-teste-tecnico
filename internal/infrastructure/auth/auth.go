package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/config"
)

type principalKey struct{}

type Principal struct {
	Subject    string
	ProviderID string
	roles      map[string]struct{}
}

func (p Principal) HasRole(role string) bool {
	_, ok := p.roles[role]
	return ok
}

type Authenticator struct {
	config   config.OIDC
	mu       sync.RWMutex
	verifier *oidc.IDTokenVerifier
}

func New(lifecycle fx.Lifecycle, cfg config.Config) *Authenticator {
	authenticator := &Authenticator{config: cfg.OIDC}
	lifecycle.Append(fx.Hook{OnStart: func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, cfg.OIDC.StartupTimeout)
		defer cancel()
		discoveryURL := cfg.OIDC.DiscoveryURL
		if discoveryURL == "" {
			discoveryURL = cfg.OIDC.IssuerURL
		}
		if discoveryURL != cfg.OIDC.IssuerURL {
			ctx = oidc.InsecureIssuerURLContext(ctx, cfg.OIDC.IssuerURL)
		}
		provider, err := oidc.NewProvider(ctx, discoveryURL)
		if err != nil {
			return fmt.Errorf("discover OIDC provider: %w", err)
		}
		authenticator.mu.Lock()
		authenticator.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.Audience})
		authenticator.mu.Unlock()
		return nil
	}})
	return authenticator
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeAuthError(w, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "missing or malformed bearer token")
			return
		}
		a.mu.RLock()
		verifier := a.verifier
		a.mu.RUnlock()
		if verifier == nil {
			writeAuthError(w, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "identity provider is not ready")
			return
		}
		token, err := verifier.Verify(r.Context(), parts[1])
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "INVALID_TOKEN", "invalid or expired token")
			return
		}
		principal, err := a.principal(token)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "INVALID_TOKEN_CLAIMS", "token claims are invalid")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func (a *Authenticator) Check(ctx context.Context) error {
	a.mu.RLock()
	ready := a.verifier != nil
	a.mu.RUnlock()
	if !ready {
		return fmt.Errorf("OIDC verifier is not initialized")
	}

	discoveryURL := a.config.DiscoveryURL
	if discoveryURL == "" {
		discoveryURL = a.config.IssuerURL
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(discoveryURL, "/")+"/.well-known/openid-configuration",
		nil,
	)
	if err != nil {
		return fmt.Errorf("build OIDC readiness request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("request OIDC discovery: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OIDC discovery returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (a *Authenticator) RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := FromContext(r.Context())
		if !ok || !principal.HasRole(a.config.InternalRole) {
			writeAuthError(w, http.StatusForbidden, "INTERNAL_ROLE_REQUIRED", "internal service role is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) principal(token *oidc.IDToken) (Principal, error) {
	var claims map[string]json.RawMessage
	if err := token.Claims(&claims); err != nil {
		return Principal{}, err
	}
	principal := Principal{
		Subject: token.Subject,
		roles:   make(map[string]struct{}),
	}
	if raw, ok := claims[a.config.ProviderClaim]; ok {
		if err := json.Unmarshal(raw, &principal.ProviderID); err != nil {
			return Principal{}, err
		}
	}
	var realm struct {
		Roles []string `json:"roles"`
	}
	if raw, ok := claims["realm_access"]; ok {
		if err := json.Unmarshal(raw, &realm); err != nil {
			return Principal{}, err
		}
		for _, role := range realm.Roles {
			principal.roles[role] = struct{}{}
		}
	}
	return principal, nil
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
