// Package telegramtransport classifies network failures into a bounded,
// payload-free vocabulary that is safe to pass across logging adapters.
package telegramtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"syscall"
)

type Class string

const (
	DNS     Class = "dns"
	Connect Class = "connect"
	TLS     Class = "tls"
	Reset   Class = "reset"
	Timeout Class = "timeout"
	Unknown Class = "unknown"
)

type Failure struct {
	method string
	class  Class
}

func NewFailure(method string, class Class) error { return &Failure{method: method, class: class} }

func (err *Failure) Error() string {
	return fmt.Sprintf("transient Telegram failure: telegram %s transport failure (class=%s)", err.method, err.class)
}

func (err *Failure) Is(target error) bool {
	return err.class == Timeout && target == context.DeadlineExceeded
}

func ClassOf(err error) (Class, bool) {
	var failure *Failure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.class, true
}

func Classify(requestContext context.Context, requestErr error) Class {
	if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(requestErr, context.DeadlineExceeded) {
		return Timeout
	}
	if class, ok := ClassOf(requestErr); ok {
		return class
	}
	if errors.Is(requestErr, syscall.ECONNRESET) {
		return Reset
	}
	var dnsErr *net.DNSError
	if errors.As(requestErr, &dnsErr) {
		return DNS
	}
	var recordHeaderErr tls.RecordHeaderError
	var certificateVerificationErr *tls.CertificateVerificationError
	var unknownAuthorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certificateInvalidErr x509.CertificateInvalidError
	if errors.As(requestErr, &recordHeaderErr) || errors.As(requestErr, &certificateVerificationErr) ||
		errors.As(requestErr, &unknownAuthorityErr) || errors.As(requestErr, &hostnameErr) || errors.As(requestErr, &certificateInvalidErr) {
		return TLS
	}
	var operationErr *net.OpError
	if errors.As(requestErr, &operationErr) && (operationErr.Op == "dial" || operationErr.Op == "connect") {
		return Connect
	}
	var networkErr net.Error
	if errors.As(requestErr, &networkErr) && networkErr.Timeout() {
		return Timeout
	}
	return Unknown
}
