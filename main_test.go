package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type docError struct {
	msg string
	doc []byte
}

func (e docError) Error() string            { return e.msg }
func (e docError) APIErrorDocument() []byte { return e.doc }

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
