package pool

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Helper hub moved from nduwork/pengupool-marketplace to nduwork/pengupool-agent-hub.
// migrateHub re-points what an existing install holds, once, on the first start after the move and
// harmlessly again: the catalog's clone, the remote of every helper PenguPool cloned from the old
// repository, the helpers' keys (their journals follow) and the "the owner deleted it" markers. Helper
// ids, pins (the checkout stays at its commit until the next sync moves it) and memory are kept, and no
// second catalog is left behind. A helper in the owner's own checkout of the old repository is theirs: not touched.
const legacyMarketplace = "https://github.com/nduwork/pengupool-marketplace.git"

func (p *Pool) migrateHub(ctx context.Context) {
	oldKey, newKey := repoKey(legacyMarketplace), repoKey(mainMarketplace)
	// Every step below decides from what is on disk or in the record, not from whether an earlier step
	// ran, so a run cut short at any point is finished by the next start; each step is a no-op when done.

	// The catalog clone is a cache of the hub, and its history may not be the old repository's, so it is
	// never moved or re-pointed: the old clone is dropped once the hub's is cloned fresh (kept, to try again
	// at the next start, when that clone is refused), and a clone carrying the hub's name that still has
	// the old origin, or a history unrelated to the hub's, is dropped and cloned again.
	hubDir := p.marketDirFor(mainMarketplace) // the default hub, whatever hub is in use
	if old := p.marketDirFor(legacyMarketplace); old != hubDir {
		if origin, err := p.gitIn(ctx, old, "remote", "get-url", "origin"); err == nil && repoKey(origin) == oldKey {
			if _, err := os.Stat(hubDir); err == nil {
				_ = os.RemoveAll(old)
			} else if _, err := p.ensureMarketFor(ctx, mainMarketplace); err != nil {
				p.logf("pool: helper hub: cloning the hub: %v (the old catalog clone stays until it works)", err)
			} else if os.RemoveAll(old) == nil {
				p.logf("pool: helper hub: replaced the old catalog clone with a clone of the hub")
			}
		}
	}
	if origin, err := p.gitIn(ctx, hubDir, "remote", "get-url", "origin"); err == nil && (repoKey(origin) == oldKey || p.unrelatedToOrigin(ctx, hubDir)) {
		if os.RemoveAll(hubDir) == nil {
			if _, err := p.ensureMarketFor(ctx, mainMarketplace); err != nil {
				p.logf("pool: helper hub: cloning the hub again: %v", err)
			} else {
				p.logf("pool: helper hub: the catalog clone did not follow the hub's history; cloned again")
			}
		}
	}
	// The helpers: the key first, then the journal, the remote last. A helper that already has the hub's
	// key is checked for journal rows still under the old key (a machine that had not upgraded wrote
	// them): rekey is a no-op when there are none.
	for _, sk := range p.st.State().Domain.Skills {
		key := sk.Key
		_, own := p.ownClone(ctx, sk.Path) // the owner's own checkout keeps its key and its remote
		if rest, ok := strings.CutPrefix(sk.Key, oldKey); ok && own {
			key = newKey + rest
			if err := p.update(func(st *store.State) error {
				if s := findSkill(&st.Domain, sk.ID); s != nil && s.Key == sk.Key {
					s.Key = key
				}
				return nil
			}); err != nil {
				p.logf("pool: helper hub: %s: %v", sk.Name, err)
				continue
			}
		}
		// Not while another helper still holds the old key (the owner's own checkout of the old repository):
		// its journal rows belong to it.
		held := func(k string) bool {
			return slices.ContainsFunc(p.st.State().Domain.Skills, func(o protocol.Skill) bool { return o.ID != sk.ID && o.Key == k })
		}
		if rest, ok := strings.CutPrefix(key, newKey); ok && !held(oldKey+rest) {
			if err := p.rekey(oldKey+rest, key); err != nil {
				p.logf("pool: helper hub: %s: journal: %v", sk.Name, err)
				continue
			}
		}
		origin, err := p.gitIn(ctx, sk.Path, "remote", "get-url", "origin")
		if err != nil || repoKey(origin) != oldKey {
			continue
		}
		if top, _ := p.ownClone(ctx, sk.Path); own {
			if _, err := p.gitIn(ctx, top, "remote", "set-url", "origin", strings.Replace(origin, "pengupool-marketplace", "pengupool-agent-hub", 1)); err != nil {
				p.logf("pool: helper hub: %s: %v", sk.Name, err)
				continue
			}
			p.logf("pool: helper hub: %s now follows %s", sk.Name, newKey)
		}
	}
	// "The owner deleted it" markers are keyed by the helper's catalog key.
	if p.db != nil {
		for k, v := range p.db.SettingsPrefix(deletedKey(oldKey)) {
			if v != "" {
				_ = p.db.SetSetting(deletedKey(newKey)+strings.TrimPrefix(k, deletedKey(oldKey)), v)
			}
			_ = p.db.SetSetting(k, "")
		}
	}
}

// unrelatedToOrigin reports whether a clone's HEAD shares no history with its origin's default branch
// (a repository pushed again from scratch): a fast-forward pull can never work, so the clone is not kept.
// An offline or empty origin is not "unrelated": nothing is decided without having looked.
func (p *Pool) unrelatedToOrigin(ctx context.Context, dir string) bool {
	if _, err := p.ghGit(ctx, dir, "fetch", "-q", "origin"); err != nil {
		return false
	}
	if _, err := p.gitIn(ctx, dir, "rev-parse", "-q", "--verify", "HEAD"); err != nil {
		return false
	}
	if _, err := p.gitIn(ctx, dir, "rev-parse", "-q", "--verify", "origin/HEAD"); err != nil {
		return false
	}
	// merge-base exits 1 with no output when there is none; any other failure is not an answer.
	_, err := p.gitIn(ctx, dir, "merge-base", "HEAD", "origin/HEAD")
	return err != nil && strings.HasSuffix(strings.TrimSpace(err.Error()), "git merge-base:")
}
