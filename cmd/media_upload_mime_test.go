package cmd

import (
	"mime"
	"os"
	"path/filepath"
	"testing"
)

// MIO-5931: macOS's mime table maps .m4a to audio/mp4a-latm, which Apple
// Podcasts refuses to play. The CLI pins the canonical type itself.
func TestSniffMimePinsM4AToAudioMP4(t *testing.T) {
	// Reproduce the macOS table on every OS: the later entry wins there.
	if err := mime.AddExtensionType(".m4a", "audio/mp4a-latm"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := map[string]string{
		"ep.m4a":    "audio/mp4",
		"EP.M4A":    "audio/mp4",
		"ep.mp3":    "audio/mpeg",
		"clip.mp4":  "video/mp4",
		"notes.pdf": "application/pdf",
	}
	for name, want := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := sniffMime(path); got != want {
			t.Errorf("sniffMime(%q) = %q, want %q", name, got, want)
		}
	}
}
