## Summary

Describe the behavior, edition, and operational impact.

## Edition dependency

- [ ] Personal/shared behavior landed in `soulacy-personal` first.
- [ ] `.personal-base` and `dependencies/editions.json` still agree.
- [ ] Teams remains dependent on Personal; Scale remains dependent on Teams.
- [ ] No private source, branch, tag, or artifact is pushed to the public repo.

## Verification

- [ ] `make lint`
- [ ] `make test`
- [ ] `make edition-boundary`
- [ ] `make deployment-modes-test`
- [ ] Release or migration notes are updated when needed.
