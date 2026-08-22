package telegramapp

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
)

type clusterUpdaterStub struct{}

func (clusterUpdaterStub) Start(context.Context) (domain.ClusterUpdate, error) {
	return domain.ClusterUpdate{}, nil
}

func (clusterUpdaterStub) Retry(context.Context) (domain.ClusterUpdate, error) {
	return domain.ClusterUpdate{}, nil
}

func (clusterUpdaterStub) Progress(context.Context, string) map[domain.NodeID]clusterupdate.Status {
	return nil
}

func TestSetClusterUpdaterAcceptsConsumerOwnedContract(t *testing.T) {
	handler := &Handler{}
	stub := clusterUpdaterStub{}
	if err := handler.SetClusterUpdater(stub); err != nil {
		t.Fatal(err)
	}
	if handler.clusterUpdater != stub {
		t.Fatal("consumer-owned cluster updater was not retained")
	}
}

func TestSetClusterUpdaterRejectsNil(t *testing.T) {
	if err := (&Handler{}).SetClusterUpdater(nil); err == nil {
		t.Fatal("nil cluster updater was accepted")
	}
}
