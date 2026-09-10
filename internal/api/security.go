package api

import (
	"crypto/subtle"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const APITokenFileName = "api-token"

func LoadAPIToken(dataDir string) string {
	if token := strings.TrimSpace(os.Getenv("VPN_MANAGER_API_TOKEN")); token != "" {
		return token
	}
	data, err := os.ReadFile(filepath.Join(dataDir, APITokenFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func BearerAuth(next http.Handler, token string) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="vpn-manager"`)
			writeError(w, http.StatusUnauthorized, os.ErrPermission)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func LimitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}
