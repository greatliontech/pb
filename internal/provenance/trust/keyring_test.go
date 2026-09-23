package trust

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"pgregory.net/rapid"
)

// block indents a multi-line key for a YAML literal block scalar.
func block(s string) string {
	return "      " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n      ") + "\n"
}

// The keyring parses into pinned keys the verifier made — each entry's
// kind naming its parser, its fingerprint the key's own — and a
// modules rule's keys resolve onto the rule from the keyring wherever
// the keyring stands; a plugins rule carries none
// (REQ-prov-pinned-keys-schema).
func TestParseKeyring(t *testing.T) {
	ssh := sigstoretest.NewSSHKey(t)
	pgp := sigstoretest.NewOpenPGPKey(t)
	keyring := "keyring:\n" +
		"  - kind: ssh\n    fingerprint: " + ssh.Fingerprint() + "\n    key: " + ssh.Public() + "\n" +
		"  - kind: openpgp\n    fingerprint: " + pgp.Fingerprint() + "\n    key: |\n" + block(pgp.Public(t))

	t.Run("happy: keys resolve from the keyring, listed after the rules", func(t *testing.T) {
		p, err := Parse([]byte("modules:\n  - prefix: github.com/acme\n    keys:\n      - " + pgp.Fingerprint() + "\n      - " + ssh.Fingerprint() + "\n  - prefix: github.com/other\n    identity:\n      san: \"*\"\n      issuer: https://x\n    keys: [" + ssh.Fingerprint() + "]\n" + keyring))
		if err != nil {
			t.Fatalf("Parse = %v, want nil", err)
		}
		got := p.Modules[0].Keys
		if len(got) != 2 || got[0].Kind() != gitprov.OpenPGP || got[0].Fingerprint() != pgp.Fingerprint() || got[1].Kind() != gitprov.SSH || got[1].Fingerprint() != ssh.Fingerprint() {
			t.Fatalf("rule[0].Keys = %+v", got)
		}
		if len(p.Modules[1].Keys) != 1 || p.Modules[1].Identity == nil {
			t.Fatalf("rule[1] = %+v: keys beside identity", p.Modules[1])
		}
		d := p.EvaluateModule("github.com/acme/widget")
		if len(d.Keys) != 2 || d.Keys[0].Fingerprint() != pgp.Fingerprint() {
			t.Fatalf("decision keys = %+v", d.Keys)
		}
		if d := p.EvaluateModule("example.org/x"); d.Keys != nil {
			t.Fatalf("decision keys = %+v, want none under the default", d.Keys)
		}
	})
	t.Run("happy: a keyring with no rule naming it", func(t *testing.T) {
		if _, err := Parse([]byte(keyring)); err != nil {
			t.Fatalf("Parse = %v, want nil", err)
		}
	})
	sshFP := ssh.Fingerprint()
	otherFP := sigstoretest.NewSSHKey(t).Fingerprint()
	invalid := map[string]struct{ data, want string }{
		"keyring not a list":    {"keyring: yes\n", "keyring must be a list"},
		"entry not a mapping":   {"keyring:\n  - ssh\n", "keyring[0] must be a mapping"},
		"entry unknown key":     {"keyring:\n  - kind: ssh\n    fingerprint: x\n    key: y\n    comment: z\n", `unknown key "comment"`},
		"entry missing key":     {"keyring:\n  - kind: ssh\n    fingerprint: x\n", "keyring[0]: missing key"},
		"entry missing kind":    {"keyring:\n  - fingerprint: x\n    key: y\n", "keyring[0]: missing kind"},
		"kind unknown":          {"keyring:\n  - kind: x509\n    fingerprint: x\n    key: y\n", `"x509" is neither openpgp nor ssh`},
		"kind not a line":       {"keyring:\n  - kind: [ssh]\n    fingerprint: x\n    key: y\n", "keyring[0].kind must be one non-empty line of text"},
		"key not text":          {"keyring:\n  - kind: ssh\n    fingerprint: x\n    key: [y]\n", "keyring[0].key must be text"},
		"key unparsable":        {"keyring:\n  - kind: ssh\n    fingerprint: " + sshFP + "\n    key: not a key\n", "keyring[0].key: gitprov: SSH public key"},
		"key of the other kind": {"keyring:\n  - kind: openpgp\n    fingerprint: " + sshFP + "\n    key: " + ssh.Public() + "\n", "keyring[0].key:"},
		"fingerprint mismatch":  {"keyring:\n  - kind: ssh\n    fingerprint: " + otherFP + "\n    key: " + ssh.Public() + "\n", `is not the key's, which is "` + sshFP + `"`},
		"rule key unknown":      {"modules:\n  - keys: [" + otherFP + "]\n" + keyring, `modules[0].keys: fingerprint "` + otherFP + `" names no keyring entry`},
		"rule keys not a list":  {"modules:\n  - keys: x\n" + keyring, "modules[0].keys must be a list"},
		"rule key not a line":   {"modules:\n  - keys: [[x]]\n" + keyring, "modules[0].keys must hold non-empty lines of text"},
		"rule keys empty":       {"modules:\n  - keys: []\n" + keyring, "modules[0].keys must name at least one keyring entry"},
		"rule key listed twice": {"modules:\n  - keys: [" + sshFP + ", " + sshFP + "]\n" + keyring, `modules[0].keys: fingerprint "` + sshFP + `" listed twice`},
		"keyring entry twice":   {keyring + "  - kind: ssh\n    fingerprint: " + sshFP + "\n    key: " + ssh.Public() + "\n", `keyring[2]: fingerprint "` + sshFP + `" listed twice`},
		"key of the other kind, an OpenPGP block under ssh": {"keyring:\n  - kind: ssh\n    fingerprint: " + pgp.Fingerprint() + "\n    key: |\n" + block(pgp.Public(t)), "keyring[0].key:"},
		"rule key empty":         {"modules:\n  - keys: [\"\"]\n" + keyring, "modules[0].keys must hold non-empty lines of text"},
		"plugins rule with keys": {"plugins:\n  - prefix: ghcr.io/x\n    keys: [" + sshFP + "]\n" + keyring, "plugins[0]: a plugins rule carries no keys"},
	}
	for name, c := range invalid {
		t.Run("invalid: "+name, func(t *testing.T) {
			_, err := Parse([]byte(c.data))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Parse = %v, want ErrInvalid containing %q", err, c.want)
			}
		})
	}
}

// Over arbitrary rule sets naming keys from a keyring, the decision's
// keys are exactly the governing rule's, in its order.
func TestEvaluateCarriesTheGoverningRulesKeys(t *testing.T) {
	ring := []gitprov.PinnedKey{}
	for i := 0; i < 3; i++ {
		k, err := gitprov.ParsePinnedKey(gitprov.SSH, sigstoretest.NewSSHKey(t).Public())
		if err != nil {
			t.Fatal(err)
		}
		ring = append(ring, k)
	}
	seg := rapid.SampledFrom([]string{"a", "b", "acme", "x"})
	pathGen := rapid.Custom(func(rt *rapid.T) string {
		// Zero segments is the empty prefix, which governs everything.
		n := rapid.IntRange(0, 3).Draw(rt, "segs")
		parts := make([]string, n)
		for i := range parts {
			parts[i] = seg.Draw(rt, "seg")
		}
		return strings.Join(parts, "/")
	})
	rapid.Check(t, func(rt *rapid.T) {
		var rules []Rule
		seen := map[string]bool{}
		for i, n := 0, rapid.IntRange(0, 4).Draw(rt, "rules"); i < n; i++ {
			prefix := pathGen.Draw(rt, "prefix")
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			var keys []gitprov.PinnedKey
			for _, j := range rapid.SliceOfNDistinct(rapid.IntRange(0, len(ring)-1), 0, len(ring), rapid.ID[int]).Draw(rt, "keys") {
				keys = append(keys, ring[j])
			}
			rules = append(rules, Rule{Prefix: prefix, Keys: keys})
		}
		p := &Policy{Modules: rules}
		subject := pathGen.Draw(rt, "subject")
		if subject == "" {
			subject = "a"
		}
		d := p.EvaluateModule(subject)
		var want []gitprov.PinnedKey
		bestLen := -1
		for _, r := range rules {
			if !(r.Prefix == "" || subject == r.Prefix || strings.HasPrefix(subject, r.Prefix+"/")) {
				continue
			}
			if len(r.Prefix) > bestLen {
				bestLen, want = len(r.Prefix), r.Keys
			}
		}
		if len(d.Keys) != len(want) {
			t.Fatalf("keys = %d, want %d (subject %q)", len(d.Keys), len(want), subject)
		}
		for i := range want {
			if d.Keys[i].Fingerprint() != want[i].Fingerprint() {
				t.Fatalf("keys[%d] = %s, want %s", i, d.Keys[i].Fingerprint(), want[i].Fingerprint())
			}
		}
	})
}
