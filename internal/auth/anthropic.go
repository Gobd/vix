package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// anthropicProvider implements the Claude Pro/Max OAuth flow (authorization
// code + PKCE with a local callback server). The granted access token is used
// directly as the inference credential (Authorization: Bearer), and refreshed
// on expiry.
//
// IMPORTANT — billing/ToS posture: vix deliberately does NOT impersonate the
// Claude Code CLI. We crib only the public OAuth *client id* to obtain a token
// for the user's own account; we never replicate Claude Code's request
// fingerprint (its identifying system-prompt prefix, user-agent, etc.). As a
// result Anthropic meters this as third-party usage billed per token rather
// than against the subscription's plan limits. The inference adapter
// (internal/daemon/llm/anthropic.go) must keep vix's own system prompt and must
// not add any Claude-Code identity markers.
type anthropicProvider struct {
	clientID     string
	authorizeURL string
	tokenURL     string
	callbackPort int
	callbackPath string
	redirectURI  string
	scopes       string
}

// anthropicClientIDB64 is the public OAuth client id, base64-encoded so it is
// not a bare string in the source.
const anthropicClientIDB64 = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"

func newAnthropicProvider() *anthropicProvider {
	p := &anthropicProvider{
		clientID:     decodeClientID(anthropicClientIDB64),
		authorizeURL: "https://claude.ai/oauth/authorize",
		tokenURL:     "https://platform.claude.com/v1/oauth/token",
		callbackPort: 53692,
		callbackPath: "/callback",
		scopes:       "user:profile user:inference",
	}
	if s, ok := loginSpec("anthropic"); ok {
		if s.ClientIDB64 != "" {
			p.clientID = decodeClientID(s.ClientIDB64)
		} else if s.ClientID != "" {
			p.clientID = s.ClientID
		}
		if s.AuthorizeURL != "" {
			p.authorizeURL = s.AuthorizeURL
		}
		if s.TokenURL != "" {
			p.tokenURL = s.TokenURL
		}
		if s.CallbackPort != 0 {
			p.callbackPort = s.CallbackPort
		}
		if s.CallbackPath != "" {
			p.callbackPath = s.CallbackPath
		}
		if s.Scope != "" {
			p.scopes = s.Scope
		}
	}
	p.redirectURI = fmt.Sprintf("http://localhost:%d%s", p.callbackPort, p.callbackPath)
	return p
}

func (p *anthropicProvider) ID() string                  { return "anthropic" }
func (p *anthropicProvider) Name() string                { return "Anthropic (Claude Pro/Max)" }
func (p *anthropicProvider) UsesCallbackServer() bool    { return true }
func (p *anthropicProvider) APIKey(c Credentials) string { return c.Access }

func (p *anthropicProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, err
	}

	// The PKCE verifier doubles as the OAuth state value.
	server, err := startCallbackServer(callbackHost(), p.callbackPort, p.callbackPath,
		"Anthropic authentication completed. You can close this window.", verifier)
	if err != nil {
		return Credentials{}, err
	}
	defer server.close()

	authParams := url.Values{}
	authParams.Set("code", "true")
	authParams.Set("client_id", p.clientID)
	authParams.Set("response_type", "code")
	authParams.Set("redirect_uri", p.redirectURI)
	authParams.Set("scope", p.scopes)
	authParams.Set("code_challenge", challenge)
	authParams.Set("code_challenge_method", "S256")
	authParams.Set("state", verifier)

	authURL := p.authorizeURL + "?" + authParams.Encode()
	lg().Info("anthropic: authorization URL ready (waiting for browser callback)", "url", authURL, "redirect_uri", p.redirectURI)
	if cb.OnAuth != nil {
		cb.OnAuth(AuthInfo{
			URL: authURL,
			Instructions: "Complete login in your browser. If the browser is on another machine, " +
				"paste the final redirect URL here.",
		})
	}

	code, state, err := waitForAuthorizationCode(ctx, server, cb, verifier)
	if err != nil {
		return Credentials{}, err
	}

	if code == "" && cb.OnPrompt != nil {
		input, err := cb.OnPrompt(Prompt{
			Message:     "Paste the authorization code or full redirect URL:",
			Placeholder: p.redirectURI,
		})
		if err != nil {
			return Credentials{}, err
		}
		parsed := parseAuthorizationInput(input)
		if parsed.state != "" && parsed.state != verifier {
			return Credentials{}, errors.New("OAuth state mismatch")
		}
		code = parsed.code
		state = parsed.state
		if state == "" {
			state = verifier
		}
	}

	if code == "" {
		return Credentials{}, errors.New("missing authorization code")
	}
	if state == "" {
		return Credentials{}, errors.New("missing OAuth state")
	}

	cb.progress("Exchanging authorization code for tokens...")
	return p.exchangeAuthorizationCode(ctx, code, state, verifier)
}

func (p *anthropicProvider) exchangeAuthorizationCode(ctx context.Context, code, state, verifier string) (Credentials, error) {
	data, err := postJSONForToken(ctx, p.tokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     p.clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  p.redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("token exchange request failed. url=%s; redirect_uri=%s; response_type=authorization_code: %w", p.tokenURL, p.redirectURI, err)
	}
	return p.credsFromTokenResponse(data, "exchange")
}

func (p *anthropicProvider) RefreshToken(ctx context.Context, creds Credentials) (Credentials, error) {
	data, err := postJSONForToken(ctx, p.tokenURL, map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     p.clientID,
		"refresh_token": creds.Refresh,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("anthropic token refresh request failed. url=%s: %w", p.tokenURL, err)
	}
	return p.credsFromTokenResponse(data, "refresh")
}

// credsFromTokenResponse parses an OAuth token response into Credentials,
// storing the access token directly (used as a Bearer inference credential).
// A 5-minute skew is subtracted from the expiry so refresh happens slightly
// early.
func (p *anthropicProvider) credsFromTokenResponse(data []byte, op string) (Credentials, error) {
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &token); err != nil || token.AccessToken == "" {
		lg().Error("anthropic: token "+op+" returned invalid JSON", "body_bytes", len(data), "err", err)
		return Credentials{}, fmt.Errorf("anthropic token %s returned invalid response. url=%s; body=%s", op, p.tokenURL, string(data))
	}
	expires := int64(0)
	if token.ExpiresIn > 0 {
		expires = nowMillis() + token.ExpiresIn*1000 - 5*60*1000
	}
	lg().Info("anthropic: token "+op+" succeeded", "expires_in_s", token.ExpiresIn, "access", redact(token.AccessToken), "refresh", redact(token.RefreshToken))
	return Credentials{
		Access:  token.AccessToken,
		Refresh: token.RefreshToken,
		Expires: expires,
	}, nil
}
