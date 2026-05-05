package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// RespondUnauthorized writes a 401 with the WWW-Authenticate header set per
// RFC 7235. Auth middleware implementations should call this on every
// rejection so the response is RFC-compliant and clients get a hint about
// which auth scheme to use.
//
// Body is intentionally empty — auth-failure responses should not leak
// information that could help an attacker probe (e.g. distinguishing
// "missing header" from "wrong token").
func RespondUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="proto-enum-api"`)
	w.WriteHeader(http.StatusUnauthorized)
}

// RequireBearer wraps next so requests without a valid bearer token are rejected.
//
// ============================================================================
//
//	★ LEARNING-MODE CONTRIBUTION POINT ★
//
// ============================================================================
//
// Implement this middleware. The signature is fixed (so router.go keeps
// working), but the body is yours. ~5–10 lines of Go.
//
// Required behavior:
//
//  1. Read the Authorization header. The expected format is:
//
//     Authorization: Bearer <secret>
//
//     Anything else (missing header, wrong scheme, no token) → call
//     RespondUnauthorized(w) and return.
//
//  2. Compare the presented token to `secret`. Use
//     `crypto/subtle.ConstantTimeCompare` so the comparison time does
//     not leak information about how many characters matched. A naive
//     `==` works functionally but is a textbook timing-attack target.
//
//  3. On failure: call RespondUnauthorized(w) (sets WWW-Authenticate +
//     emits 401 with empty body) and return.
//
//  4. On success: call `next.ServeHTTP(w, r)`.
//
// Trade-offs to think about while you write it:
//
//   - `strings.HasPrefix` vs `strings.SplitN` vs `strings.CutPrefix` for
//     parsing the header — which is clearest?
//
//   - `ConstantTimeCompare` returns 0 OR 1, and it requires equal-length
//     inputs to be meaningful. What happens if the presented token is
//     a different length than the secret? (Hint: hashing both sides
//     with SHA-256 first sidesteps the length-leak issue entirely.)
//
//   - Should the middleware short-circuit if `secret` is empty? main.go
//     already logs a warning in that case; you decide whether empty
//     secret means "deny everything" or "let everything through".
//
// When you've filled this in, the handler tests in handlers_test.go will
// cover both the auth-rejection and auth-success paths.
func RequireBearer(secret string, next http.Handler) http.Handler {
	secretHash := sha256.Sum256([]byte(secret))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || secret == "" {
			RespondUnauthorized(w)
			return
		}

		tokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(tokenHash[:], secretHash[:]) != 1 {
			RespondUnauthorized(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}
