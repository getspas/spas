package publicgit

import "testing"

func TestCountWorktreeRecords(t *testing.T) {
	t.Parallel()

	single := "worktree /home/user/project\x00HEAD 0123456789abcdef0123456789abcdef01234567\x00branch refs/heads/main\x00\x00"
	if got := countWorktreeRecords([]byte(single)); got != 1 {
		t.Fatalf("countWorktreeRecords(single) = %d, want 1", got)
	}
	linked := single + "worktree /home/user/project-hotfix\x00HEAD 89abcdef0123456789abcdef0123456789abcdef\x00detached\x00\x00"
	if got := countWorktreeRecords([]byte(linked)); got != 2 {
		t.Fatalf("countWorktreeRecords(linked) = %d, want 2", got)
	}
	// A body line must not be confused with a record header.
	tricky := "worktree /home/user/with\nnewline\x00HEAD 0123456789abcdef0123456789abcdef01234567\x00branch refs/heads/worktree-experiments\x00\x00"
	if got := countWorktreeRecords([]byte(tricky)); got != 1 {
		t.Fatalf("countWorktreeRecords(tricky) = %d, want 1", got)
	}
	if got := countWorktreeRecords(nil); got != 0 {
		t.Fatalf("countWorktreeRecords(nil) = %d, want 0", got)
	}
}
