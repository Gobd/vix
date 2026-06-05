package auth

import (
	"encoding/base64"

	"github.com/get-vix/vix/internal/providers"
)

// loginSpec returns the registry AuthLogin for id, or false when absent. The
// built-in OAuth provider constructors overlay these data-driven endpoints
// (authorize/token/device URLs, client ids, scopes, callback ports) onto their
// compiled defaults, so a new or relocated endpoint is a providers.json edit
// rather than a code change. The flow mechanics stay compiled.
func loginSpec(id string) (providers.AuthLogin, bool) {
	return providers.Default().AuthLogin(id)
}

// decodeClientID returns the base64-decoded client id, or the input unchanged
// when it is not valid base64 (i.e. already a plain client id).
func decodeClientID(b64 string) string {
	if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
		return string(decoded)
	}
	return b64
}
