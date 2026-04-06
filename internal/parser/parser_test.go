package parser

import (
	"os"
	"reflect"
	"testing"
)

func TestParseBoxfile(t *testing.T) {
	// Create a temporary MiniBox file
	content := []byte(`
# This is a comment
FROM alpine:latest
WORKDIR /app
COPY . .
RUN echo "hello world"
CMD sh
`)
	tmpfile, err := os.CreateTemp("", "MiniBox")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfile, err := ParseBoxfile(tmpfile.Name())
	if err != nil {
		t.Fatalf("ParseBoxfile failed: %v", err)
	}

	if cfile.BaseImage != "alpine:latest" {
		t.Errorf("Expected BaseImage 'alpine:latest', got '%s'", cfile.BaseImage)
	}

	if len(cfile.Instructions) != 3 {
		t.Errorf("Expected 3 instructions, got %d", len(cfile.Instructions))
	}

	if !reflect.DeepEqual(cfile.Cmd, []string{"sh"}) {
		t.Errorf("Expected Cmd ['sh'], got %v", cfile.Cmd)
	}
}
