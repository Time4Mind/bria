package terminalbinding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Command is an already selected private-server transport, never a launcher.
type Command func(context.Context, ...string) (string, error)

func (r Record) Verify(ctx context.Context, run Command) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	device, inode, err := SocketIdentity(r.Socket)
	if err != nil {
		return err
	}
	if device != r.SocketDevice || inode != r.SocketInode {
		return ErrMismatch
	}
	for _, identity := range []struct {
		pid   int
		birth string
	}{{r.ServerPID, r.ServerBirth}, {r.PanePID, r.PaneBirth}} {
		birth, err := ProcessBirth(ctx, identity.pid)
		if err != nil {
			return err
		}
		if birth != identity.birth {
			return ErrMismatch
		}
	}
	value, err := run(ctx, "display-message", "-p", "-t", "cli:0.0", "#{pid} #{pane_pid} #{pane_id} #{pane_dead} #{@bria_binding}")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	expected := strings.Join([]string{strconv.Itoa(r.ServerPID), strconv.Itoa(r.PanePID), r.PaneID, "0", r.Nonce}, " ")
	if strings.TrimSpace(value) != expected {
		return ErrMismatch
	}
	return nil
}

// Destroy consumes pinned process ownership only after verifying the exact
// binding. A partial destruction closes the lease too; it cannot be retried
// using an already released pidfd. No unknown/reused PID is signalled.
func (l *Lease) Destroy(ctx context.Context, r Record, pane *Process, server *exec.Cmd, run Command) (closed bool, result error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.Verify(ctx, run); err != nil {
		return false, err
	}
	if server == nil {
		process, err := os.FindProcess(r.ServerPID)
		if err != nil {
			return false, err
		}
		defer process.Release()
		server = &exec.Cmd{Process: process}
	}
	resume, pauseErr := PauseServer(server)
	if pauseErr != nil {
		return false, pauseErr
	}
	treeErr := pane.KillTree()
	if resume != nil {
		resume()
	}
	closed = true
	defer func() { result = errors.Join(result, l.Close()) }()
	_, stopErr := run(ctx, "kill-server")
	for {
		birth, err := ProcessBirth(ctx, r.ServerPID)
		if errors.Is(err, ErrUnavailable) || err == nil && birth != r.ServerBirth {
			break
		}
		select {
		case <-ctx.Done():
			return true, errors.Join(treeErr, stopErr, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if device, inode, err := SocketIdentity(r.Socket); err == nil {
		if device != r.SocketDevice || inode != r.SocketInode {
			return true, ErrMismatch
		}
		if err := os.Remove(r.Socket); err != nil {
			return true, err
		}
	} else if !errors.Is(err, ErrUnavailable) {
		return true, err
	}
	if err := l.Remove(); err != nil {
		return true, errors.Join(treeErr, err)
	}
	return true, errors.Join(treeErr, os.Remove(filepath.Dir(r.Socket)))
}
