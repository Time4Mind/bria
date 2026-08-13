package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

func TestInvitationReplayProducesOnlyUsableInvitations(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetEnrollmentInvitationConfig(application.EnrollmentInvitationConfig{
		ClusterID: "cluster", IssuerNodeID: "alpha", Endpoint: "alpha:7948",
		CACertificate: "certificate",
	}); err != nil {
		t.Fatal(err)
	}
	actor := application.Principal{UserID: 1}
	ctx := application.WithOperationScope(context.Background(), "same-telegram-update")
	first, _, err := service.CreateEnrollmentInvitation(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.CreateEnrollmentInvitation(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("repeated request reused a plaintext enrollment secret")
	}
	for _, encoded := range []string{first, second} {
		invitation, err := security.DecodeClusterInvitation(encoded, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		stored, ok := machine.State().EnrollmentInvites[invitation.TokenID]
		if !ok || stored.ValidateSecret(invitation.Secret, time.Now()) != nil {
			t.Fatalf("displayed invitation %q is not usable", invitation.TokenID)
		}
	}
}

func TestInvitationRequiresAvailableIssuer(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, _ := application.NewService(port, port)
	_ = service.SetEnrollmentInvitationConfig(application.EnrollmentInvitationConfig{
		ClusterID: "cluster", IssuerNodeID: "alpha", Endpoint: "alpha:7948",
		CACertificate: "certificate",
	})
	if _, _, err := service.CreateEnrollmentInvitation(
		context.Background(), application.Principal{UserID: 1},
	); err == nil {
		t.Fatal("invitation issued while CA issuer was offline")
	}
}
