package direct

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	xssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/greatliontech/pb/internal/source"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSHTransport is the transport client option every SSH operation
// takes (REQ-resolve-ssh): the running agent's keys alone, the host's
// key verified against the user's known hosts, and the host and port
// as the user's OpenSSH client configuration maps them — the
// transport's own host resolution, which the option leaves in place.
func SSHTransport() client.Option {
	return client.WithSSHAuth(agentAuth{})
}

// agentAuth builds each SSH connection's client configuration from the
// running agent: the URL's user, `git` where it names none; the agent
// reached through SSH_AUTH_SOCK at the first SSH connection of the
// process and kept for the rest — one socket per run, however many
// origins are probed and fetched — so a run fetching nothing over SSH
// never needs one; the known-hosts callback the transport's default,
// which reads SSH_KNOWN_HOSTS or the user's and the system's files and
// refuses a host none holds. No key file is read and no password
// asked.
type agentAuth struct{}

// theAgent is the process's one agent connection, dialed on first use
// over the socket SSH_AUTH_SOCK names then and kept while it names the
// same; a failed dial holds nothing, so an agent started later is
// found, and a socket named anew is dialed anew.
var theAgent struct {
	sync.Mutex
	sock   string
	conn   net.Conn
	client agent.ExtendedAgent
}

// agentClient is the process's agent, dialed on first use.
func agentClient() (agent.ExtendedAgent, error) {
	theAgent.Lock()
	defer theAgent.Unlock()
	sock := os.Getenv("SSH_AUTH_SOCK")
	if theAgent.client != nil && theAgent.sock == sock {
		return theAgent.client, nil
	}
	if theAgent.conn != nil {
		theAgent.conn.Close()
		theAgent.conn, theAgent.client = nil, nil
	}
	if sock == "" {
		return nil, fmt.Errorf("%w: SSH_AUTH_SOCK is not set", source.ErrNoAgent)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", source.ErrNoAgent, err)
	}
	theAgent.sock, theAgent.conn, theAgent.client = sock, conn, agent.NewClient(conn)
	return theAgent.client, nil
}

func (agentAuth) ClientConfig(ctx context.Context, req *transport.Request) (*gossh.ClientConfig, error) {
	user := xssh.DefaultUsername
	if req.URL.User != nil && req.URL.User.Username() != "" {
		user = req.URL.User.Username()
	}
	ag, err := agentClient()
	if err != nil {
		return nil, fmt.Errorf("reaching %s over SSH: %w", req.URL.Host, err)
	}
	auth := &xssh.PublicKeysCallback{User: user, Callback: func() ([]gossh.Signer, error) {
		signers, err := ag.Signers()
		if err != nil {
			// The agent went away under its socket — restarted, or gone:
			// the connection is dropped so the next one dials anew, and
			// this one fails naming the agent.
			dropAgent(ag)
			return nil, fmt.Errorf("%w: %v", source.ErrNoAgent, err)
		}
		return signers, nil
	}}
	return auth.ClientConfig(ctx, req)
}

// dropAgent forgets the process's agent connection where it is still
// the one given, so a later dial finds the agent anew.
func dropAgent(ag agent.ExtendedAgent) {
	theAgent.Lock()
	defer theAgent.Unlock()
	if theAgent.client != ag {
		return
	}
	theAgent.conn.Close()
	theAgent.sock, theAgent.conn, theAgent.client = "", nil, nil
}
