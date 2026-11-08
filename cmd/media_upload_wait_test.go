package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func fileStatusServer(t *testing.T, polls []string) (*httptest.Server, *int32) {
	t.Helper()
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"file_1","type":"files","attributes":{}}}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"file_1","type":"files","attributes":{"status_upload":"PENDING"},"meta":{"upload_url":%q}}}`,
				"http://"+r.Host+"/s3put")
		case r.Method == http.MethodGet:
			i := int(atomic.AddInt32(&gets, 1)) - 1
			if i >= len(polls) {
				i = len(polls) - 1
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(polls[i]))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

func writeTempUpload(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
		t.Fatalf("write temp upload: %v", err)
	}
	return p
}

func fileState(mime, up, tc string) string {
	transcode := "null"
	if tc != "" {
		transcode = fmt.Sprintf("%q", tc)
	}
	return fmt.Sprintf(
		`{"data":{"id":"file_1","type":"files","attributes":{"mime_type":%q,"status_upload":%q,"status_transcode":%s,"status_transcribe":null}}}`,
		mime, up, transcode)
}

func fastPolling(t *testing.T, grace time.Duration) {
	t.Helper()
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = time.Millisecond, grace
	t.Cleanup(func() { mediaPollInterval, transcodeStartGrace = origInterval, origGrace })
}

func TestUploadWait_VideoWaitsForTranscodeToStartThenFinish(t *testing.T) {
	fastPolling(t, time.Second)

	srv, gets := fileStatusServer(t, []string{
		fileState("video/mp4", "READY", ""),
		fileState("video/mp4", "READY", "PENDING"),
		fileState("video/mp4", "READY", "PROCESSING"),
		fileState("video/mp4", "READY", "READY"),
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "10s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n < 4 {
		t.Errorf("polled %d time(s), want >= 4 — returning early on a null status_transcode is exactly the bug", n)
	}
	if !strings.Contains(res.Stdout, `"status_transcode": "READY"`) &&
		!strings.Contains(res.Stdout, `"status_transcode":"READY"`) {
		t.Errorf("the returned resource must be the TRANSCODED one, not the null-transcode snapshot; stdout=%q", res.Stdout)
	}
}

func TestUploadWait_NonVideoReturnsAsSoonAsUploadIsReady(t *testing.T) {
	fastPolling(t, time.Hour)

	srv, gets := fileStatusServer(t, []string{fileState("image/png", "READY", "")})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "pic.png"),
			"--wait", "--timeout", "300ms", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n != 1 {
		t.Errorf("polled %d time(s), want exactly 1 — an image never transcodes and must not be waited on", n)
	}
	if strings.Contains(res.Stderr, "transcod") {
		t.Errorf("a non-video upload must say nothing about transcoding; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_VideoWhoseTranscodeNeverStartsIsBoundedAndSucceeds(t *testing.T) {
	const grace = 200 * time.Millisecond
	fastPolling(t, grace)

	srv, gets := fileStatusServer(t, []string{fileState("video/mp4", "READY", "")})

	start := time.Now()
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "30s", "--output", "json")...)
	elapsed := time.Since(start)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — the upload SUCCEEDED; failing here breaks every video upload on a video-disabled backend; stderr=%q",
			res.Code, res.Stderr)
	}
	if elapsed < grace {
		t.Errorf("gave up after %s but the grace is %s — the window is no longer governed by "+
			"transcodeStartGrace; a shortened window ships green against an upper bound alone", elapsed, grace)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s — the wait must be bounded by the transcode-start grace, not by --timeout (30s)", elapsed)
	}
	if n := atomic.LoadInt32(gets); n < 2 {
		t.Errorf("polled %d time(s), want >= 2 — it must actually WAIT out the grace before giving up, not return on poll 1", n)
	}
	if !strings.Contains(res.Stderr, "had not started") {
		t.Errorf("giving up on the transcode must be disclosed — silence here is the original bug with extra steps; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_TranscodeFailedIsAnError(t *testing.T) {
	fastPolling(t, time.Second)

	srv, _ := fileStatusServer(t, []string{
		fileState("video/mp4", "READY", ""),
		fileState("video/mp4", "READY", "FAILED"),
	})

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "5s", "--output", "json")...)
	if err == nil {
		t.Fatal("a FAILED transcode must not succeed")
	}
	if !strings.Contains(err.Error(), "processing failed") || !strings.Contains(err.Error(), "transcode=FAILED") {
		t.Errorf("the error must name the FAILED transcode — a timeout produces the same exit code, so only "+
			"the message distinguishes 'the transcode failed' from 'we gave up waiting'; err=%v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("this must fail on the FAILED status, not by running out the clock; err=%v", err)
	}
}

func TestUploadWait_AudioIsNotWaitedOnForTranscode(t *testing.T) {
	fastPolling(t, time.Hour)

	srv, gets := fileStatusServer(t, []string{fileState("audio/mpeg", "READY", "")})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "song.mp3"),
			"--wait", "--timeout", "300ms", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n != 1 {
		t.Errorf("polled %d time(s), want exactly 1 — only video transcodes; audio must not be waited on", n)
	}
}

func TestUploadWait_UnknownMimeReturnsButSaysSo(t *testing.T) {
	fastPolling(t, time.Hour)

	srv, gets := fileStatusServer(t, []string{
		`{"data":{"id":"file_1","type":"files","attributes":{"status_upload":"READY","status_transcode":null,"status_transcribe":null}}}`,
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "mystery.bin"),
			"--wait", "--timeout", "300ms", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — an unclassifiable file must not fail the upload; stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n != 1 {
		t.Errorf("polled %d time(s), want exactly 1 — with no mime_type there is nothing to wait FOR", n)
	}
	if !strings.Contains(res.Stderr, "no mime_type") {
		t.Errorf("returning without waiting must be disclosed when the kind is unknown; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_GiveUpWarningReportsTheEffectiveWindow(t *testing.T) {
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = time.Millisecond, time.Hour
	t.Cleanup(func() { mediaPollInterval, transcodeStartGrace = origInterval, origGrace })

	srv, _ := fileStatusServer(t, []string{fileState("video/mp4", "READY", "")})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "400ms", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "1h0m0s") {
		t.Errorf("the warning reported the GRACE CONSTANT, not the window it actually waited "+
			"(--timeout truncated it); stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "had not started") {
		t.Errorf("the give-up must still be disclosed; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_TranscodeWindowOpensWhenTheUploadIsReady(t *testing.T) {
	fastPolling(t, 40*time.Millisecond)

	polls := make([]string, 0, 64)
	for i := 0; i < 60; i++ {
		polls = append(polls, fileState("video/mp4", "PROCESSING", ""))
	}
	polls = append(polls,
		fileState("video/mp4", "READY", ""),
		fileState("video/mp4", "READY", "PROCESSING"),
		fileState("video/mp4", "READY", "READY"),
	)

	srv, _ := fileStatusServer(t, polls)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "20s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "had not started") {
		t.Errorf("the grace was spent waiting for the UPLOAD, so the transcode never got its window; stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, `"status_transcode": "READY"`) {
		t.Errorf("must return the TRANSCODED resource — the window opens at upload READY; stdout=%q", res.Stdout)
	}
}

func TestUploadWait_GiveUpWindowIsNeverNegative(t *testing.T) {
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = 60*time.Millisecond, time.Hour
	t.Cleanup(func() { mediaPollInterval, transcodeStartGrace = origInterval, origGrace })

	srv, _ := fileStatusServer(t, []string{
		fileState("video/mp4", "PROCESSING", ""),
		fileState("video/mp4", "READY", ""),
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "20ms", "--output", "json")...)

	if !strings.Contains(res.Stderr, "had not started") {
		t.Fatalf("precondition: the give-up warning must have been emitted, else this test asserts nothing; stderr=%q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "after -") {
		t.Errorf("the warning reported a NEGATIVE wait — the planned window is negative once --timeout is already "+
			"spent, so the message must report time ELAPSED, not the plan; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_MimeTypeMatchIsCaseInsensitive(t *testing.T) {
	fastPolling(t, time.Second)

	srv, gets := fileStatusServer(t, []string{
		fileState("Video/MP4", "READY", ""),
		fileState("Video/MP4", "READY", "PROCESSING"),
		fileState("Video/MP4", "READY", "READY"),
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "10s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n < 3 {
		t.Errorf("polled %d time(s), want >= 3 — `Video/MP4` IS a video (the backend lowercases before "+
			"deriving asset_kind), so it must be waited on", n)
	}
}

func TestUploadWait_DocumentedGraceMatchesTheConstant(t *testing.T) {
	want := transcodeStartGrace.String()

	flag := mediaFilesUploadCmd.Flags().Lookup("wait")
	if flag == nil {
		t.Fatal("--wait flag not registered")
	}
	replaceFlag := mediaFilesReplaceCmd.Flags().Lookup("wait")
	if replaceFlag == nil {
		t.Fatal("replace --wait flag not registered")
	}
	for name, text := range map[string]string{
		"--wait help":         flag.Usage,
		"upload Long help":    mediaFilesUploadCmd.Long,
		"replace --wait help": replaceFlag.Usage,
		"replace Long help":   mediaFilesReplaceCmd.Long,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not state the real grace (%s); it reads: %s", name, want, text)
		}
	}

	for _, path := range []string{"../llms.txt", "../docs/internal/api-surface.md"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(b)
		i := strings.Index(body, "--wait` and video transcoding")
		if i < 0 {
			t.Errorf("%s carries no --wait/transcoding section to check", path)
			continue
		}
		section := body[i:min(i+2500, len(body))]
		if !strings.Contains(section, "capped at "+want) {
			t.Errorf("%s does not document the real grace (%q) — the constant moved and the docs did not; "+
				"agents execute this text literally", path, "capped at "+want)
		}
		if !strings.Contains(section, "one "+mediaPollInterval.String()+" poll interval") {
			t.Errorf("%s does not document the real poll interval (%s)", path, mediaPollInterval)
		}
	}
}

func TestUploadWait_UnknownKindNoteDoesNotSuppressTheGiveUpNote(t *testing.T) {
	fastPolling(t, 60*time.Millisecond)

	srv, _ := fileStatusServer(t, []string{
		`{"data":{"id":"file_1","type":"files","attributes":{"status_upload":"PROCESSING","status_transcode":null,"status_transcribe":null}}}`,
		fileState("video/mp4", "READY", ""),
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
			"--wait", "--timeout", "10s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "had not started") {
		t.Errorf("the give-up warning must still print — an earlier unknown-kind note must not suppress it, "+
			"or an untranscoded video is returned silently; stderr=%q", res.Stderr)
	}
}

func TestUploadWait_TerminalTranscribeStatesAreNotWaitedOn(t *testing.T) {
	for _, tr := range []string{"NOT_APPLICABLE", "REJECTED"} {
		t.Run(tr, func(t *testing.T) {
			fastPolling(t, time.Second)

			body := fmt.Sprintf(
				`{"data":{"id":"file_1","type":"files","attributes":{"mime_type":"video/mp4","status_upload":"READY","status_transcode":"READY","status_transcribe":%q}}}`, tr)
			srv, gets := fileStatusServer(t, []string{body})

			start := time.Now()
			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
					"--wait", "--timeout", "3s", "--output", "json")...)
			elapsed := time.Since(start)

			if res.Code != errs.ExitOK {
				t.Errorf("exit = %d, want 0 — %s is TERMINAL and the upload succeeded; waiting for it to become "+
					"READY turns a success into a timeout failure; stderr=%q", res.Code, tr, res.Stderr)
			}
			if elapsed > 2*time.Second {
				t.Errorf("took %s — a terminal %s must not be waited on at all", elapsed, tr)
			}
			if n := atomic.LoadInt32(gets); n != 1 {
				t.Errorf("polled %d time(s), want exactly 1 — %s will never change", n, tr)
			}
		})
	}
}

func TestUploadWait_InFlightTranscribeIsStillWaitedOn(t *testing.T) {
	for _, tr := range []string{"PENDING", "PROCESSING"} {
		t.Run(tr, func(t *testing.T) {
			fastPolling(t, time.Second)

			inFlight := fmt.Sprintf(
				`{"data":{"id":"file_1","type":"files","attributes":{"mime_type":"video/mp4","status_upload":"READY","status_transcode":"READY","status_transcribe":%q}}}`, tr)
			srv, gets := fileStatusServer(t, []string{
				inFlight,
				`{"data":{"id":"file_1","type":"files","attributes":{"mime_type":"video/mp4","status_upload":"READY","status_transcode":"READY","status_transcribe":"READY"}}}`,
			})

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "media", "files", "upload", writeTempUpload(t, "clip.mp4"),
					"--wait", "--timeout", "10s", "--output", "json")...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if n := atomic.LoadInt32(gets); n < 2 {
				t.Errorf("polled %d time(s), want >= 2 — %s is not terminal and must still be waited on", n, tr)
			}
		})
	}
}
