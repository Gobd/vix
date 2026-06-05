package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// openRouterProvider implements OpenRouter's OAuth PKCE flow
// (https://openrouter.ai/docs/guides/overview/auth/oauth). Unlike the
// subscription providers it uses no client id — the app is identified solely by
// its localhost callback URL — and the flow yields a normal, user-scoped
// OpenRouter API key (not a refreshable token). The key never expires, so
// RefreshToken is never invoked.
type openRouterProvider struct {
	authorizeURL string
	keysURL      string
	callbackPort int
	callbackPath string
}

func newOpenRouterProvider() *openRouterProvider {
	p := &openRouterProvider{
		authorizeURL: "https://openrouter.ai/auth",
		keysURL:      "https://openrouter.ai/api/v1/auth/keys",
		callbackPort: 53781,
		callbackPath: "/callback",
	}
	if s, ok := loginSpec("openrouter"); ok {
		if s.AuthorizeURL != "" {
			p.authorizeURL = s.AuthorizeURL
		}
		if s.KeysURL != "" {
			p.keysURL = s.KeysURL
		}
		if s.CallbackPort != 0 {
			p.callbackPort = s.CallbackPort
		}
		if s.CallbackPath != "" {
			p.callbackPath = s.CallbackPath
		}
	}
	return p
}

func (p *openRouterProvider) ID() string                  { return "openrouter" }
func (p *openRouterProvider) Name() string                { return "OpenRouter" }
func (p *openRouterProvider) UsesCallbackServer() bool    { return true }
func (p *openRouterProvider) APIKey(c Credentials) string { return c.Access }

func (p *openRouterProvider) redirectURI() string {
	return fmt.Sprintf("http://localhost:%d%s", p.callbackPort, p.callbackPath)
}

func (p *openRouterProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, err
	}

	// OpenRouter does not echo a `state` parameter, so the callback server
	// accepts an empty state; CSRF protection rests on PKCE plus the
	// single-use, loopback-only callback server.
	server, err := startCallbackServer(callbackHost(), p.callbackPort, p.callbackPath,
		"OpenRouter authentication completed. You can close this window.", "")
	if err != nil {
		return Credentials{}, err
	}
	defer server.close()

	q := url.Values{}
	q.Set("callback_url", p.redirectURI())
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL := p.authorizeURL + "?" + q.Encode()

	lg().Info("openrouter: authorization URL ready (waiting for browser callback)", "url", authURL, "redirect_uri", p.redirectURI())
	if cb.OnAuth != nil {
		cb.OnAuth(AuthInfo{
			URL:          authURL,
			Instructions: "Authorize vix in your browser. If the browser is on another machine, paste the redirect URL here.",
		})
	}

	code, _, err := waitForAuthorizationCode(ctx, server, cb, "")
	if err != nil {
		return Credentials{}, err
	}
	if code == "" && cb.OnPrompt != nil {
		input, err := cb.OnPrompt(Prompt{
			Message:     "Paste the authorization code or full redirect URL:",
			Placeholder: p.redirectURI(),
		})
		if err != nil {
			return Credentials{}, err
		}
		code = parseAuthorizationInput(input).code
	}
	if code == "" {
		return Credentials{}, errors.New("missing authorization code")
	}

	cb.progress("Exchanging authorization code for an API key...")
	key, err := p.exchangeCodeForKey(ctx, code, verifier)
	if err != nil {
		return Credentials{}, err
	}
	lg().Info("openrouter: minted user API key", "api_key", redact(key))
	// Stored as a non-expiring access credential; used as a normal API key.
	return Credentials{Access: key}, nil
}

func (p *openRouterProvider) exchangeCodeForKey(ctx context.Context, code, verifier string) (string, error) {
	data, err := postJSONForToken(ctx, p.keysURL, map[string]any{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	if err != nil {
		return "", fmt.Errorf("openrouter key exchange failed. url=%s: %w", p.keysURL, err)
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Key == "" {
		return "", fmt.Errorf("openrouter key exchange returned no key. body=%s", string(data))
	}
	return resp.Key, nil
}

// RefreshToken is never called (OpenRouter keys do not expire); it returns the
// credentials unchanged to satisfy the Provider interface.
func (p *openRouterProvider) RefreshToken(_ context.Context, creds Credentials) (Credentials, error) {
	return creds, nil
}
