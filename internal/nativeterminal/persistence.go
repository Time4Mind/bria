package nativeterminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"bria/internal/terminalbinding"
)

type Binding = terminalbinding.Identity

var (
	ErrBindingMismatch     = terminalbinding.ErrMismatch
	ErrTerminalUnavailable = terminalbinding.ErrUnavailable
	ErrNotManaged          = terminalbinding.ErrNotManaged
	ErrAlreadyAttached     = terminalbinding.ErrOwned
)

func (t *Terminal) PersistBinding(ctx context.Context, stateDir string, binding Binding) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	identity, err := terminalbinding.Canonical(binding)
	if err != nil {
		return err
	}
	workdir, err := filepath.EvalSymlinks(t.workdir)
	if err != nil || identity.Workdir != workdir || !t.persistent || t.closed {
		return ErrBindingMismatch
	}
	if t.lease != nil {
		if t.binding.Identity != identity {
			return ErrBindingMismatch
		}
		return t.prove(ctx)
	}
	lease, err := terminalbinding.Acquire(stateDir, identity, true)
	if err != nil {
		return err
	}
	defer func() {
		if t.lease == nil {
			_ = lease.Close()
		}
	}()
	value, err := t.run(ctx, nil, "display-message", "-p", "-t", "cli:0.0", "#{pid} #{pane_pid} #{pane_id} #{pane_dead}")
	if err != nil {
		return ErrTerminalUnavailable
	}
	fields := strings.Fields(value)
	if len(fields) != 4 || fields[2] != "%0" || fields[3] != "0" {
		return ErrBindingMismatch
	}
	serverPID, e1 := strconv.Atoi(fields[0])
	panePID, e2 := strconv.Atoi(fields[1])
	if e1 != nil || e2 != nil || t.server == nil || t.server.Process.Pid != serverPID {
		return ErrBindingMismatch
	}
	serverBirth, err := terminalbinding.ProcessBirth(ctx, serverPID)
	if err != nil {
		return err
	}
	paneBirth, err := terminalbinding.ProcessBirth(ctx, panePID)
	if err != nil {
		return err
	}
	device, inode, err := terminalbinding.SocketIdentity(t.socket)
	if err != nil {
		return err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	record := terminalbinding.Record{Version: 1, Identity: identity, Socket: t.socket, SocketDevice: device, SocketInode: inode,
		ServerPID: serverPID, PanePID: panePID, ServerBirth: serverBirth, PaneBirth: paneBirth, PaneID: fields[2],
		Nonce: hex.EncodeToString(nonce), LiteralInputBarrier: t.literalInputBarrier}
	if _, err := t.run(ctx, nil, "set-option", "-g", "@bria_binding", record.Nonce); err != nil {
		return err
	}
	t.binding = record
	if err := t.prove(ctx); err != nil {
		t.binding = terminalbinding.Record{}
		return err
	}
	if err := lease.Save(record); err != nil {
		t.binding = terminalbinding.Record{}
		return err
	}
	t.lease = lease
	return nil
}

func AttachExisting(ctx context.Context, stateDir string, expected Binding) (*Terminal, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := terminalbinding.Acquire(stateDir, expected, false)
	if err != nil {
		return nil, err
	}
	record, err := lease.Read(expected)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		_ = lease.Close()
		return nil, ErrTerminalUnavailable
	}
	t := &Terminal{path: path, dir: filepath.Dir(record.Socket), socket: record.Socket, persistent: true,
		workdir: record.Identity.Workdir, binding: record, lease: lease, literalInputBarrier: record.LiteralInputBarrier}
	if err = t.prove(ctx); err == nil {
		t.process, err = terminalbinding.OwnProcess(record.PanePID)
	}
	if err == nil {
		if err = t.prove(ctx); err != nil {
			t.process.Release()
		}
	}
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	return t, nil
}

// Detach relinquishes observation without signalling the server or its pane.
// An unbound terminal cannot be orphaned: failed startup still requires Close.
func (t *Terminal) Detach(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.closeErr
	}
	if !t.persistent || t.lease == nil {
		return ErrBindingMismatch
	}
	t.closed = true
	if t.process != nil {
		t.process.Release()
	}
	t.closeErr = t.lease.Close()
	return t.closeErr
}

func (t *Terminal) bindingCommand(ctx context.Context, args ...string) (string, error) {
	return t.run(ctx, nil, args...)
}

func (t *Terminal) prove(ctx context.Context) error {
	return t.binding.Verify(ctx, t.bindingCommand)
}

func (t *Terminal) usable(ctx context.Context) error {
	if t.closed {
		return errors.New("native terminal closed")
	}
	if t.lease != nil {
		return t.prove(ctx)
	}
	return ctx.Err()
}

// closePersistent is called under mu.
func (t *Terminal) closePersistent() error {
	closed, err := t.lease.Destroy(context.Background(), t.binding, t.process, t.server, t.bindingCommand)
	if closed {
		t.closed, t.closeErr = true, err
	}
	return err
}
