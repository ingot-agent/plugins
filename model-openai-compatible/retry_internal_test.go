package openaicompat

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterBounds(t *testing.T) {
	now := time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"longer than default runtime cap", "120", 2 * time.Minute},
		{"maximum", "300", 5 * time.Minute},
		{"seconds capped", "3600", 5 * time.Minute},
		{"large seconds do not overflow", "9223372036854775807", 5 * time.Minute},
		{"http date", now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute},
		{"http date capped", now.Add(time.Hour).Format(http.TimeFormat), 5 * time.Minute},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"zero", "0", 0},
		{"negative", "-1", 0},
		{"empty", "", 0},
		{"invalid", "later", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.value, now); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
