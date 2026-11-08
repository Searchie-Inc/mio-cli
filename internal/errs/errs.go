package errs

import (
	"errors"
	"fmt"
	"net/http"
)

const (
	ExitOK          = 0
	ExitGeneric     = 1
	ExitUsage       = 2
	ExitAuth        = 3
	ExitNotFound    = 4
	ExitNeedsConfir = 5
	ExitRateLimited = 6
	ExitServer      = 7
)

type CLIError struct {
	Code       int
	Err        error
	HTTPStatus int
}

func (e *CLIError) Error() string {
	if e == nil || e.Err == nil {
		return "unknown error"
	}
	return e.Err.Error()
}

func (e *CLIError) Unwrap() error { return e.Err }

func New(code int, format string, args ...any) *CLIError {
	return &CLIError{Code: code, Err: fmt.Errorf(format, args...)}
}

func Wrap(code int, err error) *CLIError {
	if err == nil {
		return nil
	}
	return &CLIError{Code: code, Err: err}
}

func NewHTTP(status int, format string, args ...any) *CLIError {
	ce := New(ExitCodeForStatus(status), format, args...)
	ce.HTTPStatus = normalizeHTTPStatus(status)
	return ce
}

func WrapHTTP(status int, err error) *CLIError {
	if err == nil {
		return nil
	}
	ce := Wrap(ExitCodeForStatus(status), err)
	ce.HTTPStatus = normalizeHTTPStatus(status)
	return ce
}

func normalizeHTTPStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func HTTPStatusOf(err error) int {
	for err != nil {
		var ce *CLIError
		if !errors.As(err, &ce) {
			return 0
		}
		if ce.HTTPStatus > 0 {
			return ce.HTTPStatus
		}
		err = ce.Unwrap()
	}
	return 0
}

type APIErrorDocumentCarrier interface {
	APIErrorDocument() []byte
}

func APIErrorDocumentOf(err error) []byte {
	var c APIErrorDocumentCarrier
	if errors.As(err, &c) {
		return c.APIErrorDocument()
	}
	return nil
}

func CodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ExitGeneric
}

func ExitCodeForStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ExitAuth
	case status == http.StatusNotFound:
		return ExitNotFound
	case status == http.StatusTooManyRequests:
		return ExitRateLimited
	case status >= 500:
		return ExitServer
	case status == http.StatusBadRequest,
		status == http.StatusConflict,
		status == http.StatusUnprocessableEntity:
		return ExitUsage
	case status >= 400:
		return ExitGeneric
	default:
		return ExitGeneric
	}
}
