package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// docError is an API failure carrying a JSON:API error document, the shape the
// client builds for a non-2xx JSON:API answer.
type docError struct {
	msg string
	doc []byte
}

func (e docError) Error() string            { return e.msg }
func (e docError) APIErrorDocument() []byte { return e.doc }

// TestWriteJSONErrorEnvelope_UncarriableDocumentKeepsCLIEnvelope pins the
// fallback of MIO-3912: a document the CLI cannot carry faithfully — an error
// object that is not an object, an `errors` member the client matched only
// case-insensitively, an empty array, meta that is not an object, trailing
// data — must produce exactly the envelope a failure with no document at all
// produces, in both modes, rather than a half-built or invalid one.
//
// `{"errors":[null]}` is the reachable case: encoding/json decodes a null
// element into a zero apiError, so the client does build an error from it.
func TestWriteJSONErrorEnvelope_UncarriableDocumentKeepsCLIEnvelope(t *testing.T) {
	docs := []string{
		`{"errors":[null]}`,
		`{"errors":[{"detail":"a"},"b"]}`,
		`{"Errors":[{"detail":"a"}]}`,
		`{"errors":[]}`,
		`{"errors":{"detail":"a"}}`,
		`[{"detail":"a"}]`,
		`{"errors":[{"detail":"a","meta":"not-an-object"}]}`,
		`{"errors":[{"detail":"a"}]} trailing`,
	}
	for _, doc := range docs {
		for _, raw := range []bool{false, true} {
			base := errs.WrapHTTP(422, errors.New("the CLI message"))
			var want bytes.Buffer
			writeJSONErrorEnvelope(&want, base, errs.ExitUsage, raw)

			withDoc := errs.WrapHTTP(422, docError{msg: "the CLI message", doc: []byte(doc)})
			var got bytes.Buffer
			writeJSONErrorEnvelope(&got, withDoc, errs.ExitUsage, raw)

			if got.String() != want.String() {
				t.Errorf("doc %s (raw=%v): envelope is not the CLI-built one.\n got: %s\nwant: %s", doc, raw, got.String(), want.String())
			}
		}
	}
}
