package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const (
	replFileID   = "file_x"
	oldMediaID   = "media_old"
	replMediaID  = "media_new"
	replTeamBase = "/api/v1/teams/t_team1/files/" + replFileID
)

func replState(mediaID, mime, up, tc string) string {
	transcode := "null"
	if tc != "" {
		transcode = fmt.Sprintf("%q", tc)
	}
	return fmt.Sprintf(
		`{"data":{"id":%q,"type":"files","attributes":{"media_id":%q,"mime_type":%q,"status_upload":%q,"status_transcode":%s,"status_transcribe":null}}}`,
		replFileID, mediaID, mime, up, transcode)
}

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
				i = len(polls) - 1
			}
			_, _ = w.Write([]byte(polls[i]))
		default:
			failUnlessStrayProbe(t, r, "unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

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

func TestReplaceWait_HoldsUntilTheFilePointsAtTheReplacement(t *testing.T) {
	for _, path := range replacePaths {
		t.Run(path.name, func(t *testing.T) {
			fastPolling(t, time.Second)
			srv, gets := replaceStatusServer(t, []string{
				replState(oldMediaID, "video/mp4", "READY", "READY"),
				replState(replMediaID, "video/mp4", "READY", ""),
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

func TestReplaceWait_StaleFailedOldMediaIsNotAFailure(t *testing.T) {
	fastPolling(t, time.Second)
	srv, gets := replaceStatusServer(t, []string{
		replState(oldMediaID, "video/mp4", "READY", "FAILED"),
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

func TestReplaceWait_TranscodeWindowOpensOnTheReplacement(t *testing.T) {
	fastPolling(t, 40*time.Millisecond)

	polls := make([]string, 0, 64)
	for i := 0; i < 60; i++ {
		polls = append(polls, replState(oldMediaID, "video/mp4", "READY", "READY"))
	}
	polls = append(polls,
		replState(replMediaID, "video/mp4", "READY", ""),
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

func TestReplace_WithoutWaitDoesNotPoll(t *testing.T) {
	for _, path := range replacePaths {
		t.Run(path.name, func(t *testing.T) {
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

func TestIngestHelp_TranscodeClaimNamesTheVideoGate(t *testing.T) {
	claim := regexp.MustCompile(`(?i)\b(trigger|start|enqueue|kick|begin|began|launch|dispatch|initiat)\w*\b.*\btranscod`)
	split := regexp.MustCompile(`[.;] `)
	for _, c := range []*cobra.Command{mediaFilesUploadCmd, mediaFilesReplaceCmd, mediaFilesFinalizeCmd} {
		claims := 0
		for _, s := range split.Split(strings.Join(strings.Fields(c.Long), " "), -1) {
			if !claim.MatchString(s) {
				continue
			}
			claims++
			if !strings.Contains(strings.ToLower(s), "video processing") {
				t.Errorf("media files %s --help says a transcode follows without naming the gate "+
					"(video processing, FEATURE_MEDIA_VIDEO_ENABLED, off by default): %q", c.Name(), s)
			}
		}
		if claims == 0 {
			t.Errorf("media files %s --help no longer says a transcode follows, so this check is vacuous for it; it reads: %s",
				c.Name(), c.Long)
		}
	}
}
