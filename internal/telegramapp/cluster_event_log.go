package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const clusterEventLogLimit = 6

type clusterEventKind uint8

const (
	clusterEventLeader clusterEventKind = iota + 1
	clusterEventNodeLost
	clusterEventNodeRestored
)

type clusterEvent struct {
	Kind     clusterEventKind
	NodeName string
	At       time.Time
}

type clusterEventLog struct {
	Message telegrambot.Message
	Events  []clusterEvent
}

type nodeEventState struct {
	Status  domain.NodeStatus
	Enabled bool
}

type clusterEventTracker struct {
	previousLeader string
	previousNodes  map[domain.NodeID]nodeEventState
}

// appendClusterEvent keeps a small rolling log only while its message remains
// the newest message in the chat. Any intervening user or bot message starts a
// fresh log, so cluster notices never overwrite session cards or normal output.
func (h *Handler) appendClusterEvent(
	ctx context.Context,
	target application.ClusterAlertTarget,
	event clusterEvent,
) error {
	chatID := int64(target.OwnerID)
	if chatID <= 0 || strings.TrimSpace(event.NodeName) == "" {
		return domain.ErrInvalidState
	}
	h.clusterEventMu.Lock()
	defer h.clusterEventMu.Unlock()
	if h.clusterEventLogs == nil {
		h.clusterEventLogs = make(map[int64]clusterEventLog)
	}
	previous := h.clusterEventLogs[chatID]
	events := append([]clusterEvent(nil), previous.Events...)
	events = append(events, event)
	if len(events) > clusterEventLogLimit {
		events = events[len(events)-clusterEventLogLimit:]
	}
	screen := renderClusterEventLog(target.Language, events)
	newEvents := []clusterEvent{event}
	newScreen := renderClusterEventLog(target.Language, newEvents)
	var (
		message telegrambot.Message
		edited  bool
		err     error
	)
	if h.activity != nil {
		message, edited, err = h.activity.UpsertNewest(
			ctx, chatID, previous.Message, screen, newScreen,
		)
	} else {
		message, err = h.messenger.SendScreen(ctx, chatID, newScreen)
	}
	if err != nil {
		return err
	}
	if !edited {
		// Another chat message split the log. Do not carry old lines into the new
		// block: the earlier message remains visible immediately above it.
		events = newEvents
	}
	h.clusterEventLogs[chatID] = clusterEventLog{Message: message, Events: events}
	return nil
}

func renderClusterEventLog(language domain.Language, events []clusterEvent) telegramui.Screen {
	copy := i18n.For(string(language))
	lines := make([]string, 0, len(events)+1)
	lines = append(lines, copy.Text(i18n.ClusterEventLogTitle))
	for _, event := range events {
		key := i18n.ClusterEventLeader
		switch event.Kind {
		case clusterEventNodeLost:
			key = i18n.ClusterEventNodeLost
		case clusterEventNodeRestored:
			key = i18n.ClusterEventNodeRestored
		}
		lines = append(lines, fmt.Sprintf("%s %s", event.At.Format("15:04"),
			copy.Format(key, event.NodeName)))
	}
	return telegramui.Screen{Name: telegramui.ScreenStatus, Text: strings.Join(lines, "\n")}
}

// RunClusterEventNotifications observes replicated node reachability and Raft
// leadership. Only the current leader reports node state, and only a newly
// elected local leader reports the leader transition, preventing N-fold spam.
func (h *Handler) RunClusterEventNotifications(
	ctx context.Context,
	leaders ClusterLeaderObserver,
	config ClusterConnectivityConfig,
) {
	if leaders == nil || config.LocalNodeID == "" {
		return
	}
	interval := config.Interval
	if interval <= 0 {
		interval = time.Second
	}
	tracker := clusterEventTracker{previousLeader: leaders.LeaderID()}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		h.observeClusterEvents(ctx, leaders, config.LocalNodeID, &tracker, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) observeClusterEvents(
	ctx context.Context,
	leaders ClusterLeaderObserver,
	localNodeID domain.NodeID,
	tracker *clusterEventTracker,
	now time.Time,
) {
	leaderID := leaders.LeaderID()
	if leaderID != "" && leaderID != tracker.previousLeader &&
		leaderID == string(localNodeID) {
		if target, ok := h.service.ClusterAlertTarget(domain.NodeID(leaderID)); ok {
			_ = h.appendClusterEvent(ctx, target, clusterEvent{
				Kind: clusterEventLeader, NodeName: target.NodeName, At: now,
			})
		}
	}
	tracker.previousLeader = leaderID
	if leaderID == string(localNodeID) {
		tracker.previousNodes = h.observeNodeEvents(
			ctx, localNodeID, tracker.previousNodes, now,
		)
	} else {
		tracker.previousNodes = nil
	}
}

func (h *Handler) observeNodeEvents(
	ctx context.Context,
	localNodeID domain.NodeID,
	previous map[domain.NodeID]nodeEventState,
	now time.Time,
) map[domain.NodeID]nodeEventState {
	target, ok := h.service.ClusterAlertTarget(localNodeID)
	if !ok {
		return previous
	}
	items, err := h.service.ListNodes(application.Principal{UserID: target.OwnerID})
	if err != nil {
		return previous
	}
	current := make(map[domain.NodeID]nodeEventState, len(items))
	for _, item := range items {
		node := item.Node
		state := nodeEventState{Status: node.Status, Enabled: node.Enabled()}
		current[node.ID] = state
		before, existed := previous[node.ID]
		if previous == nil || !existed || !state.Enabled || !before.Enabled {
			continue
		}
		wasOnline := before.Status != domain.NodeOffline
		isOnline := state.Status != domain.NodeOffline
		if wasOnline == isOnline {
			continue
		}
		kind := clusterEventNodeLost
		if isOnline {
			kind = clusterEventNodeRestored
		}
		_ = h.appendClusterEvent(ctx, target, clusterEvent{
			Kind: kind, NodeName: node.Name, At: now,
		})
	}
	return current
}
