package cmd

// mio4175_synthetic_pdf_mime_test.go — `media files register-synthetic` help
// must not teach a pdf that is stored as a generic blob (MIO-4175).
//
// The CLI sends mime_type only when --mime-type is passed
// (TestFilesRegisterSynthetic_Body pins that, and it stays: which layer owns a
// kind-derived default was decided for the backend). The endpoint then stores
// `mime_type or "application/octet-stream"` (mio-backend
// app/media/service.py register_synthetic_file) and does not look at asset_kind.
// So the help's own `--asset-kind pdf` example registered a pdf with mime
// application/octet-stream, which mime-keyed viewers treat as an unknown file.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/docexamples"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// storedSyntheticMime is what the endpoint stores for a register body: the
// mime_type sent, else application/octet-stream whatever asset_kind says
// (service.py: `mime_type=mime_type or "application/octet-stream"`).
func storedSyntheticMime(attrs map[string]any) string {
	if m, _ := attrs["mime_type"].(string); m != "" {
		return m
	}
	return "application/octet-stream"
}

// Every register-synthetic Example is RUN against a stub, and each one that
// registers a pdf (asset_kind "pdf" on the wire) must leave the endpoint storing
// application/pdf. The oracle is the request body the example produces, put
// through the endpoint's own defaulting rule: no edit to the help prose alone
// can satisfy it.
func TestRegisterSyntheticExamples_APdfIsStoredAsAPdf(t *testing.T) {
	res := docexamples.FromScript("register-synthetic Example", 1, mediaFilesRegisterSyntheticCmd.Example)
	if len(res.Uncovered) > 0 {
		t.Fatalf("Example lines the extractor could not read, so they are unchecked: %+v", res.Uncovered)
	}
	pdfs := 0
	for _, inv := range res.Invocations {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files/synthetic") {
				body, _ = io.ReadAll(r.Body)
			}
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"file_syn","type":"files","attributes":{"status_upload":"READY"}}}`))
		}))
		out := runContract(t, baseEnv(srv.URL), withTeam("t_team1", inv.Args...)...)
		srv.Close()
		if out.Code != errs.ExitOK {
			t.Errorf("`%s` exited %d; stderr=%q", inv.Text(), out.Code, out.Stderr)
			continue
		}
		if body == nil {
			t.Errorf("`%s` sent no register request", inv.Text())
			continue
		}
		_, attrs := decodeDataTypeAttrs(t, body)
		if attrs["asset_kind"] != "pdf" {
			continue
		}
		pdfs++
		if got := storedSyntheticMime(attrs); got != "application/pdf" {
			t.Errorf("the help example `%s` registers a pdf that the endpoint stores as %s — it must pass "+
				"--mime-type application/pdf (the endpoint does not derive a mime from asset_kind)", inv.Text(), got)
		}
	}
	if pdfs == 0 {
		t.Fatal("no register-synthetic Example registers a pdf, so this check is vacuous — the pdf example " +
			"is the one MIO-4175 was filed against")
	}
}

// The --mime-type flag help must say what an omitted flag stores. It said only
// "Optional mime type.", and the endpoint's default is the generic blob type.
func TestRegisterSyntheticHelp_MimeTypeFlagNamesTheEndpointDefault(t *testing.T) {
	f := mediaFilesRegisterSyntheticCmd.Flags().Lookup("mime-type")
	if f == nil {
		t.Fatal("--mime-type is not registered on register-synthetic")
	}
	for _, want := range []string{"application/octet-stream", "application/pdf"} {
		if !strings.Contains(f.Usage, want) {
			t.Errorf("--mime-type help must name %q (what an omission stores, and what a pdf needs); it reads: %q",
				want, f.Usage)
		}
	}
}
