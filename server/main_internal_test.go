package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
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
		{name: "empty", level: "", wantDebug: false, wantTrace: false},
		{name: logLevelInfo, level: logLevelInfo, wantDebug: false, wantTrace: false},
		{name: logLevelDebug, level: logLevelDebug, wantDebug: true, wantTrace: false},
		{name: logLevelTrace, level: logLevelTrace, wantDebug: true, wantTrace: true},
		{name: "bogus", level: "bogus", wantDebug: false, wantTrace: false},
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

func TestSetupStdLoggerAppliesLogLevel(t *testing.T) {
	if os.Getenv("LGR_STD_LOG_HELPER") == "1" {
		var output bytes.Buffer
		opts := append(logOptionsFromEnv(os.Getenv("LGR_STD_LOG_LEVEL")), lgr.Out(&output), lgr.Err(&output))
		lgr.SetupStdLogger(opts...)
		//nolint:gosec // G706: LGR_STD_LOG_MESSAGE is set by the parent test process below, not remote input
		log.Print(os.Getenv("LGR_STD_LOG_MESSAGE"))
		if _, err := os.Stdout.WriteString(output.String()); err != nil {
			t.Fatalf("write helper output: %v", err)
		}

		return
	}

	tests := []struct {
		name    string
		level   string
		message string
		wantLog bool
	}{
		{name: "info suppresses debug", level: logLevelInfo, message: "DEBUG debug message", wantLog: false},
		{name: "debug emits debug", level: logLevelDebug, message: "DEBUG debug message", wantLog: true},
		{name: "trace emits trace", level: logLevelTrace, message: "TRACE trace message", wantLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:gosec // G204/G702: os.Args[0] is this test binary re-executing itself, and the -test.run
			// filter is a fixed string literal — neither is attacker-controlled.
			cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestSetupStdLoggerAppliesLogLevel$")
			cmd.Env = append(os.Environ(), "LGR_STD_LOG_HELPER=1", "LGR_STD_LOG_LEVEL="+tt.level,
				"LGR_STD_LOG_MESSAGE="+tt.message)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper process failed: %v; output: %s", err, output)
			}

			// lgr strips the "LEVEL " prefix it parses from the message and
			// re-inserts the level plus caller info, so the logged line never
			// contains the original message verbatim — match on the body only.
			body := strings.TrimPrefix(strings.TrimPrefix(tt.message, "DEBUG "), "TRACE ")
			gotLog := strings.Contains(string(output), body)
			if gotLog != tt.wantLog {
				t.Fatalf("log presence = %v, want %v; output: %q", gotLog, tt.wantLog, output)
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
