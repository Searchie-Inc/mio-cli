package cmd

// media_upload_wait_test.go — `media files upload --wait` processing gate (MIO-3001).
//
// THE ORACLE IS THE POLL SEQUENCE. Every test here drives a stub that changes
// its answer between polls, exactly as the real backend does, and asserts on
// what the CLI did across those polls — not on a single terminal snapshot. A
// stub that returned a finished state on the FIRST poll could not fail against
// the bug being fixed here (the CLI returned on poll 1), so it would be a guard
// that cannot fail.

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

// fileStatusServer serves the upload handshake and then answers each file GET
// with the next entry in `polls` (repeating the last one forever), counting the
// status polls so a test can assert HOW MANY happened.
func fileStatusServer(t *testing.T, polls []string) (*httptest.Server, *int32) {
	t.Helper()
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		// Presigned PUT target — the bytes go here.
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"file_1","type":"files","attributes":{}}}`))
		case r.Method == http.MethodPost:
			// File create. The presigned URL lives in the resource META, not in
			// attributes (uploadSinglePart reads created.Meta["upload_url"]).
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"file_1","type":"files","attributes":{"status_upload":"PENDING"},"meta":{"upload_url":%q}}}`,
				"http://"+r.Host+"/s3put")
		case r.Method == http.MethodGet:
			i := int(atomic.AddInt32(&gets, 1)) - 1
			if i >= len(polls) {
				i = len(polls) - 1 // the terminal state repeats
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

// writeTempUpload creates a small real file for the upload to stream.
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

// fastPolling shrinks the poll tick and the transcode-start grace so the tests
// exercise the real loop without wall-clock sleeps.
func fastPolling(t *testing.T, grace time.Duration) {
	t.Helper()
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = time.Millisecond, grace
	t.Cleanup(func() { mediaPollInterval, transcodeStartGrace = origInterval, origGrace })
}

// THE BUG. A freshly finalized video reads status_transcode: null for a window
// before the dispatch handler's arq job sets it. --wait must not read that null
// as "done" — it must keep polling to READY.
func TestUploadWait_VideoWaitsForTranscodeToStartThenFinish(t *testing.T) {
	fastPolling(t, time.Second) // grace far longer than this sequence takes

	srv, gets := fileStatusServer(t, []string{
		fileState("video/mp4", "READY", ""),           // the window: transcode not enqueued YET
		fileState("video/mp4", "READY", "PENDING"),    // handler ran
		fileState("video/mp4", "READY", "PROCESSING"), // job running
		fileState("video/mp4", "READY", "READY"),      // done
	})

	// --timeout is bounded like every other test in this file. Without it this one
	// inherits the CLI's 5-minute default, so a broken terminal condition does not
	// fail by name — it outlives CI's `-timeout 120s` and takes the whole package
	// down with a panic that names nothing.
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

// The narrowing must not cost non-video uploads anything: their transcode
// status is permanently null and must still read as "not applicable".
func TestUploadWait_NonVideoReturnsAsSoonAsUploadIsReady(t *testing.T) {
	fastPolling(t, time.Hour) // a grace this long would hang if it were consulted

	srv, gets := fileStatusServer(t, []string{fileState("image/png", "READY", "")})

	// --timeout is bounded so a regression fails THIS test by name. Without it the
	// hour-long grace outlives Go's package watchdog: the mutation that broke this
	// (awaitsTranscode always true) produced `panic: test timed out`, which is red
	// but names nothing and destroys every other result in the package.
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

// THE REGRESSION GUARD. On a backend with FEATURE_MEDIA_VIDEO_ENABLED off —
// which today includes production — a video's status_transcode stays null
// FOREVER. Waiting for it would burn the whole --timeout and then fail, turning
// a fast success into a slow failure. The wait must be bounded by the grace,
// succeed, and say what happened.
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
	// TWO-SIDED, and the lower bound is the load-bearing half. An upper bound
	// alone only proves "not --timeout": swapping transcodeStartGrace for
	// mediaPollInterval — a 2s window in production instead of 30s — left every
	// gate green, because a 1ms window is also "well under 30s". The wait must be
	// governed BY THE GRACE.
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

// A transcode that reaches FAILED is a real failure and must not be reported as
// a successful wait.
func TestUploadWait_TranscodeFailedIsAnError(t *testing.T) {
	fastPolling(t, time.Second)

	srv, _ := fileStatusServer(t, []string{
		fileState("video/mp4", "READY", ""),
		fileState("video/mp4", "READY", "FAILED"),
	})

	// --timeout is SHORT and the message is asserted. Both are load-bearing:
	// with the default 5m timeout and a bare `!= ExitOK` check, deleting the
	// `tc == "FAILED"` branch left this test PASSING (in 300s) because a TIMEOUT
	// is also a non-zero exit. The exit code alone cannot discriminate — it is
	// ExitGeneric either way — so the failure MESSAGE is the oracle.
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

// audio/* is the kind most likely to be swept into awaitsTranscode by a later
// edit: it has its own asset_kind, its own MediaUploaded handler and a
// transcription pipeline. image/png alone would not notice that mistake.
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

// A file whose mime_type is absent cannot be classified. Returning is right (it
// is what the old code did for everything), but doing it SILENTLY is
// indistinguishable from "this kind never transcodes" when the file may be an
// untranscoded video.
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

// The give-up warning must report the window it ACTUALLY waited. --timeout below
// the grace truncates it, and reporting the 30s constant would have an operator
// diagnose a disabled backend from a 1s wait.
func TestUploadWait_GiveUpWarningReportsTheEffectiveWindow(t *testing.T) {
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = time.Millisecond, time.Hour // grace >> timeout
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

// The transcode window must open when the UPLOAD reaches READY, not when the
// command started. A slow upload would otherwise consume the transcode's grace
// before the transcode could possibly have been enqueued — the wait would then
// give up the instant the upload finished and hand back an untranscoded file
// with a misleading "transcoding had not started" warning.
func TestUploadWait_TranscodeWindowOpensWhenTheUploadIsReady(t *testing.T) {
	fastPolling(t, 40*time.Millisecond)

	// The upload takes many polls; by the time it is READY, a window clocked from
	// the command's start has long expired.
	polls := make([]string, 0, 64)
	for i := 0; i < 60; i++ {
		polls = append(polls, fileState("video/mp4", "PROCESSING", ""))
	}
	polls = append(polls,
		fileState("video/mp4", "READY", ""),           // window must open HERE
		fileState("video/mp4", "READY", "PROCESSING"), // transcode arrives
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

// A --timeout already spent on the upload must not produce a NEGATIVE window in
// the warning (time.Until is negative past the deadline), and the duration must
// be what was actually waited — llms.txt and api-surface.md both promise the
// message names "the window it actually waited".
func TestUploadWait_GiveUpWindowIsNeverNegative(t *testing.T) {
	// The deadline is only checked at poll boundaries, so the overshoot must be a
	// FULL poll interval for the planned window to be visibly negative. With a 1ms
	// interval the overshoot is sub-millisecond and Round(time.Millisecond) prints
	// "0s" — the guard then passes against its own mutation, which is how this
	// test shipped unfailable. A slow interval plus an upload that is still
	// PROCESSING on poll 1 makes the READY observation land a full interval past
	// the deadline.
	origInterval, origGrace := mediaPollInterval, transcodeStartGrace
	mediaPollInterval, transcodeStartGrace = 60*time.Millisecond, time.Hour
	t.Cleanup(func() { mediaPollInterval, transcodeStartGrace = origInterval, origGrace })

	srv, _ := fileStatusServer(t, []string{
		fileState("video/mp4", "PROCESSING", ""), // deadline expires during the sleep
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

// mime_type is compared lowercased because the backend stores the client's value
// verbatim while deriving asset_kind through a lowercasing helper — so
// `--mime-type Video/MP4` is a real video with a non-lowercase MIME. Dropping the
// ToLower left the entire suite green.
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

// The 30s bound is restated in the flag help, the Long description, llms.txt and
// api-surface.md. Nothing bound those copies to the constant: changing
// transcodeStartGrace to 90s left every gate green while all four surfaces kept
// claiming 30s — the MIO-2741 drift shape, in documents agents execute.
func TestUploadWait_DocumentedGraceMatchesTheConstant(t *testing.T) {
	want := transcodeStartGrace.String()

	flag := mediaFilesUploadCmd.Flags().Lookup("wait")
	if flag == nil {
		t.Fatal("--wait flag not registered")
	}
	for name, text := range map[string]string{
		"--wait help":      flag.Usage,
		"upload Long help": mediaFilesUploadCmd.Long,
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
		// Anchored to the PHRASE, not the bare duration: a loose Contains was
		// satisfiable by the unrelated "2s poll interval" text in the same window
		// if the grace ever became 2s.
		if !strings.Contains(section, "capped at "+want) {
			t.Errorf("%s does not document the real grace (%q) — the constant moved and the docs did not; "+
				"agents execute this text literally", path, "capped at "+want)
		}
		// The poll interval is quoted in the same paragraph and drifts the same way.
		if !strings.Contains(section, "one "+mediaPollInterval.String()+" poll interval") {
			t.Errorf("%s does not document the real poll interval (%s)", path, mediaPollInterval)
		}
	}
}

// The two warnings must not share a suppression flag. mime_type is nullable on
// the file resource (`str | None`), so a poll can legitimately report no kind and
// a later one report video/*. With one shared flag the early unknown-kind note
// consumed the suppression and the give-up note never printed — returning an
// untranscoded file silently, which is the original bug wearing a different hat.
func TestUploadWait_UnknownKindNoteDoesNotSuppressTheGiveUpNote(t *testing.T) {
	fastPolling(t, 60*time.Millisecond)

	srv, _ := fileStatusServer(t, []string{
		// Poll 1: upload not done AND no mime_type — trips the unknown-kind note.
		`{"data":{"id":"file_1","type":"files","attributes":{"status_upload":"PROCESSING","status_transcode":null,"status_transcribe":null}}}`,
		// Thereafter: a video whose transcode never starts — must still warn.
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

// status_transcribe has terminal values that are neither null nor READY, and
// waiting for them to "finish" burns the whole --timeout and exits 1 on an upload
// that fully succeeded — the same two-meanings trap as status_transcode, one
// field over. NOT_APPLICABLE is the NORMAL outcome for a video with no speech
// ("READY" if words else "NOT_APPLICABLE", app/media/admin_router.py).
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

// ...but a transcription still in flight IS waited on, so the fix is a narrowing
// and not "stop looking at status_transcribe".
//
// PENDING is the load-bearing case and the first draft did not have it. That
// draft probed "PROCESSING", which the backend NEVER writes for this field
// (`git grep status_transcribe app/` yields FAILED / NOT_APPLICABLE / PENDING /
// READY / REJECTED and nothing else) — an invented value. Adding the REAL
// in-flight value to the terminal set, i.e. returning while a transcription is
// still queued, left the whole suite green.
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
