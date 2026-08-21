package interaction

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

const maxIngressLabelBytes = 32

type Ingress struct {
	adapter string
	id      string
	kind    string
}

func NewIngress(adapter, externalID, kind string) (Ingress, error) {
	adapter = strings.ToLower(strings.TrimSpace(adapter))
	kind = strings.ToLower(strings.TrimSpace(kind))
	externalID = strings.TrimSpace(externalID)
	if !validIngressLabel(adapter) || !validIngressLabel(kind) || externalID == "" || len(externalID) > 256 {
		return Ingress{}, errors.New("interaction ingress identity is invalid")
	}
	digest := sha256.Sum256([]byte(adapter + "\x00" + externalID))
	return Ingress{
		adapter: adapter,
		id:      fmt.Sprintf("ix-%x", digest[:16]),
		kind:    kind,
	}, nil
}

func validIngressLabel(value string) bool {
	if value == "" || len(value) > maxIngressLabelBytes {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func (ingress Ingress) Adapter() string { return ingress.adapter }
func (ingress Ingress) ID() string      { return ingress.id }
func (ingress Ingress) Kind() string    { return ingress.kind }

type ingressContextKey struct{}

func WithIngress(ctx context.Context, ingress Ingress) context.Context {
	if ctx == nil || ingress.adapter == "" || ingress.id == "" || ingress.kind == "" {
		return ctx
	}
	return context.WithValue(ctx, ingressContextKey{}, ingress)
}

func IngressFromContext(ctx context.Context) (Ingress, bool) {
	if ctx == nil {
		return Ingress{}, false
	}
	ingress, ok := ctx.Value(ingressContextKey{}).(Ingress)
	return ingress, ok && ingress.adapter != "" && ingress.id != "" && ingress.kind != ""
}
