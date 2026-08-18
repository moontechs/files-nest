package store

import (
	"testing"
	"time"
)

func TestParseCreationTime(t *testing.T) {
	tests := []struct {
		name string
		date string
		want time.Time
	}{
		{
			name: "RFC3339Nano",
			date: "2024-03-15T10:30:00.123456789Z",
			want: time.Date(2024, time.March, 15, 10, 30, 0, 123456789, time.UTC),
		},
		{
			name: "date only",
			date: "2024-03-15",
			want: time.Date(2024, time.March, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCreationTime(tt.date)
			if !ok {
				t.Fatalf("parseCreationTime(%q) returned ok=false", tt.date)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseCreationTime(%q) = %v, want %v", tt.date, got, tt.want)
			}
		})
	}
}
