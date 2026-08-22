package telegramview_test

import (
	"errors"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramview"
)

func TestProjectionACLFailuresDoNotRevealOrTokenizeEntities(t *testing.T) {
	tests := []struct {
		name string
		run  func(*telegramview.Projector) error
		want error
	}{
		{
			name: "unknown actor",
			run: func(projector *telegramview.Projector) error {
				_, err := projector.OpenSessions(application.Principal{UserID: 99})
				return err
			},
			want: domain.ErrAccessDenied,
		},
		{
			name: "forbidden live node",
			run: func(projector *telegramview.Projector) error {
				_, err := projector.NodeSessions(application.Principal{UserID: 2}, "secret")
				return err
			},
			want: domain.ErrNotFound,
		},
		{
			name: "forbidden archive node",
			run: func(projector *telegramview.Projector) error {
				_, err := projector.NodeArchives(application.Principal{UserID: 2}, "secret")
				return err
			},
			want: domain.ErrNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector, _, tokens := projectorFixture(t)
			if err := test.run(projector); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(tokens.calls) != 0 {
				t.Fatalf("ACL failure tokenized entities: %v", tokens.calls)
			}
		})
	}
}
