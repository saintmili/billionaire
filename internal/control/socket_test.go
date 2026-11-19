package control_test

import (
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestUnsafeAndAbsentEndpointsAreRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "control.sock")
	if _, err := control.Call(context.Background(), path, control.Request{Lookup: "missing"}); err == nil {
		t.Fatal("absent owner accepted")
	}
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Listen(path, nil); err == nil {
		t.Fatal("regular file overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "keep" {
		t.Fatal("endpoint changed")
	}
	os.Remove(path)
	os.Chmod(dir, 0755)
	if _, err := control.Listen(path, nil); err == nil {
		t.Fatal("unsafe directory accepted")
	}
}

func TestSocketLostResponseConcurrentRetryAndOwnerRestart(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "control.sock")
	service := app.NewControl(owner, nil, time.Minute)
	server, err := control.Listen(path, service)
	if err != nil {
		t.Fatal(err)
	}
	command := app.Command{ID: "lost-response", Kind: "account.create", Payload: json.RawMessage(`{"RequestID":"socket-account","Name":"Socket","Currency":"USDT","InitialBalance":"1000"}`)}
	// A caller sends its command but loses the response before reading it.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(conn).Encode(control.Request{Command: &command})
	conn.(*net.UnixConn).CloseWrite()
	conn.Close()
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := control.Call(ctx, path, control.Request{Command: &command})
			failures <- e
		}()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	first, err := control.Call(ctx, path, control.Request{Lookup: command.ID})
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	owner.Close()
	owner, err = store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	server, err = control.Listen(path, app.NewControl(owner, nil, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	recovered, err := control.Call(ctx, path, control.Request{Lookup: command.ID})
	if err != nil || !reflect.DeepEqual(first, recovered) {
		t.Fatalf("restart outcome %v %v", recovered, err)
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("duplicate account %v %v", accounts, err)
	}
	os.Chmod(path, 0666)
	if _, err = control.Call(ctx, path, control.Request{Lookup: command.ID}); err == nil {
		t.Fatal("unsafe endpoint accepted")
	}
	os.Chmod(path, 0600)
	owner.Close()
	if _, err = control.Call(ctx, path, control.Request{Command: &command}); err == nil {
		t.Fatal("dead owner accepted mutation")
	}
}

func TestExistingSocketSymlinkAndMalformedRequestsAreRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	if _, err = control.Listen(path, nil); err == nil {
		t.Fatal("stale socket replaced")
	}
	os.Remove(path)
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0600)
	os.Symlink(target, path)
	if _, err = control.Listen(path, nil); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(path)
	server, err := control.Listen(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte(`{"lookup":"x","unexpected":true}`))
	conn.(*net.UnixConn).CloseWrite()
	var response control.Response
	if err = json.NewDecoder(conn).Decode(&response); err != nil || response.Error == "" {
		t.Fatalf("malformed command: %+v %v", response, err)
	}
}
