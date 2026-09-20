// Package assemble closes fetchtest's fixture parts over a
// fetch.Client. It exists because fetchtest itself cannot import
// fetch — fetch's own in-package test suite imports fetchtest,
// so the assembly must live one package removed to break the cycle.
// Every suite outside internal/source/fetch assembles its client here;
// fetch's in-package suite keeps a local copy of the same literal,
// which the cycle makes unimportable from this package.
package assemble

import (
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// Client assembles the client under test over the fixture's transport,
// with a fresh in-memory cache and empty pin store. pbproxy is the
// PBPROXY-shaped source configuration handed to the fixture's Sources.
func Client(fx *fetchtest.Fixture, pbproxy string) *fetch.Client {
	return &fetch.Client{
		HTTP:          fx.HTTPClient(),
		Sources:       fx.Sources(pbproxy),
		Cache:         &fetch.Cache{FS: memfs.New()},
		Lock:          &lockfile.File{},
		ResolveOrigin: fx.Resolve,
		Fetcher:       fx.Fetcher(),
	}
}
