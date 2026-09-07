//go:build linux

package nativeterminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A pidfd pins a process identity even if its numeric PID is later reused.
// No signal in this file addresses a bare PID or an ambient process group.
type ownedProcess struct{ pid, fd int }

func pauseServer(server *exec.Cmd) (func(), error) {
	if server == nil || server.Process == nil {
		return nil, errors.New("owned tmux server missing")
	}
	// os.Process serializes Signal against Wait and rejects reaped identities.
	if err := server.Process.Signal(syscall.SIGSTOP); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil, nil
		}
		return nil, err
	}
	resume := func() { _ = server.Process.Signal(syscall.SIGCONT) }
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", server.Process.Pid))
		if err != nil {
			resume()
			return nil, errors.New("owned tmux server stop unconfirmed")
		}
		end := strings.LastIndexByte(string(data), ')')
		fields := strings.Fields(string(data)[end+1:])
		if end >= 0 && len(fields) > 0 && (fields[0] == "T" || fields[0] == "t" || fields[0] == "Z") {
			return resume, nil
		}
		if time.Now().After(deadline) {
			resume()
			return nil, errors.New("owned tmux server stop timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func ownProcess(pid int) (*ownedProcess, error) {
	if pid < 2 {
		return nil, errors.New("invalid owned pane PID")
	}
	fd, _, errno := syscall.Syscall(434, uintptr(pid), 0, 0) // pidfd_open
	if errno != 0 {
		return nil, fmt.Errorf("open owned pane pidfd: %w", errno)
	}
	return &ownedProcess{pid: pid, fd: int(fd)}, nil
}

func (p *ownedProcess) signal(signal syscall.Signal) error {
	_, _, errno := syscall.Syscall6(424, uintptr(p.fd), uintptr(signal), 0, 0, 0, 0) // pidfd_send_signal
	if errno == syscall.ESRCH {
		return nil
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func (p *ownedProcess) exited() bool {
	// A ready pidfd is kernel proof of process exit, independent of PID reuse.
	var set syscall.FdSet
	if p.fd >= len(set.Bits)*64 {
		return false
	}
	set.Bits[p.fd/64] |= 1 << uint(p.fd%64)
	timeout := syscall.Timeval{}
	n, err := syscall.Select(p.fd+1, &set, nil, nil, &timeout)
	return err == nil && n > 0
}

func (p *ownedProcess) killTree() error {
	defer syscall.Close(p.fd)
	if p.exited() {
		if err := syscall.Kill(-p.pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return errors.New("pane exited before owned process group cleanup could be proven")
	}
	deadline := time.Now().Add(3 * time.Second)
	owned := []*ownedProcess{p}
	// Even a discovery error must not strand processes we already stopped.
	defer func() {
		for _, process := range owned {
			_ = process.signal(syscall.SIGKILL)
		}
		for _, process := range owned[1:] {
			_ = syscall.Close(process.fd)
		}
	}()
	seen := map[int]bool{p.pid: true}
	// Stop each parent before discovering its children; this closes the ordinary
	// fork-during-cleanup race. Each child receives its own pinned descriptor.
	for i := 0; i < len(owned); i++ {
		if time.Now().After(deadline) {
			return errors.New("owned process discovery timed out")
		}
		current := owned[i]
		if err := current.signal(syscall.SIGSTOP); err != nil {
			return fmt.Errorf("stop owned CLI: %w", err)
		}
		for !current.exited() {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", current.pid))
			if err != nil {
				return errors.New("owned process stop unconfirmed")
			}
			end := strings.LastIndexByte(string(stat), ')')
			fields := strings.Fields(string(stat)[end+1:])
			if end >= 0 && len(fields) > 0 && (fields[0] == "T" || fields[0] == "t" || fields[0] == "Z") {
				break
			}
			if time.Now().After(deadline) {
				state := "unknown"
				if len(fields) > 0 {
					state = fields[0]
				}
				return fmt.Errorf("owned process stop timed out (pid=%d state=%s index=%d)", current.pid, state, i)
			}
			time.Sleep(time.Millisecond)
		}
		if current.exited() {
			continue
		}
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		if len(entries) > 131072 {
			return errors.New("process discovery exceeds cleanup bound")
		}
		for _, entry := range entries {
			value := entry.Name()
			if _, parseErr := strconv.Atoi(value); parseErr != nil {
				continue
			}
			data, readErr := os.ReadFile("/proc/" + value + "/stat")
			if readErr != nil {
				continue
			}
			end := strings.LastIndexByte(string(data), ')')
			fields := strings.Fields(string(data)[end+1:])
			if end < 0 || len(fields) < 2 || fields[1] != strconv.Itoa(current.pid) {
				continue
			}
			{
				pid, err := strconv.Atoi(value)
				if err != nil || seen[pid] {
					continue
				}
				child, err := ownProcess(pid)
				if errors.Is(err, syscall.ESRCH) {
					continue
				}
				if err != nil {
					return err
				}
				// The children file is only a discovery hint. Re-check parentage
				// against the pinned child before any side effect, so stale PID
				// numbers cannot cause a signal to an unrelated reused process.
				stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				end := strings.LastIndexByte(string(stat), ')')
				fields := strings.Fields(string(stat)[end+1:])
				if statErr != nil || end < 0 || len(fields) < 2 || fields[1] != strconv.Itoa(current.pid) || child.exited() {
					_ = syscall.Close(child.fd)
					continue
				}
				seen[pid] = true
				owned = append(owned, child)
				if len(owned) > 2048 {
					return errors.New("owned CLI tree exceeds cleanup bound")
				}
			}
		}
	}
	var result error
	for i := len(owned) - 1; i >= 0; i-- {
		result = errors.Join(result, owned[i].signal(syscall.SIGKILL))
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		allExited := true
		for _, process := range owned {
			allExited = allExited && process.exited()
		}
		if allExited {
			return result
		}
		if time.Now().After(deadline) {
			return errors.Join(result, errors.New("owned CLI tree exit unconfirmed"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
