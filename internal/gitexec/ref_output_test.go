package gitexec

import "testing"

func TestParseRefOutputPreservesLiteralNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ output, namespace, want string }{
		{"refs/heads/main\u00a0\n", "refs/heads/", "main\u00a0"},
		{"refs/remotes/origin/main\n", "refs/remotes/origin/", "main"},
		{"\u00a0topic\u00a0\n", "", "\u00a0topic\u00a0"},
		{"refs/heads/refs/heads/main\n", "refs/heads/", "refs/heads/main"},
	} {
		got, err := ParseRefOutput([]byte(test.output), test.namespace)
		if err != nil || got != test.want {
			t.Errorf("ParseRefOutput(%q, %q) = %q, %v", test.output, test.namespace, got, err)
		}
	}
}

func TestParseRefOutputRejectsMalformedIdentity(t *testing.T) {
	t.Parallel()
	for _, output := range []string{"", "refs/heads/\n", "refs/heads/main", "refs/tags/main\n", "refs/heads/main\r\n", "refs/heads/main\nextra\n", "refs/heads/ma\x00in\n"} {
		if _, err := ParseRefOutput([]byte(output), "refs/heads/"); err == nil {
			t.Errorf("accepted malformed ref %q", output)
		}
	}
}
