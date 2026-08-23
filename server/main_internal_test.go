package main

import (
	"bytes"
	"fmt"
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

func TestSetupStdLoggerAppliesLogLevel(t *testing.T) {
	if os.Getenv("LGR_STD_LOG_HELPER") == "1" {
		var output bytes.Buffer
		opts := append(logOptionsFromEnv(os.Getenv("LGR_STD_LOG_LEVEL")), lgr.Out(&output), lgr.Err(&output))
		lgr.SetupStdLogger(opts...)
		log.Print(os.Getenv("LGR_STD_LOG_MESSAGE"))
		if _, err := fmt.Print(output.String()); err != nil {
			t.Fatalf("write helper output: %v", err)
		}

		return
	}

	tests := []struct {
		name   string
		level  string
		message string
		wantLog bool
	}{
		{name: "info suppresses debug", level: "info", message: "DEBUG debug message"},
		{name: "debug emits debug", level: "debug", message: "DEBUG debug message", wantLog: true},
		{name: "trace emits trace", level: "trace", message: "TRACE trace message", wantLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSetupStdLoggerAppliesLogLevel$")
			cmd.Env = append(os.Environ(), "LGR_STD_LOG_HELPER=1", "LGR_STD_LOG_LEVEL="+tt.level,
				"LGR_STD_LOG_MESSAGE="+tt.message)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper process failed: %v; output: %s", err, output)
			}

			gotLog := strings.Contains(string(output), tt.message)
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
