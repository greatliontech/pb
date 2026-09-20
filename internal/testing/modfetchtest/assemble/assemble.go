// Package assemble closes modfetchtest's fixture parts over a
// modfetch.Client. It exists because modfetchtest itself cannot import
// modfetch — modfetch's own in-package test suite imports modfetchtest,
// so the assembly must live one package removed to break the cycle.
// Every suite outside internal/modfetch assembles its client here;
// modfetch's in-package suite keeps a local copy of the same literal,
// which the cycle makes unimportable from this package.
package assemble

import (
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/testing/modfetchtest"
)

// Client assembles the client under test over the fixture's transport,
// with a fresh in-memory cache and empty pin store. pbproxy is the
// PBPROXY-shaped source configuration handed to the fixture's Sources.
func Client(fx *modfetchtest.Fixture, pbproxy string) *modfetch.Client {
	return &modfetch.Client{
		HTTP:          fx.HTTPClient(),
		Sources:       fx.Sources(pbproxy),
		Cache:         &modfetch.Cache{FS: memfs.New()},
		Lock:          &lockfile.File{},
		ResolveOrigin: fx.Resolve,
		Fetcher:       fx.Fetcher(),
	}
}
