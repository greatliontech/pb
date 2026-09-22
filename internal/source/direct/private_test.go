package direct

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/greatliontech/pb/internal/source"
	"github.com/greatliontech/pb/internal/source/netrc"
	"github.com/greatliontech/pb/internal/testing/gittest"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// privateOrigin is a fixture repository with one tagged commit, served
// by go-git's own backend over whatever transport a test wraps around
// it.
func privateOrigin(t *testing.T) (*gittest.Repo, *backend.Backend) {
	t.Helper()
	fs := memfs.New()
	g := gittest.NewAt(t, fs, "repo")
	h := g.Commit("init", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	g.Branch("main", h)
	g.Tag("v1.0.0", h)
	g.Head("main")
	return g, backend.New(transport.NewFilesystemLoader(fs, false))
}

// An origin over HTTPS demanding basic authorization is fetched with
// the credential file's entry for its host (REQ-resolve-credentials):
// the listing and the fetch both carry it; without an entry the origin
// refuses; and over cleartext the same entry is withheld, so the
// origin refuses there too.
func TestFetchPrivateOriginOverHTTPS(t *testing.T) {
	ctx := context.Background()
	_, be := privateOrigin(t)
	var mu sync.Mutex
	var seen []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if !ok || user != "alice" || pass != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		be.ServeHTTP(w, r)
	})
	tlsSrv := httptest.NewTLSServer(handler)
	defer tlsSrv.Close()
	plainSrv := httptest.NewServer(handler)
	defer plainSrv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})

	creds, err := netrc.Parse([]byte("machine 127.0.0.1 login alice password s3cret\n"))
	if err != nil {
		t.Fatal(err)
	}
	withCreds := append(creds.ClientOptions(), client.WithCABundle(ca))
	repo, err := Fetcher{ClientOptions: withCreds, Store: memfs.New()}.Fetch(ctx, tlsSrv.URL+"/repo")
	if err != nil {
		t.Fatalf("with the entry: %v", err)
	}
	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "refs/tags/v1.0.0") {
		t.Fatalf("listed %v", names)
	}
	if _, err := repo.Head(ctx); err != nil {
		t.Fatalf("fetching the head with the entry: %v", err)
	}
	mu.Lock()
	n := len(seen)
	mu.Unlock()
	if n < 2 {
		t.Fatalf("the origin saw %v: the listing and the fetch both reach it", seen)
	}

	// No entry for the host: the origin's refusal is the error.
	none := &netrc.File{}
	_, err = Fetcher{ClientOptions: append(none.ClientOptions(), client.WithCABundle(ca)), Store: memfs.New()}.Fetch(ctx, tlsSrv.URL+"/repo")
	if err == nil || !errors.Is(err, transport.ErrAuthenticationRequired) {
		t.Fatalf("without an entry: %v", err)
	}
	// The entry exists, the channel is cleartext: withheld.
	_, err = Fetcher{ClientOptions: withCreds, Store: memfs.New()}.Fetch(ctx, plainSrv.URL+"/repo")
	if err == nil || !errors.Is(err, transport.ErrAuthenticationRequired) {
		t.Fatalf("over cleartext: %v", err)
	}
}

// sshOrigin serves the fixture over SSH from an in-process server:
// public-key authentication for the user git with the one key the
// agent holds, a session's exec running git-upload-pack over go-git's
// backend. It returns the address and the host's public key.
func sshOrigin(t *testing.T, be *backend.Backend, authorized gossh.PublicKey) (string, gossh.PublicKey) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &gossh.ServerConfig{
		PublicKeyCallback: func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if conn.User() != "git" || string(key.Marshal()) != string(authorized.Marshal()) {
				return nil, fmt.Errorf("no key for %s", conn.User())
			}
			return &gossh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, cfg, be)
		}
	}()
	return ln.Addr().String(), hostSigner.PublicKey()
}

// serveSSH handles one connection: session channels whose exec request
// names git-upload-pack are served over the backend, the environment's
// GIT_PROTOCOL handed on.
func serveSSH(nc net.Conn, cfg *gossh.ServerConfig, be *backend.Backend) {
	conn, chans, reqs, err := gossh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go gossh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(gossh.UnknownChannelType, "session only")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			defer ch.Close()
			gitProtocol := ""
			for req := range creqs {
				switch req.Type {
				case "env":
					var kv struct{ Name, Value string }
					if gossh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" {
						gitProtocol = kv.Value
					}
					req.Reply(true, nil)
				case "exec":
					var cmd struct{ Command string }
					if err := gossh.Unmarshal(req.Payload, &cmd); err != nil {
						req.Reply(false, nil)
						continue
					}
					req.Reply(true, nil)
					status := uint32(0)
					if err := execUploadPack(ch, be, cmd.Command, gitProtocol); err != nil {
						status = 1
					}
					ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
					return
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

// execUploadPack runs `git-upload-pack '<path>'` over the channel.
func execUploadPack(ch gossh.Channel, be *backend.Backend, command, gitProtocol string) error {
	name, quoted, ok := strings.Cut(command, " ")
	if !ok || name != transport.UploadPackService {
		return fmt.Errorf("unsupported command %q", command)
	}
	path := strings.Trim(quoted, "'")
	ep, err := transport.ParseURL(path)
	if err != nil {
		return err
	}
	return be.Serve(context.Background(), ch, ch, &backend.Request{URL: ep, Service: transport.UploadPackService, GitProtocol: gitProtocol})
}

// testAgent is an in-process agent on a unix socket: the connections
// it accepted, counted, and a way to end them all.
type testAgent struct {
	conns atomic.Int32
	mu    sync.Mutex
	open  []net.Conn
	ln    net.Listener
}

// startAgent serves a keyring holding the one key on a unix socket —
// a fresh one, or the path given — and points SSH_AUTH_SOCK at it.
func startAgent(t *testing.T, key ed25519.PrivateKey, sock string) *testAgent {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	if sock == "" {
		sock = filepath.Join(t.TempDir(), "agent.sock")
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	a := &testAgent{ln: ln}
	t.Cleanup(a.stop)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			a.conns.Add(1)
			a.mu.Lock()
			a.open = append(a.open, c)
			a.mu.Unlock()
			go agent.ServeAgent(keyring, c)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	return a
}

// stop ends the agent: the listener and every connection it accepted.
func (a *testAgent) stop() {
	a.ln.Close()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.open {
		c.Close()
	}
	a.open = nil
}

// knownHosts writes a known-hosts file naming the host's key at addr
// and points SSH_KNOWN_HOSTS at it.
func knownHosts(t *testing.T, addr string, key gossh.PublicKey) {
	t.Helper()
	line := fmt.Sprintf("%s %s", knownHostsName(addr), strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))))
	p := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", p)
}

// knownHostsName is the known-hosts spelling of a host with a port.
func knownHostsName(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	return fmt.Sprintf("[%s]:%s", host, port)
}

// An origin over SSH is reached through the running agent alone, as
// the user git, the host's key verified against the known hosts
// (REQ-resolve-ssh): with the agent's key authorized the listing and
// the fetch succeed over one agent connection for the run; with no
// agent to reach the fetch fails naming it; with the host's key
// unknown — no line for the host, or another key on its line — the
// fetch fails at the host key, before any authentication; and the
// option reads no key file — a key on disk at the place OpenSSH reads
// authenticates nothing while the agent holds only a stranger's.
func TestFetchPrivateOriginOverSSH(t *testing.T) {
	ctx := context.Background()
	_, be := privateOrigin(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	addr, hostKey := sshOrigin(t, be, authorized)
	knownHosts(t, addr, hostKey)
	ag := startAgent(t, priv, "")
	url := "ssh://git@" + addr + "/repo"

	repo, err := Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}.Fetch(ctx, url)
	if err != nil {
		t.Fatalf("through the agent: %v", err)
	}
	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "refs/tags/v1.0.0") {
		t.Fatalf("listed %v", names)
	}
	if _, err := repo.Head(ctx); err != nil {
		t.Fatalf("fetching the head through the agent: %v", err)
	}
	if n := ag.conns.Load(); n != 1 {
		t.Fatalf("the listing and the fetch opened %d agent connections, want one for the run", n)
	}

	// The agent restarted under the same socket: the dead connection
	// fails once naming the agent, and the next dials the new one.
	sock := os.Getenv("SSH_AUTH_SOCK")
	ag.stop()
	_, err = Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}.Fetch(ctx, url)
	if !errors.Is(err, source.ErrNoAgent) {
		t.Fatalf("with the agent gone under its socket: %v", err)
	}
	again := startAgent(t, priv, sock)
	if _, err := (Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}).Fetch(ctx, url); err != nil {
		t.Fatalf("with the agent back under its socket: %v", err)
	}
	if n := again.conns.Load(); n != 1 {
		t.Fatalf("the restarted agent saw %d connections", n)
	}

	// No agent: named, before any connection is attempted.
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err = Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}.Fetch(ctx, url)
	if !errors.Is(err, source.ErrNoAgent) {
		t.Fatalf("without an agent: %v", err)
	}
	startAgent(t, priv, "")

	// The host's key unknown, two ways: no line for the host, and
	// another key on its line. Each is refused at the host key.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := gossh.NewSignerFromKey(otherPriv)
	for name, key := range map[string]gossh.PublicKey{"no line for the host": nil, "another key on its line": otherSigner.PublicKey()} {
		if key == nil {
			knownHosts(t, "127.0.0.1:1", hostKey) // a line for some other host
		} else {
			knownHosts(t, addr, key)
		}
		_, err = Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}.Fetch(ctx, url)
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	knownHosts(t, addr, hostKey)

	// A key the origin does not authorize, the agent's only one: the
	// origin refuses, and the authorized key on disk where OpenSSH
	// would read it authenticates nothing.
	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	startAgent(t, strangerPriv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	_, err = Fetcher{ClientOptions: []client.Option{SSHTransport()}, Store: memfs.New()}.Fetch(ctx, url)
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("a key the agent does not hold: %v", err)
	}
}
