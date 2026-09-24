package cmd

// media_replace_wait_test.go — `media files replace --wait` (MIO-4172).
//
// replace reuses upload's poller (media_upload_wait_test.go covers its
// transcode/transcribe rules), plus ONE rule only replace needs: the backend
// commits the relink AFTER it has answered — mio-backend's get_db commits in a
// request-scoped yield dependency, which FastAPI exits only after the response
// is sent — so a poll issued right after finalize can still read the OLD media.
// That media is typically fully processed (READY/READY), so a poller that did not
// check which media it was looking at would return on poll 1 with the file as it
// was BEFORE the replace, exit 0.
//
// THE ORACLE IS THE POLL SEQUENCE AND THE MEDIA ID. Each stub serves the stale
// pre-replace read first, exactly as the real race does, and the tests assert on
// what the CLI did across the polls and which media it handed back.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const (
	replFileID   = "file_x"
	oldMediaID   = "media_old"
	replMediaID  = "media_new" // the id the replace INIT answers with
	replTeamBase = "/api/v1/teams/t_team1/files/" + replFileID
)

// replState is one poll of the file: which media it points at, and that media's
// statuses.
func replState(mediaID, mime, up, tc string) string {
	transcode := "null"
	if tc != "" {
		transcode = fmt.Sprintf("%q", tc)
	}
	return fmt.Sprintf(
		`{"data":{"id":%q,"type":"files","attributes":{"media_id":%q,"mime_type":%q,"status_upload":%q,"status_transcode":%s,"status_transcribe":null}}}`,
		replFileID, mediaID, mime, up, transcode)
}

// replaceStatusServer serves the replace handshake — single-part (init → PUT →
// finalize) and multipart (init → part → terminal complete) — and answers each
// file GET with the next entry of polls, repeating the last one. The relink
// responses carry the NEW media, as the real ones do (they are built inside the
// uncommitted transaction); only the GETs can see the stale state. Any request
// the flow should not make fails the test.
func replaceStatusServer(t *testing.T, polls []string) (*httptest.Server, *int32) {
	t.Helper()
	var gets int32
	relinked := replState(replMediaID, "video/mp4", "READY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"etag1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && p == replTeamBase+"/replace":
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"file_replacements","attributes":{"file_id":%q,"status_upload":"PENDING"},"meta":{"upload_url":%q}}}`,
				replMediaID, replFileID, "http://"+r.Host+"/s3repl")
		case r.Method == http.MethodPost && p == replTeamBase+"/replace/"+replMediaID+"/finalize":
			_, _ = w.Write([]byte(relinked))
		case r.Method == http.MethodPost && p == replTeamBase+"/replace/multipart":
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"file_replacements","attributes":{"file_id":%q},"meta":{"upload_id":"up1"}}}`,
				replMediaID, replFileID)
		case r.Method == http.MethodPost && p == replTeamBase+"/replace/"+replMediaID+"/multipart/up1/parts/1":
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"file_replacements","meta":{"part_url":%q}}}`,
				replMediaID, "http://"+r.Host+"/s3part1")
		case r.Method == http.MethodPost && p == replTeamBase+"/replace/"+replMediaID+"/multipart/up1/complete":
			_, _ = w.Write([]byte(relinked))
		case r.Method == http.MethodGet && p == replTeamBase:
			i := int(atomic.AddInt32(&gets, 1)) - 1
			if i >= len(polls) {
				i = len(polls) - 1 // the terminal state repeats
			}
			_, _ = w.Write([]byte(polls[i]))
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

// replacePaths runs a test once per relink path: the replacement id reaches the
// wait from the single-part init response and from the multipart init response
// by different code, and either could drop it.
var replacePaths = []struct {
	name  string
	extra []string
}{
	{"single-part", nil},
	{"multipart", []string{"--multipart"}},
}

func replaceArgs(t *testing.T, extra []string, flags ...string) []string {
	args := []string{"media", "files", "replace", replFileID, writeTempUpload(t, "new.mp4")}
	args = append(args, extra...)
	args = append(args, flags...)
	return withTeam("t_team1", args...)
}

// THE RACE. The first poll after the relink still sees the OLD media, fully
// processed. --wait must not take it for the replacement: it holds until the file
// points at the replacement's media id, then follows THAT media's transcode.
func TestReplaceWait_HoldsUntilTheFilePointsAtTheReplacement(t *testing.T) {
	for _, path := range replacePaths {
		t.Run(path.name, func(t *testing.T) {
			fastPolling(t, time.Second)
			srv, gets := replaceStatusServer(t, []string{
				replState(oldMediaID, "video/mp4", "READY", "READY"), // stale: relink not committed yet
				replState(replMediaID, "video/mp4", "READY", ""),     // relinked; transcode not enqueued yet
				replState(replMediaID, "video/mp4", "READY", "PROCESSING"),
				replState(replMediaID, "video/mp4", "READY", "READY"),
			})

			res := runContract(t, baseEnv(srv.URL),
				replaceArgs(t, path.extra, "--wait", "--timeout", "10s", "--output", "json")...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if strings.Contains(res.Stdout, oldMediaID) {
				t.Fatalf("--wait returned the PRE-REPLACE media (%s) — the stale first poll was taken for the "+
					"replacement; stdout=%q", oldMediaID, res.Stdout)
			}
			if n := atomic.LoadInt32(gets); n < 4 {
				t.Errorf("polled %d time(s), want >= 4 — the wait must follow the REPLACEMENT's transcode to READY", n)
			}
			if !strings.Contains(res.Stdout, `"media_id": "`+replMediaID+`"`) ||
				!strings.Contains(res.Stdout, `"status_transcode": "READY"`) {
				t.Errorf("must return the replacement media, transcoded; stdout=%q", res.Stdout)
			}
		})
	}
}

// Replacing a video whose transcode FAILED is the ordinary way to fix it. The
// stale read shows that old FAILED media; it says nothing about the replacement,
// so it must not fail the command.
func TestReplaceWait_StaleFailedOldMediaIsNotAFailure(t *testing.T) {
	fastPolling(t, time.Second)
	srv, gets := replaceStatusServer(t, []string{
		replState(oldMediaID, "video/mp4", "READY", "FAILED"), // the broken video being replaced
		replState(replMediaID, "video/mp4", "READY", "READY"),
	})

	res := runContract(t, baseEnv(srv.URL),
		replaceArgs(t, nil, "--wait", "--timeout", "10s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — the FAILED status belongs to the media being replaced, not the replacement; "+
			"stderr=%q", res.Code, res.Stderr)
	}
	if n := atomic.LoadInt32(gets); n < 2 {
		t.Errorf("polled %d time(s), want >= 2", n)
	}
	if !strings.Contains(res.Stdout, `"media_id": "`+replMediaID+`"`) {
		t.Errorf("must return the replacement media; stdout=%q", res.Stdout)
	}
}

// The 30s transcode-start window must open when the REPLACEMENT reads READY. A
// stale read of the old media also says status_upload READY; opening the window
// on it would spend the grace before the new transcode could be enqueued, then
// give up with a misleading "had not started" and an untranscoded file.
func TestReplaceWait_TranscodeWindowOpensOnTheReplacement(t *testing.T) {
	fastPolling(t, 40*time.Millisecond)

	polls := make([]string, 0, 64)
	for i := 0; i < 60; i++ { // stale reads outlast the whole grace
		polls = append(polls, replState(oldMediaID, "video/mp4", "READY", "READY"))
	}
	polls = append(polls,
		replState(replMediaID, "video/mp4", "READY", ""), // window must open HERE
		replState(replMediaID, "video/mp4", "READY", "PROCESSING"),
		replState(replMediaID, "video/mp4", "READY", "READY"),
	)
	srv, _ := replaceStatusServer(t, polls)

	res := runContract(t, baseEnv(srv.URL),
		replaceArgs(t, nil, "--wait", "--timeout", "20s", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "had not started") {
		t.Errorf("the grace was spent on stale reads of the OLD media, so the replacement's transcode never got "+
			"its window; stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, `"media_id": "`+replMediaID+`"`) ||
		!strings.Contains(res.Stdout, `"status_transcode": "READY"`) {
		t.Errorf("must return the TRANSCODED replacement; stdout=%q", res.Stdout)
	}
}

// If the file never comes to point at the replacement (another replace won the
// race, say), --timeout still bounds the wait, and the error must say WHICH wait
// ran out: the statuses it would otherwise print are the old media's.
func TestReplaceWait_TimeoutNamesTheMediaTheFileStillPointsAt(t *testing.T) {
	fastPolling(t, time.Second)
	srv, _ := replaceStatusServer(t, []string{replState(oldMediaID, "video/mp4", "READY", "READY")})

	err := executeCLI(t, baseEnv(srv.URL),
		replaceArgs(t, nil, "--wait", "--timeout", "150ms", "--output", "json")...)
	if err == nil {
		t.Fatal("a file that never points at the replacement must not be reported as a finished wait")
	}
	if code := errs.CodeOf(err); code != errs.ExitGeneric {
		t.Errorf("exit = %d, want %d (a --wait timeout, as for upload)", code, errs.ExitGeneric)
	}
	msg := err.Error()
	if !strings.Contains(msg, "timed out") || !strings.Contains(msg, oldMediaID) || !strings.Contains(msg, replMediaID) {
		t.Errorf("the timeout must name the media the file still points at (%s) and the replacement it waited "+
			"for (%s); err=%v", oldMediaID, replMediaID, err)
	}
}

// Without --wait, replace is unchanged: it renders the relink response and makes
// no status poll.
func TestReplace_WithoutWaitDoesNotPoll(t *testing.T) {
	for _, path := range replacePaths {
		t.Run(path.name, func(t *testing.T) {
			// A GET answers with a replacement that is already finished, so a replace
			// that polled anyway returns after ONE poll and fails the count below by
			// name. A stale or unfinished answer here would instead leave such a
			// mutation polling for the 5m default --timeout, and the package would
			// die on go test's own deadline with a panic that names nothing (seen).
			srv, gets := replaceStatusServer(t, []string{replState(replMediaID, "image/png", "READY", "")})
			res := runContract(t, baseEnv(srv.URL), replaceArgs(t, path.extra, "--output", "json")...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if n := atomic.LoadInt32(gets); n != 0 {
				t.Errorf("polled %d time(s) without --wait, want 0", n)
			}
		})
	}
}

// replace's --wait and --timeout are upload's, not a copy of them: same usage
// text, same default. A copy drifts (MIO-4155 was upload's and replace's
// multipart help drifting apart).
func TestReplaceWait_FlagsAreUploads(t *testing.T) {
	for _, name := range []string{"wait", "timeout"} {
		up := mediaFilesUploadCmd.Flags().Lookup(name)
		rp := mediaFilesReplaceCmd.Flags().Lookup(name)
		if up == nil || rp == nil {
			t.Fatalf("--%s: registered on upload=%v, replace=%v; want both", name, up != nil, rp != nil)
		}
		if rp.Usage != up.Usage || rp.DefValue != up.DefValue || rp.Value.Type() != up.Value.Type() {
			t.Errorf("--%s differs between upload and replace:\n upload:  %s %q (default %s)\n replace: %s %q (default %s)",
				name, up.Value.Type(), up.Usage, up.DefValue, rp.Value.Type(), rp.Usage, rp.DefValue)
		}
	}
}
