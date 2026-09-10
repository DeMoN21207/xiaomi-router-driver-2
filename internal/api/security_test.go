package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerAuthRequiresConfiguredToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := BearerAuth(next, "secret-token")

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/rules/apply", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("request without token = %d, want 401", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/rules/apply", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("request with token = %d, want 204", authorized.Code)
	}
}

func TestLimitRequestBodyRejectsOversizedPayload(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Body.Read(make([]byte, 32)); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, err)
			return
		}
	})
	handler := LimitRequestBody(next, 4)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/system/update/upload", strings.NewReader("too large")))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request = %d, want 413", recorder.Code)
	}
}
