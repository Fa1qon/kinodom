package torrents

import "testing"

func TestNeedsStoredFileVerifyWhenFileIsMissing(t *testing.T) {
	if !needsStoredFileVerify(`C:\does-not-exist\film.mkv`, 100) {
		t.Fatal("missing stored file must be verified again")
	}
}
