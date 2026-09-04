# Public Personal and private Commercial workflow

Soulacy Personal is the canonical public upstream. Soulacy Commercial is the
private downstream distribution that contains Personal plus Teams and Scale.
The dependency direction is one-way:

```text
vmodekurti/soulacy-personal (public main)
                    |
                    | reviewed merge PR
                    v
vmodekurti/soulacy-commercial (private main)
```

`.personal-base` records the exact public commit incorporated by Commercial.
That commit is also a Git ancestor of Commercial: the initial relationship was
established with an explicit history-linking merge, and every later update is a
normal merge. This makes Git—not a source-copy script—the synchronization and
conflict-resolution mechanism.

## Shared change

1. Create the change in `soulacy-personal` and merge it into public `main`.
2. The `Sync Personal upstream` workflow fetches public `main`, creates
   `automation/sync-personal`, performs a merge, updates `.personal-base`, and
   opens or refreshes a pull request in this private repository.
3. Review the merge as a normal Commercial change. Commercial CI runs the
   Personal, Team, and Scale deployment contracts before it may merge.
4. Merge the synchronization PR. Commercial releases can report both their own
   revision and the `.personal-base` revision.

## Commercial-only change

Develop Teams and Scale features only in `soulacy-commercial`. Never push a
Commercial branch, tag, artifact, or Git object to the public remote. Shared
code can be modified here for integration, but the reusable change must first
land independently in Personal.

## Shared bug discovered in Commercial

Do not permanently fix the same bug twice. Reproduce the issue in a Personal
checkout, make the smallest public-safe fix there, and merge its public PR.
Then let the synchronization workflow bring that commit into Commercial. Put
tenant, entitlement, billing, distributed-runtime, or workspace integration in
a separate Commercial commit layered after the upstream merge.

## Local commands

Add the public upstream once:

```bash
git remote add personal https://github.com/vmodekurti/soulacy-personal.git
git fetch personal main
```

Check lineage and public availability:

```bash
./scripts/verify-personal-upstream.sh
```

To prepare the same merge locally when automation reports a conflict:

```bash
git switch -c codex/sync-personal origin/main
git fetch personal main
git merge --no-ff personal/main
git rev-parse personal/main > .personal-base
git add .personal-base
git commit --amend --no-edit
```

Resolve conflicts in favor of Personal for shared behavior, then reapply only
the necessary Commercial integration. Never resolve by copying the Commercial
tree into the public repository.

## Enforcement

- The public repository independently checks that it remains public and contains
  no Teams/Scale implementation paths.
- This private repository checks its visibility, anonymous availability of the
  public upstream, recorded Git ancestry, the edition dependency boundary, and
  all deployment-mode contracts.
- The synchronization workflow only reads Personal and only writes branches and
  pull requests in Commercial. It has no write credential for the public repo.
