package transport

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

// Socket-only until the separately accepted ingress profile. Never use XFF,
// Forwarded, X-Real-IP or a client field from request JSON to identify a caller.
func credentialClient(r *http.Request) (netip.Addr, error) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || peer.Addr().Zone() != "" {
		return netip.Addr{}, credentials.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE", RetryAfter: 3}
	}
	return peer.Addr().Unmap(), nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	data, err := body(w, r)
	if err == nil {
		data, err = experiment.Canonical(data)
	}
	if err != nil {
		failure(w, err)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err = experiment.Decode(data, &input); err != nil {
		failure(w, err)
		return
	}
	client, err := credentialClient(r)
	if err != nil {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token, err := s.Identity.Login(ctx, identity.LoginInput{Client: client, Username: input.Username, Password: input.Password})
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			err = identityFailure(err)
		}
		failure(w, err)
		return
	}
	write(w, 200, map[string]string{"token": token})
}
