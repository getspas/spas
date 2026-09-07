package gitexec

import "testing"

func TestParsePathOutputPreservesWhitespace(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/work/project ", "/work/project\t", "/work/project\u00a0", "/work/project\r", "/work/project\n", "/work/with spaces/project", " metadata "} {
		got, err := ParsePathOutput([]byte(path + "\n"))
		if err != nil || got != path {
			t.Errorf("ParsePathOutput(%q) = %q, %v", path, got, err)
		}
	}
}

func TestParsePathOutputRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "\n", "/work/project", "/work/\x00project\n"} {
		if _, err := ParsePathOutput([]byte(value)); err == nil {
			t.Errorf("ParsePathOutput(%q) accepted malformed output", value)
		}
	}
}
