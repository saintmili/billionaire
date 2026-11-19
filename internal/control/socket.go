// Package control provides a current-user-only Linux Unix control transport.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/paper"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const maxFrame = 1 << 20

type Request struct {
	Health  bool         `json:"health,omitempty"`
	Command *app.Command `json:"command,omitempty"`
	Lookup  string       `json:"lookup,omitempty"`
}
type Response struct {
	Health  *app.HealthSnapshot  `json:"health,omitempty"`
	Outcome paper.ControlOutcome `json:"outcome"`
	Error   string               `json:"error,omitempty"`
}
type Service interface {
	Execute(context.Context, app.Command) (paper.ControlOutcome, error)
	Outcome(context.Context, string) (paper.ControlOutcome, error)
}
type Server struct {
	listener *net.UnixListener
	service  Service
	done     chan struct{}
	workers  sync.WaitGroup
	path     string
	identity os.FileInfo
}

func private(path string, socket bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control path must be owned by current user without symlinks")
	}
	if socket {
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			return errors.New("control socket requires mode 0600")
		}
	} else if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("control directory requires mode 0700")
	}
	return nil
}
func Listen(path string, service Service) (*Server, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("control socket path must be absolute")
	}
	if err := private(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, errors.New("control endpoint already exists; verify owner stopped and remove stale socket explicitly")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	info, _ := os.Lstat(path)
	s := &Server{listener: listener, service: service, done: make(chan struct{}), path: path, identity: info}
	go s.serve()
	return s, nil
}
func (s *Server) serve() {
	defer close(s.done)
	slots := make(chan struct{}, 32)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			s.workers.Add(1)
			go func() { defer s.workers.Done(); defer func() { <-slots }(); defer conn.Close(); s.handle(conn) }()
		default:
			conn.Close()
		}
	}
}
func sameUser(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	valid := false
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		valid = e == nil && int(cred.Uid) == os.Getuid()
	})
	return err == nil && valid
}
func (s *Server) handle(conn *net.UnixConn) {
	if !sameUser(conn) {
		return
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	data, err := io.ReadAll(io.LimitReader(conn, maxFrame+1))
	if err != nil || len(data) > maxFrame {
		return
	}
	var request Request
	response := Response{}
	if err = app.DecodeControl(data, &request); err == nil {
		choices := 0
		if request.Command != nil {
			choices++
		}
		if request.Lookup != "" {
			choices++
		}
		if request.Health {
			choices++
		}
		if choices != 1 {
			err = errors.New("provide exactly one command, outcome lookup or health read")
		}
	}
	if err == nil && s.service == nil {
		err = errors.New("owner unavailable")
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if request.Health {
			reader, ok := s.service.(interface {
				Health(context.Context) (app.HealthSnapshot, error)
			})
			if !ok {
				err = errors.New("owner health unavailable")
			} else {
				snapshot, e := reader.Health(ctx)
				response.Health = &snapshot
				err = e
			}
		} else if request.Command != nil {
			response.Outcome, err = s.service.Execute(ctx, *request.Command)
		} else {
			response.Outcome, err = s.service.Outcome(ctx, request.Lookup)
		}
	}
	if err != nil {
		response.Error = err.Error()
	}
	json.NewEncoder(conn).Encode(response)
}
func (s *Server) Close() error {
	err := s.listener.Close()
	<-s.done
	s.workers.Wait()
	if info, e := os.Lstat(s.path); e == nil && os.SameFile(info, s.identity) {
		os.Remove(s.path)
	}
	return err
}
func call(ctx context.Context, path string, request Request) (Response, error) {
	var result Response
	if err := private(filepath.Dir(path), false); err != nil {
		return result, err
	}
	if err := private(path, true); err != nil {
		return result, errors.New("owner unavailable or unsafe control socket; no mutation attempted")
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return result, errors.New("owner unavailable; no direct mutation fallback")
	}
	defer conn.Close()
	unix := conn.(*net.UnixConn)
	if !sameUser(unix) {
		return result, errors.New("control owner identity mismatch")
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	data, err := json.Marshal(request)
	if err != nil || len(data) > maxFrame {
		return result, errors.New("invalid or oversized control request")
	}
	if _, err = conn.Write(data); err == nil {
		err = unix.CloseWrite()
	}
	if err != nil {
		return result, errors.New("control outcome unknown; query original request ID")
	}
	data, err = io.ReadAll(io.LimitReader(conn, maxFrame+1))
	if err != nil || len(data) > maxFrame {
		return result, errors.New("control outcome unknown; query original request ID")
	}
	var response Response
	if app.DecodeControl(data, &response) != nil {
		return result, errors.New("control outcome unknown; query original request ID")
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func Call(ctx context.Context, path string, request Request) (paper.ControlOutcome, error) {
	response, err := call(ctx, path, request)
	return response.Outcome, err
}

// ReadHealth always requests a new authoritative owner snapshot. A partial
// snapshot can accompany an error and must never be treated as ready.
func ReadHealth(ctx context.Context, path string) (*app.HealthSnapshot, error) {
	response, err := call(ctx, path, Request{Health: true})
	return response.Health, err
}
