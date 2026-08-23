package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/go-pkgz/lgr"
)

func TestLogOptionsFromEnv(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantDebug bool
		wantTrace bool
	}{
		{name: "empty"},
		{name: "info", level: "info"},
		{name: "debug", level: "debug", wantDebug: true},
		{name: "trace", level: "trace", wantDebug: true, wantTrace: true},
		{name: "bogus", level: "bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			opts := append(logOptionsFromEnv(tt.level), lgr.Out(&output))
			logger := lgr.New(opts...)
			logger.Logf("DEBUG debug message")
			debugLogged := strings.Contains(output.String(), "debug message")
			if debugLogged != tt.wantDebug {
				t.Fatalf("debug output presence = %v, want %v; output: %q", debugLogged, tt.wantDebug, output.String())
			}

			output.Reset()
			logger.Logf("TRACE trace message")
			traceLogged := strings.Contains(output.String(), "trace message")
			if traceLogged != tt.wantTrace {
				t.Fatalf("trace output presence = %v, want %v; output: %q", traceLogged, tt.wantTrace, output.String())
			}
		})
	}
}

func TestLogOptionsFromEnvInvalidLevelWarns(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previousOutput)

	logOptionsFromEnv("bogus")
	if !strings.Contains(output.String(), "invalid LOG_LEVEL") {
		t.Fatalf("warning output = %q, want invalid LOG_LEVEL warning", output.String())
	}
}
