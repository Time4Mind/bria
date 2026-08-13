package nodecontrol

import (
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
)

const MaxSessionFileBytes int64 = 45 << 20

type SessionFileQuery struct {
	ActorID            int64  `json:"actor_id"`
	NodeID             string `json:"node_id"`
	SessionID          string `json:"session_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	Path               string `json:"path"`
}

type SessionFile struct {
	Name     string
	MIMEType string
	Size     int64
	Content  io.ReadCloser
}

type SessionFileReader interface {
	OpenSessionFile(context.Context, SessionFileQuery) (SessionFile, error)
}

type LocalSessionFileService struct {
	nodeID string
	state  StateReader
}

func NewLocalSessionFileService(nodeID string, state StateReader) (*LocalSessionFileService, error) {
	if nodeID == "" || state == nil {
		return nil, errors.New("node id and state reader are required")
	}
	return &LocalSessionFileService{nodeID: nodeID, state: state}, nil
}

func (s *LocalSessionFileService) OpenSessionFile(
	_ context.Context,
	query SessionFileQuery,
) (SessionFile, error) {
	if query.ActorID <= 0 || query.NodeID != s.nodeID || query.SessionID == "" ||
		query.ExpectedGeneration == 0 || strings.TrimSpace(query.Path) == "" {
		return SessionFile{}, domain.ErrAccessDenied
	}
	state := s.state.State()
	if state == nil {
		return SessionFile{}, domain.ErrAccessDenied
	}
	ref := domain.SessionRef{NodeID: domain.NodeID(query.NodeID), SessionID: domain.SessionID(query.SessionID)}
	session, ok := state.Sessions[ref.Key()]
	if !ok || !session.IsLive() || session.RuntimeGeneration != query.ExpectedGeneration ||
		!state.CanPerformSessionAction(domain.UserID(query.ActorID), ref, domain.ActionCapture) {
		return SessionFile{}, domain.ErrNotFound
	}
	root, err := filepath.EvalSymlinks(session.Workdir)
	if err != nil {
		return SessionFile{}, domain.ErrNotFound
	}
	requested := strings.TrimSpace(strings.TrimPrefix(query.Path, "file://"))
	if strings.ContainsRune(requested, '\x00') {
		return SessionFile{}, domain.ErrAccessDenied
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(root, requested)
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return SessionFile{}, domain.ErrNotFound
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return SessionFile{}, domain.ErrAccessDenied
	}
	file, err := os.Open(resolved)
	if err != nil {
		return SessionFile{}, domain.ErrNotFound
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxSessionFileBytes {
		_ = file.Close()
		return SessionFile{}, domain.ErrInvalidState
	}
	mimeType := mime.TypeByExtension(filepath.Ext(info.Name()))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return SessionFile{Name: info.Name(), MIMEType: mimeType, Size: info.Size(), Content: file}, nil
}

type SessionFileRouter struct {
	localNodeID string
	local       SessionFileReader
	remote      SessionFileReader
}

func NewSessionFileRouter(
	localNodeID string,
	local SessionFileReader,
	remote SessionFileReader,
) (*SessionFileRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("local node id and session file readers are required")
	}
	return &SessionFileRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *SessionFileRouter) OpenSessionFile(
	ctx context.Context,
	query SessionFileQuery,
) (SessionFile, error) {
	if query.NodeID == r.localNodeID {
		return r.local.OpenSessionFile(ctx, query)
	}
	return r.remote.OpenSessionFile(ctx, query)
}
