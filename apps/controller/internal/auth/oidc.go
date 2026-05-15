// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

// OIDCProvider is implemented by Google, GitHub, and any future identity
// integrations. Implementations are stateless — state lives in the URL
// query string and the user's browser.
type OIDCProvider interface {
	// Name returns a stable identifier used in routes and logs.
	Name() string
	// AuthURL returns the URL to which a browser should be redirected to
	// initiate login. state must round-trip back on the callback to defeat
	// CSRF.
	AuthURL(state, redirectURI string) string
	// Exchange takes the authorization code returned by the provider and
	// returns the resolved user identity.
	Exchange(ctx context.Context, code, redirectURI string) (*UserIdentity, error)
}

// UserIdentity is the minimum we record about a user after OIDC.
type UserIdentity struct {
	Provider    string
	Subject     string
	Email       string
	DisplayName string
}

// GoogleProvider implements OIDCProvider against Google's OAuth/OIDC.
type GoogleProvider struct {
	conf *oauth2.Config
}

// NewGoogleProvider constructs a Google provider; Phase 1 just calls the
// userinfo endpoint to fill UserIdentity.
func NewGoogleProvider(clientID, clientSecret string) *GoogleProvider {
	return &GoogleProvider{
		conf: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			Scopes:       []string{"openid", "email", "profile"},
		},
	}
}

// Name implements OIDCProvider.
func (g *GoogleProvider) Name() string { return "google" }

// AuthURL implements OIDCProvider.
func (g *GoogleProvider) AuthURL(state, redirectURI string) string {
	conf := *g.conf
	conf.RedirectURL = redirectURI
	return conf.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

// Exchange implements OIDCProvider.
func (g *GoogleProvider) Exchange(ctx context.Context, code, redirectURI string) (*UserIdentity, error) {
	conf := *g.conf
	conf.RedirectURL = redirectURI
	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}

	client := conf.Client(ctx, tok)
	resp, err := client.Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo: %s: %s", resp.Status, body)
	}
	var data struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	return &UserIdentity{
		Provider:    g.Name(),
		Subject:     data.Sub,
		Email:       data.Email,
		DisplayName: data.Name,
	}, nil
}

// GitHubProvider implements OIDCProvider against GitHub OAuth. GitHub does
// not run a full OpenID Connect provider; we adapt by calling the REST
// /user endpoint after exchange.
type GitHubProvider struct {
	conf *oauth2.Config
}

// NewGitHubProvider constructs a GitHub provider.
func NewGitHubProvider(clientID, clientSecret string) *GitHubProvider {
	return &GitHubProvider{
		conf: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     github.Endpoint,
			Scopes:       []string{"read:user", "user:email"},
		},
	}
}

// Name implements OIDCProvider.
func (g *GitHubProvider) Name() string { return "github" }

// AuthURL implements OIDCProvider.
func (g *GitHubProvider) AuthURL(state, redirectURI string) string {
	conf := *g.conf
	conf.RedirectURL = redirectURI
	return conf.AuthCodeURL(state)
}

// Exchange implements OIDCProvider.
func (g *GitHubProvider) Exchange(ctx context.Context, code, redirectURI string) (*UserIdentity, error) {
	conf := *g.conf
	conf.RedirectURL = redirectURI
	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}
	client := conf.Client(ctx, tok)

	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return nil, fmt.Errorf("github /user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github /user: %s: %s", resp.Status, body)
	}
	var data struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode github /user: %w", err)
	}
	// GitHub returns null email if user keeps it private; fall back to
	// querying the /user/emails endpoint for the primary verified entry.
	if data.Email == "" {
		emailResp, err := client.Get("https://api.github.com/user/emails")
		if err == nil && emailResp.StatusCode == http.StatusOK {
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			if json.NewDecoder(emailResp.Body).Decode(&emails) == nil {
				for _, e := range emails {
					if e.Primary && e.Verified {
						data.Email = e.Email
						break
					}
				}
			}
			_ = emailResp.Body.Close()
		}
	}
	displayName := data.Name
	if displayName == "" {
		displayName = data.Login
	}
	return &UserIdentity{
		Provider:    g.Name(),
		Subject:     fmt.Sprintf("%d", data.ID),
		Email:       data.Email,
		DisplayName: displayName,
	}, nil
}

// NewState produces an opaque CSRF/replay-resistant state token. We pack
// (random nonce, tenant slug, expiry) into a JSON blob and HMAC-sign it
// with the same secret used for session JWTs. The unsigned form is
// base64-url for inclusion in URLs.
type stateClaims struct {
	Nonce  string `json:"n"`
	Tenant string `json:"t"`
	// Invite carries the invitation UUID (string) when the login was
	// initiated via an invite link. Empty for regular sign-ins. The
	// callback uses it to redeem the invitation atomically with user
	// creation. It travels via the signed-and-HMAC'd state so a
	// tampered link cannot inject a fake invite into a session.
	Invite string `json:"i,omitempty"`
	// AppCallback is the native-app callback URI the controller should
	// redirect to once the OIDC exchange completes (carrying the freshly
	// minted session JWT). Set when the login was initiated by a native
	// client via ?app_callback=bamboo://... Empty for browser flows
	// (which render the HTML success page instead). The value rides in
	// the signed state so a tampered query-string can't redirect the
	// session token to an attacker-controlled URI.
	AppCallback string `json:"a,omitempty"`
	ExpiresAt   int64  `json:"exp"`
}

// OIDCStateClaims is the public projection of state contents returned
// by VerifyOIDCState — Tenant always populated, Invite empty for
// regular flows.
type OIDCStateClaims struct {
	Tenant      string
	Invite      string
	AppCallback string
}

// IssueOIDCState returns a signed state token bound to a tenant slug.
// inviteID is the invitation UUID (string) when the login was started
// via an invite link, or empty for a regular sign-in. appCallback is the
// native-app callback URI (e.g. "bamboo://auth/callback") set when the
// flow was initiated by a native client; empty for browser flows.
func IssueOIDCState(secret []byte, tenantSlug, inviteID, appCallback string, ttl time.Duration) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	claims := stateClaims{
		Nonce:       base64.RawURLEncoding.EncodeToString(nonce),
		Tenant:      tenantSlug,
		Invite:      inviteID,
		AppCallback: appCallback,
		ExpiresAt:   time.Now().Add(ttl).Unix(),
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(body)
	mac := signHMAC(secret, []byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifyOIDCState validates a state token and returns the bound
// tenant slug + optional invite id. Invite is empty for state tokens
// minted without an invitation context.
func VerifyOIDCState(secret []byte, state string) (OIDCStateClaims, error) {
	var out OIDCStateClaims
	body, sig, ok := splitOnDot(state)
	if !ok {
		return out, ErrInvalidToken
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return out, ErrInvalidToken
	}
	want := signHMAC(secret, []byte(body))
	if !constantTimeEqual(gotSig, want) {
		return out, ErrInvalidToken
	}
	rawBody, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return out, ErrInvalidToken
	}
	var claims stateClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return out, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return out, ErrInvalidToken
	}
	out.Tenant = claims.Tenant
	out.Invite = claims.Invite
	out.AppCallback = claims.AppCallback
	return out, nil
}
