package migrate

import (
	"testing"

	"github.com/greatliontech/stipulator/stipulate/structural"
)

// The migrate package reaches no network of its own
// (REQ-migrate-no-network-but-tidy): it imports none of pb's source
// clients — the fetchers, the proxy, the origin resolution — nor the
// plugin acquirer nor the registry client, so a buf file, a BSR or an
// origin can be reached only through the discovery and the tidy the
// invocation hands in. go-git is imported for the origin remote's
// URL, read from the repository's own configuration, its transports
// with it unused.
func TestNoNetworkImports(t *testing.T) {
	structural.NoImport(t, "github.com/greatliontech/pb/internal/migrate",
		"github.com/greatliontech/pb/internal/source/...",
		"github.com/greatliontech/pb/internal/plugin/oci",
		"github.com/greatliontech/pb/internal/dep",
		"github.com/google/go-containerregistry/pkg/v1/remote",
	)
}
