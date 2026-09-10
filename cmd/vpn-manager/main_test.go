package main

import (
	"net/http"
	"testing"
	"time"
)

func TestShouldLogRequestFiltersRoutineFastReads(t *testing.T) {
	if shouldLogRequest(http.MethodGet, http.StatusOK, 20*time.Millisecond) {
		t.Fatal("fast successful GET should not be logged")
	}
	for _, test := range []struct {
		method   string
		status   int
		duration time.Duration
	}{
		{http.MethodPost, http.StatusAccepted, 20 * time.Millisecond},
		{http.MethodGet, http.StatusInternalServerError, 20 * time.Millisecond},
		{http.MethodGet, http.StatusOK, 3 * time.Second},
	} {
		if !shouldLogRequest(test.method, test.status, test.duration) {
			t.Fatalf("request should be logged: %+v", test)
		}
	}
}
